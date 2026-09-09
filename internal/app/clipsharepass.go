package app

import (
	"context"
	"fmt"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// loop is the only goroutine that reads this machine's clipboard.
func (c *Clip) loop(ctx context.Context) {
	defer c.wg.Done()
	defer c.shutdown()

	ticker := time.NewTicker(c.cfg.Poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

// shutdown marks the loop gone, so a later Start does not begin a second one,
// and switches sharing off — a loop that has stopped cannot share anything,
// and a window still showing «включён» would be lying.
func (c *Clip) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.started, c.stopped, c.on, c.haveBaseline = false, true, false, false
}

// item is one clipboard change, already decided about.
type item struct {
	snap clipshare.Snapshot
	mark clipshare.Print
	why  clipshare.Why
}

// tick is one poll: has the clipboard changed, and if so what should happen.
//
// Reading the clipboard and deciding happen under access, so that an arriving
// item being written cannot land in the middle of them; the delivery, which
// takes SSH round trips, happens outside it.
func (c *Clip) tick(ctx context.Context) {
	if !c.On() {
		return
	}
	c.access.Lock()
	got, ok := c.take()
	c.access.Unlock()
	if !ok {
		return
	}
	// The content is destroyed here whatever happens to it, including on the
	// path where it was sent: from this point the print is all that is kept.
	defer clipshare.Zero(got.snap.Text)

	if !got.why.Sends() {
		c.note(got.why, got.snap.Bytes)
		return
	}
	c.deliver(ctx, got)
}

// take reads the clipboard if it has changed and decides about what it found.
// It reports false when there is nothing to do — which is the common case, and
// the case in which the clipboard is not read at all.
func (c *Clip) take() (item, bool) {
	seq, err := c.deps.Here.Sequence()
	if err != nil {
		c.fail(msgClipNoClipboard)
		return item{}, false
	}
	if !c.moved(seq) {
		return item{}, false
	}
	snap, err := c.deps.Here.Look(c.cfg.MaxBytes)
	if err != nil {
		c.fail(msgClipReadFailed)
		return item{}, false
	}
	why, mark := clipshare.Decide(snap, c.memory(), c.cfg.MaxBytes)
	return item{snap: snap, mark: mark, why: why}, true
}

// moved reports whether the clipboard has changed since the last look, and
// records the new position either way.
//
// With no baseline — the feature has just been switched on, or the loop has
// just started — the answer is no, and the number is remembered. That is what
// makes switching the feature on not share whatever happened to be on the
// clipboard already.
func (c *Clip) moved(seq uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	was, had := c.baseline, c.haveBaseline
	c.baseline, c.haveBaseline = seq, true
	return had && was != seq
}

// memory is what the loop remembers: the last item sent, and the last planted.
func (c *Clip) memory() clipshare.Memory {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.mem
}

// deliver hands one item to the peer's instance and records what came of it.
func (c *Clip) deliver(ctx context.Context, got item) {
	sendCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	err := c.deps.There.Deliver(sendCtx, got.snap.Text)
	switch {
	case err == nil:
		c.sent(got.mark, got.snap.Bytes)
	case ctx.Err() != nil:
		// The program is shutting down. Whatever this item was, there is
		// nothing worth putting on a window that is about to disappear.
	case localendpoint.NotRunning(err):
		// A normal state, not a fault: the feature needs the program running
		// on both machines, and the window says exactly that.
		c.peerGone()
	default:
		c.failed(msgClipSendFailed, got.snap.Bytes)
	}
}

// Receive puts an item that arrived from the peer on this machine's clipboard.
//
// This is the receiving half of the echo defence, and the order inside it is
// the defence. The item's print is remembered *before* the clipboard is
// written, so there is no instant in which the loop could read the new
// contents without already knowing they are ours; the sequence number is taken
// afterwards and becomes the new baseline, so in the ordinary case the loop
// sees no change at all. Both are needed: the first is what holds when
// something else changes the clipboard in the same moment.
//
// It refuses while sharing is off, which is rule 1 seen from this side: a
// feature that is switched off does not put things on the user's clipboard.
func (c *Clip) Receive(text []byte) error {
	if !c.Available() {
		return ErrClipUnavailable
	}
	if !c.On() {
		return ErrClipOff
	}
	if len(text) > c.cfg.MaxBytes {
		c.note(clipshare.WhyTooBig, len(text))
		return fmt.Errorf("%w: %d bytes", ErrClipTooBig, len(text))
	}

	c.access.Lock()
	defer c.access.Unlock()

	c.plant(clipshare.Fingerprint(text))
	if err := c.deps.Here.Put(text); err != nil {
		c.fail(msgClipPutFailed)
		return fmt.Errorf("putting the arriving item on this machine's clipboard: %w", err)
	}
	c.rebase()
	c.received(len(text))
	return nil
}

// plant remembers the print of an item about to be written here, so the change
// it causes is recognised as ours rather than as a new copy.
func (c *Clip) plant(mark clipshare.Print) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.mem = c.mem.AfterPlanting(mark)
}

// rebase moves the baseline to where the clipboard is now, so the change the
// write just caused is never even looked at.
//
// A sequence number that cannot be read is not an error here: the item has
// already been delivered, and the print recorded by plant is what stops it
// bouncing. The baseline is cleared instead, which makes the next poll adopt
// whatever it finds without acting on it.
func (c *Clip) rebase() {
	seq, err := c.deps.Here.Sequence()

	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		c.haveBaseline = false
		return
	}
	c.baseline, c.haveBaseline = seq, true
}

// sent records an item that reached the peer.
func (c *Clip) sent(mark clipshare.Print, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.mem = c.mem.AfterSending(mark)
	c.counts.Sent++
	c.failure, c.peerMissing = "", false
	c.record(ClipSent, size)
}

// received records an item that arrived and is on this clipboard now.
func (c *Clip) received(size int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.counts.Received++
	c.failure = ""
	c.record(ClipReceived, size)
}

// note records an item that went nowhere, and why.
//
// The two refusals rules 2 and 3 ask to be visible get a line of their own as
// well as a count. An echo and a repeat of the last item get neither: they are
// the mechanism working, not something that happened to the user. A file or a
// picture is counted and no more — a line for every one of them would bury the
// two that matter.
func (c *Clip) note(why clipshare.Why, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch why {
	case clipshare.WhyTooBig:
		c.counts.TooBig++
		c.record(ClipTooBig, size)
	case clipshare.WhyMarked:
		c.counts.Marked++
		c.record(ClipMarked, 0)
	case clipshare.WhyNotText:
		c.counts.NotText++
	case clipshare.WhySend, clipshare.WhyEmpty, clipshare.WhyEcho, clipshare.WhySame:
		// Nothing to say. An empty clipboard, an echo and a repeat are all the
		// feature behaving; WhySend never reaches here.
	}
}

// fail records a failure with nothing to count.
func (c *Clip) fail(message string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failure = message
}

// failed records a failed delivery, which is both a failure and a line.
func (c *Clip) failed(message string, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failure = message
	c.counts.Failed++
	c.record(ClipFailed, size)
}

// peerGone records that the peer has no instance running. It is not a failure
// and does not clear the counts; the window says what to do about it.
func (c *Clip) peerGone() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.peerMissing, c.failure = true, ""
}

// record appends one line to the window's list, oldest dropped first. The
// caller holds mu.
func (c *Clip) record(kind ClipEventKind, size int) {
	c.events = append(c.events, ClipEvent{At: c.deps.Now(), Kind: kind, Bytes: size})
	if len(c.events) > clipEventsKept {
		c.events = c.events[len(c.events)-clipEventsKept:]
	}
}
