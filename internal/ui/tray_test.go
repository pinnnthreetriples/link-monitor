package ui

import (
	"context"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// runIn starts run in the background and hands back a channel carrying its
// result, so a test can assert that it actually returns.
func runIn(ctx context.Context, cfg Config, t tray) <-chan error {
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, t) }()
	return done
}

func config(source Source, notifier Notifier, opener Opener) Config {
	return Config{
		Source:   source,
		Opener:   opener,
		Notifier: notifier,
		UIURL:    "http://127.0.0.1:8731/",
		Logger:   quiet(),
	}
}

func TestRunRefusesAConfigThatCannotWork(t *testing.T) {
	if err := run(t.Context(), Config{}, newFakeTray()); err == nil {
		t.Fatal("run started a tray with no status source")
	}
}

func TestRunPaintsTheLinkGoingDownAndComingBack(t *testing.T) {
	statuses := make(chan Status)
	tray := newFakeTray()
	notifier := &fakeNotifier{}

	cfg := config(
		statusSource(statuses),
		notifier,
		&fakeOpener{},
	)
	done := runIn(t.Context(), cfg, tray)

	// The tray paints itself before the first probe, so it is never blank.
	waitFor(t, "the starting tooltip", func() bool { return len(tray.allTooltips()) == 1 })

	statuses <- Status{Overall: core.StateOK, Summary: "Связь установлена"}
	waitFor(t, "the first status", func() bool { return len(tray.allTooltips()) == 2 })
	if notifier.count() != 0 {
		t.Error("the first probe raised a notification")
	}

	statuses <- Status{Overall: core.StateFail, Summary: "Связь потеряна", Detail: "Tailscale не запущен"}
	waitFor(t, "the notification", func() bool { return notifier.count() == 1 })

	tips := tray.allTooltips()
	if last := tips[len(tips)-1]; last != "Связь потеряна\nTailscale не запущен" {
		t.Errorf("tooltip = %q, want the Russian summary and detail", last)
	}

	tray.quit <- struct{}{}
	assertStopped(t, done)
}

func TestRunStopsOnQuit(t *testing.T) {
	tray := newFakeTray()
	cfg := config(statusSource(nil), &fakeNotifier{}, &fakeOpener{})

	done := runIn(t.Context(), cfg, tray)
	waitFor(t, "the menu to be built", func() bool {
		tray.mu.Lock()
		defer tray.mu.Unlock()
		return tray.menus == 1
	})

	tray.quit <- struct{}{}
	assertStopped(t, done)

	tray.mu.Lock()
	defer tray.mu.Unlock()
	if !tray.exited {
		t.Error("the tray was left running after «Выход»")
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	tray := newFakeTray()
	cfg := config(statusSource(nil), &fakeNotifier{}, &fakeOpener{})

	ctx, cancel := context.WithCancel(context.Background())
	done := runIn(ctx, cfg, tray)
	waitFor(t, "the menu to be built", func() bool {
		tray.mu.Lock()
		defer tray.mu.Unlock()
		return tray.menus == 1
	})

	cancel()
	assertStopped(t, done)

	tray.mu.Lock()
	defer tray.mu.Unlock()
	if !tray.exited {
		t.Error("cancelling the context did not stop the tray")
	}
}

func TestRunStopsWhenCancelledBeforeTheTrayIsUp(t *testing.T) {
	// The window the tray library quits by posting to does not exist yet at
	// this point, which is the case most likely to leave a stuck event loop.
	tray := newFakeTray()
	tray.delayReady = make(chan struct{})
	cfg := config(statusSource(nil), &fakeNotifier{}, &fakeOpener{})

	ctx, cancel := context.WithCancel(context.Background())
	done := runIn(ctx, cfg, tray)
	cancel()

	// The tray comes up a moment after the program was told to stop.
	time.Sleep(10 * time.Millisecond)
	close(tray.delayReady)

	assertStopped(t, done)
}

func TestRunSurvivesASourceThatClosesItsChannel(t *testing.T) {
	statuses := make(chan Status)
	close(statuses)

	tray := newFakeTray()
	cfg := config(statusSource(statuses), &fakeNotifier{}, &fakeOpener{})

	done := runIn(t.Context(), cfg, tray)
	waitFor(t, "the starting tooltip", func() bool { return len(tray.allTooltips()) == 1 })

	tray.quit <- struct{}{}
	assertStopped(t, done)
}

func TestRunFallsBackToTheBrowserWithoutAWindow(t *testing.T) {
	// No Window in the Config: a machine with no Edge WebView2 runtime, or
	// -browser. «Открыть» must still put the UI in front of the user.
	tray := newFakeTray()
	opener := &fakeOpener{}
	cfg := config(statusSource(nil), &fakeNotifier{}, opener)

	done := runIn(t.Context(), cfg, tray)
	tray.open <- struct{}{}
	waitFor(t, "the browser to be opened", func() bool { return len(opener.opened()) == 1 })

	if got := opener.opened()[0]; got != "http://127.0.0.1:8731/" {
		t.Errorf("opened %q, want the window URL", got)
	}

	tray.quit <- struct{}{}
	assertStopped(t, done)
}

func TestRunShowsTheWindowFromTheMenuAndFromTheIcon(t *testing.T) {
	tray := newFakeTray()
	opener := &fakeOpener{}
	win := &fakeWindow{}
	cfg := config(statusSource(nil), &fakeNotifier{}, opener)
	cfg.Window = win

	done := runIn(t.Context(), cfg, tray)

	tray.open <- struct{}{}
	waitFor(t, "«Открыть» to raise the window", func() bool { return win.shown() == 1 })
	tray.icon <- struct{}{}
	waitFor(t, "the icon click to raise the window", func() bool { return win.shown() == 2 })

	if got := opener.opened(); len(got) != 0 {
		t.Errorf("a browser was opened even though there is a window: %q", got)
	}

	tray.quit <- struct{}{}
	assertStopped(t, done)
}

// assertStopped fails unless run returned, which is what proves the goroutine
// it started was waited for rather than abandoned.
func assertStopped(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run never returned: a goroutine is still holding the tray")
	}
}

// statusSource is a [Source] that always hands back the same channel.
func statusSource(ch <-chan Status) Source {
	return SourceFunc(func(context.Context) <-chan Status { return ch })
}
