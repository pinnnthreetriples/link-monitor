package main

import (
	"flag"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/clipboard"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/peerclip"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/sshx"
	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// The shared clipboard's own flags, registered here so that this feature's
// wiring is one file — flag.Var in an init runs before flag.Parse, which
// parseOptions calls. Every description is Russian, like the rest of them.
//
// Note what is *not* here: a flag that switches sharing on. Rule 1 says the
// clipboard is never shared until the user turns it on, and it is deliberately
// not something a shortcut, an installer or a stale command line can decide
// once and for all. The switch lives in the window and in the tray, it starts
// off every time the program starts, and it is not written down anywhere — so a
// machine that reboots comes back with the clipboard private again.
//
// -no-clipboard is the other direction, and it is the one thing a command line
// can settle: it leaves the feature unwired, so the switch is unavailable
// rather than merely off and nothing — not even a request to the local API —
// can turn it on.
var (
	clipboardOff = flag.Bool("no-clipboard", false,
		"полностью отключить общий буфер обмена: переключатель будет недоступен")
	clipMaxBytes = flag.Int("clipboard-max-bytes", clipshare.DefaultMaxBytes,
		"наибольший размер текста, который передаётся; больше — пропускается и считается в окне")
	clipPoll = flag.Duration("clipboard-poll", app.DefaultClipPoll,
		"как часто проверять, изменился ли буфер обмена")
)

// clipWiring is the shared clipboard's two ends and the configuration that
// describes them. Every field is zero when -no-clipboard was given, which
// leaves the feature unavailable rather than half-built.
type clipWiring struct {
	cfg  app.ClipConfig
	here app.Clipboard
	peer app.PeerInbox
}

// wireClip builds the shared clipboard over the SSH client the program already
// keeps to the peer.
//
// It contacts nothing and reads nothing: the clipboard is not touched until the
// user switches sharing on, and the peer is not asked about its instance until
// there is an item to deliver.
func wireClip(o options, ssh *sshx.Lazy) clipWiring {
	if *clipboardOff {
		return clipWiring{}
	}
	return clipWiring{
		cfg: app.ClipConfig{
			PeerName: o.peer.TailnetName,
			MaxBytes: *clipMaxBytes,
			Poll:     clipPollOr(*clipPoll),
		},
		here: clipboard.New(),
		// One argument twice on purpose: *sshx.Lazy is both the shell that
		// reads the peer's endpoint record and the forwarder that carries the
		// request to it, and peerclip declares them separately so that neither
		// half has to name a concrete type.
		peer: peerclip.New(ssh, ssh, o.peer.TailnetName),
	}
}

// clipPollOr refuses a poll interval that would be a mistake rather than a
// setting. Zero or less means the default; anything under a tenth of a second
// would read the sequence number ten times a second for no benefit a person
// could perceive, so it is raised to the floor rather than honoured.
func clipPollOr(d time.Duration) time.Duration {
	const floor = 100 * time.Millisecond
	if d <= 0 {
		return app.DefaultClipPoll
	}
	if d < floor {
		return floor
	}
	return d
}
