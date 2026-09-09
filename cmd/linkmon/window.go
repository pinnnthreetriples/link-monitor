package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/ui"
	"github.com/pinnnthreetriples/link-monitor/internal/ui/trayicon"
	"github.com/pinnnthreetriples/link-monitor/internal/ui/window"
)

// Every string in this block is read by the user and is therefore in Russian.
// The WebView2 runtime is named the way Microsoft names it, so that the one
// sentence the user can act on is also the one they can search for.
const (
	windowTitle = "Link Monitor"

	hiddenTitle = "Link Monitor свернулся в трей"
	hiddenBody  = "Программа продолжает работать и следит за связью. Значок — " +
		"справа на панели задач: нажми на него, чтобы открыть окно снова."

	noWindowTitle = "Окно не открылось"
	noWindowBody  = "Для своего окна программе нужен компонент Microsoft Edge WebView2 " +
		"Runtime. В Windows 10 и 11 он идёт вместе с Microsoft Edge; если его нет, " +
		"он ставится отдельно с сайта Microsoft. Пока интерфейс открыт в браузере."
)

// startWindow builds the program's own window and starts its message loop.
//
// It returns what the tray raises when the user asks for the interface, and
// the function to call on the way out. A nil [ui.Windower] is a machine
// without a window rather than a failure: it is what makes «Открыть» hand the
// UI to the browser, which is where this program's interface lived before it
// had a window of its own. The returned stop is never nil, so the caller can
// call it unconditionally.
func startWindow(ctx context.Context, o options, uiURL string, notes *notices) (ui.Windower, func()) {
	log := slog.Default()
	browser := browserFallback(ctx, uiURL, log)
	nothing := func() {}

	declareDPI(log)

	if o.browser {
		// The flag asks for exactly the old behaviour, which included not
		// opening anything until the user chose «Открыть».
		log.Info("the window is switched off by -browser; the interface opens in the browser")
		return nil, nothing
	}
	if !window.RuntimeInstalled() {
		noWindowHere(ctx, notes, browser)
		return nil, nothing
	}

	w, err := window.New(newWindowConfig(ctx, uiURL, notes, log))
	if err != nil {
		// New makes no native calls, so the only way it can fail is a
		// configuration this program built wrong. That is a defect here rather
		// than a property of the machine, so it is reported to the log and
		// «Открыть» quietly goes back to the browser.
		log.Error("building the window", "err", err)
		return nil, nothing
	}

	d := &desktop{win: w, browser: browser, log: log}
	d.start(ctx, notes)
	return d, d.stop
}

// declareDPI tells Windows this process scales its own windows, before the
// process owns one.
//
// This is the earliest point in the program that can: nothing before it
// creates a window — the poller and the HTTP listener have none, and toasts
// are raised in a separate PowerShell process — while the tray's own window is
// created moments later, in ui.Run. It is done even under -browser, where this
// program shows no window of its own, because the tray icon and its menu are
// windows too and they are drawn by the same process.
//
// A failure is a line in the log and nothing more. The window still sizes
// itself from whatever DPI it turns out to have, so the worst case is the
// blurry, stretched window this program had before — not no window.
func declareDPI(log *slog.Logger) {
	if err := window.DeclarePerMonitorDPI(); err != nil {
		log.Warn("the process could not declare per-monitor DPI awareness; "+
			"the window will be scaled by Windows instead of by itself", "err", err)
	}
}

// newWindowConfig describes the window: the loopback address of the UI, the
// tray's own icon, and the two callbacks that reach back into this program.
func newWindowConfig(ctx context.Context, uiURL string, notes *notices, log *slog.Logger) window.Config {
	return window.Config{
		Title: windowTitle,
		URL:   uiURL,
		Icon:  appIcon(log),
		// No size is given on purpose. The approved design is a 520 CSS-pixel
		// column, and only the window itself can turn that into pixels: it has
		// to know the DPI of the monitor it opened on and the work area it has
		// to fit inside. A number here would be this program guessing at the
		// user's display from the wrong side of the process.
		// Left false deliberately: the user double-clicked an exe and expects
		// a window, not a tray icon and nothing else.
		StartHidden:  false,
		OnHide:       hideNotice(ctx, notes),
		OpenExternal: externalLinks(ctx, log),
		Logger:       log,
	}
}

// desktop is the program's own window plus the goroutine its message loop runs
// on. It is what the tray holds as a [ui.Windower].
//
// Show falls back to the browser once the window is gone, and gone is a real
// possibility rather than a theoretical one: [window.RuntimeInstalled] answers
// from the registry, and a registry that says the Edge WebView2 runtime is
// installed is not a promise that the control can be created. When that turns
// out to be a lie the click still has to go somewhere.
type desktop struct {
	win     *window.Window
	browser func()
	log     *slog.Logger

	// up is true while the message loop is running. It is the only state
	// shared between the tray's goroutine and the window's.
	up atomic.Bool
	wg sync.WaitGroup
}

