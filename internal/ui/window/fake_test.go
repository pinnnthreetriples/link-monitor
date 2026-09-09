package window

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testTimeout bounds every wait in these tests. The lifecycle is all in
// memory, so anything slower than this is a deadlock rather than a slow
// machine.
const testTimeout = 2 * time.Second

// fakeBackend stands in for the native window.
//
// It mimics the three things about the real one that the lifecycle above
// depends on: run blocks until terminate arrives, dispatch runs its function
// on the goroutine that is inside run, and a terminate that arrives before run
// still stops it. Everything else it merely records.
type fakeBackend struct {
	mu sync.Mutex
	// calls is every method called, in order.
	calls []string
	// queue holds dispatched functions waiting for the loop.
	queue []func()
	// loopID is the goroutine run was called on, and 0 before that.
	loopID uint64
	// offThread counts show, hide and destroy calls made from a goroutine
	// other than the one running the loop. Every one of them is a defect: the
	// real ShowWindow must be called from the thread that owns the window.
	offThread int
	// lastVisible is the last visibility the window was put into.
	lastVisible bool
	// visibleSet reports whether lastVisible means anything yet.
	visibleSet bool

	// started closes when run begins, quit when terminate arrives.
	started chan struct{}
	quit    chan struct{}
	// wake carries one token per dispatch, so the loop never sleeps through a
	// queued function.
	wake chan struct{}
	// visibility carries every visibility change, for tests that want to wait
	// for one. Sends never block: a full channel only means a test is not
	// reading them.
	visibility chan bool
	// destroyed closes when destroy is called.
	destroyed chan struct{}

	startOnce   sync.Once
	quitOnce    sync.Once
	destroyOnce sync.Once
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		started:    make(chan struct{}),
		quit:       make(chan struct{}),
		wake:       make(chan struct{}, 1),
		visibility: make(chan bool, 64),
		destroyed:  make(chan struct{}),
	}
}

func (f *fakeBackend) run() {
	f.mu.Lock()
	f.loopID = goroutineID()
	f.calls = append(f.calls, "run")
	f.mu.Unlock()
	f.startOnce.Do(func() { close(f.started) })

	for {
		f.drain()
		select {
		case <-f.quit:
			return
		case <-f.wake:
		}
	}
}

// drain runs everything dispatched so far, on the loop's own goroutine.
func (f *fakeBackend) drain() {
	for {
		f.mu.Lock()
		if len(f.queue) == 0 {
			f.mu.Unlock()
			return
		}
		next := f.queue[0]
		f.queue = f.queue[1:]
		f.mu.Unlock()

		next()
	}
}

func (f *fakeBackend) dispatch(fn func()) {
	f.mu.Lock()
	f.calls = append(f.calls, "dispatch")
	f.queue = append(f.queue, fn)
	f.mu.Unlock()

	select {
	case f.wake <- struct{}{}:
	default: // a token is already pending; the loop will drain everything
	}
}

func (f *fakeBackend) terminate() {
	f.record("terminate")
	f.quitOnce.Do(func() { close(f.quit) })
}

func (f *fakeBackend) show() { f.setVisible(true) }

func (f *fakeBackend) hide() { f.setVisible(false) }

func (f *fakeBackend) destroy() {
	f.record("destroy")
	f.destroyOnce.Do(func() { close(f.destroyed) })
}

// setVisible records a visibility change and checks it happened where it is
// allowed to.
func (f *fakeBackend) setVisible(visible bool) {
	name := "hide"
	if visible {
		name = "show"
	}

	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.lastVisible, f.visibleSet = visible, true
	if id := goroutineID(); f.loopID != 0 && id != f.loopID {
		f.offThread++
	}
	f.mu.Unlock()

	select {
	case f.visibility <- visible:
	default:
	}
}

// record notes a call, and flags it when it came from the wrong goroutine.
// Only destroy and terminate go through here with that check: terminate is
// explicitly allowed from anywhere.
func (f *fakeBackend) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, name)
	if name == "destroy" && f.loopID != 0 && goroutineID() != f.loopID {
		f.offThread++
	}
}

// history returns the calls made so far.
func (f *fakeBackend) history() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

// counted reports how many times one method was called.
func (f *fakeBackend) counted(name string) int {
	n := 0
	for _, c := range f.history() {
		if c == name {
			n++
		}
	}
	return n
}

// visibleNow reports the last visibility the window was put into.
func (f *fakeBackend) visibleNow(t *testing.T) bool {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.visibleSet {
		t.Fatal("the window was never shown or hidden")
	}
	return f.lastVisible
}

// offThreadCalls reports how many calls reached the window from a goroutine
// that does not own it.
func (f *fakeBackend) offThreadCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.offThread
}

// awaitStart blocks until the loop is running.
func (f *fakeBackend) awaitStart(t *testing.T) {
	t.Helper()
	await(t, f.started, "the message loop never started")
}

// awaitDestroy blocks until the window has been destroyed.
func (f *fakeBackend) awaitDestroy(t *testing.T) {
	t.Helper()
	await(t, f.destroyed, "the window was never destroyed")
}

// nextVisibility returns the next visibility change the window makes.
func (f *fakeBackend) nextVisibility(t *testing.T) bool {
	t.Helper()

	select {
	case v := <-f.visibility:
		return v
	case <-time.After(testTimeout):
		t.Fatalf("the window made no visibility change; calls so far: %v", f.history())
		return false
	}
}

// awaitVisibility blocks until the window reaches the given visibility.
//
// It skips over changes on the way there rather than insisting on the next
// one: Show and Hide are idempotent, so two Shows may legitimately produce two
// ShowWindow calls, and a test that demanded exactly one would be testing the
// coalescing rather than the result.
func (f *fakeBackend) awaitVisibility(t *testing.T, want bool) {
	t.Helper()

	deadline := time.After(testTimeout)
	for {
		select {
		case got := <-f.visibility:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("the window never became visible=%t; calls: %v", want, f.history())
			return
		}
	}
}

// await blocks on a channel until it closes, failing on the deadline.
func await(t *testing.T, ch <-chan struct{}, complaint string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(testTimeout):
		t.Fatal(complaint)
	}
}

// goroutineID returns the current goroutine's number, scraped from the header
// of its own stack.
//
// There is no supported way to ask, and no other way to prove the one rule the
// real backend cannot recover from breaking: show and hide are ShowWindow, and
// ShowWindow belongs to the thread that owns the window. A test that cannot
// tell one goroutine from another cannot check that at all.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)

	// The header reads "goroutine 42 [running]:".
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}
