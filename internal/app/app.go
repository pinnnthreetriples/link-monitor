package app

import (
	"context"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// Deps are the adapters the application runs on. Every field is an interface
// declared in this package, so the wiring in cmd/ passes real adapters and a
// test passes fakes. A nil field is allowed: the feature that needed it then
// reports why it cannot run, which is what happens in real life anyway when
// there is no SSH session to the peer.
type Deps struct {
	Tailnet   TailnetProbe
	Remote    RemoteProbe
	Local     LocalServiceProbe
	Services  LocalServiceControl
	Shell     RemoteShell
	Files     FileMover
	Forwarder Forwarder
	Publisher Publisher
	Peers     PeerLister
	// LinkCtl connects and disconnects the tailnet — the same Tailscale client
	// as Tailnet, which is also what tells the controller the current state.
	LinkCtl LinkControl
	// The three halves of the shared folder. All three nil — which is what
	// happens when the user has chosen no folder — leaves the feature off
	// rather than broken, and Folder.On reports as much.
	SyncHere  Tree
	SyncPeer  PeerTrees
	SyncState SyncHistory
	// The two halves of the shared clipboard: this machine's clipboard and the
	// program's own instance on the peer. Both nil leaves the feature
	// unavailable rather than broken, and Clip.Available reports as much.
	// Sharing is enabled at process start when Clip.EnableOnStart is set.
	ClipHere  Clipboard
	ClipPeer  PeerInbox
	ClipSaved SavedScreenshots
}

// Config is what the application needs to know about the two machines and how
// often to look at them.
type Config struct {
	// Local and Peer are the two ends of the link.
	Local core.Machine
	Peer  core.Machine
	// Interval is how often to probe; zero means DefaultInterval.
	Interval time.Duration
	// ProbeTimeout bounds one diagnosis; zero means DefaultProbeTimeout.
	ProbeTimeout time.Duration
	// HistoryWindow is how far the uptime strip reaches back; zero means
	// DefaultHistoryWindow.
	HistoryWindow time.Duration
	// InboxDir is where received files land.
	InboxDir string
	// MaxTransfers is how many recent transfers to remember; zero means the
	// package default.
	MaxTransfers int
	// LinkTimeout bounds one connect or disconnect; zero means
	// DefaultLinkTimeout.
	LinkTimeout time.Duration
	// Sync describes the shared folder. Its zero value — no folder chosen —
	// leaves the feature off, which is what rule 5 asks for.
	Sync FolderConfig
	// Clip describes the shared clipboard. Its zero value is the defaults, and
	// command wiring enables it on startup; the user can pause it.
	Clip ClipConfig
}

// App is everything the UI talks to, assembled once and shut down once.
//
// The fields are exported because the UI and the HTTP API each need a different
// subset of them, and hiding them behind forwarding methods would add a layer
// that only repeats itself.
type App struct {
	Probe     *Probe
	Poller    *Poller
	History   *History
	Fixer     *Fixer
	Transfers *Transfers
	Forwards  *Forwards
	Peers     *PeerService
	Link      *Link
	// Folder is the shared folder. It is always built and is switched off
	// unless a folder was chosen, so the UI has something to ask either way.
	Folder *Folder
	// Clip is the shared clipboard. It is always built, so the UI can show
	// whether startup activation succeeded and offer a pause switch.
	Clip *Clip

	ctx context.Context
}

// New assembles the application. ctx is the process lifetime: Start polls
// against it, and every port forward opened later stops when it ends.
//
// It contacts nothing, so it cannot fail; the first probe happens in Start.
func New(ctx context.Context, cfg Config, d Deps) *App {
	probe := NewProbe(d.Tailnet, d.Remote, d.Local)
	history := NewHistory(cfg.HistoryWindow)
	poller := NewPoller(PollerConfig{
		Probe:    probe,
		Local:    cfg.Local,
		Peer:     cfg.Peer,
		Interval: cfg.Interval,
		Timeout:  cfg.ProbeTimeout,
		History:  history,
	})

	return &App{
		Probe:     probe,
		History:   history,
		Poller:    poller,
		Fixer:     NewFixer(d.Services, d.Shell),
		Transfers: NewTransfers(d.Files, cfg.InboxDir, cfg.MaxTransfers),
		Forwards:  NewForwards(ctx, d.Forwarder, d.Publisher),
		Peers:     NewPeerService(d.Peers),
		// The Tailscale client answers both questions, so the state the
		// controller looks at before acting is the daemon's own.
		Link: NewLink(d.LinkCtl, d.Tailnet, poller, cfg.LinkTimeout),
		Folder: NewFolder(cfg.Sync, FolderDeps{
			Here: d.SyncHere, There: d.SyncPeer, History: d.SyncState,
		}),
		Clip: NewClip(cfg.Clip, ClipDeps{Here: d.ClipHere, There: d.ClipPeer, Saved: d.ClipSaved}),
		ctx:  ctx,
	}
}

// Start begins polling, sweeping the shared folder when one was chosen, and
// watching the clipboard. It returns at once; every loop stops when the lifetime context
// ends.
func (a *App) Start() {
	a.Poller.Start(a.ctx)
	a.Folder.Start(a.ctx)
	a.Clip.Start(a.ctx)
	a.Transfers.Start(a.ctx)
}

// Close stops every port forward and waits for the poller and the folder sweep
// to finish, so that after it returns there is no goroutine of this package
// left running.
//
// It does not cancel the lifetime context — whoever created it owns it — so a
// caller shutting down cancels first and then calls Close.
func (a *App) Close() error {
	err := a.Forwards.CloseAll()
	a.Poller.Wait()
	a.Folder.Wait()
	a.Clip.Wait()
	a.Transfers.Wait()
	return err
}
