package tailscale

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// defaultUpTimeout is how long "tailscale up" is given to reach a running
	// state. The poller must never wedge behind this call, so it is bounded
	// even when the caller hands over a context without a deadline.
	defaultUpTimeout = 45 * time.Second

	// upGrace is the extra time the process gets after its own --timeout, so
	// that it prints its own diagnosis instead of being killed mid-sentence.
	upGrace = 5 * time.Second
)

var (
	// ErrLoginRequired means the daemon cannot connect without a person
	// authenticating in a browser: it is logged out, or the node key expired.
	// Match it with errors.Is; use errors.As with [LoginRequiredError] to get
	// the URL when the CLI printed one.
	ErrLoginRequired = errors.New("the Tailscale daemon needs an interactive login")

	// ErrAccessDenied means the CLI was refused for want of privilege.
	ErrAccessDenied = errors.New("the operation needs administrator rights")
)

// LoginRequiredError carries the login URL alongside [ErrLoginRequired]. The
// URL is a one-time credential, so it lives in the field and never in the
// error text: the UI opens it, logs never repeat it.
type LoginRequiredError struct {
	// URL is where the user must authenticate, empty when the CLI named none.
	URL string
}

func (e *LoginRequiredError) Error() string {
	if e.URL == "" {
		return ErrLoginRequired.Error()
	}
	return ErrLoginRequired.Error() + " (a login URL was offered)"
}

// Unwrap lets callers match with errors.Is(err, ErrLoginRequired).
func (e *LoginRequiredError) Unwrap() error { return ErrLoginRequired }

// Up connects the daemon to the tailnet. A daemon that is already connected is
// a success, not a failure: these are buttons, and buttons get pressed twice.
//
// The call is bounded by the context and, failing that, by the up timeout, so
// it cannot wedge the caller. When the daemon needs a browser login it gives
// up rather than waiting, returning [LoginRequiredError], which wraps
// [ErrLoginRequired] and carries the URL when the CLI printed one.
func (c *Client) Up(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tailscale up: %w", err)
	}
	if c.connected(ctx) == stateUp {
		return nil
	}

	budget := c.upTimeout
	if budget <= 0 {
		budget = defaultUpTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, budget+upGrace)
	defer cancel()

	res, runErr := c.runner.Run(runCtx, "up", "--timeout="+budget.String())
	return interpret(ctx, "up", res, runErr)
}

// Down disconnects the daemon from the tailnet, leaving the service itself
// running. A daemon that is already disconnected is a success.
func (c *Client) Down(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("tailscale down: %w", err)
	}
	if c.connected(ctx) == stateDown {
		return nil
	}

	res, runErr := c.runner.Run(ctx, "down")
	return interpret(ctx, "down", res, runErr)
}

// linkState is what the daemon says about itself, including "would not say".
type linkState int

const (
	stateUnknown linkState = iota
	stateUp
	stateDown
)

// connected asks the daemon where it stands. A daemon that will not answer
// leaves the question to the CLI, which is the one doing the work anyway.
func (c *Client) connected(ctx context.Context) linkState {
	up, _, err := c.TailscaleUp(ctx)
	switch {
	case err != nil:
		return stateUnknown
	case up:
		return stateUp
	default:
		return stateDown
	}
}

// interpret turns one CLI outcome into the error the app layer can act on.
func interpret(ctx context.Context, verb string, res CommandResult, runErr error) error {
	if runErr == nil && res.Code == 0 {
		return nil
	}
	if url, needed := loginRequest(res); needed {
		return &LoginRequiredError{URL: url}
	}
	if accessDenied(res) {
		return fmt.Errorf("tailscale %s: %s: %w", verb, cliMessage(res), ErrAccessDenied)
	}
	if runErr != nil {
		// A cancelled parent context explains the failure better than the kill
		// that followed from it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("tailscale %s: %w", verb, ctxErr)
		}
		return fmt.Errorf("tailscale %s: %w", verb, runErr)
	}
	return fmt.Errorf("tailscale %s: exit %d: %s", verb, res.Code, cliMessage(res))
}

// loginMarkers are the phrases the CLI uses when it wants a person, lowercased.
var loginMarkers = []string{
	"to authenticate, visit",
	"authenticate this machine",
	"please log in",
	"log in again",
	"logged out",
	"needs login",
	"not logged in",
	"needslogin",
	"key has expired",
	"node key expired",
}

// loginRequest reports whether the CLI asked for an interactive login, and the
// URL it offered. The URL is found by looking for it, not by counting lines,
// so a reworded message does not break the detection.
func loginRequest(res CommandResult) (string, bool) {
	text := res.Stdout + "\n" + res.Stderr
	for _, candidate := range urlPattern.FindAllString(text, -1) {
		trimmed := strings.Trim(candidate, `.,)"'`)
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "login.tailscale.com") || strings.Contains(lower, "/a/") {
			return trimmed, true
		}
	}

	lower := strings.ToLower(text)
	for _, marker := range loginMarkers {
		if strings.Contains(lower, marker) {
			return "", true
		}
	}
	return "", false
}

// denialMarkers are how Windows and the CLI spell "you are not allowed".
var denialMarkers = []string{
	"access is denied",
	"access denied",
	"permission denied",
	"operation not permitted",
	"requires elevation",
	"run as administrator",
	"administrator privileges",
	"must be run as root",
}

func accessDenied(res CommandResult) bool {
	lower := strings.ToLower(res.Stdout + "\n" + res.Stderr)
	for _, marker := range denialMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
