package ui

import (
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// at is a fixed starting point, so a test never depends on the wall clock.
var at = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func status(s core.State) Status { return Status{Overall: s, Summary: "тест"} }

func TestPolicyStaysQuietOnTheFirstStatus(t *testing.T) {
	// A tray that shouts the moment it starts gets muted, and the very first
	// probe is the most likely one to arrive as "fail" simply because nothing
	// has been measured yet.
	for _, s := range []core.State{core.StateOK, core.StateUnknown, core.StateWarn, core.StateFail} {
		p := newNotifyPolicy(time.Minute)
		if p.consider(at, status(s)) {
			t.Errorf("notified on the first status (%s)", s)
		}
	}
}

func TestPolicyNotifiesOnlyOnAChangeForTheWorse(t *testing.T) {
	tests := []struct {
		name       string
		from, to   core.State
		wantNotify bool
	}{
		{"ok to fail", core.StateOK, core.StateFail, true},
		{"ok to warn", core.StateOK, core.StateWarn, true},
		{"ok to unknown", core.StateOK, core.StateUnknown, true},
		{"warn to fail", core.StateWarn, core.StateFail, true},
		{"unknown to warn", core.StateUnknown, core.StateWarn, true},
		{"fail to ok", core.StateFail, core.StateOK, false},
		{"fail to warn", core.StateFail, core.StateWarn, false},
		{"warn to ok", core.StateWarn, core.StateOK, false},
		{"ok to ok", core.StateOK, core.StateOK, false},
		{"fail to fail", core.StateFail, core.StateFail, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newNotifyPolicy(time.Minute)
			p.consider(at, status(tc.from)) // the first status is only recorded
			if got := p.consider(at.Add(time.Second), status(tc.to)); got != tc.wantNotify {
				t.Errorf("consider(%s -> %s) = %v, want %v", tc.from, tc.to, got, tc.wantNotify)
			}
		})
	}
}

func TestPolicyDebouncesAFlappingLink(t *testing.T) {
	p := newNotifyPolicy(time.Minute)
	p.consider(at, status(core.StateOK))

	if !p.consider(at.Add(time.Second), status(core.StateFail)) {
		t.Fatal("the first drop did not notify")
	}
	// Up, down, up, down within the window: the icon follows every change, the
	// user's attention is asked for once.
	now := at.Add(2 * time.Second)
	for i := range 20 {
		p.consider(now, status(core.StateOK))
		now = now.Add(time.Second)
		if p.consider(now, status(core.StateFail)) {
			t.Fatalf("flap %d notified again %v after the first", i, now.Sub(at))
		}
		now = now.Add(time.Second)
	}

	// Once the window has passed, a fresh drop is news again.
	p.consider(at.Add(2*time.Minute), status(core.StateOK))
	if !p.consider(at.Add(2*time.Minute+time.Second), status(core.StateFail)) {
		t.Error("a drop a full minute later did not notify")
	}
}

func TestPolicyDebouncesEachStateSeparately(t *testing.T) {
	p := newNotifyPolicy(time.Minute)
	p.consider(at, status(core.StateOK))

	if !p.consider(at.Add(time.Second), status(core.StateWarn)) {
		t.Fatal("ok -> warn did not notify")
	}
	// Getting worse still is different news, even inside warn's window.
	if !p.consider(at.Add(2*time.Second), status(core.StateFail)) {
		t.Error("warn -> fail was debounced against warn's own window")
	}
}

func TestPolicyDefaultsToAMinute(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if got := newNotifyPolicy(d).debounce; got != defaultDebounce {
			t.Errorf("newNotifyPolicy(%v).debounce = %v, want %v", d, got, defaultDebounce)
		}
	}
}
