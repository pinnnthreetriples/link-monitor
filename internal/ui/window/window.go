package window

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
)

// errAlreadyRun rejects a second [Window.Run] on the same value. One Window is
// one native window, and the native window is created inside Run.
var errAlreadyRun = errors.New("the window has already been run")

// backend is the native side of one window: the WebView2 control, its HWND and
// its message loop.
//
// It is an interface because none of it can be tested — there is no HWND on a
// build agent — while everything above it can: which call happens when, what a
// second Show does, what a Close during creation does. The fake in the tests
// answers those questions; the implementation in native_windows.go answers to
// Windows.
//
// Only run, dispatch and terminate may be called from another goroutine.
// show, hide and destroy belong to the thread run is blocked on, which is why
// the lifecycle below never reaches them except from inside a dispatch.
type backend interface {
	// run pumps the message loop until terminate. It blocks.
	run()
	// dispatch queues f on the window thread. Safe from any goroutine.
	dispatch(f func())
	// show makes the window visible and brings it to the front.
	show()
	// hide hides the window without destroying it.
	hide()
	// terminate makes run return. Safe from any goroutine, and tolerates
	// arriving before run has started.
	terminate()
	// destroy releases the native window. Called once, after run returns.
	destroy()
}

// Window is the program's own window. Create it with [New].
type Window struct {
	cfg Config
	log *slog.Logger

	// newBackend builds the native side. It is a field so the lifecycle can be
	// exercised against a fake.
	newBackend func(cfg Config, onHide func()) (backend, error)

	mu sync.Mutex
	// b is the backend once Run has built it, and nil before and after.
	b backend
	// visible is the visibility the window should have. Before Run it is the
	// wish Run will apply; after Run it is what the last caller asked for.
	visible bool
	started bool
	closed  bool
}

// Run creates the window and pumps its message loop until Close is called or
// ctx is cancelled. It locks the OS thread it runs on, so it must be given a
// goroutine of its own. A normal close returns nil; a missing runtime returns
// an error wrapping [ErrRuntimeMissing].
func (w *Window) Run(ctx context.Context) error {
	// The WebView2 control, the HWND and the message loop all belong to one
	// thread for their whole life. A goroutine that migrated between them
	// would post messages to a queue nobody is reading.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	wanted, err := w.claim()
	if err != nil {
		return err
	}
	if !wanted {
		w.log.Debug("the window was closed before it opened")
		return nil
	}

	b, err := w.newBackend(w.cfg, w.hidden)
	if err != nil {
		w.finish()
		return fmt.Errorf("opening the window %q: %w", w.cfg.Title, err)
	}

	visible, attached := w.attach(b)
	if !attached {
		// Close arrived while the control was booting. Nothing has been shown,
		// so there is nothing to hide first.
		b.destroy()
		w.finish()
		return nil
	}

	stopWatching := w.watch(ctx)
	defer stopWatching()

	applyVisibility(b, visible)
	b.run()
	b.destroy()
	w.finish()
	return nil
}

// Show makes the window visible and brings it to the front.
//
// It is safe from any goroutine and idempotent, and a call made before Run
// starts takes effect once it does. Show after Close does nothing: a closed
// window has no message loop to raise it.
func (w *Window) Show() { w.setVisible(true) }

// Hide hides the window to the tray, leaving the message loop running so that
// the next Show is instant. Same rules as [Window.Show].
func (w *Window) Hide() { w.setVisible(false) }

// Close makes Run return, after which the native window is destroyed. It is
// safe from any goroutine, and calling it twice — or before Run — is harmless.
func (w *Window) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	b := w.b
	w.mu.Unlock()

	if b == nil {
		// Either Run has not built the window yet — claim will see the flag and
		// give up — or it has already returned. Nothing to stop either way.
		return
	}
	b.terminate()
}

// setVisible records the wish and, if the window is up, queues it onto the
// window thread.
func (w *Window) setVisible(visible bool) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.visible = visible
	b := w.b
	w.mu.Unlock()

	if b == nil {
		return // Run reads w.visible when it attaches the backend.
	}
	// ShowWindow belongs to the thread that owns the window, so the change is
	// posted to it rather than made here.
	b.dispatch(func() { w.applyWish(b) })
}

// applyWish brings the window in line with the latest wish, on the window
// thread. It re-reads the field rather than closing over a value, so a burst
// of calls from several goroutines settles on the last one rather than on
// whichever dispatch happens to run last.
func (w *Window) applyWish(b backend) {
	w.mu.Lock()
	visible, closed := w.visible, w.closed
	w.mu.Unlock()

	if closed {
		return
	}
	applyVisibility(b, visible)
}

// applyVisibility is the one place show and hide are chosen between.
func applyVisibility(b backend, visible bool) {
	if visible {
		b.show()
		return
	}
	b.hide()
}

// hidden records that the window hid itself — the user clicked X and the
// window procedure swallowed the close — and tells the caller once, so it can
// say «программа осталась в трее». It runs on the window thread.
func (w *Window) hidden() {
	w.mu.Lock()
	w.visible = false
	w.mu.Unlock()

	if w.cfg.OnHide != nil {
		w.cfg.OnHide()
	}
}

// claim marks the window as started and reports whether Run should carry on.
func (w *Window) claim() (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return false, fmt.Errorf("running the window %q: %w", w.cfg.Title, errAlreadyRun)
	}
	w.started = true
	return !w.closed, nil
}

// attach publishes the backend and reports the visibility to open with. It
// returns false when Close arrived while the backend was being built.
func (w *Window) attach(b backend) (visible, attached bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return false, false
	}
	w.b = b
	return w.visible, true
}

// finish drops the backend and marks the window closed for good. Show and Hide
// must not reach a message loop that has already returned.
func (w *Window) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.b = nil
	w.closed = true
}

// watch closes the window when ctx is cancelled — the tray's «Выход» does
// exactly that — and returns the function Run calls to stop watching.
func (w *Window) watch(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			w.log.Debug("closing the window: the context was cancelled")
			w.Close()
		case <-done:
		}
	}()

	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
