package app

import (
	"context"
	"errors"
	"time"
)

// DefaultLinkTimeout bounds one connect or disconnect. "tailscale up" can sit
// waiting for an interactive login that never comes, and a request that waits
// with it would wedge the UI; after this it fails and says so.
const DefaultLinkTimeout = 45 * time.Second

// LinkAction is what the user pressed.
type LinkAction string

const (
	// LinkUp connects this machine to the tailnet.
	LinkUp LinkAction = "up"
	// LinkDown disconnects it.
	LinkDown LinkAction = "down"
)

// ParseLinkAction turns a string from the wire into an action, reporting
// whether it is one this program knows. It is exported so the HTTP layer and
// this package share one definition of what is valid.
func ParseLinkAction(s string) (LinkAction, bool) {
	switch LinkAction(s) {
	case LinkUp:
		return LinkUp, true
	case LinkDown:
		return LinkDown, true
	default:
		return "", false
	}
}

// LinkControl connects and disconnects the tailnet. *tailscale.Client
// implements it.
type LinkControl interface {
	Up(ctx context.Context) error
	Down(ctx context.Context) error
}

// LinkState reports whether the daemon is connected right now. It is the same
// method core.Probe asks for, so the wiring passes the same Tailscale client.
//
// It exists so that pressing a button twice is harmless: the second press finds
// the link already in the state it asked for and says so, instead of running a
// command that would either fail or quietly do nothing.
type LinkState interface {
	TailscaleUp(ctx context.Context) (up bool, note string, err error)
}

// LinkOutcome is what came of connecting or disconnecting. Message is Russian
// and is shown to the user verbatim.
//
// LoginURL is filled in only when the daemon wants an interactive browser
// login; the UI offers to open it. NeedsAdmin means elevation of this process
// would have helped.
type LinkOutcome struct {
	OK         bool
	Message    string
	LoginURL   string
	NeedsAdmin bool
}

// Link is the connect/disconnect button, with the honesty around it: it looks
// before it acts, bounds the call, and nudges the poller afterwards so the
// dashboard changes at once rather than at the next tick.
type Link struct {
	ctl     LinkControl
	state   LinkState
	poller  *Poller
	timeout time.Duration
}

// NewLink builds the controller. state and poller may be nil — without state it
// simply acts without looking first, and without a poller the UI waits for the
// next scheduled probe. timeout of zero means DefaultLinkTimeout.
func NewLink(ctl LinkControl, state LinkState, poller *Poller, timeout time.Duration) *Link {
	if timeout <= 0 {
		timeout = DefaultLinkTimeout
	}
	return &Link{ctl: ctl, state: state, poller: poller, timeout: timeout}
}

// Apply carries out one action and reports what happened, in Russian. Like
// Fixer.Apply it returns no error: every outcome is something the user must
// read, and a Go error string is not that.
func (l *Link) Apply(ctx context.Context, action LinkAction) LinkOutcome {
	if l.ctl == nil {
		return LinkOutcome{Message: msgNoLinkControl}
	}
	switch action {
	case LinkUp:
		return l.change(ctx, true)
	case LinkDown:
		return l.change(ctx, false)
	default:
		return LinkOutcome{Message: msgUnknownLinkAction}
	}
}

// change moves the link to wantUp, doing nothing when it is already there.
func (l *Link) change(ctx context.Context, wantUp bool) LinkOutcome {
	if up, known := l.currentlyUp(ctx); known && up == wantUp {
		return LinkOutcome{OK: true, Message: alreadyMessage(wantUp)}
	}

	callCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()

	var err error
	if wantUp {
		err = l.ctl.Up(callCtx)
	} else {
		err = l.ctl.Down(callCtx)
	}
	if err != nil {
		return explainLinkError(err, failedMessage(wantUp))
	}

	// The state just changed under the poller's feet; asking it to look again
	// is what makes the dashboard agree with the button.
	if l.poller != nil {
		l.poller.CheckNow()
	}
	return LinkOutcome{OK: true, Message: doneMessage(wantUp)}
}

// currentlyUp asks the daemon what it is doing. The second result says whether
// the answer is worth trusting: a daemon that cannot be asked leaves the action
// to run anyway, because refusing to act on a missing answer would be worse.
func (l *Link) currentlyUp(ctx context.Context) (bool, bool) {
	if l.state == nil {
		return false, false
	}
	up, _, err := l.state.TailscaleUp(ctx)
	if err != nil {
		return false, false
	}
	return up, true
}

// explainLinkError turns a failed command into a Russian outcome. The sentinels
// belong to the Tailscale adapter and are matched with errors.Is, never by
// message text.
func explainLinkError(err error, fallback string) LinkOutcome {
	switch {
	case isLoginRequired(err):
		return LinkOutcome{Message: msgLinkLoginRequired, LoginURL: loginURL(err)}
	case isLinkAccessDenied(err):
		return LinkOutcome{Message: msgLinkNeedsAdmin, NeedsAdmin: true}
	case errors.Is(err, context.DeadlineExceeded):
		return LinkOutcome{Message: msgLinkTimedOut}
	case errors.Is(err, context.Canceled):
		return LinkOutcome{Message: msgCancelled}
	default:
		return LinkOutcome{Message: fallback}
	}
}

// alreadyMessage, doneMessage and failedMessage pick the Russian sentence for
// the direction being asked for.
func alreadyMessage(wantUp bool) string {
	if wantUp {
		return msgLinkAlreadyUp
	}
	return msgLinkAlreadyDown
}

func doneMessage(wantUp bool) string {
	if wantUp {
		return msgLinkUpDone
	}
	return msgLinkDownDone
}

func failedMessage(wantUp bool) string {
	if wantUp {
		return msgLinkUpFailed
	}
	return msgLinkDownFailed
}
