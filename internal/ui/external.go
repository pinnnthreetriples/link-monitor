package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
)

// errNotWebURL rejects anything that is not an ordinary web address.
var errNotWebURL = errors.New("only an http or https link may be opened")

// errExternalOpenFailed reports that a link could not be handed over, and
// says nothing else. See [ExternalOpener] for why it carries no detail.
var errExternalOpenFailed = errors.New("the link could not be opened")

// ExternalOpener opens the links that have to leave the program's own window:
// a Tailscale login URL, a `tailscale serve` address, the page the user asked
// to see on the real internet. The window hands them here rather than
// navigating to them, because a program that turns into a web page for one
// click is the complaint this window was built to answer.
//
// It is deliberately a second opener rather than a relaxation of
// [SystemOpener]. That one exists to open this program's own address and
// nothing else, and a narrow rule that is widened for one caller stops being
// a rule. What the two share is the refusal to hand the shell anything but a
// web address: `file:`, `javascript:`, `ms-settings:`, `mailto:` and every
// other scheme are turned away, so an address arriving from `tailscale status`
// can never become a way to start a program.
//
// The URL is treated as a secret. A Tailscale login URL is a one-time
// credential carried in its own path, and a credential that reaches a log file
// has already leaked. So the host is the only part of a URL this type ever
// records, and a failure is reported as [errExternalOpenFailed] rather than by
// wrapping the underlying error — the command line and any output it produced
// both carry the URL in full.
type ExternalOpener struct {
	runner runner
	log    *slog.Logger
}

// NewExternalOpener returns an [Opener] for links that belong in a real
// browser. A nil logger means slog.Default().
func NewExternalOpener(logger *slog.Logger) *ExternalOpener {
	if logger == nil {
		logger = slog.Default()
	}
	return &ExternalOpener{runner: execRunner{}, log: logger}
}

// Open hands rawURL to the machine's own browser.
func (o *ExternalOpener) Open(ctx context.Context, rawURL string) error {
	host, err := webURLHost(rawURL)
	if err != nil {
		o.log.Warn("refusing to open a link that is not a web address", "reason", err)
		return err
	}

	name, args, err := openCommand(rawURL)
	if err != nil {
		// Neither wrapped nor logged: openCommand names the URL it was handed,
		// and that URL may be the credential. Only the non-Windows stub
		// answers this way, and the host says which link was lost.
		o.log.Warn("this platform cannot open a link", "host", host)
		return errExternalOpenFailed
	}
	if err := o.runner.run(ctx, name, args...); err != nil {
		// Same reason, with more at stake: the runner's error carries both the
		// command line and whatever the command printed.
		o.log.Warn("opening a link failed", "host", host)
		return errExternalOpenFailed
	}

	o.log.Info("opened a link outside the window", "host", host)
	return nil
}

// webURLHost checks that rawURL is an ordinary web address and returns its
// host, which is the only part of it that may be repeated.
func webURLHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Only the fact of the failure is reported: the parser's own message
		// quotes the URL it choked on, which is the one thing that may not be
		// written down.
		return "", fmt.Errorf("%w: it could not be parsed", errNotWebURL)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		// The scheme is safe to name — it is what the user needs to be told —
		// and it is the part before the credential.
		return "", fmt.Errorf("%w: scheme %q", errNotWebURL, u.Scheme)
	case u.Host == "":
		return "", fmt.Errorf("%w: it names no host", errNotWebURL)
	default:
		return u.Host, nil
	}
}
