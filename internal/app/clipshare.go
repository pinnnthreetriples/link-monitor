package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

const (
	// DefaultClipPoll is how often the clipboard's sequence number is read.
	//
	// It is short because a shared clipboard that arrives a second late is one
	// people stop trusting, and it costs almost nothing: reading the sequence
	// number opens nothing, locks nobody out and touches no content. The
	// clipboard itself is read only when that number has moved.
	DefaultClipPoll = 400 * time.Millisecond

	// DefaultClipTimeout bounds one delivery to the peer — two SSH round trips
	// and one POST through a forwarded port. Past it the item is abandoned
	// rather than left to queue up behind the next copy.
	DefaultClipTimeout = 30 * time.Second

	// clipEventsKept is how many recent items the window is told about. The
	// list carries what happened and how big it was, never what it was, and a
	// short one is enough to see the feature working.
	clipEventsKept = 20
)

var (
	// ErrClipOff means the shared clipboard is switched off, so nothing may be
	// written to this machine's clipboard. Rule 1: off is the default, and off
	// means nothing happens — including nothing arriving.
	ErrClipOff = errors.New("app: the shared clipboard is switched off")

	// ErrClipTooBig means an arriving item is past this machine's own cap. The
	// sender caps too; this is the end that has to be right.
	ErrClipTooBig = errors.New("app: the arriving clipboard item is past the size cap")

	// ErrClipUnavailable means this build or this machine has no clipboard to
	// share — away from Windows, or in a session with no clipboard at all.
	ErrClipUnavailable = errors.New("app: there is no clipboard to share on this machine")
)

// Clipboard is this machine's clipboard, as the loop needs it.
// *clipboard.Clipboard implements it.
type Clipboard interface {
	// Sequence is the counter Windows bumps whenever the clipboard changes. It
	// is what the loop polls, so that the contents are read only when there is
	// something new to read.
	Sequence() (uint32, error)
	// Look reports what the clipboard offers: whether it is text, how big it
	// is, whether Windows' own markers allow it to be recorded, and the
	// content itself when it is text within maxBytes. An item past the cap is
	// measured and never copied out.
	Look(maxBytes int) (clipshare.Snapshot, error)
	// Put replaces the clipboard's contents with text.
	Put(text []byte) error
}

// PeerInbox is the program's own instance on the other machine.
// *peerclip.Peer implements it.
//
// Deliver must report a peer with no instance running as an error
// localendpoint.NotRunning answers for, because that is a normal state — the
// feature needs the program on both machines — and the window says so rather
// than showing a fault.
type PeerInbox interface {
	Deliver(ctx context.Context, text []byte) error
	Name() string
}

// ClipConfig describes the shared clipboard.
type ClipConfig struct {
	// PeerName is what the UI calls the other machine.
	PeerName string
	// MaxBytes is rule 2's cap on one item; zero means
	// clipshare.DefaultMaxBytes.
	MaxBytes int
	// Poll is how often the sequence number is read; zero means
	// DefaultClipPoll.
	Poll time.Duration
	// Timeout bounds one delivery; zero means DefaultClipTimeout.
	Timeout time.Duration
}

