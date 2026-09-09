package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/ui/trayicon"
)

// defaultTimeout bounds one call out of the tray — opening a browser, showing
// a notification, asking for a probe. None of them should take this long; the
// point is that a hung one cannot wedge the menu forever.
const defaultTimeout = 15 * time.Second

// defaultReadyGrace is how long a cancelled tray waits for the tray library to
// finish coming up. Quitting a tray that has not yet created its window is a
// no-op on Windows, which would leave the event loop running with nothing to
// stop it, so we wait briefly for the window rather than racing it.
const defaultReadyGrace = 5 * time.Second

// errNoSource is returned when nothing would ever change the icon.
var errNoSource = errors.New("a tray without a status source would never change colour")

// controller is everything the tray does that is worth testing: turning a
// status into an icon, a tooltip and sometimes a notification, and turning a
// menu click into a call on the rest of the program. It talks to the tray
// library only through [tray], so tests drive it with a fake.
type controller struct {
	tray     tray
	source   Source
	actions  Actions
	opener   Opener
	notifier Notifier
	window   Windower
	shell    ShellMenu
	quick    QuickActions
	clip     ClipShare
	policy   *notifyPolicy

	uiURL   string
	timeout time.Duration
	grace   time.Duration
	now     func() time.Time
	log     *slog.Logger
}

// newController validates a Config and fills in every default.
func newController(cfg Config, t tray) (*controller, error) {
	if cfg.Source == nil {
		return nil, errNoSource
	}
	if err := checkLoopbackURL(cfg.UIURL); err != nil {
		return nil, fmt.Errorf("the window URL %q: %w", cfg.UIURL, err)
	}
	c := &controller{
		tray:     t,
		source:   cfg.Source,
		actions:  cfg.Actions,
		opener:   cfg.Opener,
		notifier: cfg.Notifier,
		// A nil Window is not a missing dependency. It is a machine with no
		// Edge WebView2 runtime, or a deliberate `-browser`, and it is what
		// makes «Открыть» hand the UI to the browser instead.
		window: cfg.Window,
		// A nil ShellMenu is a tray with no Explorer item to switch, a nil
		// Quick is a tray with no way onto the other machine, and a nil Clip is
		// a tray with no clipboard to share. The menu is built without the
		// block in every case; see Config.ShellMenu, Config.Quick and
		// Config.Clip. The fields are still kept, because these methods are
		// reachable directly and a nil one has to answer for itself.
		shell:   cfg.ShellMenu,
		quick:   cfg.Quick,
		clip:    cfg.Clip,
		policy:  newNotifyPolicy(cfg.Debounce),
		uiURL:   cfg.UIURL,
		timeout: cfg.Timeout,
		grace:   defaultReadyGrace,
		now:     cfg.Now,
		log:     cfg.Logger,
	}
	if c.opener == nil {
		c.opener = NewSystemOpener()
	}
	if c.notifier == nil {
		c.notifier = NewToastNotifier(cfg.ToastAppID)
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	return c, nil
}

// seed paints the tray before the first status arrives, so the icon is never
// the blank square Windows shows for an icon nobody set.
func (c *controller) seed() {
	c.paint(Status{Overall: core.StateUnknown, Summary: startingSummary})
}

// apply is the whole job: a new status becomes a colour, a tooltip, and — only
// when the policy says the user needs to know — a notification.
func (c *controller) apply(ctx context.Context, st Status) {
	c.paint(st)
	if !c.policy.consider(c.now(), st) {
		return
	}
	c.notify(ctx, notificationFor(st))
}

// paint pushes the icon and the tooltip for a status.
func (c *controller) paint(st Status) {
	icon, err := trayicon.Render(st.Overall)
	if err != nil {
		// The tooltip still carries the truth, so this is worth reporting but
		// not worth stopping for.
		c.log.Error("rendering the tray icon", "state", st.Overall.String(), "error", err)
	} else {
		c.tray.SetIcon(icon)
	}
	c.tray.SetTooltip(tooltip(st))
}

func (c *controller) notify(ctx context.Context, n Notification) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.notifier.Notify(ctx, n); err != nil {
		c.log.Warn("showing a notification", "title", n.Title, "error", err)
	}
}

// show is what «Открыть» and a left click on the tray icon both do: bring the
// program's own window to the front.
//
// The browser is the exception rather than the alternative. It is what a
// machine with no Edge WebView2 runtime gets, and what `-browser` asks for; on
// every other machine the point of this program is that no browser appears.
func (c *controller) show(ctx context.Context) {
	if c.window != nil {
		c.window.Show()
		return
	}
	c.open(ctx, c.uiURL)
}

// open hands a URL to the browser. A failure is logged rather than returned:
// there is nowhere to return it to from a menu click.
func (c *controller) open(ctx context.Context, rawURL string) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.opener.Open(ctx, rawURL); err != nil {
		c.log.Error("opening the window", "url", rawURL, "error", err)
	}
}

