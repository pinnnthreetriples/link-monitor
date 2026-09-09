package window

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
)

// externalBindName is the global the page looks for: window.openExternal.
// frontend/desktop.js calls it for every link marked data-external, and falls
// back to a separate window only when it is not there.
const externalBindName = "openExternal"

var (
	// errNoExternalOpener reports that nothing was wired up to open links.
	errNoExternalOpener = errors.New("no external opener is configured")
	// errExternalOpenFailed is what the page is told when a link could not be
	// handed over. It carries no detail on purpose — see [externalLinks].
	errExternalOpenFailed = errors.New("the link could not be opened")
	// errNotWebLink rejects anything that is not an ordinary web address.
	errNotWebLink = errors.New("only http and https links may be opened")
)

// externalLinks is the host half of window.openExternal.
//
// A link that must leave the app — a Tailscale login URL, a `tailscale serve`
// address — is handed to the browser rather than navigated to, because the
// window is the program: a program that turns into a web page for one click is
// the complaint that started all of this.
//
// The URL never reaches the log, an error string or the window title. A
// Tailscale login URL is a one-time credential carried in its own path, and a
// credential that reaches a log file has already leaked. Only the host is ever
// recorded — the part that is safe, and the part worth knowing.
type externalLinks struct {
	open func(rawURL string) error
	log  *slog.Logger
}

// newExternalLinks returns the handler to bind for cfg.
func newExternalLinks(cfg Config) externalLinks {
	return externalLinks{open: cfg.OpenExternal, log: cfg.Logger}
}

// handle is what window.openExternal calls. The error it returns reaches the
// page as a rejected promise, so it says nothing the page did not already
// know.
func (e externalLinks) handle(rawURL string) error {
	if err := checkWebLink(rawURL); err != nil {
		e.log.Warn("refusing an external link", "reason", err)
		return err
	}
	if e.open == nil {
		e.log.Warn("a page asked to open an external link, but no opener is configured",
			"host", linkHost(rawURL))
		return errNoExternalOpener
	}

	if err := e.open(rawURL); err != nil {
		// The error is deliberately not recorded. Every opener in this program
		// names the URL it was handed in its message, and that URL may be a
		// one-time credential; the host is all this can safely say about it,
		// and it is enough to tell a failed launch from a refused address.
		e.log.Warn("opening an external link failed", "host", linkHost(rawURL))
		return errExternalOpenFailed
	}
	e.log.Info("opened an external link", "host", linkHost(rawURL))
	return nil
}

// checkWebLink accepts only http and https.
//
// The page is ours, but the addresses in it come from `tailscale status` and
// `tailscale serve`. A scheme this does not recognise must not become a way to
// launch a program through the shell's URL handler.
func checkWebLink(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Only the fact of the failure is reported: the parser's own message
		// quotes the URL, which is the one thing that may not be repeated.
		return fmt.Errorf("%w: it could not be parsed", errNotWebLink)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("%w: scheme %q", errNotWebLink, u.Scheme)
	case u.Host == "":
		return fmt.Errorf("%w: it names no host", errNotWebLink)
	default:
		return nil
	}
}

// linkHost returns just the host of a URL, which is the only part of it that
// is safe to log. Everything after the host — path, query, fragment — is
// where a login token lives.
func linkHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// An unparseable URL has no host to name, and its text is not ours to
		// write down.
		return ""
	}
	return u.Host
}
