package ui

import (
	"context"
	"strings"
)

// QuickActions are the tray's three ways out of this program and onto the
// other machine: a terminal already logged in to the peer, the shared folder
// on this machine, and the peer's desktop. internal/adapters/quickopen
// implements them, and its package comment argues why they are an adapter
// rather than a third kind of process started from here.
//
// Every method starts a window and returns as soon as it has been asked for,
// without waiting for it to appear or for the user to finish with it. A
// context is still taken because the tray bounds a menu click, and refusing to
// start something after the user has given up waiting is the honest answer.
//
// SharedFolder is asked rather than assumed for the same reason
// [ShellMenu.Installed] is: the tray has to tell «the folder is not
// configured» apart from «the folder would not open», and those are two
// different sentences to a user who has just clicked the item.
type QuickActions interface {
	// OpenTerminal opens a terminal logged in to the peer, as the program
	// itself logs in — the peer's account, and the key from the program's own
	// configuration.
	OpenTerminal(ctx context.Context) error
	// OpenSharedFolder opens this machine's shared folder in Explorer.
	OpenSharedFolder(ctx context.Context) error
	// OpenPeerDesktop opens the peer's desktop.
	OpenPeerDesktop(ctx context.Context) error
	// SharedFolder is the folder OpenSharedFolder would open, or "" when the
	// user has configured none.
	SharedFolder() string
}

// openTerminal is «Открыть терминал на ПК».
func (c *controller) openTerminal(ctx context.Context) {
	if c.quick == nil {
		c.warnUnwired(menuTerminal)
		return
	}
	c.quickAction(ctx, menuTerminal, c.quick.OpenTerminal,
		Notification{Title: quickTermFailTitle, Body: quickTermFailBody})
}

// openSharedFolder is «Открыть общую папку».
//
// The folder is checked before anything is started. A folder that was never
// configured is not a failure — nothing is broken, the feature was simply
// never switched on — so the item stays in the menu and answers the question
// the click was asking. Leaving the item out instead would tell a user who
// expected a shared folder nothing at all.
func (c *controller) openSharedFolder(ctx context.Context) {
	if c.quick == nil {
		c.warnUnwired(menuFolder)
		return
	}
	if strings.TrimSpace(c.quick.SharedFolder()) == "" {
		c.log.Info("the shared folder was asked for before one was configured", "item", menuFolder)
		c.notify(ctx, Notification{Title: quickNoFolderTitle, Body: quickNoFolderBody})
		return
	}
	c.quickAction(ctx, menuFolder, c.quick.OpenSharedFolder,
		Notification{Title: quickFolderFailTitle, Body: quickFolderFailBody})
}

// openPeerDesktop is «Открыть рабочий стол ПК».
func (c *controller) openPeerDesktop(ctx context.Context) {
	if c.quick == nil {
		c.warnUnwired(menuDesktop)
		return
	}
	c.quickAction(ctx, menuDesktop, c.quick.OpenPeerDesktop,
		Notification{Title: quickDesktopFailTitle, Body: quickDesktopFailBody})
}

// quickAction runs one quick action and makes sure the user hears about a
// failure.
//
// A click that does nothing is the one unacceptable outcome: there is no
// console in this build and no window necessarily open, so a menu item that
// quietly failed is indistinguishable from a menu item that is broken. Hence
// both the log line, which carries the technical detail, and the notification,
// which carries the Russian sentence.
func (c *controller) quickAction(
	ctx context.Context,
	item string,
	run func(context.Context) error,
	failure Notification,
) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	err := run(callCtx)
	cancel()
	if err == nil {
		return
	}
	c.log.Error("running a quick action", "item", item, "error", err)
	// The notification goes on the outer context: the deadline that just
	// expired may be exactly why there is something to report.
	c.notify(ctx, failure)
}

// warnUnwired records a quick action that has nothing behind it. It cannot
// happen through the menu — the block is left out when Config.Quick is nil —
// but the controller's methods are also reachable directly, and a silent
// return would be the wrong answer if they ever were.
func (c *controller) warnUnwired(item string) {
	c.log.Warn("a quick action is not wired to anything", "item", item)
}
