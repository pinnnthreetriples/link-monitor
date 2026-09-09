package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// wsaEACCES is the Windows sockets error WSAEACCES, "permission denied" (10013).
// A connect that fails with it never left the machine: a local packet filter
// refused it. On these machines that filter has been AmneziaVPN's kill switch,
// whose WFP rule denies the whole Tailscale range.
//
// It is spelled as a number because the name lives in golang.org/x/sys/windows,
// which would make this file Windows-only and untestable elsewhere. The value
// itself is fixed by Winsock.
const wsaEACCES = syscall.Errno(10013)

// wsaETIMEDOUT is WSAETIMEDOUT (10060): the connect left and went unanswered
// until the stack gave up. It matters for PeerCanReachUs, where the number
// arrives from the peer rather than from our own stack and so has to be
// recognised by hand.
const wsaETIMEDOUT = syscall.Errno(10060)

// wsaECONNREFUSED is WSAECONNREFUSED (10061): the packet arrived and the far end
// answered with a reset, which means that machine is up, its stack is
// answering, and nothing is listening on that port. It is the commonest SSH
// failure there is — sshd stopped on a machine that is otherwise fine — and
// until this constant existed it matched no classification at all and the
// diagnosis reported it as a probe that did not finish.
//
// Spelled as a number for the same reason as the two above: the name lives in
// golang.org/x/sys/windows, and the value is fixed by Winsock.
const wsaECONNREFUSED = syscall.Errno(10061)

// DefaultProbeTimeout bounds a TCPReachable whose context has no deadline. It is
// short: this call runs inside a polling loop and a slow answer is a bad answer.
const DefaultProbeTimeout = 3 * time.Second

// TCPReachable opens a plain TCP connection to addr:port, exactly as an
// ordinary application would, and classifies why it failed.
//
// It is the half of the diagnosis that Tailscale's own ping cannot give: a ping
// that succeeds while this fails means the tailnet is fine and something on
// this machine is refusing sockets.
//
// The result is one of:
//   - nil - the connection was established (and immediately closed).
//   - *core.BlockedError - a local packet filter refused it (WSAEACCES, 10013).
//   - *core.PortClosedError - the far end reset the connection: it is up and
//     nothing is listening there (WSAECONNREFUSED, 10061).
//   - *core.TimeoutError - the packet left and nothing answered in time.
//   - a wrapped error - anything else.
//
// The errno is matched numerically through errors.As, never by message text:
// these machines run a Russian Windows and every system message is translated.
func TCPReachable(ctx context.Context, addr string, port int) error {
	if addr == "" {
		return errors.New("sshx: TCPReachable needs an address")
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("sshx: TCPReachable got an out-of-range port %d", port)
	}

	probeCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, DefaultProbeTimeout)
		defer cancel()
	}

	var d net.Dialer
	conn, err := d.DialContext(probeCtx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
	if err != nil {
		return classifyDialError(err, addr, port)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("closing the probe connection to %s:%d: %w", addr, port, err)
	}
	return nil
}

// TCPReachable is the method form, so a *Client can stand in for the part of
// core.Probe that asks this question. The probe is local and does not use the
// SSH connection: it deliberately tests the same path an ordinary application
// would take, which is what makes its answer comparable to Tailscale's.
func (c *Client) TCPReachable(ctx context.Context, addr string, port int) error {
	return TCPReachable(ctx, addr, port)
}

// classifyDialError maps a dial failure onto the core error types.
func classifyDialError(err error, addr string, port int) error {
	if err == nil {
		return nil
	}
	if isBlocked(err) {
		return &core.BlockedError{Addr: addr, Port: port}
	}
	// A reset is a stronger answer than silence, so it is asked about before
	// the timeout: the far end is up and has told us there is nothing there.
	if isErrno(err, wsaECONNREFUSED) {
		return &core.PortClosedError{Addr: addr, Port: port}
	}
	if isTimeout(err) {
		return &core.TimeoutError{Addr: addr, Port: port}
	}
	return fmt.Errorf("connecting to %s:%d: %w", addr, port, err)
}

// isBlocked reports whether err carries WSAEACCES: a local packet filter
// refused the connect before it ever left the machine.
func isBlocked(err error) bool {
	return isErrno(err, wsaEACCES)
}

// isErrno reports whether err carries the given winsock number. It looks
// through *os.SyscallError first, which is how net wraps a failed connect
// syscall, and then falls back to a bare syscall.Errno anywhere in the chain.
//
// The number is compared numerically and never the message text: these
// machines run a Russian Windows and every system message is translated.
func isErrno(err error, want syscall.Errno) bool {
	var sysErr *os.SyscallError
	if errors.As(err, &sysErr) {
		var errno syscall.Errno
		if errors.As(sysErr.Err, &errno) && errno == want {
			return true
		}
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == want
}

// isTimeout reports whether err means the attempt went unanswered. A context
// deadline counts: the dialer reports its own expiry that way.
func isTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
