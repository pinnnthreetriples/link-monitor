package ui

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// quiet keeps the failures the tray logs out of the test output.
func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeTray stands in for fyne.io/systray. It mimics the two behaviours the
// shell depends on: Run blocks until Quit, and onReady is called on another
// goroutine once the tray is up.
type fakeTray struct {
	mu       sync.Mutex
	icons    [][]byte
	tooltips []string
	menus    int
	exited   bool

	clicks   menuClicks
	open     chan struct{}
	check    chan struct{}
	icon     chan struct{}
	quit     chan struct{}
	shell    chan struct{}
	clip     chan struct{}
	terminal chan struct{}
	folder   chan struct{}
	desktop  chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	// parts records what run asked AddMenu for, and shellChecks every tick
	// mark the controller set, newest last.
	parts       menuParts
	shellChecks []bool
	clipChecks  []bool
	// delayReady holds onReady back, so a test can cancel a tray that has not
	// finished coming up.
	delayReady chan struct{}
}

func newFakeTray() *fakeTray {
	f := &fakeTray{
		open:     make(chan struct{}),
		check:    make(chan struct{}),
		icon:     make(chan struct{}),
		quit:     make(chan struct{}),
		shell:    make(chan struct{}),
		clip:     make(chan struct{}),
		terminal: make(chan struct{}),
		folder:   make(chan struct{}),
		desktop:  make(chan struct{}),
		stop:     make(chan struct{}),
	}
	f.clicks = menuClicks{
		open: f.open, check: f.check, quit: f.quit, icon: f.icon, shell: f.shell,
		clip: f.clip, terminal: f.terminal, folder: f.folder, desktop: f.desktop,
	}
	return f
}

func (f *fakeTray) Run(onReady, onExit func()) {
	go func() {
		if f.delayReady != nil {
			<-f.delayReady
		}
		onReady()
	}()
	<-f.stop
	f.mu.Lock()
	f.exited = true
	f.mu.Unlock()
	onExit()
}

func (f *fakeTray) Quit() { f.stopOnce.Do(func() { close(f.stop) }) }

func (f *fakeTray) AddMenu(parts menuParts) menuClicks {
	f.mu.Lock()
	f.menus++
	f.parts = parts
	f.mu.Unlock()

	// The real tray returns nil channels for a block it did not build, and the
	// controller relies on a nil channel never firing.
	clicks := f.clicks
	if !parts.shell {
		clicks.shell = nil
	}
	if !parts.clip {
		clicks.clip = nil
	}
	if !parts.quick {
		clicks.terminal, clicks.folder, clicks.desktop = nil, nil, nil
	}
	return clicks
}

func (f *fakeTray) SetShellChecked(checked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shellChecks = append(f.shellChecks, checked)
}

func (f *fakeTray) checks() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.shellChecks...)
}

func (f *fakeTray) SetClipChecked(checked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipChecks = append(f.clipChecks, checked)
}

func (f *fakeTray) clipTicks() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.clipChecks...)
}

func (f *fakeTray) askedFor() menuParts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parts
}

func (f *fakeTray) SetIcon(icon []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.icons = append(f.icons, icon)
}

func (f *fakeTray) SetTooltip(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tooltips = append(f.tooltips, text)
}

func (f *fakeTray) lastIcon() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.icons) == 0 {
		return nil
	}
	return f.icons[len(f.icons)-1]
}

func (f *fakeTray) allTooltips() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tooltips...)
}

// fakeNotifier records the notifications the tray decided to show.
type fakeNotifier struct {
	mu    sync.Mutex
	shown []Notification
	err   error
}

func (n *fakeNotifier) Notify(_ context.Context, note Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.shown = append(n.shown, note)
	return n.err
}

func (n *fakeNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.shown)
}

// fakeOpener records URLs instead of launching anything.
type fakeOpener struct {
	mu   sync.Mutex
	urls []string
	err  error
}

func (o *fakeOpener) Open(_ context.Context, rawURL string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.urls = append(o.urls, rawURL)
	return o.err
}

func (o *fakeOpener) opened() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.urls...)
}

// fakeWindow counts the times the tray asked for the program's own window,
// which is what «Открыть» must do whenever there is a window to ask.
type fakeWindow struct {
	shows atomic.Int64
}

func (w *fakeWindow) Show() { w.shows.Add(1) }

