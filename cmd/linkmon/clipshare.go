package main

import (
	"flag"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/clipboard"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/peerclip"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/savedshots"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/sshx"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/winpaths"
	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// The shared clipboard's own flags, registered here so that this feature's
// wiring is one file — flag.Var in an init runs before flag.Parse, which
// parseOptions calls. Every description is Russian, like the rest of them.
//
// Sharing starts off. The window and tray can enable it for the current
// session, and -no-clipboard disables it completely.
//
// -no-clipboard is the other direction, and it is the one thing a command line
// can settle: it leaves the feature unwired, so the switch is unavailable
// rather than merely off and nothing — not even a request to the local API —
// can turn it on.
var (
	clipboardOff = flag.Bool("no-clipboard", false,
		"полностью отключить общий буфер обмена: переключатель будет недоступен")
	clipMaxBytes = flag.Int("clipboard-max-bytes", clipshare.DefaultMaxBytes,
		"наибольший размер текста; PNG-скриншоты имеют отдельный лимит")
	clipPoll = flag.Duration("clipboard-poll", app.DefaultClipPoll,
		"как часто проверять, изменился ли буфер обмена")
)

// clipWiring is the shared clipboard's two ends and the configuration that
// describes them. Every field is zero when -no-clipboard was given, which
// leaves the feature unavailable rather than half-built.
type clipWiring struct {
	cfg   app.ClipConfig
	here  app.Clipboard
	peer  app.PeerInbox
	saved app.SavedScreenshots
}

// wireClip builds the shared clipboard over the SSH client the program already
// keeps to the peer.
//
// It contacts nothing here; startup takes the clipboard sequence as a baseline
// without sharing the content that was copied before launch.
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
		here:  clipboard.New(),
		saved: savedshots.New(winpaths.Screenshots("")),
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
