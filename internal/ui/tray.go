package ui

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"fyne.io/systray"
)

// Config is everything the tray needs from the rest of the program. Only
// Source and UIURL are required; the rest have working defaults.
type Config struct {
	// Source supplies the statuses that drive the icon's colour.
	Source Source
	// Actions backs «Проверить сейчас». When nil, that item does nothing but
	// say so in the log.
	Actions Actions
	// Opener opens URLs. Defaults to the user's own browser.
	Opener Opener
	// Notifier shows notifications. Defaults to a Windows toast.
	Notifier Notifier
	// Window, when non-nil, is what «Открыть» shows. The browser is only used
	// when there is no window — a machine without the WebView2 runtime.
	Window Windower
	// ShellMenu, when non-nil, is Explorer's right-click item, and the tray
	// grows a checkable entry that switches it on and off. Nil leaves the entry
	// out of the menu entirely: a control that cannot work is worse than none.
	ShellMenu ShellMenu
	// Quick, when non-nil, are the three ways onto the other machine, and the
	// tray grows a block for them. Nil leaves all three out, for the same
	// reason ShellMenu does: cmd/linkmon returns nil when the program is
	// configured in a way no quick action could honour, and three items that
	// could only ever apologise are worse than none.
	Quick QuickActions
	// Clip, when non-nil and reporting itself available, is the shared
	// clipboard, and the tray grows a checkable entry that switches it on and
	// off. Nil leaves the entry out, exactly as a nil ShellMenu does.
	//
	// So does a Clip whose Available reports false, and that is the deliberate
	// choice between leaving the entry out and drawing it greyed. Available is
	// settled by how the program was started — -no-clipboard, or no peer to
	// share with — and never changes while it runs, so a greyed entry would be
	// a control that could never come back to life, sitting in the menu for the
	// rest of the session as a promise nothing will keep. A clipboard that is
	// merely out of reach right now, which is what a locked screen looks like,
	// is a different thing: Available still says yes, the entry is drawn, and
	// the click says what happened.
	Clip ClipShare

	// UIURL is the loopback address of the window. It is what «Открыть» falls
	// back to when Window is nil, and it is the address the window itself
	// shows, so the two paths agree about where the UI lives.
	UIURL string

	// ToastAppID overrides the application identity notifications are shown
	// under. See [NewToastNotifier] for why the default is what it is.
	ToastAppID string
	// Debounce is the shortest gap between two notifications about the same
	// state. Defaults to a minute.
	Debounce time.Duration
	// Timeout bounds one call out of the tray. Defaults to fifteen seconds.
	Timeout time.Duration
	// Now is the clock, for tests. Defaults to time.Now.
	Now func() time.Time
	// Logger receives the failures a tray cannot report any other way.
	// Defaults to slog.Default().
	Logger *slog.Logger
}

// menuClicks carries everything the user can click: every menu item, and the
// tray icon itself. The controller only ever selects on them, so tests hand it
// plain channels.
type menuClicks struct {
	open, check, quit <-chan struct{}
	// icon is a left click on the tray icon, which does what «Открыть» does —
	// a tray program that ignores a click on its own icon feels broken.
	icon <-chan struct{}
	// shell is the checkable Explorer-menu item. It is nil when the menu was
	// built without one, which a select handles exactly right: a nil channel
	// never fires.
	shell <-chan struct{}
	// clip is the checkable shared-clipboard item, and is nil for the same
	// reason and with the same effect when the menu was built without one.
	clip <-chan struct{}
	// terminal, folder and desktop are the three quick actions, and are nil
	// together when the menu was built without them.
	terminal, folder, desktop <-chan struct{}
}

// menuParts says which optional blocks the menu carries. Every answer comes
// from what cmd/linkmon managed to wire up, and an item nobody could serve is
// left out rather than drawn dead.
type menuParts struct {
	// quick asks for the block of three quick actions.
	quick bool
	// shell asks for the checkable Explorer-menu item.
	shell bool
	// clip asks for the checkable shared-clipboard item.
	clip bool
}

