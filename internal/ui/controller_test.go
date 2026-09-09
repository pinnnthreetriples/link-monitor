package ui

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/ui/trayicon"
)

// harness is a controller wired to fakes, plus the fakes themselves.
type harness struct {
	c        *controller
	tray     *fakeTray
	notifier *fakeNotifier
	opener   *fakeOpener
	checks   chan struct{}
	now      time.Time
}

// newHarness wires a controller to fakes. Each option is applied to the
// Config before the controller is built, which is how a test asks for the
// window branch instead of the browser one.
func newHarness(t *testing.T, statuses <-chan Status, opts ...func(*Config)) *harness {
	t.Helper()

	h := &harness{
		tray:     newFakeTray(),
		notifier: &fakeNotifier{},
		opener:   &fakeOpener{},
		checks:   make(chan struct{}, 8),
		now:      at,
	}

	cfg := Config{
		Source:   SourceFunc(func(context.Context) <-chan Status { return statuses }),
		Actions:  ActionsFunc(func(context.Context) error { h.checks <- struct{}{}; return nil }),
		Opener:   h.opener,
		Notifier: h.notifier,
		UIURL:    "http://127.0.0.1:8731/",
		Now:      func() time.Time { return h.now },
		Logger:   quiet(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	c, err := newController(cfg, h.tray)
	if err != nil {
		t.Fatalf("newController: %v", err)
	}
	c.grace = 10 * time.Millisecond
	h.c = c
	return h
}

func TestNewControllerRejectsAConfigThatCouldNeverWork(t *testing.T) {
	source := SourceFunc(func(context.Context) <-chan Status { return nil })
	tests := map[string]Config{
		"no source":     {UIURL: "http://127.0.0.1:8731/"},
		"no window URL": {Source: source},
		"a remote window": {
			Source: source,
			UIURL:  "http://100.127.188.87:8731/",
		},
		"a window off this machine by name": {
			Source: source,
			UIURL:  "http://example.com/",
		},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := newController(cfg, newFakeTray()); err == nil {
				t.Fatal("newController accepted it")
			}
		})
	}
}

func TestNewControllerFillsInDefaults(t *testing.T) {
	c, err := newController(Config{
		Source: SourceFunc(func(context.Context) <-chan Status { return nil }),
		UIURL:  "http://127.0.0.1:8731/",
	}, newFakeTray())
	if err != nil {
		t.Fatalf("newController: %v", err)
	}

	if c.window != nil {
		t.Error("a Config with no Window produced a controller that thinks it has one")
	}
	if c.timeout != defaultTimeout {
		t.Errorf("timeout = %v, want %v", c.timeout, defaultTimeout)
	}
	if c.policy.debounce != defaultDebounce {
		t.Errorf("debounce = %v, want %v", c.policy.debounce, defaultDebounce)
	}
	if c.now == nil || c.log == nil {
		t.Error("the clock or the logger was left nil")
	}
	if _, ok := c.opener.(*SystemOpener); !ok {
		t.Errorf("opener = %T, want a *SystemOpener", c.opener)
	}
	if _, ok := c.notifier.(*ToastNotifier); !ok {
		t.Errorf("notifier = %T, want a *ToastNotifier", c.notifier)
	}
}

func TestSeedPaintsBeforeTheFirstProbe(t *testing.T) {
	h := newHarness(t, nil)
	h.c.seed()

	want, err := trayicon.Render(core.StateUnknown)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Equal(h.tray.lastIcon(), want) {
		t.Error("the tray did not start on the unknown-state icon")
	}
	if got := h.tray.allTooltips(); len(got) != 1 || got[0] != startingSummary {
		t.Errorf("tooltips = %q, want just %q", got, startingSummary)
	}
	if h.notifier.count() != 0 {
		t.Error("seeding the tray notified the user")
	}
}

