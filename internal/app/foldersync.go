package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

const (
	// DefaultSyncInterval is how often the shared folder is swept when nobody
	// says otherwise. It is longer than the link poll because a pass costs
	// round trips over the tailnet and because a folder people work in does
	// not need to be checked twice a minute.
	DefaultSyncInterval = 60 * time.Second

	// DefaultSyncTimeout bounds one pass. A pass that has not finished by then
	// is abandoned rather than left to queue up behind the next one; nothing
	// half-written survives it, because every write is published by a rename.
	DefaultSyncTimeout = 10 * time.Minute

	// defaultSyncKeep bounds the moved and skipped lists a finished pass
	// reports, so a first sync of a large folder cannot fill the window — or
	// the API response — with thousands of rows.
	defaultSyncKeep = 60
)

// errBusy is rule 4: a file somebody is writing. It is not an error the user
// is shown as a fault — it is a file left for the next pass.
var errBusy = errors.New("app: the file is being written")

// Tree is one side of the shared folder, as the pass needs it. Both
// implementations live in internal/adapters/syncfs: one on this machine's
// filesystem, one on the peer's over SFTP.
type Tree interface {
	// Name is what the UI calls this side.
	Name() string
	// Scan lists every regular file under the root, as paths relative to it
	// with forward slashes. keep is asked about every directory as well, and a
	// directory it refuses is not descended into.
	Scan(ctx context.Context, keep func(rel string) bool) ([]foldersync.Entry, error)
	// Stat reports one file's length and stamp without reading it.
	Stat(ctx context.Context, rel string) (foldersync.State, error)
	// Open opens one file for reading.
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
	// Receive writes one file through a temporary name in its own directory and
	// renames it into place, stamped with stamp. write is handed the temporary
	// file and may refuse the copy by returning an error, in which case the
	// destination is left exactly as it was.
	Receive(ctx context.Context, rel string, stamp time.Time, write func(io.Writer) error) error
}

// PeerTrees opens the peer's side of the folder for one pass.
//
// One session per pass, closed at the end of it: an SFTP client is bound to
// the SSH connection that carried it and does not heal when that connection
// dies, and a pass is the natural place to notice.
type PeerTrees interface {
	Open(ctx context.Context) (Tree, io.Closer, error)
}

// PeerTreeFunc adapts a function to [PeerTrees], so the wiring in cmd/ can
// hand over a concrete adapter without this package naming its type.
type PeerTreeFunc func(ctx context.Context) (Tree, io.Closer, error)

// Open opens the peer's side.
func (f PeerTreeFunc) Open(ctx context.Context) (Tree, io.Closer, error) { return f(ctx) }

// SyncHistory remembers what was last synced. *syncfs.Store implements it.
type SyncHistory interface {
	// Load reads the baseline for one folder and one peer. An empty baseline
	// with a nil error means "first run"; an error means the history exists and
	// cannot be used, and the pass then treats every difference as a conflict.
	Load(folder, peer string) (foldersync.Baseline, error)
	Save(folder, peer string, base foldersync.Baseline) error
}

// FolderConfig describes the shared folder.
type FolderConfig struct {
	// Root is the folder on this machine. Empty means the whole feature is
	// off: rule 5 says there is no default folder and no silent first sync.
	Root string
	// PeerRoot is the folder on the peer, shown in the window so the user can
	// see both halves of what they turned on.
	PeerRoot string
	// PeerName is what the peer is called — in the window, and inside a
	// conflict copy's file name.
	PeerName string
	// Interval is how often to sweep; zero means DefaultSyncInterval.
	Interval time.Duration
	// Timeout bounds one pass; zero means DefaultSyncTimeout.
	Timeout time.Duration
	// Limits are the size cap and the exclude list.
	Limits foldersync.Limits
}

// FolderDeps are the two trees, the history, and the clock.
type FolderDeps struct {
	Here    Tree
	There   PeerTrees
	History SyncHistory
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Log is where a pass's technical detail goes. Nil means slog.Default.
	// Nothing written here is ever a file's contents.
	Log *slog.Logger
}

// SyncMoved is one file that moved.
type SyncMoved struct {
	Name string
	Size int64
	Way  foldersync.Way
	// Conflict marks a copy written beside the local file rather than onto it.
	Conflict bool
	// Saved is the name a conflict copy landed under, empty otherwise.
	Saved string
}

// SyncSkipped is one file left alone, and why.
type SyncSkipped struct {
	Name string
	Size int64
	Why  foldersync.Why
}

// SyncStatus is everything the Файлы tab shows about the shared folder.
type SyncStatus struct {
	// On says the user has chosen a folder. When it is false every other
	// field is empty and nothing has ever been read or written.
	On         bool
	Folder     string
	PeerFolder string
	PeerName   string
	MaxBytes   int64
	// Running says a pass is in flight right now.
	Running bool
	// HistoryLost says the state file could not be used, so this pass treated
	// every difference as a conflict rather than as a change to copy.
	HistoryLost bool
	// HasRun says a pass has finished, so the fields below mean something.
	HasRun  bool
	At      time.Time
	Took    time.Duration
	Moved   []SyncMoved
	Skipped []SyncSkipped
	// Err is one Russian sentence when the last pass could not finish.
	Err string
}