// tray is the tray library as this package uses it. It exists so the shell's
// lifecycle — start, hand over the menu, stop on cancellation or on «Выход» —
// can be tested without a desktop session.
type tray interface {
	// Run shows the tray, calls onReady once it is up, and blocks until Quit.
	Run(onReady, onExit func())
	// Quit stops Run. It must tolerate being called more than once.
	Quit()
	// AddMenu builds the menu and returns its click channels. It is called
	// from onReady, where the tray is ready to accept items. A block that
	// parts does not ask for is not built, and the channels for its items come
	// back nil.
	AddMenu(parts menuParts) menuClicks
	// SetShellChecked ticks the Explorer-menu item or unticks it. A menu built
	// without the item has nothing to tick, and that is not a failure.
	SetShellChecked(checked bool)
	// SetClipChecked ticks the shared-clipboard item or unticks it, and
	// tolerates a menu built without one for the same reason: the controller
	// calls it as soon as it has the menu, whatever the menu turned out to
	// contain.
	SetClipChecked(checked bool)
	SetIcon(icon []byte)
	SetTooltip(text string)
}

// Run shows the tray icon and blocks until the user chooses «Выход» or ctx is
// cancelled, whichever comes first. It returns an error only when the tray
// could not be configured; a normal stop returns nil.
//
// Returning is the signal to shut the rest of the program down: the user
// choosing «Выход» is indistinguishable, from here, from the program being
// asked to stop.
func Run(ctx context.Context, cfg Config) error {
	return run(ctx, cfg, newSystrayTray())
}