func TestApplyPaintsTheStateColourAndTheTooltip(t *testing.T) {
	h := newHarness(t, nil)

	for _, state := range []core.State{core.StateOK, core.StateWarn, core.StateFail, core.StateUnknown} {
		h.c.apply(t.Context(), Status{Overall: state, Summary: "Проверка", Detail: "подробность"})

		want, err := trayicon.Render(state)
		if err != nil {
			t.Fatalf("Render(%s): %v", state, err)
		}
		if !bytes.Equal(h.tray.lastIcon(), want) {
			t.Errorf("the icon painted for %s is not the %s icon", state, state)
		}
	}

	got := h.tray.allTooltips()
	if len(got) != 4 {
		t.Fatalf("set %d tooltips, want 4", len(got))
	}
	if got[0] != "Проверка\nподробность" {
		t.Errorf("tooltip = %q, want the Russian summary and detail", got[0])
	}
}

func TestApplyNotifiesOnlyWhenThePolicySaysSo(t *testing.T) {
	h := newHarness(t, nil)
	ctx := t.Context()

	h.c.apply(ctx, Status{Overall: core.StateOK, Summary: "Связь установлена"})
	if h.notifier.count() != 0 {
		t.Fatal("notified on the very first status")
	}

	h.now = h.now.Add(time.Second)
	h.c.apply(ctx, Status{Overall: core.StateFail, Summary: "Связь потеряна", Detail: "Tailscale не запущен"})
	if h.notifier.count() != 1 {
		t.Fatalf("showed %d notifications on the first drop, want 1", h.notifier.count())
	}
	got := h.notifier.shown[0]
	if got.Title != "Связь потеряна" || got.Body != "Tailscale не запущен" {
		t.Errorf("notification = %+v, want the snapshot's own Russian words", got)
	}

	// Recovering is good news, and good news does not interrupt.
	h.now = h.now.Add(time.Second)
	h.c.apply(ctx, Status{Overall: core.StateOK, Summary: "Связь установлена"})
	if h.notifier.count() != 1 {
		t.Error("notified on recovery")
	}
}

func TestApplyKeepsGoingWhenTheNotifierFails(t *testing.T) {
	h := newHarness(t, nil)
	h.notifier.err = errFake
	ctx := t.Context()

	h.c.apply(ctx, Status{Overall: core.StateOK})
	h.now = h.now.Add(time.Second)
	h.c.apply(ctx, Status{Overall: core.StateFail, Summary: "Связь потеряна"})

	want, err := trayicon.Render(core.StateFail)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Equal(h.tray.lastIcon(), want) {
		t.Error("a failed notification stopped the icon from turning red")
	}
}

func TestOpenAndCheckNow(t *testing.T) {
	h := newHarness(t, nil)
	ctx := t.Context()

	h.c.open(ctx, h.c.uiURL)
	if got := h.opener.opened(); len(got) != 1 || got[0] != h.c.uiURL {
		t.Errorf("opened %q, want the window URL", got)
	}

	h.c.checkNow(ctx)
	select {
	case <-h.checks:
	default:
		t.Error("the immediate check did not reach the app")
	}
}

func TestCheckNowSurvivesBeingUnwired(t *testing.T) {
	h := newHarness(t, nil)
	h.c.actions = nil
	h.c.checkNow(t.Context()) // must not panic
}

func TestOpenAndCheckNowSwallowFailures(t *testing.T) {
	// There is nowhere to return an error to from a menu click, so a failure
	// must be logged and the tray must stay alive.
	h := newHarness(t, nil)
	h.opener.err = errFake
	h.c.actions = ActionsFunc(func(context.Context) error { return errFake })

	h.c.open(t.Context(), h.c.uiURL)
	h.c.checkNow(t.Context())
}

func TestPumpAppliesEveryStatus(t *testing.T) {
	statuses := make(chan Status)
	h := newHarness(t, statuses)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.pump(t.Context(), statuses, h.tray.clicks)
	}()

	statuses <- Status{Overall: core.StateOK, Summary: "Связь установлена"}
	statuses <- Status{Overall: core.StateFail, Summary: "Связь потеряна"}
	waitFor(t, "both statuses to be painted", func() bool { return len(h.tray.allTooltips()) == 2 })

	close(h.tray.quit)
	<-done
}

