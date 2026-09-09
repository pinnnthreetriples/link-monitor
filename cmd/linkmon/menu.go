package main

import (
	"fmt"
	"log/slog"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/shellmenu"
	"github.com/pinnnthreetriples/link-monitor/internal/ui"
)

// The actions -menu takes. They are English words on a command line rather
// than Russian ones: nothing the user reads goes through here — the tray's own
// checkable item is how a person switches the Explorer entry — and a flag value
// is code.
const (
	menuInstall = "install"
	menuRemove  = "remove"
	menuStatus  = "status"
)

// menuMode is `linkmon -menu install|remove|status`.
//
// The tray item is how the user turns «Отправить на ПК» on and off. This flag
// exists for the two jobs a tray cannot do: an installer or an uninstaller
// script that has to leave the machine clean without a person clicking
// anything, and checking from outside the program what is actually in the
// registry. It writes its answer to the log rather than to a console, because
// the shipping build is -H=windowsgui and has none — a caller that wants to
// read it redirects stderr.
//
// install and remove also raise the same notification the tray raises, so that
// a person who runs the flag by hand on the GUI build is not left guessing.
func menuMode(action string) int {
	log := slog.Default()

	menu, err := shellmenu.Current()
	if err != nil {
		log.Error("finding this program's own path", "err", err)
		return 1
	}

	installed, err := runMenuAction(menu, action)
	if err != nil {
		log.Error("changing the Explorer menu item", "action", action, "err", err)
		if action != menuStatus {
			tell(ui.NewToastNotifier(""), log, ui.ShellMenuFailure())
		}
		return 1
	}

	log.Info("the Explorer menu item", "action", action, "installed", installed,
		"exe", menu.ExePath(), "label", menu.Label())
	if action != menuStatus {
		tell(ui.NewToastNotifier(""), log, ui.ShellMenuNotice(installed))
	}
	return 0
}

// runMenuAction carries one action out and reports the state it left behind.
// The state is read from the registry afterwards rather than assumed, so that
// what is logged and what is announced are what is really there.
func runMenuAction(menu *shellmenu.Menu, action string) (bool, error) {
	switch action {
	case menuInstall:
		if err := menu.Install(); err != nil {
			return false, fmt.Errorf("installing the Explorer menu item: %w", err)
		}
	case menuRemove:
		if err := menu.Remove(); err != nil {
			return false, fmt.Errorf("removing the Explorer menu item: %w", err)
		}
	case menuStatus:
		// Nothing to do: the state is read below, which is all -menu status is.
	default:
		return false, fmt.Errorf("unknown -menu action %q: use %s, %s or %s",
			action, menuInstall, menuRemove, menuStatus)
	}

	installed, err := menu.Installed()
	if err != nil {
		return false, fmt.Errorf("reading back the Explorer menu item: %w", err)
	}
	return installed, nil
}

// explorerMenu is the tray's handle on Explorer's «Отправить на ПК» item.
//
// A nil result is a tray with no such entry in its menu, and it happens only
// when this program cannot work out its own path — without which the command
// written into the registry would point nowhere. Offering a control that
// installs a broken menu item would be worse than not offering it.
func explorerMenu(log *slog.Logger) ui.ShellMenu {
	menu, err := shellmenu.Current()
	if err != nil {
		log.Warn("Explorer's right-click item cannot be offered", "err", err)
		return nil
	}
	return menu
}
