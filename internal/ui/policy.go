package ui

import (
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// defaultDebounce is the shortest gap between two notifications about the same
// state. A link that flaps once a second must not produce sixty toasts.
const defaultDebounce = time.Minute

// notifyPolicy decides which status changes are worth interrupting the user
// for. It is deliberately separate from anything that can show a notification,
// so the rule can be tested by asserting a boolean.
//
// The rule, in order:
//   - never on the first status after start: the tray has nothing to compare
//     it to, and a program that shouts on launch gets muted;
//   - only when the state got worse, using the severity order core.State
//     already defines (ok < unknown < warn < fail). Recovery is visible in the
//     icon turning green and does not need a toast;
//   - and at most once per state per debounce window.
type notifyPolicy struct {
	debounce time.Duration
	seen     bool
	last     core.State
	lastAt   map[core.State]time.Time
}

func newNotifyPolicy(debounce time.Duration) *notifyPolicy {
	if debounce <= 0 {
		debounce = defaultDebounce
	}
	return &notifyPolicy{debounce: debounce, lastAt: make(map[core.State]time.Time)}
}

// consider records a status and reports whether it deserves a notification.
// It is called once per status, in order, from the tray's own goroutine.
func (p *notifyPolicy) consider(now time.Time, st Status) bool {
	previous, seen := p.last, p.seen
	p.last, p.seen = st.Overall, true

	switch {
	case !seen:
		return false
	case st.Overall <= previous:
		return false
	}

	if at, ok := p.lastAt[st.Overall]; ok && now.Sub(at) < p.debounce {
		return false
	}
	p.lastAt[st.Overall] = now
	return true
}
