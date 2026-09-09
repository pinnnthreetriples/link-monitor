package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strings"
)

// errNotLoopback rejects URLs that do not point at this machine.
var errNotLoopback = errors.New("only a loopback address may be opened")

// runner runs an external command and waits for it. It is an interface so that
// tests can see exactly what would have been launched without launching it.
type runner interface {
	run(ctx context.Context, name string, args ...string) error
}

// execRunner is the real runner, and the only thing in this package that
// touches the world outside the process.
//
// Running a process normally belongs in internal/adapters. It sits here
// because both callers are shell gestures rather than domain operations —
// handing a URL to the browser and handing a toast to Windows — and because
// neither has anything for the app layer to decide about. Everything above it
// takes [runner], [Opener] or [Notifier], so nothing else in the package knows
// a process exists.
type execRunner struct{}

// run is the one place in this package that starts a process.
//
// name is never a variable in practice: all three callers get it from
// openCommand or toastCommand, which return the literals "rundll32.exe" and
// "powershell.exe". What varies is argv, and the callers validate it before
// they get here — [SystemOpener.Open] through checkLoopbackURL (http or https,
// host on the loopback), [ExternalOpener.Open] through webURLHost (http or
// https only, so no `file:`, `javascript:` or `ms-settings:` ever reaches
// here), and [ToastNotifier.Notify] by handing PowerShell a base64
// -EncodedCommand rather than a command line. exec.CommandContext passes argv
// as a vector, so no shell re-reads any of it.
func (execRunner) run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: see above
	if err != nil {
		if text := strings.TrimSpace(string(out)); text != "" {
			return fmt.Errorf("running %s: %w: %s", name, err, text)
		}
		return fmt.Errorf("running %s: %w", name, err)
	}
	return nil
}

// SystemOpener opens URLs with whatever the user has set as their browser.
type SystemOpener struct {
	runner runner
}

// NewSystemOpener returns an [Opener] backed by the operating system's own
// URL handler.
func NewSystemOpener() *SystemOpener {
	return &SystemOpener{runner: execRunner{}}
}

// Open hands rawURL to the default browser. Only loopback URLs are accepted:
// the tray exists to open this program's own window, and refusing everything
// else means a status line can never turn into a command to launch something.
func (o *SystemOpener) Open(ctx context.Context, rawURL string) error {
	if err := checkLoopbackURL(rawURL); err != nil {
		return fmt.Errorf("opening %q: %w", rawURL, err)
	}

	name, args, err := openCommand(rawURL)
	if err != nil {
		return fmt.Errorf("opening %q: %w", rawURL, err)
	}
	if err := o.runner.run(ctx, name, args...); err != nil {
		return fmt.Errorf("opening %q: %w", rawURL, err)
	}
	return nil
}

// checkLoopbackURL accepts only http(s) URLs whose host is this machine.
func checkLoopbackURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return errors.New("no URL was configured")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parsing the URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q: %w", u.Scheme, errNotLoopback)
	}

	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("host %q: %w", host, errNotLoopback)
}
