package ui

import "context"

// ClipShare is the shared clipboard, as the tray switches it on and off.
// *app.Clip implements it, and cmd/linkmon hands it over.
//
// Four methods, and not one of them can answer with what was copied. That is
// deliberate, and it is what keeps this file outside the list in
// tools/gates/noclipleak_test.go without weakening rule 4: the tray logs a
// switch that would not move, the way it logs every other menu item, and there
// is nothing in scope here for such a line to carry. The feature's own Status()
// would be safe too — it holds counts and sizes and never content — but the
// tray has nothing to draw with it, so it is not asked for.
//
// No method takes a context, for the same reason [ShellMenu]'s do not: each is
// a mutex and, at most, one Win32 read on this machine, with no network in
// between, so a context here would be a cancellation nothing could honour.
//
// On is asked rather than remembered, exactly as [ShellMenu.Installed] is. The
// window carries this same switch and the local API carries the same two
// routes, so the tray's tick mark is never the state — and a tick that
// disagrees with the feature is worse than no tick at all.
type ClipShare interface {
	// Available reports whether this machine has a clipboard to share and the
	// peer an instance to share it with. False is the whole feature missing
	// rather than switched off, and it is settled by how the program was
	// started; see [Config.Clip] for what the tray does about it.
	Available() bool
	// On reports whether the user has switched sharing on. It starts false
	// every time the program starts, and nothing but a user action sets it.
	On() bool
	// TurnOn starts sharing and reports whether it could. It fails when the
	// clipboard itself cannot be reached — a locked screen, or a session other
	// than the one the user is sitting at.
	TurnOn() error
	// TurnOff stops sharing at once and cannot fail. The tray has to keep that
	// promise as well as state it: one click, nothing asked first, nothing to
	// go wrong.
	TurnOff()
}

// syncClip makes the tick mark on «Общий буфер обмена» agree with the feature
// itself. It is called when the menu appears and again after every click,
// failed ones included, for the reason syncShell is: the window and the local
// API switch the same thing, so the tick the tray drew is never what it reads.
func (c *controller) syncClip() {
	if c.clip == nil {
		return
	}
	c.tray.SetClipChecked(c.clip.On())
}

// toggleClip is what a click on «Общий буфер обмена» does: switch sharing off
// when it is on, on when it is off, and say which happened.
//
// Off is the direction that matters, and it is the short branch on purpose. It
// asks nothing, waits for nothing and cannot fail, so a user who wants the
// clipboard private again gets it from one click whatever else is wrong with
// the machine. Turning it on is the branch that is allowed to refuse.
//
// The direction comes from On() at the moment of the click rather than from the
// tick mark, because between the menu being built and the click the same switch
// may have been thrown in the window or through the local API.
func (c *controller) toggleClip(ctx context.Context) {
	if c.clip == nil {
		c.log.Warn("the shared-clipboard item is not wired to anything", "item", menuClip)
		return
	}

	if c.clip.On() {
		c.clip.TurnOff()
		c.syncClip()
		c.notify(ctx, clipNotice(false))
		return
	}

	if err := c.clip.TurnOn(); err != nil {
		// There is no console in this build and no window necessarily open, so
		// silence is the one unacceptable outcome. Available() is deliberately
		// not consulted first: TurnOn is the call that knows, and it answers
		// for both the feature being absent and the clipboard being out of
		// reach right now.
		c.log.Error("switching the shared clipboard on", "item", menuClip, "error", err)
		c.notify(ctx, clipFailure())
		c.syncClip()
		return
	}

	c.syncClip()
	c.notify(ctx, clipNotice(true))
}
