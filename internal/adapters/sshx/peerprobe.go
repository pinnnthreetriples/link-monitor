package sshx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// DefaultPeerProbeTimeout is how long the peer waits for its connection back to
// us when Config.PeerProbeTimeout is unset. It is longer than
// DefaultProbeTimeout because a filtered inbound port often answers with
// nothing at all, and cutting that short would turn "blocked" into "unknown".
const DefaultPeerProbeTimeout = 5 * time.Second

// peerProbeGrace is the extra time the SSH call gets on top of the deadline the
// remote script is given. PowerShell has to start first, which on a cold
// Windows box is not instant; without the slack we would report our own
// cancellation instead of the peer's verdict.
const peerProbeGrace = 6 * time.Second

// peerProbeToken prefixes the one line the probe script prints. It is ASCII and
// chosen by us, so no display language can change it.
//
// "Token" here means a marker in a stream of text, not a credential: nothing
// authenticates with it, it is written to the peer's stdout on purpose and
// scanned for on the way back, and changing it would break parsing rather
// than access. gosec matches the name, not the value.
const peerProbeToken = "LINKMON-PROBE" //nolint:gosec // G101: a marker, not a credential

// The statuses the probe script reports. Numbers, not words: the peer runs a
// Russian Windows and anything Windows prints is translated.
const (
	peerProbeConnected = 0 // the peer opened the connection
	peerProbeFailed    = 1 // the connect failed; the second field is the winsock errno
	peerProbeTimedOut  = 2 // the connect neither succeeded nor failed in time
)

// PeerCanReachUs asks the peer to open a TCP connection back to this machine at
// localAddr:port - normally our own Tailscale address, 100.124.47.73 for the
// laptop or 100.127.188.87 for the work PC.
//
// It exists because the inbound direction cannot be inferred from the outbound
// one. On these two machines outbound SSH worked for hours while inbound was
// impossible, because this side had no OpenSSH Server installed at all. The
// only honest test is to make the far side dial back, which is what this does.
//
// The outcome is classified exactly as TCPReachable classifies the outbound
// direction, so the two answers are comparable:
//   - nil - the peer connected.
//   - *core.BlockedError - a packet filter on the PEER's side refused the
//     connect (WSAEACCES, 10013).
//   - *core.PortClosedError - this machine reset the peer's connect: nothing is
//     listening on the port (WSAECONNREFUSED, 10061).
//   - *core.TimeoutError - the peer's connect went unanswered.
//   - a wrapped error - the connect failed for another reason, or the probe
//     could not be run at all.
//
// It needs a working outbound session, so it is unavailable exactly when the
// link is already known to be broken; that is the contract in core.Probe.
func (c *Client) PeerCanReachUs(ctx context.Context, localAddr string, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("sshx: PeerCanReachUs got an out-of-range port %d", port)
	}
	script, err := peerProbeScript(localAddr, port, c.cfg.PeerProbeTimeout)
	if err != nil {
		return err
	}

	// The remote script has its own deadline; ours only has to outlast it by
	// enough for PowerShell to start. WithTimeout still respects an earlier
	// deadline on ctx, so a caller in a hurry keeps the last word.
	runCtx, cancel := context.WithTimeout(ctx, c.cfg.PeerProbeTimeout+peerProbeGrace)
	defer cancel()

	stdout, stderr, code, err := c.RunPowerShell(runCtx, script)
	if err != nil {
		return fmt.Errorf("asking %s to connect back to %s:%d: %w",
			c.cfg.hostPort(), localAddr, port, err)
	}

	// The peer's verdict wins over its exit code: a verdict on the stream is an
	// answer however the shell felt about the run. The exit code and stderr
	// only become the answer when no verdict arrived at all.
	values, parseErr := tokenLineFields(stdout, peerProbeToken, 2)
	if parseErr != nil {
		if code != 0 {
			return fmt.Errorf("asking %s to connect back to %s:%d: powershell exited with %d: %s",
				c.cfg.hostPort(), localAddr, port, code, strings.TrimSpace(stderr))
		}
		return fmt.Errorf("reading the probe result from %s: %w", c.cfg.hostPort(), parseErr)
	}
	return classifyPeerProbe(values[0], values[1], localAddr, port)
}

