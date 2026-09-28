package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// defaultMaxTransfers bounds the recent-transfer list. It is a display aid, not
// a log: the newest entries push the oldest out and nothing is written to disk.
const defaultMaxTransfers = 50

// ErrNoTaildrop means the Taildrop side was never wired up.
var ErrNoTaildrop = errors.New("app: file transfer is not available")

// FileMover is the Taildrop half of the Tailscale adapter.
// *tailscale.Client implements it.
type FileMover interface {
	SendFile(ctx context.Context, path string, peer string) error
	ReceiveFiles(ctx context.Context, targetDir string) ([]string, error)
}

// TransferDir says which way a file went.
type TransferDir string

const (
	// TransferTo is a file this machine sent to the peer.
	TransferTo TransferDir = "to"
	// TransferFrom is a file this machine took out of the Taildrop inbox.
	TransferFrom TransferDir = "from"
)

// TransferState is how a transfer ended. A transfer still in flight carries
// TransferOK with a Pct below 100: nothing has gone wrong with it yet.
type TransferState string

const (
	// TransferOK is a transfer that nothing has gone wrong with. It covers both
	// "finished" and "still moving": the two are told apart by Pct, which is
	// 100 only once every byte has arrived.
	TransferOK TransferState = "ok"

	// TransferErr is a transfer that failed. The file did not arrive, and the
	// user has to be told — this is the one state that warrants a notification.
	TransferErr TransferState = "err"

	// TransferCancel is a transfer that was given up rather than broken: the
	// context was cancelled or its deadline passed, which on this program's
	// paths means the user aborted it or the app is shutting down. It is kept
	// apart from TransferErr so that quitting mid-copy does not report a fault.
	TransferCancel TransferState = "cancel"
)

// Transfer is one file that moved, or is moving, between the two machines.
// Only the name and the size are ever kept — never the contents.
type Transfer struct {
	// ID identifies this entry for as long as the list holds it, so progress can
	// be reported without handing a pointer into shared state to the caller.
	ID    string
	Name  string
	Size  int64
	At    time.Time
	Dir   TransferDir
	State TransferState
	// Pct is how much of the file has moved, 0..100. Taildrop reports no
	// progress of its own, so in practice this steps from 0 to 100; it exists so
	// the UI has one place to read progress from when it can be had.
	Pct int
}

// Transfers moves files over Taildrop and remembers the recent ones so the UI
// can list them. The list lives in memory and dies with the process.
type Transfers struct {
	mover           FileMover
	uploader        UploadMover
	receiveGate     chan struct{}
	startOnce       sync.Once
	receiveWG       sync.WaitGroup
	receiveInterval time.Duration
	receiveTimeout  time.Duration
	receiveErr      error
	inbox           string
	max             int
	now             func() time.Time
	// size reports a local file's length. It is a field so a test needs no real
	// files; the default asks the filesystem.
	size func(path string) (int64, error)

	mu    sync.Mutex
	items []*Transfer // newest first
	seq   int
}

// NewTransfers builds the transfer service. inboxDir is where received files
// land; keep is how many recent transfers to remember, defaulted when not
// positive. The parameter is not called max because that is the name of a
// builtin, and a local one shadowing it is a trap for the next reader.
func NewTransfers(mover FileMover, inboxDir string, keep int) *Transfers {
	if keep <= 0 {
		keep = defaultMaxTransfers
	}
	return &Transfers{
		mover:           mover,
		inbox:           inboxDir,
		max:             keep,
		receiveGate:     make(chan struct{}, 1),
		receiveInterval: 2 * time.Second,
		receiveTimeout:  10 * time.Minute,
		now:             time.Now,
		size:            fileSize,
	}
}

// Inbox reports the directory received files land in.
func (t *Transfers) Inbox() string { return t.inbox }