// start runs the window's message loop on a goroutine of its own — Run locks
// the OS thread it is given — and deals with a machine that turns out not to
// have a runtime after all.
func (d *desktop) start(ctx context.Context, notes *notices) {
	d.up.Store(true)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		err := d.win.Run(ctx)
		d.up.Store(false)
		if err == nil {
			return
		}

		d.log.Error("the window stopped", "err", err)
		if errors.Is(err, window.ErrRuntimeMissing) {
			noWindowHere(ctx, notes, d.browser)
		}
	}()
}

// Show brings the window to the front, or opens the browser when there is no
// longer a window to bring.
func (d *desktop) Show() {
	if d.up.Load() {
		d.win.Show()
		return
	}
	d.browser()
}

// stop closes the window and waits for its message loop to return. Closing
// twice is harmless — [window.Window.Close] is idempotent — and so is stopping
// a loop that has already ended.
func (d *desktop) stop() {
	d.win.Close()
	d.wg.Wait()
}

// noWindowHere is what a machine that cannot show the window gets: the name of
// the missing Microsoft component, and the interface in the browser, exactly
// as the notification promises.
func noWindowHere(ctx context.Context, notes *notices, browser func()) {
	notes.raise(ctx, ui.Notification{Title: noWindowTitle, Body: noWindowBody})
	browser()
}

// hideNotice returns the OnHide the window calls once the X button has hidden
// it. Hiding to the tray is not obvious the first time and obvious every time
// after that, so it is explained once and then never again — a notification on
// every hide would be nagging.
func hideNotice(ctx context.Context, notes *notices) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			notes.raise(ctx, ui.Notification{Title: hiddenTitle, Body: hiddenBody})
		})
	}
}

// externalLinks hands the page's outward links — a Tailscale login URL, a
// `tailscale serve` address — to the machine's own browser, so that clicking
// one does not navigate the app window away from the app.
//
// The URL may be a one-time credential, which is why the opener behind this is
// the one that records only a host, and why the error wrapped here adds words
// of its own rather than the address.
func externalLinks(ctx context.Context, log *slog.Logger) func(string) error {
	opener := ui.NewExternalOpener(log)
	return func(rawURL string) error {
		ctx, cancel := context.WithTimeout(ctx, noticeTimeout)
		defer cancel()

		if err := opener.Open(ctx, rawURL); err != nil {
			return fmt.Errorf("handing a link to the browser: %w", err)
		}
		return nil
	}
}

// browserFallback returns the function that opens the interface the old way:
// the user's own browser at the loopback address. It is what «Открыть» falls
// back to, and what the runtime-missing notification promises.
func browserFallback(ctx context.Context, uiURL string, log *slog.Logger) func() {
	opener := ui.NewSystemOpener()
	return func() {
		if ctx.Err() != nil {
			return // the program is shutting down; there is nothing to open
		}

		ctx, cancel := context.WithTimeout(ctx, noticeTimeout)
		defer cancel()

		if err := opener.Open(ctx, uiURL); err != nil {
			log.Error("opening the interface in the browser", "url", uiURL, "err", err)
		}
	}
}

// appIcon is the picture in the window's title bar and on its taskbar button.
//
// It is the tray's own icon rather than a second one. A .ico file for the
// window would mean a resource section in the exe, a .syso and a build step,
// for a picture this program already draws. The grey unknown-state disc is the
// one used because a window icon never changes, and an icon claiming the link
// was up would be wrong half the time.
func appIcon(log *slog.Logger) []byte {
	ico, err := trayicon.Render(core.StateUnknown)
	if err != nil {
		// A window with no icon is a window with the system default one, which
		// is worth a line in the log and nothing more.
		log.Warn("rendering the window icon", "err", err)
		return nil
	}
	return ico
}

// notices raises Windows notifications from the wiring.
//
// Each one gets a goroutine because the caller may be the window's own
// message-loop thread — OnHide runs there — and a toast shells out to
// PowerShell, which takes about a second. Freezing the window for a second
// just after the user hid it would be a poor way to explain that it is still
// running.
type notices struct {
	notifier ui.Notifier
	log      *slog.Logger
	wg       sync.WaitGroup
}

// raise shows one notification. It never blocks the caller.
func (n *notices) raise(ctx context.Context, note ui.Notification) {
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()

		ctx, cancel := context.WithTimeout(ctx, noticeTimeout)
		defer cancel()

		if err := n.notifier.Notify(ctx, note); err != nil {
			n.log.Warn("showing a notification", "title", note.Title, "err", err)
		}
	}()
}

// wait blocks until every notification has been shown or given up on. A
// cancelled context kills the PowerShell behind one, so shutdown does not wait
// out the timeout.
func (n *notices) wait() { n.wg.Wait() }
