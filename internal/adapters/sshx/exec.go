package sshx

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"golang.org/x/crypto/ssh"
)

// ExitUnknown is the exit code reported when the command produced none - the
// connection dropped, the peer killed it with a signal, or ctx expired first.
const ExitUnknown = -1

// Run executes one command on the peer and collects both streams.
//
// The peer's default shell is cmd.exe, so cmd is a cmd.exe command line. A
// command that exits non-zero is not an error: exitCode carries the status and
// err is nil, because "the service is stopped" and "the call failed" are
// different answers and the caller needs to tell them apart. err is non-nil
// only when the command could not be run or its status could not be learned.
//
// Run honours ctx: on cancellation it closes the session, waits for the
// in-flight copy to finish so nothing races the buffers, and returns ctx.Err().
func (c *Client) Run(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error) {
	if strings.TrimSpace(cmd) == "" {
		return "", "", ExitUnknown, errors.New("sshx: empty command")
	}
	if err := ctx.Err(); err != nil {
		return "", "", ExitUnknown, fmt.Errorf("running a remote command: %w", err)
	}

	session, err := c.conn.NewSession()
	if err != nil {
		return "", "", ExitUnknown, fmt.Errorf("opening an ssh session on %s: %w", c.cfg.hostPort(), err)
	}
	// This is the real cleanup on the normal path. On the cancellation path the
	// session is already closed and this second Close reports io.EOF, which
	// says nothing we need.
	defer func() { _ = session.Close() }()

	var outBuf, errBuf bytes.Buffer
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		// Closing the session unblocks Run; waiting for it establishes the
		// happens-before edge that makes reading the buffers safe.
		_ = session.Close() // best effort: the point is only to unblock Run
		<-done
		return "", "", ExitUnknown, fmt.Errorf("running a remote command on %s: %w",
			c.cfg.hostPort(), ctx.Err())
	case runErr := <-done:
		code, err := exitCodeOf(runErr)
		return outBuf.String(), errBuf.String(), code, err
	}
}

// exitCodeOf turns the error from ssh.Session.Run into an exit code.
func exitCodeOf(runErr error) (int, error) {
	if runErr == nil {
		return 0, nil
	}
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitStatus(), nil
	}
	var missing *ssh.ExitMissingError
	if errors.As(runErr, &missing) {
		return ExitUnknown, fmt.Errorf("the peer ended the command without a status: %w", missing)
	}
	return ExitUnknown, fmt.Errorf("running a remote command: %w", runErr)
}

// RunPowerShell runs script through PowerShell on the peer.
//
// It is the way to run anything with quotes, spaces or Cyrillic in it. The
// peer's shell is cmd.exe, and a script handed to cmd.exe has to survive
// cmd.exe's quoting, PowerShell's own quoting and the console code page - on
// these machines a Russian one, so a literal Cyrillic argument arrives mangled.
// Encoding the script as base64 UTF-16LE and passing it as -EncodedCommand
// sidesteps all three: the command line then contains nothing but ASCII base64.
func (c *Client) RunPowerShell(ctx context.Context, script string) (
	stdout, stderr string, exitCode int, err error,
) {
	if strings.TrimSpace(script) == "" {
		return "", "", ExitUnknown, errors.New("sshx: empty PowerShell script")
	}
	return c.Run(ctx, powerShellCommandLine(script))
}

// powerShellPreamble fixes the other half of the encoding problem. Passing the
// script in as UTF-16 gets it to the peer intact; this gets the answer back.
// PowerShell writes a redirected stdout in the console code page, which on
// these machines is the Russian OEM page 866, and Go would read those bytes as
// broken UTF-8. Asking for UTF-8 without a byte-order mark makes the round trip
// lossless. The try/catch is because the assignment can fail on a host with no
// console at all, and a failure there must not take the caller's script with
// it: worst case the output is mis-encoded, which is what it would have been
// anyway.
// $ProgressPreference is silenced in the same breath so that cmdlets in the
// caller's script do not serialise progress records into the output. Note what
// this does NOT fix: started from cmd.exe, PowerShell emits a CLIXML blob
// ("Preparing modules for first use") on a cold run, and it does so from the
// host before the first line of script executes, so nothing in here can stop
// it. Measured on a real Windows host, it goes to stderr and stdout stays
// clean - which is why PeerCanReachUs still scans stdout for its own token
// line rather than trusting the stream as a whole.
const powerShellPreamble = "try { $OutputEncoding = [Console]::OutputEncoding = " +
	"New-Object System.Text.UTF8Encoding $false } catch { }\n" +
	"$ProgressPreference = 'SilentlyContinue'\n"

// powerShellCommandLine renders script as a cmd.exe command line that runs it
// under PowerShell:
//
//	powershell -NoLogo -NoProfile -NonInteractive -EncodedCommand <base64 UTF-16LE>
//
// -NoProfile matters for more than speed: a profile on either machine could
// print a banner into stdout and corrupt whatever the caller is parsing.
func powerShellCommandLine(script string) string {
	return "powershell -NoLogo -NoProfile -NonInteractive -EncodedCommand " +
		encodePowerShell(powerShellPreamble+script)
}

// encodePowerShell encodes script the way -EncodedCommand wants it: UTF-16
// little-endian, no byte-order mark, standard base64.
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}