// peerProbeScript builds the PowerShell the peer runs.
//
// It uses System.Net.Sockets.TcpClient rather than Test-NetConnection, which
// prints a localised object with no error number in it. Task.WaitAny is used
// rather than Task.Wait because WaitAny never throws: it returns -1 when the
// deadline passes and the task's index otherwise, leaving the task itself to be
// asked whether it faulted. Task.Wait instead throws on a faulted task, and
// PowerShell's handling of a method that throws from inside an "if" condition
// is not something this probe should depend on.
//
// The three outcomes are therefore read structurally, never from text, and the
// script prints one ASCII line and nothing else.
func peerProbeScript(localAddr string, port int, wait time.Duration) (string, error) {
	quoted, err := quotePowerShellLiteral(localAddr)
	if err != nil {
		return "", fmt.Errorf("sshx: PeerCanReachUs got a bad address: %w", err)
	}
	ms := wait.Milliseconds()
	if ms < 1 {
		ms = 1
	}

	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		// A faulted task wraps its SocketException in an AggregateException,
		// and PowerShell may add a layer of its own, so walk the chain rather
		// than guess its depth. The bound stops a cyclic chain hanging the peer.
		"function Get-Errno($e) {",
		"  for ($i = 0; $i -lt 8 -and $null -ne $e; $i++) {",
		"    if ($e -is [System.Net.Sockets.SocketException]) { return [int]$e.NativeErrorCode }",
		"    $e = $e.InnerException",
		"  }",
		"  return -1",
		"}",
		"$status = " + strconv.Itoa(peerProbeFailed),
		"$errno = -1",
		"$client = New-Object System.Net.Sockets.TcpClient",
		"try {",
		"  $task = $client.ConnectAsync(" + quoted + ", " + strconv.Itoa(port) + ")",
		"  $tasks = [System.Threading.Tasks.Task[]]@($task)",
		"  $idx = [System.Threading.Tasks.Task]::WaitAny($tasks, " + strconv.FormatInt(ms, 10) + ")",
		"  if ($idx -lt 0) { $status = " + strconv.Itoa(peerProbeTimedOut) + " }",
		"  elseif ($task.IsFaulted) { $errno = Get-Errno $task.Exception }",
		"  elseif ($task.IsCanceled) { $status = " + strconv.Itoa(peerProbeTimedOut) + " }",
		"  else { $status = " + strconv.Itoa(peerProbeConnected) + "; $errno = 0 }",
		"} catch {",
		// The probe itself broke - no permission to open a socket, say. $status
		// is already "failed"; recover a number for it if there is one.
		"  $errno = Get-Errno $_.Exception",
		"} finally {",
		"  $client.Close()",
		"}",
		"Write-Output ('" + peerProbeToken + " ' + $status + ' ' + $errno)",
	}, "\n"), nil
}

// classifyPeerProbe maps the peer's verdict onto the core error types, matching
// what TCPReachable returns for the outbound direction.
func classifyPeerProbe(status, errno int, localAddr string, port int) error {
	switch status {
	case peerProbeConnected:
		return nil
	case peerProbeTimedOut:
		return &core.TimeoutError{Addr: localAddr, Port: port}
	case peerProbeFailed:
		return peerProbeErrno(errno, localAddr, port)
	default:
		return fmt.Errorf("the peer reported an unknown probe status %d for %s:%d",
			status, localAddr, port)
	}
}

// peerProbeErrno turns a winsock number reported by the peer into an error. The
// number is compared numerically; it is wrapped as a syscall.Errno so a caller
// can inspect it the same way, though the text it renders comes from this
// machine's message table, not the peer's.
func peerProbeErrno(errno int, localAddr string, port int) error {
	switch syscall.Errno(errno) { //nolint:gosec // a winsock number, always small and positive
	case wsaEACCES:
		return &core.BlockedError{Addr: localAddr, Port: port}
	case wsaETIMEDOUT:
		return &core.TimeoutError{Addr: localAddr, Port: port}
	case wsaECONNREFUSED:
		// Our own stack answered the peer with a reset: this machine is up and
		// nothing is listening on that port. Classified here as it is for the
		// outbound direction, so the two rows stay comparable.
		return &core.PortClosedError{Addr: localAddr, Port: port}
	}
	if errno < 0 {
		return fmt.Errorf("the peer could not connect to %s:%d and reported no error number",
			localAddr, port)
	}
	return fmt.Errorf("the peer could not connect to %s:%d: %w",
		localAddr, port, syscall.Errno(errno)) //nolint:gosec // a winsock number
}
