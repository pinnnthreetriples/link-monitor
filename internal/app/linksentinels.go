package app

import (
	"errors"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
)

// This is the one file in the package that knows the shape of the Tailscale
// adapter's connect errors. The two conditions the button has to recognise are
// the adapter's own sentinels, matched with errors.Is against its values — this
// package keeps no copies of them, so there is nothing here to drift out of
// step. If the adapter renames one, exactly one file changes.

// isLoginRequired reports whether the daemon refuses to connect until somebody
// completes an interactive browser login.
func isLoginRequired(err error) bool {
	return errors.Is(err, tailscale.ErrLoginRequired)
}

// isLinkAccessDenied reports whether the command was refused for want of
// administrator rights.
func isLinkAccessDenied(err error) bool {
	return errors.Is(err, tailscale.ErrAccessDenied)
}

// loginURL digs the interactive login address out of an error, and returns ""
// when there is none.
//
// The URL is read from the adapter's typed error rather than from the message,
// because the adapter deliberately keeps it out of the text: it is a one-time
// credential, and an error string ends up in logs.
func loginURL(err error) string {
	var login *tailscale.LoginRequiredError
	if errors.As(err, &login) {
		return login.URL
	}
	return ""
}
