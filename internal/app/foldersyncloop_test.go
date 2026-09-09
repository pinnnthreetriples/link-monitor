package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// The loop, as opposed to what one pass decides. What matters here is that
// exactly one goroutine ever touches either folder, that it stops when the
// process does, and that it never reports a shutdown as a fault.

// blockingSide is a side whose listing waits until a test lets it through, so
// a pass can be caught in flight.
type blockingSide struct {
	*fakeSide
	// entered is closed by the first scan, so a test knows a pass has started.
	entered chan struct{}
	// release is closed by the test to let the pass finish.
	release chan struct{}

	mu    sync.Mutex
	scans int
	once  sync.Once
}

func newBlockingSide() *blockingSide {
	return &blockingSide{
		fakeSide: newSide("workspace-claude-pc", map[string]string{"notes.txt": "hello"}),
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (b *blockingSide) Scan(
	ctx context.Context, keep func(rel string) bool,
) ([]foldersync.Entry, error) {
	b.mu.Lock()
	b.scans++
	b.mu.Unlock()
	b.once.Do(func() { close(b.entered) })

	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.fakeSide.Scan(ctx, keep)
}

func (b *blockingSide) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.scans
}

// loopKit is a folder whose loop is running.
func loopKit(t *testing.T, here Tree, interval time.Duration) (*Folder, *fakeSide) {
	t.Helper()

	there := newSide("win-sttm11d02rd", nil)
	state := newState()
	folder := NewFolder(FolderConfig{
		Root: `C:\Shared`, PeerName: "win-sttm11d02rd", Interval: interval,
	}, FolderDeps{
		Here: here, There: &fakePeerSide{side: there}, History: state, Log: quietLog(),
	})
	return folder, there
}

func TestTheLoopSweepsOnceAtOnceAndThenOnTheClock(t *testing.T) {
	t.Parallel()

	here := newSide("workspace-claude-pc", map[string]string{"notes.txt": "hello"})
	folder, there := loopKit(t, here, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	folder.Start(ctx)
	// The very first pass happens without waiting for a tick, which is what
	// makes the window say something useful the moment it is opened.
	waitUntil(t, func() bool { _, found := there.body("notes.txt"); return found })

	here.put("second.txt", "again", 2000)
	waitUntil(t, func() bool { _, found := there.body("second.txt"); return found })

	cancel()
	folder.Wait()
}

func TestStartingTwiceRunsOneLoopAndStartingAfterAStopRunsNone(t *testing.T) {
	t.Parallel()

	here := newBlockingSide()
	folder, _ := loopKit(t, here, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	folder.Start(ctx)
	folder.Start(ctx)
	<-here.entered
	close(here.release)

	cancel()
	folder.Wait()

	// A loop that has stopped stays stopped: a late Start must not begin a
	// second one against a context that is already dead.
	folder.Start(context.Background())
	folder.Wait()
	if got := here.count(); got != 1 {
		t.Errorf("the folder was scanned %d times, want once", got)
	}
}

// Two passes over one folder would each see the other's temporary files and
// half-written destinations, so the nudge must never start a second one.
func TestAPassInFlightIsNotJoinedByAnother(t *testing.T) {
	t.Parallel()

	here := newBlockingSide()
	folder, _ := loopKit(t, here, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	folder.Start(ctx)
	<-here.entered

	if !folder.Status().Running {
		t.Error("Running = false while a pass is in flight")
	}
	for range 20 {
		folder.SyncNow()
	}
	if got := here.count(); got != 1 {
		t.Fatalf("the folder was scanned %d times while one pass was in flight, want once", got)
	}

	close(here.release)
	waitUntil(t, func() bool { return folder.Status().HasRun })
	// The nudges collapsed into one, which then runs after the first pass.
	waitUntil(t, func() bool { return here.count() == 2 })

	cancel()
	folder.Wait()
}

// The pass the user asks for restarts the clock, so a person pressing the
// button cannot end up with two passes a moment apart.
func TestAPassTheUserAsksForHappensAtOnce(t *testing.T) {
	t.Parallel()

	here := newSide("workspace-claude-pc", map[string]string{"notes.txt": "hello"})
	folder, there := loopKit(t, here, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	folder.Start(ctx)
	waitUntil(t, func() bool { return folder.Status().HasRun })

	here.put("later.txt", "added", 2000)
	folder.SyncNow()
	waitUntil(t, func() bool { _, found := there.body("later.txt"); return found })

	cancel()
	folder.Wait()
}

// Shutting the program down mid-pass must not leave a fault on the screen: it
// is not one, and the screen is about to disappear anyway.
func TestAPassCutShortByShutdownIsNotReportedAsAFailure(t *testing.T) {
	t.Parallel()

	here := newBlockingSide()
	folder, _ := loopKit(t, here, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())

	folder.Start(ctx)
	<-here.entered
	cancel()
	folder.Wait()

	if got := folder.Status(); got.HasRun || got.Err != "" {
		t.Errorf("Status = %+v, want nothing recorded for a pass we abandoned", got)
	}
}

// A pass that runs past its own deadline is abandoned and said so. Nothing
// half-written survives it, because every write is published by a rename.
func TestAPassThatOverrunsItsDeadlineSaysSo(t *testing.T) {
	t.Parallel()

	here := newBlockingSide()
	folder, _ := loopKit(t, here, time.Hour)
	folder.cfg.Timeout = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	folder.Start(ctx)
	<-here.entered
	// The scan gives up when the pass's own context expires, and the loop then
	// records the timeout rather than the scan's complaint.
	waitUntil(t, func() bool { return folder.Status().HasRun })

	if got := folder.Status().Err; got != msgSyncTimedOut {
		t.Errorf("Err = %q, want %q", got, msgSyncTimedOut)
	}
	close(here.release)
	cancel()
	folder.Wait()
}

// A pass asked for after the loop has gone must do nothing at all, rather than
// run one inline on whoever called it.
func TestRunningAPassOnADeadContextDoesNothing(t *testing.T) {
	t.Parallel()

	here := newSide("workspace-claude-pc", map[string]string{"notes.txt": "hello"})
	folder, there := loopKit(t, here, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	folder.runOnce(ctx)

	if got := there.writes(); len(got) != 0 {
		t.Errorf("the peer was written to: %v", got)
	}
	if folder.Status().HasRun {
		t.Error("HasRun = true for a pass that never ran")
	}
}

// waitFor spins until a condition holds, or fails the test. The loop's own
// timing is what is being observed, so a poll is the honest way to watch it.
func waitUntil(t *testing.T, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the condition never held: the loop is not doing what it was asked")
}
