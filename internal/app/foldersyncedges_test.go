package app

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// The edges of one pass: everything that can go wrong between deciding to copy
// a file and being able to believe the copy. None of them may lose a file, and
// none of them may be recorded as a copy that happened.

func TestTheWiringAdapterHandsOverThePeerAndItsCloser(t *testing.T) {
	t.Parallel()

	side := newSide("win-sttm11d02rd", nil)
	var closed bool
	opener := PeerTreeFunc(func(context.Context) (Tree, io.Closer, error) {
		return side, closerFunc(func() error { closed = true; return nil }), nil
	})

	tree, closer, err := opener.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if tree.Name() != "win-sttm11d02rd" {
		t.Errorf("Name = %q", tree.Name())
	}
	if err := closer.Close(); err != nil || !closed {
		t.Errorf("Close = %v, closed = %v", err, closed)
	}
}

// A source file we cannot even look at is rule 4's case, one step before the
// read: skipped now, tried again next pass, no fault reported.
func TestASourceFileThatCannotBeLookedAtIsSkipped(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.here.statErr["notes.txt"] = errFake

	st := kit.pass(t)

	if got := skippedNames(st); len(got) != 1 || got[0] != "notes.txt:busy" {
		t.Errorf("skipped = %s, want notes.txt left for next time", joined(got))
	}
	if st.Err != "" {
		t.Errorf("Err = %q, want none", st.Err)
	}
	if _, found := kit.there.body("notes.txt"); found {
		t.Error("the peer got a file we could not measure")
	}
}

// The same one step earlier still, while the file was being weighed to decide
// whether it had changed at all.
func TestAFileThatCannotBeLookedAtWhileBeingWeighedIsSkipped(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"notes.txt": "mine"},
		map[string]string{"notes.txt": "theirs different"},
	)
	kit.here.statErr["notes.txt"] = errFake

	st := kit.pass(t)

	if got := skippedNames(st); len(got) != 1 || got[0] != "notes.txt:busy" {
		t.Errorf("skipped = %s", joined(got))
	}
	if len(st.Moved) != 0 {
		t.Errorf("moved = %s, want nothing", joined(movedNames(st)))
	}
}

// A read that gives up partway is a link that dropped. Nothing may be
// published, and it must not be recorded as done.
func TestAReadThatGivesUpPartwayPublishesNothing(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.here.readErr["notes.txt"] = errFake

	st := kit.pass(t)

	if _, found := kit.there.body("notes.txt"); found {
		t.Error("half a file was published on the peer")
	}
	if _, recorded := kit.state.record("notes.txt"); recorded {
		t.Error("a copy that broke mid-read was recorded as synced")
	}
	if len(st.Moved) != 0 {
		t.Errorf("moved = %s, want nothing", joined(movedNames(st)))
	}
}

// The same during the weighing, which is the other place a file is read right
// through.
func TestAWeighingThatGivesUpPartwayLeavesTheFileForNextTime(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"notes.txt": "mine"},
		map[string]string{"notes.txt": "theirs different"},
	)
	kit.here.readErr["notes.txt"] = errFake

	st := kit.pass(t)

	if got := skippedNames(st); len(got) != 1 || got[0] != "notes.txt:busy" {
		t.Errorf("skipped = %s", joined(got))
	}
	if got, _ := kit.here.body("notes.txt"); got != "mine" {
		t.Errorf("my file is now %q", got)
	}
}

// The file was read whole, and then could not be looked at again — so there is
// no way to know whether it held still. That is not a copy anybody may
// believe, so nothing is published.
func TestASourceThatCannotBeCheckedAfterTheReadPublishesNothing(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.here.afterOpen = func(side *fakeSide, rel string) {
		side.mu.Lock()
		defer side.mu.Unlock()
		side.statErr[rel] = errFake
	}

	st := kit.pass(t)

	if _, found := kit.there.body("notes.txt"); found {
		t.Error("a copy nobody could vouch for was published")
	}
	if got := skippedNames(st); len(got) != 1 || got[0] != "notes.txt:busy" {
		t.Errorf("skipped = %s", joined(got))
	}
}

// A pass given up on while it is weighing files must stop weighing them. What
// it had worked out so far is fine to keep; what it never looked at is not
// recorded either way.
func TestWeighingStopsWhenThePassIsGivenUpOn(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"a.txt": "mine", "b.txt": "mine"},
		map[string]string{"a.txt": "theirs", "b.txt": "theirs"},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sweep := &sweep{f: kit.folder, here: kit.here, there: kit.there}
	sweep.in = foldersync.Input{
		Local:  []foldersync.Entry{{Path: "a.txt", Size: 4, MTime: time.Unix(1000, 0)}},
		Remote: []foldersync.Entry{{Path: "a.txt", Size: 6, MTime: time.Unix(1000, 0)}},
	}
	sweep.weighAll(ctx, []foldersync.Need{
		{Local: "a.txt", Remote: "a.txt"},
		{Local: "b.txt", Remote: "b.txt"},
	})

	if len(sweep.in.Unreadable) != 0 {
		t.Errorf("Unreadable = %v, want nothing: a pass nobody is waiting for "+
			"has learned nothing about these files", sweep.in.Unreadable)
	}
	if sweep.in.Local[0].Hash != "" {
		t.Error("a hash was written back from a weighing that never happened")
	}
}

// A first sync of a large folder must not fill the window with a row per file,
// in either list.
func TestWhatOnePassReportsIsBoundedInBothLists(t *testing.T) {
	t.Parallel()

	here := map[string]string{}
	for i := range defaultSyncKeep * 2 {
		here["file-"+strconv.Itoa(i)+".txt"] = strings.Repeat("x", i+1)
	}
	kit := newKit(t, here, nil)

	st := kit.pass(t)

	if len(st.Moved) != defaultSyncKeep {
		t.Errorf("moved %d rows, want them trimmed to %d", len(st.Moved), defaultSyncKeep)
	}
	// Trimming is about the window, not about the work: every file still moved.
	if got := len(kit.there.paths()); got != defaultSyncKeep*2 {
		t.Errorf("the peer holds %d files, want all %d", got, defaultSyncKeep*2)
	}
}
