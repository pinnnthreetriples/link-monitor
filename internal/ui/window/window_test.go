package window

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// testURL is a loopback address of the shape the app really serves on.
const testURL = "http://127.0.0.1:8765/"

// quiet is a logger that keeps test output readable.
func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// testWindow returns a window wired to a fake backend.
func testWindow(t *testing.T, cfg Config) (*Window, *fakeBackend) {
	t.Helper()

	b := newFakeBackend()
	w := windowWith(t, cfg, func(Config, func()) (backend, error) { return b, nil })
	return w, b
}

// windowWith returns a window whose backend comes from build, for the tests
// that need creation to fail or that want the hide callback.
func windowWith(t *testing.T, cfg Config, build func(Config, func()) (backend, error)) *Window {
	t.Helper()

	if cfg.URL == "" {
		cfg.URL = testURL
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet()
	}
	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%+v) = %v, want a window", cfg, err)
	}
	w.newBackend = build
	return w
}

// runBackground starts Run on its own goroutine and returns the channel its
// error arrives on.
func runBackground(ctx context.Context, t *testing.T, w *Window) <-chan error {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return done
}

// awaitRun waits for Run to return and reports its error.
func awaitRun(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(testTimeout):
		t.Fatal("Run never returned")
		return nil
	}
}

func TestAWindowOpensVisibleUnlessAskedNotTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		startHidden bool
		wantVisible bool
	}{
		{name: "the default is a window the user can see", startHidden: false, wantVisible: true},
		{name: "StartHidden keeps it in the tray", startHidden: true, wantVisible: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w, b := testWindow(t, Config{StartHidden: tc.startHidden})
			done := runBackground(t.Context(), t, w)
			b.awaitStart(t)

			if got := b.nextVisibility(t); got != tc.wantVisible {
				t.Errorf("the window opened visible=%t, want %t", got, tc.wantVisible)
			}

			w.Close()
			if err := awaitRun(t, done); err != nil {
				t.Errorf("Run() = %v, want no error", err)
			}
			b.awaitDestroy(t)
		})
	}
}

func TestTheLastWishBeforeRunIsTheOneApplied(t *testing.T) {
	t.Parallel()

	w, b := testWindow(t, Config{StartHidden: true})

	// A tray asked to open the window twice while the WebView2 control is
	// still booting must not end up hiding it.
	w.Show()
	w.Hide()
	w.Show()

	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)

	if got := b.nextVisibility(t); !got {
		t.Error("the window opened hidden, want the last wish before Run to win")
	}
	if n := b.counted("hide"); n != 0 {
		t.Errorf("the window was hidden %d times before it was ever shown", n)
	}

	w.Close()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
}

func TestShowAndHideAfterRunReachTheWindowOnItsOwnGoroutine(t *testing.T) {
	t.Parallel()

	w, b := testWindow(t, Config{StartHidden: true})
	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)

	if b.nextVisibility(t) {
		t.Fatal("the window opened visible, want hidden")
	}

	w.Show()
	if !b.nextVisibility(t) {
		t.Error("Show did not make the window visible")
	}
	w.Hide()
	if b.nextVisibility(t) {
		t.Error("Hide did not hide the window")
	}
	if n := b.offThreadCalls(); n != 0 {
		t.Errorf("%d calls reached the window from the wrong goroutine", n)
	}

	w.Close()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
}

func TestShowAndHideAreIdempotent(t *testing.T) {
	t.Parallel()

	w, b := testWindow(t, Config{StartHidden: true})
	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)

	if b.nextVisibility(t) {
		t.Fatal("the window opened visible, want hidden")
	}

	w.Show()
	w.Show()
	b.awaitVisibility(t, true)
	w.Hide()
	w.Hide()
	b.awaitVisibility(t, false)

	if b.visibleNow(t) {
		t.Error("after two Hides the window is visible")
	}

	w.Close()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
}

func TestCloseBeforeRunNeverBuildsAWindow(t *testing.T) {
	t.Parallel()

	built := false
	w := windowWith(t, Config{}, func(Config, func()) (backend, error) {
		built = true
		return newFakeBackend(), nil
	})

	w.Close()
	if err := w.Run(t.Context()); err != nil {
		t.Errorf("Run() = %v, want no error after Close", err)
	}
	if built {
		t.Error("Run built a WebView2 control for a window that was already closed")
	}
}

func TestCloseWhileTheControlIsBootingDestroysIt(t *testing.T) {
	t.Parallel()

	b := newFakeBackend()
	var w *Window
	w = windowWith(t, Config{}, func(Config, func()) (backend, error) {
		// A user who picks «Выход» while WebView2 is still starting up.
		w.Close()
		return b, nil
	})

	if err := w.Run(t.Context()); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
	b.awaitDestroy(t)
	if n := b.counted("run"); n != 0 {
		t.Errorf("the message loop ran %d times for a window closed during creation", n)
	}
}