// checkNow asks for an immediate probe.
func (c *controller) checkNow(ctx context.Context) {
	if c.actions == nil {
		c.log.Warn("the immediate-check menu item is not wired to anything", "item", menuCheck)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.actions.CheckNow(ctx); err != nil {
		c.log.Error("running an immediate check", "error", err)
	}
}

// syncShell makes the tick mark on the Explorer-menu item agree with the
// registry. It is called when the menu appears and after every attempt to
// change the item, including a failed one: a checkbox that claims something the
// registry does not say is worse than no checkbox.
//
// A failure to read is logged and nothing else. The item is left as it was
// drawn, which is the only honest thing to do when the answer is unknown, and
// the user has not asked for anything yet.
func (c *controller) syncShell() {
	if c.shell == nil {
		return
	}
	installed, err := c.shell.Installed()
	if err != nil {
		c.log.Warn("looking for the Explorer menu item", "item", menuShell, "error", err)
		return
	}
	c.tray.SetShellChecked(installed)
}

// toggleShell is what a click on the Explorer-menu item does: put the item in
// Explorer's menu when it is not there, take it out when it is, and say which
// happened.
//
// The registry is asked first rather than trusted from the tick mark. Between
// the tray coming up and the click, the user may have run the exe's -menu flag,
// installed a new copy of the program, or edited the registry by hand — and a
// toggle that acted on a stale belief would install what is already installed
// or, worse, remove what the user had just switched on.
func (c *controller) toggleShell(ctx context.Context) {
	if c.shell == nil {
		c.log.Warn("the Explorer-menu item is not wired to anything", "item", menuShell)
		return
	}

	installed, err := c.shell.Installed()
	if err == nil {
		err = c.applyShell(installed)
	}
	if err != nil {
		// There is no console here and no window necessarily open, so the one
		// unacceptable outcome would be silence.
		c.log.Error("switching the Explorer menu item", "item", menuShell, "error", err)
		c.notify(ctx, ShellMenuFailure())
		c.syncShell()
		return
	}

	c.tray.SetShellChecked(!installed)
	c.notify(ctx, ShellMenuNotice(!installed))
}

// applyShell installs the Explorer item or removes it, whichever the current
// state calls for.
func (c *controller) applyShell(installed bool) error {
	if installed {
		if err := c.shell.Remove(); err != nil {
			return fmt.Errorf("removing the Explorer menu item: %w", err)
		}
		return nil
	}
	if err := c.shell.Install(); err != nil {
		return fmt.Errorf("installing the Explorer menu item: %w", err)
	}
	return nil
}

// serve waits for the menu, paints a starting icon, then handles events until
// the context is done or the user chooses «Выход».
func (c *controller) serve(ctx context.Context, ready <-chan menuClicks) {
	var clicks menuClicks
	select {
	case clicks = <-ready:
	case <-ctx.Done():
		// See defaultReadyGrace: quitting before the tray window exists does
		// nothing, so give it a moment to appear and then quit for real.
		select {
		case <-ready:
		case <-time.After(c.grace):
		}
		return
	}

	c.seed()
	c.syncShell()
	c.syncClip()
	c.pump(ctx, c.source.Subscribe(ctx), clicks)
}

// pump is the event loop. It returns when the context is done or the user
// chooses «Выход»; the caller quits the tray on the way out either way.
//
// cyclop counts one branch per arm and calls the result complex. What it is
// measuring here is the number of things the user can click: the arms do not
// nest, none of them tests anything, and every one is a single call on a
// method next door. The two ways to make the number smaller would both make
// the code worse — a second select on its own goroutine, which is a
// concurrency bug waiting for a maintainer, or fan-in goroutines forwarding
// three menu channels onto one, which is three goroutines and a merge to
// reason about instead of three lines to read. The loop is what it looks like.
//
//nolint:cyclop // one arm per clickable thing; see above
func (c *controller) pump(ctx context.Context, updates <-chan Status, clicks menuClicks) {
	for {
		select {
		case <-ctx.Done():
			return
		case st, ok := <-updates:
			if !ok {
				// The source is finished. Keep the menu alive: the user can
				// still read the last state and quit.
				updates = nil
				continue
			}
			c.apply(ctx, st)
		case <-clicks.open:
			c.show(ctx)
		case <-clicks.icon:
			c.show(ctx)
		case <-clicks.check:
			c.checkNow(ctx)
		case <-clicks.shell:
			c.toggleShell(ctx)
		case <-clicks.clip:
			c.toggleClip(ctx)
		case <-clicks.terminal:
			c.openTerminal(ctx)
		case <-clicks.folder:
			c.openSharedFolder(ctx)
		case <-clicks.desktop:
			c.openPeerDesktop(ctx)
		case <-clicks.quit:
			return
		}
	}
}