func (w *fakeWindow) shown() int { return int(w.shows.Load()) }

// fakeShellMenu is Explorer's right-click item in a boolean. It counts the
// calls as well as holding the state, so a test can tell an install from a
// repair and see that the tray asked the registry rather than trusting its own
// tick mark.
type fakeShellMenu struct {
	mu         sync.Mutex
	installed  bool
	installs   int
	removes    int
	asks       int
	askErr     error
	installErr error
	removeErr  error
}

func (m *fakeShellMenu) Installed() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asks++
	if m.askErr != nil {
		return false, m.askErr
	}
	return m.installed, nil
}

func (m *fakeShellMenu) Install() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installs++
	if m.installErr != nil {
		return m.installErr
	}
	m.installed = true
	return nil
}

func (m *fakeShellMenu) Remove() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removes++
	if m.removeErr != nil {
		return m.removeErr
	}
	m.installed = false
	return nil
}

func (m *fakeShellMenu) counts() (installs, removes, asks int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installs, m.removes, m.asks
}

// fakeClip is the shared clipboard in two booleans. It counts the calls as
// well as holding the state, so a test can see that the tray asked On() at the
// moment of the click instead of trusting the tick mark it drew earlier, and
// that turning the feature off went straight to TurnOff.
//
// It cannot hold a clipboard item, and there is nothing here to hold one with:
// [ClipShare] has no method that carries content, which is the whole reason
// the tray can be tested against it at all.
type fakeClip struct {
	mu        sync.Mutex
	available bool
	on        bool
	ons       int
	offs      int
	asks      int
	// onErr, when set, is a clipboard that cannot be reached — the locked
	// screen, from the tray's side.
	onErr error
}

// newFakeClip is an available shared clipboard in the given state.
func newFakeClip(on bool) *fakeClip { return &fakeClip{available: true, on: on} }

func (c *fakeClip) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.available
}

func (c *fakeClip) On() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asks++
	return c.on
}

func (c *fakeClip) TurnOn() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ons++
	if c.onErr != nil {
		return c.onErr
	}
	c.on = true
	return nil
}

// TurnOff has no error to return and no failure to simulate, which is the
// contract rather than a shortcut in the fake.
func (c *fakeClip) TurnOff() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offs++
	c.on = false
}

func (c *fakeClip) counts() (ons, offs, asks int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ons, c.offs, c.asks
}

// set moves the switch behind the tray's back, the way the window and the
// local API both can.
func (c *fakeClip) set(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.on = on
}

// fakeQuick stands in for internal/adapters/quickopen. It records which action
// was asked for instead of opening a window, which is the only way these three
// items can be tested at all: every one of them ends in a process the build
// agent has no desktop for.
type fakeQuick struct {
	mu sync.Mutex
	// calls names the actions in the order they were asked for.
	calls []string
	// folder is what SharedFolder answers; "" is a machine with no shared
	// folder configured.
	folder string
	// err, when set, is what every action returns.
	err error
	// waitFor, when non-nil, holds an action up until it is closed, so a test
	// can prove that a call out of the tray is bounded by the tray's timeout.
	waitFor chan struct{}
}

func (q *fakeQuick) OpenTerminal(ctx context.Context) error { return q.record(ctx, "terminal") }

func (q *fakeQuick) OpenSharedFolder(ctx context.Context) error { return q.record(ctx, "folder") }

func (q *fakeQuick) OpenPeerDesktop(ctx context.Context) error { return q.record(ctx, "desktop") }

func (q *fakeQuick) SharedFolder() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.folder
}

func (q *fakeQuick) record(ctx context.Context, what string) error {
	q.mu.Lock()
	q.calls = append(q.calls, what)
	hold := q.waitFor
	err := q.err
	q.mu.Unlock()

	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (q *fakeQuick) asked() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.calls...)
}

// fakeRunner records commands instead of running them.
type fakeRunner struct {
	mu   sync.Mutex
	name string
	args []string
	runs int
	err  error
}

func (r *fakeRunner) run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs++
	r.name = name
	r.args = append([]string(nil), args...)
	return r.err
}

var errFake = errors.New("fake failure")

// waitFor polls until cond holds or the test's patience runs out. It keeps the
// concurrency tests from depending on a sleep long enough to be slow and short
// enough to be flaky.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