func TestCloseMakesRunReturnAndCanBeCalledTwice(t *testing.T) {
	t.Parallel()

	w, b := testWindow(t, Config{})
	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)

	w.Close()
	w.Close()

	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
	b.awaitDestroy(t)
	if n := b.counted("terminate"); n != 1 {
		t.Errorf("terminate was called %d times, want exactly 1", n)
	}
	if n := b.counted("destroy"); n != 1 {
		t.Errorf("destroy was called %d times, want exactly 1", n)
	}
}

func TestCancellingTheContextClosesTheWindow(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	w, b := testWindow(t, Config{})
	done := runBackground(ctx, t, w)
	b.awaitStart(t)

	// This is the tray's «Выход»: the program is shutting down, so the window
	// goes with it rather than waiting to be closed by hand.
	cancel()

	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error on cancellation", err)
	}
	b.awaitDestroy(t)
}

func TestAnAlreadyCancelledContextReturnsAtOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	w, b := testWindow(t, Config{})
	if err := awaitRun(t, runBackground(ctx, t, w)); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
	b.awaitDestroy(t)
}

func TestShowAfterRunHasReturnedIsIgnored(t *testing.T) {
	t.Parallel()

	w, b := testWindow(t, Config{})
	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)
	w.Close()
	if err := awaitRun(t, done); err != nil {
		t.Fatalf("Run() = %v, want no error", err)
	}

	before := b.history()
	w.Show()
	w.Hide()
	w.Close()

	if after := b.history(); len(after) != len(before) {
		t.Errorf("%d calls reached a window whose loop had already returned: %v",
			len(after)-len(before), after[len(before):])
	}
}

func TestRunTwiceIsRefused(t *testing.T) {
	t.Parallel()

	w, b := testWindow(t, Config{})
	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)

	if err := w.Run(t.Context()); !errors.Is(err, errAlreadyRun) {
		t.Errorf("a second Run() = %v, want errAlreadyRun", err)
	}

	w.Close()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
}

func TestAMissingRuntimeIsReportedAsSuch(t *testing.T) {
	t.Parallel()

	w := windowWith(t, Config{}, func(Config, func()) (backend, error) {
		return nil, ErrRuntimeMissing
	})

	err := w.Run(t.Context())
	if !errors.Is(err, ErrRuntimeMissing) {
		t.Fatalf("Run() = %v, want an error wrapping ErrRuntimeMissing", err)
	}
	if !strings.Contains(err.Error(), defaultTitle) {
		t.Errorf("Run() = %q, want the window's title in the message", err)
	}
}

func TestHidingToTheTrayTellsTheCallerOnce(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		hides int
	)
	hidden := make(chan func(), 1)
	b := newFakeBackend()
	w := windowWith(t, Config{
		OnHide: func() {
			mu.Lock()
			defer mu.Unlock()
			hides++
		},
	}, func(_ Config, onHide func()) (backend, error) {
		hidden <- onHide
		return b, nil
	})

	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)
	if !b.nextVisibility(t) {
		t.Fatal("the window opened hidden, want visible")
	}

	// The window procedure calls this after swallowing WM_CLOSE. Calling it
	// here is the only way to reach that path without a real X button.
	(<-hidden)()

	mu.Lock()
	got := hides
	mu.Unlock()
	if got != 1 {
		t.Errorf("OnHide fired %d times for one close, want 1", got)
	}

	// The X button must leave the window believing it is hidden, or the next
	// Show would be a no-op that looks like a broken tray.
	w.mu.Lock()
	visible := w.visible
	w.mu.Unlock()
	if visible {
		t.Error("the window still thinks it is visible after hiding itself")
	}

	// And a Show after that must actually show it again.
	w.Show()
	if !b.nextVisibility(t) {
		t.Error("Show after a hide-to-tray did not bring the window back")
	}

	w.Close()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
}

func TestShowHideAndCloseFromManyGoroutinesAtOnce(t *testing.T) {
	t.Parallel()

	w, b := testWindow(t, Config{})
	done := runBackground(t.Context(), t, w)
	b.awaitStart(t)

	const goroutines = 8
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if i%2 == 0 {
					w.Show()
					continue
				}
				w.Hide()
			}
		}()
	}
	wg.Wait()

	w.Close()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run() = %v, want no error", err)
	}
	b.awaitDestroy(t)

	if n := b.offThreadCalls(); n != 0 {
		t.Errorf("%d calls reached the window from a goroutine that does not own it", n)
	}
}