// Send pushes one file to the peer. The entry appears in the list before the
// push starts, so the UI can show it moving, and is updated in place when it
// finishes.
func (t *Transfers) Send(ctx context.Context, path, peer string) error {
	if t.mover == nil {
		return fmt.Errorf("sending %s: %w", filepath.Base(path), ErrNoTaildrop)
	}
	if path == "" {
		return errors.New("app: Send needs a file path")
	}

	size, sizeErr := t.size(path)
	if sizeErr != nil {
		// The size is decoration. A file we cannot measure may still send, and
		// if it cannot, SendFile is the call that should say so.
		size = 0
	}
	id := t.record(&Transfer{
		Name: filepath.Base(path), Size: size, At: t.now(),
		Dir: TransferTo, State: TransferOK, Pct: 0,
	})

	if err := t.mover.SendFile(ctx, path, peer); err != nil {
		t.finish(id, failureState(err), 0)
		return fmt.Errorf("sending %s to %s: %w", filepath.Base(path), peer, err)
	}
	t.finish(id, TransferOK, 100)
	return nil
}

// Receive drains the Taildrop inbox into the configured directory and records
// one entry per file. Files that landed before an error are still reported:
// they really did arrive.
func (t *Transfers) Receive(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("receiving files: %w", err)
	}
	select {
	case t.receiveGate <- struct{}{}:
		defer func() { <-t.receiveGate }()
	default:
		return nil, ErrReceiveBusy
	}
	if t.mover == nil {
		return nil, fmt.Errorf("draining the Taildrop inbox: %w", ErrNoTaildrop)
	}

	paths, err := t.mover.ReceiveFiles(ctx, t.inbox)
	t.mu.Lock()
	t.receiveErr = err
	t.mu.Unlock()
	for _, p := range paths {
		size, sizeErr := t.size(p)
		if sizeErr != nil {
			// Same reasoning as in Send: the file is here either way.
			size = 0
		}
		t.record(&Transfer{
			Name: filepath.Base(p), Size: size, At: t.now(),
			Dir: TransferFrom, State: TransferOK, Pct: 100,
		})
	}
	if err != nil {
		return paths, fmt.Errorf("draining the Taildrop inbox: %w", err)
	}
	return paths, nil
}

// List returns the recent transfers, newest first, as copies.
func (t *Transfers) List() []Transfer {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]Transfer, 0, len(t.items))
	for _, item := range t.items {
		out = append(out, *item)
	}
	return out
}

// record puts a transfer at the front of the list, trims the tail and returns
// the id the entry can be found by afterwards.
func (t *Transfers) record(tr *Transfer) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.seq++
	tr.ID = "tr-" + strconv.Itoa(t.seq)
	t.items = append([]*Transfer{tr}, t.items...)
	if len(t.items) > t.max {
		clear(t.items[t.max:])
		t.items = t.items[:t.max]
	}
	return tr.ID
}

// finish records how a transfer ended. An entry already pushed off the end of
// the list is simply not found, which is the right outcome: nobody can see it.
func (t *Transfers) finish(id string, state TransferState, pct int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if tr := t.find(id); tr != nil {
		tr.State, tr.Pct = state, pct
	}
}

// Progress sets how far a transfer has got, for a mover that can report it. The
// value is clamped to 0..100 so a bad number cannot reach the UI; the result
// says whether the transfer is still in the list.
func (t *Transfers) Progress(id string, pct int) bool {
	switch {
	case pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	tr := t.find(id)
	if tr == nil {
		return false
	}
	tr.Pct = pct
	return true
}

// find locates an entry by id. The caller holds the mutex. The list is a few
// dozen entries at most, so a scan costs less than a second index to maintain.
func (t *Transfers) find(id string) *Transfer {
	for _, item := range t.items {
		if item.ID == id {
			return item
		}
	}
	return nil
}

// failureState tells a cancelled transfer from a failed one: the user aborted
// the first and needs to be told about the second.
func failureState(err error) TransferState {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return TransferCancel
	}
	return TransferErr
}

// fileSize is the default size lookup.
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("measuring %s: %w", filepath.Base(path), err)
	}
	return info.Size(), nil
}