// Folder keeps one folder on this machine the same as one on the peer, in both
// directions, for as long as the program runs.
//
// Exactly one goroutine ever sweeps, so two passes can never be in flight at
// once — which matters more here than it does for the poller: two passes over
// the same folder would each see the other's temporary files and half-written
// destinations. Shutdown is by context, as everywhere else in this package.
type Folder struct {
	cfg   FolderConfig
	deps  FolderDeps
	nudge chan struct{}
	wg    sync.WaitGroup

	mu      sync.Mutex
	running bool
	started bool
	stopped bool
	lost    bool
	hasRun  bool
	at      time.Time
	took    time.Duration
	moved   []SyncMoved
	skipped []SyncSkipped
	failure string
}

// NewFolder prepares the shared folder. It reads nothing and writes nothing
// until Start, and it does neither at all when cfg.Root is empty.
func NewFolder(cfg FolderConfig, d FolderDeps) *Folder {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultSyncInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultSyncTimeout
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Folder{cfg: cfg, deps: d, nudge: make(chan struct{}, 1)}
}

// On reports whether the shared folder is switched on: a folder was chosen and
// both sides were wired up. Off is the default and is not a fault.
func (f *Folder) On() bool {
	return f.cfg.Root != "" && f.deps.Here != nil && f.deps.There != nil && f.deps.History != nil
}

// Start launches the sweeping goroutine. Calling it twice is a no-op, and
// calling it with the folder off does nothing at all.
func (f *Folder) Start(ctx context.Context) {
	if !f.On() {
		return
	}
	f.mu.Lock()
	if f.started || f.stopped {
		f.mu.Unlock()
		return
	}
	f.started = true
	f.mu.Unlock()

	f.wg.Add(1)
	go f.loop(ctx)
}

// Wait blocks until the sweeping goroutine has finished, so a test can prove
// nothing is left running.
func (f *Folder) Wait() { f.wg.Wait() }

// SyncNow asks for a pass as soon as the loop can get to one. It never blocks:
// a nudge arriving while one is already pending is the same nudge, so holding
// the button down cannot queue up passes.
func (f *Folder) SyncNow() {
	select {
	case f.nudge <- struct{}{}:
	default:
	}
}

// loop is the only goroutine that touches either folder.
func (f *Folder) loop(ctx context.Context) {
	defer f.wg.Done()
	defer f.shutdown()

	ticker := time.NewTicker(f.cfg.Interval)
	defer ticker.Stop()

	f.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.runOnce(ctx)
		case <-f.nudge:
			f.runOnce(ctx)
			// A pass the user asked for restarts the clock, so the next
			// scheduled one is a whole interval away.
			ticker.Reset(f.cfg.Interval)
		}
	}
}

// runOnce performs one pass under its own timeout and records what it did.
func (f *Folder) runOnce(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	f.setRunning(true)
	defer f.setRunning(false)

	passCtx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()

	started := f.deps.Now()
	result := f.sweep(passCtx)
	switch {
	case ctx.Err() != nil:
		// The program is shutting down. Whatever the pass managed is already on
		// disk and already in the state file; there is nothing worth putting on
		// a screen that is about to disappear, and «сверка прервана» on the way
		// out would read as a fault.
		return
	case errors.Is(passCtx.Err(), context.DeadlineExceeded):
		result.failure = msgSyncTimedOut
	}
	f.record(started, f.deps.Now().Sub(started), result)
}

// setRunning flags a pass in flight, so the window can say so.
func (f *Folder) setRunning(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.running = on
}

// record stores what one pass did.
func (f *Folder) record(at time.Time, took time.Duration, res passResult) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.hasRun, f.at, f.took = true, at, took
	f.moved, f.skipped, f.failure, f.lost = res.moved, res.skipped, res.failure, res.lost
}

// shutdown marks the loop gone, so a later Start does not begin a second one.
func (f *Folder) shutdown() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.started, f.stopped, f.running = false, true, false
}

// Status reports everything the window shows about the shared folder. The
// slices are copies: the loop keeps writing to its own.
func (f *Folder) Status() SyncStatus {
	if !f.On() {
		return SyncStatus{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	return SyncStatus{
		On:          true,
		Folder:      f.cfg.Root,
		PeerFolder:  f.cfg.PeerRoot,
		PeerName:    f.cfg.PeerName,
		MaxBytes:    maxSyncBytes(f.cfg.Limits),
		Running:     f.running,
		HistoryLost: f.lost,
		HasRun:      f.hasRun,
		At:          f.at,
		Took:        f.took,
		Moved:       append([]SyncMoved(nil), f.moved...),
		Skipped:     append([]SyncSkipped(nil), f.skipped...),
		Err:         f.failure,
	}
}

// maxSyncBytes is the cap in force, so the window can name it.
func maxSyncBytes(l foldersync.Limits) int64 {
	if l.MaxBytes <= 0 {
		return foldersync.DefaultMaxBytes
	}
	return l.MaxBytes
}