func TestShowRaisesTheWindowRatherThanTheBrowser(t *testing.T) {
	win := &fakeWindow{}
	h := newHarness(t, nil, func(cfg *Config) { cfg.Window = win })

	h.c.show(t.Context())

	if win.shown() != 1 {
		t.Errorf("raised the window %d times, want 1", win.shown())
	}
	if got := h.opener.opened(); len(got) != 0 {
		t.Errorf("a browser was opened as well: %q", got)
	}
}

func TestShowFallsBackToTheBrowserWithoutAWindow(t *testing.T) {
	// The machine has no Edge WebView2 runtime, or the user asked for
	// -browser. Either way the UI still has to appear somewhere.
	h := newHarness(t, nil)

	h.c.show(t.Context())

	if got := h.opener.opened(); len(got) != 1 || got[0] != h.c.uiURL {
		t.Errorf("opened %q, want the window URL in the browser", got)
	}
}

func TestPumpHandlesEveryMenuItem(t *testing.T) {
	statuses := make(chan Status)
	h := newHarness(t, statuses)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.pump(t.Context(), statuses, h.tray.clicks)
	}()

	h.tray.open <- struct{}{}
	h.tray.check <- struct{}{}
	waitFor(t, "the menu clicks to land", func() bool { return len(h.opener.opened()) == 1 })
	<-h.checks

	h.tray.quit <- struct{}{}
	<-done

	if got := h.opener.opened(); got[0] != h.c.uiURL {
		t.Errorf("opened %q, want the window URL", got)
	}
}

func TestPumpTreatsAClickOnTheIconAsAnOpen(t *testing.T) {
	win := &fakeWindow{}
	h := newHarness(t, nil, func(cfg *Config) { cfg.Window = win })

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.pump(t.Context(), nil, h.tray.clicks)
	}()

	h.tray.icon <- struct{}{}
	waitFor(t, "the icon click to raise the window", func() bool { return win.shown() == 1 })

	h.tray.open <- struct{}{}
	waitFor(t, "«Открыть» to raise the window too", func() bool { return win.shown() == 2 })

	h.tray.quit <- struct{}{}
	<-done
}

func TestPumpOutlivesAClosedSource(t *testing.T) {
	// The poller stopping must not take the menu with it: the user still needs
	// to be able to quit.
	statuses := make(chan Status)
	h := newHarness(t, statuses)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.pump(t.Context(), statuses, h.tray.clicks)
	}()

	close(statuses)
	h.tray.open <- struct{}{} // still answering after the source is gone

	h.tray.quit <- struct{}{}
	<-done
}

func TestPumpStopsOnCancellation(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.pump(ctx, nil, h.tray.clicks)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump ignored a cancelled context")
	}
}

func TestServeGivesUpWhenTheTrayNeverAppears(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.serve(ctx, make(chan menuClicks)) // nothing will ever be sent
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve waited for a tray that was never coming")
	}
	if elapsed := time.Since(start); elapsed < h.c.grace {
		t.Errorf("serve gave up after %v, before the %v grace period", elapsed, h.c.grace)
	}
	if len(h.tray.allTooltips()) != 0 {
		t.Error("serve painted a tray it never got hold of")
	}
}

func TestServeStopsOnceTheTrayArrivesLate(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ready := make(chan menuClicks, 1)
	ready <- h.tray.clicks

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.c.serve(ctx, ready)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not notice the tray that had already come up")
	}
}

func TestErrNoSourceIsReported(t *testing.T) {
	_, got := newController(Config{UIURL: "http://127.0.0.1:8731/"}, newFakeTray())
	if !errors.Is(got, errNoSource) {
		t.Errorf("newController = %v, want errNoSource", got)
	}
}
