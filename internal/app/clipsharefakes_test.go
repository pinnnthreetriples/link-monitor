package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// fakeClipboard is this machine's clipboard, in memory.
//
// Look hands back a fresh copy of the text every time, because the loop zeroes
// what it is given: a fake that returned its own slice would have its contents
// destroyed by the code under test and the next assertion would pass for the
// wrong reason.
type fakeClipboard struct {
	mu   sync.Mutex
	seq  uint32
	snap clipshare.Snapshot

	seqErr  error
	lookErr error
	putErr  error

	put   []string
	looks int
}

func newFakeClipboard() *fakeClipboard { return &fakeClipboard{seq: 100} }

func (f *fakeClipboard) Sequence() (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.seqErr != nil {
		return 0, f.seqErr
	}
	return f.seq, nil
}

func (f *fakeClipboard) Look(int) (clipshare.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.looks++
	if f.lookErr != nil {
		return clipshare.Snapshot{}, f.lookErr
	}
	out := f.snap
	out.Text = append([]byte(nil), f.snap.Text...)
	return out, nil
}

func (f *fakeClipboard) Put(text []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.putErr != nil {
		return f.putErr
	}
	f.seq++
	f.snap = clipshare.Snapshot{
		HasText: true, Bytes: len(text), Text: append([]byte(nil), text...), Recordable: true,
	}
	f.put = append(f.put, string(text))
	return nil
}

// copyText is the user pressing Ctrl+C on some ordinary text.
func (f *fakeClipboard) copyText(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq++
	f.snap = clipshare.Snapshot{
		HasText: true, Bytes: len(text), Text: []byte(text), Recordable: true,
	}
}

// copyMarked is a password manager copying a secret with Windows' own "do not
// record this" marker on it: the reader reports that it may not be recorded,
// and reports nothing else about it at all.
func (f *fakeClipboard) copyMarked() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq++
	f.snap = clipshare.Snapshot{HasText: true}
}

// copyBig is an item past the cap: measured, never copied out.
func (f *fakeClipboard) copyBig(size int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq++
	f.snap = clipshare.Snapshot{HasText: true, Bytes: size, Recordable: true}
}

// copyFile is a file, a picture, or anything else that is not text.
func (f *fakeClipboard) copyFile() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq++
	f.snap = clipshare.Snapshot{}
}

// bump moves the sequence number without changing the contents, which is what
// another application touching the clipboard looks like from here. It is how
// the fingerprint half of the echo defence is put under test on its own.
func (f *fakeClipboard) bump() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq++
}

func (f *fakeClipboard) delivered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.put...)
}

func (f *fakeClipboard) breakSequence(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seqErr = err
}

func (f *fakeClipboard) breakLook(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lookErr = err
}

func (f *fakeClipboard) breakPut(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.putErr = err
}

// fakeInbox is the program's instance on the other machine.
type fakeInbox struct {
	mu   sync.Mutex
	got  []string
	err  error
	name string
	// block, when set, holds Deliver until it is closed, so a test can prove
	// the loop is not holding a lock across the network.
	block chan struct{}
}

func newFakeInbox() *fakeInbox { return &fakeInbox{name: "win-sttm11d02rd"} }

func (f *fakeInbox) Name() string { return f.name }

func (f *fakeInbox) Deliver(ctx context.Context, text []byte) error {
	f.mu.Lock()
	block, err := f.block, f.err
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return fmt.Errorf("delivering: %w", ctx.Err())
		}
	}
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.got = append(f.got, string(text))
	return nil
}

func (f *fakeInbox) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.got...)
}

func (f *fakeInbox) refuse(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.err = err
}

// peerNotRunning is what peerclip reports when the peer has published no
// record, or one that no longer describes a live process.
func peerNotRunning() error {
	return fmt.Errorf("the program is not running there: %w", localendpoint.ErrNoRecord)
}

// clipUnderTest builds a shared clipboard over the two fakes, with a clock
// that does not move so events are comparable.
func clipUnderTest(maxBytes int) (*Clip, *fakeClipboard, *fakeInbox) {
	here := newFakeClipboard()
	there := newFakeInbox()
	at := time.Date(2026, 9, 8, 21, 0, 0, 0, time.UTC)
	clip := NewClip(ClipConfig{MaxBytes: maxBytes}, ClipDeps{
		Here:  here,
		There: there,
		Now:   func() time.Time { return at },
	})
	return clip, here, there
}

// errBroken is a failure with nothing interesting in it.
var errBroken = errors.New("broken")
