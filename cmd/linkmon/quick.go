package main

import (
	"log/slog"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/quickopen"
	"github.com/pinnnthreetriples/link-monitor/internal/ui"
)

// wireQuick builds the tray's three quick actions — a terminal on the peer,
// the shared folder on this machine, the peer's desktop.
//
// The folder comes from -sync-folder, the same flag the shared-folder feature
// reads (see foldersync.go), and not from a second flag of its own: two ways
// to name the same folder is two values to disagree. An unset flag is the
// folder feature switched off, and the item then says so — [quickopen.Config]
// takes the empty string and the tray asks before it opens anything.
//
// A nil result is a tray with no quick-action block at all, and it happens only
// when the program is configured in a way none of the three could honour: no
// peer address, no user, no key. A machine that is merely missing one of the
// programs an action needs still gets the block, because the action that needed
// it says so at the click — see quickopen.New.
//
// Unlike explorerMenu next door this takes no logger. It is called from the
// middle of the tray's own configuration literal in main.go, which is a file
// already at its line limit, and the only logger it would ever be handed is
// slog.Default().
func wireQuick(o options) ui.QuickActions {
	log := slog.Default()
	actions, err := quickopen.New(quickopen.Config{
		PeerName: o.peer.WindowsName,
		PeerUser: o.peer.User,
		PeerAddr: o.peer.Addr,
		KeyPath:  o.keyPath,
		Folder:   strings.TrimSpace(*syncFolder),
	}, log)
	if err != nil {
		log.Warn("the quick actions cannot be offered", "err", err)
		return nil
	}
	return actions
}