// ClipDeps are the two ends and the clock.
type ClipDeps struct {
	Here  Clipboard
	There PeerInbox
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// ClipEventKind is what became of one item. Every one of these is a count the
// window shows, and none of them says anything about content.
type ClipEventKind string

const (
	// ClipSent went to the peer.
	ClipSent ClipEventKind = "sent"
	// ClipReceived arrived from the peer and is on this clipboard now.
	ClipReceived ClipEventKind = "received"
	// ClipTooBig is rule 2's cap doing its work.
	ClipTooBig ClipEventKind = "too_big"
	// ClipMarked is rule 3's: the item asked not to be recorded.
	ClipMarked ClipEventKind = "marked"
	// ClipFailed is a delivery that could not be made.
	ClipFailed ClipEventKind = "failed"
)

// ClipEvent is one line of the window's list: when, what happened, and how
// many bytes. Never what.
type ClipEvent struct {
	At    time.Time
	Kind  ClipEventKind
	Bytes int
}

// ClipCounts is the tally rules 2 and 3 ask to be visible. NotText is counted
// but has no event of its own: copying a file or a picture is not an incident,
// and a line for every one of them would bury the two that matter.
type ClipCounts struct {
	Sent     int
	Received int
	TooBig   int
	Marked   int
	NotText  int
	Failed   int
}

// ClipStatus is everything the window shows about the shared clipboard.
type ClipStatus struct {
	// Available says this machine has a clipboard this program can share and
	// a peer to share it with. False is the whole feature missing, not off.
	Available bool
	// On says the user has switched it on. Rule 1: this starts false every
	// time the program starts, and nothing but a user action sets it.
	On       bool
	PeerName string
	MaxBytes int
	// PeerMissing says the last attempt found no instance running on the peer.
	// It is a normal state and not a failure: the feature needs the program on
	// both machines.
	PeerMissing bool
	Counts      ClipCounts
	Events      []ClipEvent
	// Err is one Russian sentence when something actually went wrong.
	Err string
}

// Clip is the shared clipboard: one goroutine watching this machine's
// clipboard for as long as the program runs, and switched off until the user
// says otherwise.
//
// Two mutexes, and the difference between them is the point. mu guards the
// state the window reads. access serialises the clipboard itself, so that the
// loop cannot be reading it while an arriving item is being written — the
// window in which the echo defence would otherwise have a hole. Neither is
// ever held across the network.
type Clip struct {
	cfg  ClipConfig
	deps ClipDeps
	wg   sync.WaitGroup

	access sync.Mutex

	mu           sync.Mutex
	on           bool
	started      bool
	stopped      bool
	baseline     uint32
	haveBaseline bool
	mem          clipshare.Memory
	counts       ClipCounts
	events       []ClipEvent
	failure      string
	peerMissing  bool
}

// NewClip prepares the shared clipboard. It reads nothing and sends nothing
// until the user switches it on, which is rule 1 and is not configurable.
func NewClip(cfg ClipConfig, d ClipDeps) *Clip {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = clipshare.DefaultMaxBytes
	}
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultClipPoll
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultClipTimeout
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if cfg.PeerName == "" && d.There != nil {
		cfg.PeerName = d.There.Name()
	}
	return &Clip{cfg: cfg, deps: d}
}

// Available reports whether there is a clipboard to share and somebody to
// share it with. It is false in a build with no clipboard, and it is not the
// same as being switched off.
func (c *Clip) Available() bool { return c.deps.Here != nil && c.deps.There != nil }

// On reports whether the user has switched sharing on.
func (c *Clip) On() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.on
}

// TurnOn starts sharing, and reports whether it could.
//
// The clipboard's current contents are deliberately not shared: switching the
// feature on takes the sequence number as it stands, so the first thing that
// travels is the next thing the user copies. Whatever was already on the
// clipboard when they reached for the switch was copied before they turned
// anything on, and sending it would be this program deciding they meant to.
func (c *Clip) TurnOn() error {
	if !c.Available() {
		return ErrClipUnavailable
	}
	seq, err := c.deps.Here.Sequence()
	if err != nil {
		return ErrClipUnavailable
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.on = true
	c.baseline, c.haveBaseline = seq, true
	c.failure = ""
	return nil
}

// TurnOff stops sharing at once. It is one action and it always succeeds:
// rule 5 asks for a switch that cannot fail to turn off.
//
// The memory of what was last sent and last planted is kept. Turning the
// feature off and on again is a pause, not a reset, and forgetting would mean
// the first copy after the pause could bounce.
func (c *Clip) TurnOff() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.on = false
	c.haveBaseline = false
}

// Status reports everything the window shows. The event list is a copy: the
// loop keeps writing to its own.
func (c *Clip) Status() ClipStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	return ClipStatus{
		Available:   c.deps.Here != nil && c.deps.There != nil,
		On:          c.on,
		PeerName:    c.cfg.PeerName,
		MaxBytes:    c.cfg.MaxBytes,
		PeerMissing: c.peerMissing,
		Counts:      c.counts,
		Events:      append([]ClipEvent(nil), c.events...),
		Err:         c.failure,
	}
}

// Start launches the watching goroutine. Calling it twice is a no-op, and it
// starts even with sharing off — the loop does nothing until it is on, and
// there is then no goroutine to start at the moment the user flips a switch.
func (c *Clip) Start(ctx context.Context) {
	if !c.Available() {
		return
	}
	c.mu.Lock()
	if c.started || c.stopped {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()

	c.wg.Add(1)
	go c.loop(ctx)
}

// Wait blocks until the watching goroutine has finished, so a test can prove
// nothing is left running.
func (c *Clip) Wait() { c.wg.Wait() }