// run is Run with the tray library injected.
func run(ctx context.Context, cfg Config, t tray) error {
	c, err := newController(cfg, t)
	if err != nil {
		return fmt.Errorf("starting the tray: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Buffered, so handing the menu over never blocks the tray's own callback
	// even if the controller has already given up and gone home.
	ready := make(chan menuClicks, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// However the loop ends — cancellation, «Выход», a panic upstream —
		// the tray has to stop, or t.Run below never returns.
		defer t.Quit()
		c.serve(ctx, ready)
	}()

	parts := menuParts{
		quick: cfg.Quick != nil,
		shell: cfg.ShellMenu != nil,
		// Both halves, and the order matters: a nil Clip is asked nothing. See
		// Config.Clip for why an unavailable one is left out rather than greyed.
		clip: cfg.Clip != nil && cfg.Clip.Available(),
	}
	t.Run(func() { ready <- t.AddMenu(parts) }, func() {})

	cancel()
	wg.Wait()
	return nil
}

// systrayTray is the real tray: a thin shell over fyne.io/systray, holding no
// logic of its own so that there is nothing here to test.
//
// It is a pointer type only because a click on the icon arrives as a callback
// and the controller wants a channel, so something has to own the channel in
// between.
type systrayTray struct {
	// icon carries a left click on the tray icon. It is buffered by one: a
	// click that lands while the previous one is still being served is worth
	// keeping, and a third is not — the window opens once either way.
	icon chan struct{}

	// shell is the checkable Explorer-menu item, once AddMenu has built it.
	//
	// It is written in AddMenu, on the tray library's own goroutine, and read
	// in SetShellChecked, on the controller's. That is not a race: AddMenu's
	// return value is handed across a channel, and the controller cannot call
	// SetShellChecked before it has received it.
	shell *systray.MenuItem

	// clip is the checkable shared-clipboard item, written and read across the
	// same two goroutines and safe for the same reason.
	clip *systray.MenuItem
}

func newSystrayTray() *systrayTray {
	return &systrayTray{icon: make(chan struct{}, 1)}
}

// Run installs the click handler and then blocks in the tray's event loop.
//
// v1.12.2 offers exactly one hook for this — SetOnTapped, for the left button;
// there is no SetOnClick or SetOnDClick in this version. Taking the left click
// also takes the menu off it, which is the Windows convention rather than a
// loss: left opens the program, right opens the menu, and the library still
// shows the menu on a right click.
//
// The hook is set here, before systray.Run creates the loop that reads it,
// rather than from onReady — which the library runs on a goroutine of its own,
// alongside a loop that would already be dispatching messages.
func (t *systrayTray) Run(onReady, onExit func()) {
	systray.SetOnTapped(t.tapped)
	systray.Run(onReady, onExit)
}

// tapped is what a left click on the icon runs, on the tray's own message
// loop. It must not block: that loop is what draws the menu and repaints the
// icon.
func (t *systrayTray) tapped() {
	select {
	case t.icon <- struct{}{}:
	default:
		// A click is already waiting to be served. Queueing a second one would
		// only ask for the window that is about to open anyway.
	}
}

func (t *systrayTray) Quit() { systray.Quit() }

func (t *systrayTray) SetIcon(icon []byte) { systray.SetIcon(icon) }

func (t *systrayTray) SetTooltip(text string) { systray.SetTooltip(text) }

// AddMenu builds the menu in four blocks, separated so that nine items stay
// scannable: this program (open its window, check the link now), the three
// ways onto the other machine, the two settings, and «Выход» on its own.
//
// The quick actions are a block of their own because they are a different kind
// of thing from everything above them — each one leaves this program for the
// second machine — and they sit above the settings because they are what a user
// opens this menu to do, while a checkbox is something they set once.
//
// The shared clipboard joined the settings block rather than starting a fifth
// one. A ninth item inside a block the reader already knows costs a line; a
// fifth block costs another rule about what belongs where, and a menu of five
// blocks is the one that stops being scannable.
func (t *systrayTray) AddMenu(parts menuParts) menuClicks {
	clicks := menuClicks{icon: t.icon}
	clicks.open = systray.AddMenuItem(menuOpen, hintOpen).ClickedCh
	clicks.check = systray.AddMenuItem(menuCheck, hintCheck).ClickedCh

	if parts.quick {
		systray.AddSeparator()
		clicks.terminal = systray.AddMenuItem(menuTerminal, hintTerminal).ClickedCh
		clicks.folder = systray.AddMenuItem(menuFolder, hintFolder).ClickedCh
		clicks.desktop = systray.AddMenuItem(menuDesktop, hintDesktop).ClickedCh
	}

	// The settings block: both checkable items together, because they are the
	// same kind of thing — a tick a person sets, not an action they take — and
	// one separator whether the block turns out to hold one of them or both.
	//
	// The shared clipboard goes first inside it. It is the one switch that may
	// be wanted in a hurry, and putting it at the top of the block keeps it in
	// the same place whether or not the Explorer item follows it.
	//
	// Both are drawn unticked and corrected a moment later: the controller
	// reads [ClipShare.On] and [ShellMenu.Installed] as soon as it has the
	// menu, so what is passed here is only what shows for the few milliseconds
	// before the truth arrives.
	if parts.clip || parts.shell {
		systray.AddSeparator()
	}
	if parts.clip {
		t.clip = systray.AddMenuItemCheckbox(menuClip, hintClip, false)
		clicks.clip = t.clip.ClickedCh
	}
	if parts.shell {
		t.shell = systray.AddMenuItemCheckbox(menuShell, hintShell, false)
		clicks.shell = t.shell.ClickedCh
	}

	systray.AddSeparator()
	clicks.quit = systray.AddMenuItem(menuQuit, hintQuit).ClickedCh
	return clicks
}

// SetShellChecked ticks the Explorer-menu item, or unticks it.
func (t *systrayTray) SetShellChecked(checked bool) {
	if t.shell == nil {
		return
	}
	setChecked(t.shell, checked)
}

// SetClipChecked ticks the shared-clipboard item, or unticks it.
func (t *systrayTray) SetClipChecked(checked bool) {
	if t.clip == nil {
		return
	}
	setChecked(t.clip, checked)
}

// checkable is one checkable menu item, as this file uses it.
// *systray.MenuItem implements it.
//
// It exists for the reason internal/ui/window's backend interface does: it
// lifts the one decision in [setChecked] — which of the library's two calls a
// boolean means — out of the tray library, so a machine with no desktop session
// can test that a tick means what it says. Nothing else about a menu item is
// wanted here: the click channel is read once, in AddMenu, where there is
// nothing to decide.
type checkable interface {
	Check()
	Uncheck()
}

// setChecked ticks a menu item or unticks it.
//
// The nil check stays in the two callers rather than moving in here, and on
// purpose: a nil *systray.MenuItem handed to a [checkable] parameter arrives as
// an interface that is not nil, so a guard written here would look right and
// pass a nil pointer straight through to a method call.
func setChecked(item checkable, checked bool) {
	if checked {
		item.Check()
		return
	}
	item.Uncheck()
}
