package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// folderKit is one shared folder wired to two fake sides, ready to sweep.
type folderKit struct {
	folder *Folder
	here   *fakeSide
	there  *fakeSide
	peer   *fakePeerSide
	state  *fakeState
	// now is the clock the folder reads, advanced by hand.
	now time.Time
}

// newKit builds a shared folder over two in-memory sides.
func newKit(t *testing.T, here, there map[string]string) *folderKit {
	t.Helper()

	kit := &folderKit{
		here:  newSide("workspace-claude-pc", here),
		there: newSide("win-sttm11d02rd", there),
		state: newState(),
		now:   time.Date(2026, 9, 8, 12, 34, 0, 0, time.UTC),
	}
	kit.peer = &fakePeerSide{side: kit.there}
	kit.folder = NewFolder(FolderConfig{
		Root:     `C:\Shared`,
		PeerRoot: `C:\Users\user\LinkMonitor\shared`,
		PeerName: "win-sttm11d02rd",
	}, FolderDeps{
		Here: kit.here, There: kit.peer, History: kit.state,
		Now: func() time.Time { return kit.now },
		Log: quietLog(),
	})
	return kit
}

// pass runs one sweep synchronously, the way the loop would.
func (k *folderKit) pass(t *testing.T) SyncStatus {
	t.Helper()

	k.folder.runOnce(context.Background())
	return k.folder.Status()
}

// movedNames lists what one pass carried, as "name→way".
func movedNames(st SyncStatus) []string {
	out := make([]string, 0, len(st.Moved))
	for _, m := range st.Moved {
		out = append(out, m.Name+"→"+string(m.Way))
	}
	return out
}

// skippedNames lists what one pass left alone, as "name:why".
func skippedNames(st SyncStatus) []string {
	out := make([]string, 0, len(st.Skipped))
	for _, s := range st.Skipped {
		out = append(out, s.Name+":"+string(s.Why))
	}
	return out
}

// joined renders a list for a failure message.
func joined(items []string) string { return "[" + strings.Join(items, " ") + "]" }

func TestASharedFolderNobodyChoseIsOffAndTouchesNothing(t *testing.T) {
	t.Parallel()

	folder := NewFolder(FolderConfig{}, FolderDeps{})
	if folder.On() {
		t.Error("On = true with no folder chosen")
	}
	got := folder.Status()
	if got.On || got.Folder != "" || got.HasRun || len(got.Moved) != 0 || len(got.Skipped) != 0 {
		t.Errorf("Status = %+v, want it empty", got)
	}
	// Start must be a no-op, and Wait must return rather than hang on a
	// goroutine that was never launched.
	folder.Start(context.Background())
	folder.SyncNow()
	folder.Wait()
}

// Every half has to be there. A folder named with no peer wired up is a
// half-built feature, and half-built is off.
func TestAFolderWithAHalfMissingIsOff(t *testing.T) {
	t.Parallel()

	full := FolderDeps{Here: newSide("here", nil), There: &fakePeerSide{}, History: newState()}
	for _, tc := range []struct {
		name string
		deps FolderDeps
	}{
		{"no local side", FolderDeps{There: full.There, History: full.History}},
		{"no peer", FolderDeps{Here: full.Here, History: full.History}},
		{"no history", FolderDeps{Here: full.Here, There: full.There}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if NewFolder(FolderConfig{Root: `C:\Shared`}, tc.deps).On() {
				t.Error("On = true with a piece missing")
			}
		})
	}
}

func TestAFileOnlyThisMachineHasArrivesOnThePeer(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)

	st := kit.pass(t)

	if got, found := kit.there.body("notes.txt"); !found || got != "hello" {
		t.Errorf("the peer has %q (found %v), want %q", got, found, "hello")
	}
	if got := movedNames(st); len(got) != 1 || got[0] != "notes.txt→to_peer" {
		t.Errorf("moved = %s, want notes.txt going to the peer", joined(got))
	}
	if st.Err != "" {
		t.Errorf("Err = %q, want none", st.Err)
	}
}

func TestAFileOnlyThePeerHasArrivesHere(t *testing.T) {
	t.Parallel()

	kit := newKit(t, nil, map[string]string{"theirs.txt": "from the pc"})

	st := kit.pass(t)

	if got, found := kit.here.body("theirs.txt"); !found || got != "from the pc" {
		t.Errorf("this machine has %q (found %v), want %q", got, found, "from the pc")
	}
	if got := movedNames(st); len(got) != 1 || got[0] != "theirs.txt→to_us" {
		t.Errorf("moved = %s, want theirs.txt coming here", joined(got))
	}
}

// Rule 1, end to end. A file deleted here must still be on the peer after the
// next pass — and it comes back, which is the price of the guarantee and the
// thing the UI says out loud.
func TestAFileDeletedHereStaysOnThePeerAndComesBack(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.pass(t)
	if _, found := kit.there.body("notes.txt"); !found {
		t.Fatal("the first pass did not put the file on the peer")
	}

	kit.here.drop("notes.txt")
	st := kit.pass(t)

	if got, found := kit.there.body("notes.txt"); !found || got != "hello" {
		t.Fatalf("the peer has %q (found %v) — a deletion propagated", got, found)
	}
	if got, found := kit.here.body("notes.txt"); !found || got != "hello" {
		t.Errorf("this machine has %q (found %v), want the file back", got, found)
	}
	if got := movedNames(st); len(got) != 1 || got[0] != "notes.txt→to_us" {
		t.Errorf("moved = %s, want the file coming back from the peer", joined(got))
	}
}

// The same from the other end, which is the demonstration the rules ask for.
func TestAFileDeletedOnThePeerStaysHere(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.pass(t)

	kit.there.drop("notes.txt")
	kit.pass(t)

	if got, found := kit.here.body("notes.txt"); !found || got != "hello" {
		t.Errorf("this machine has %q (found %v) — a deletion propagated", got, found)
	}
}

// A quiet folder costs nothing: nothing moves, nothing is skipped, and the
// history says as much.
func TestASecondPassOverAQuietFolderDoesNothing(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.pass(t)

	st := kit.pass(t)

	if len(st.Moved) != 0 || len(st.Skipped) != 0 {
		t.Errorf("second pass moved %s and skipped %s, want neither",
			joined(movedNames(st)), joined(skippedNames(st)))
	}
	if kit.state.rows() != 1 {
		t.Errorf("the history has %d rows, want one", kit.state.rows())
	}
}

func TestAChangeOnOneSideIsCarriedToTheOther(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "first"}, nil)
	kit.pass(t)

	kit.here.put("notes.txt", "second draft", 2000)
	st := kit.pass(t)

	if got, _ := kit.there.body("notes.txt"); got != "second draft" {
		t.Errorf("the peer has %q, want the new draft", got)
	}
	if got := movedNames(st); len(got) != 1 || got[0] != "notes.txt→to_peer" {
		t.Errorf("moved = %s", joined(got))
	}
}

func TestAChangeOnThePeerIsCarriedHere(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "first"}, nil)
	kit.pass(t)

	kit.there.put("notes.txt", "their draft", 2000)
	st := kit.pass(t)

	if got, _ := kit.here.body("notes.txt"); got != "their draft" {
		t.Errorf("this machine has %q, want their draft", got)
	}
	if got := movedNames(st); len(got) != 1 || got[0] != "notes.txt→to_us" {
		t.Errorf("moved = %s", joined(got))
	}
}

// The same bytes with different stamps must not travel: rule 2's "content, not
// clocks", seen from the cheap end.
func TestTheSameBytesWithDifferentStampsDoNotTravel(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"notes.txt": "identical"},
		map[string]string{"notes.txt": "identical"},
	)
	kit.there.put("notes.txt", "identical", 9999)

	st := kit.pass(t)

	if len(st.Moved) != 0 {
		t.Errorf("moved = %s, want nothing: the bytes are the same", joined(movedNames(st)))
	}
	if got := kit.there.writes(); len(got) != 0 {
		t.Errorf("the peer was written to: %v", got)
	}
}

// Rule 2, end to end. Both machines changed the file, so both copies survive
// and the local one is not touched.
func TestAFileChangedOnBothMachinesKeepsBothCopies(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"заметки.txt": "shared start"}, nil)
	kit.pass(t)

	kit.here.put("заметки.txt", "my version", 2000)
	kit.there.put("заметки.txt", "their version", 3000)
	st := kit.pass(t)

	if got, _ := kit.here.body("заметки.txt"); got != "my version" {
		t.Errorf("my file is now %q — a change was overwritten", got)
	}
	copyName := "заметки (с win-sttm11d02rd, 12-34).txt"
	if got, found := kit.here.body(copyName); !found || got != "their version" {
		t.Errorf("%s = %q (found %v), want their version kept beside mine", copyName, got, found)
	}
	if len(st.Moved) != 1 || !st.Moved[0].Conflict || st.Moved[0].Saved != copyName {
		t.Errorf("moved = %+v, want one conflict copy named %q", st.Moved, copyName)
	}
	// The record remembers both machines, which is what stops the next pass
	// reading the divergence as a one-sided change and picking a winner.
	rec, recorded := kit.state.record("заметки.txt")
	if !recorded || rec.Local.Hash == rec.Remote.Hash {
		t.Errorf("record = %+v (recorded %v), want both sides remembered", rec, recorded)
	}
}

// The conflict copy is an ordinary file afterwards, so the next pass carries
// it to the peer and both machines end up holding both versions.
func TestTheConflictCopyReachesThePeerOnTheNextPass(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "shared start"}, nil)
	kit.pass(t)
	kit.here.put("notes.txt", "my version", 2000)
	kit.there.put("notes.txt", "their version", 3000)
	kit.pass(t)

	kit.pass(t)

	copyName := "notes (с win-sttm11d02rd, 12-34).txt"
	if got, found := kit.there.body(copyName); !found || got != "their version" {
		t.Errorf("the peer has %q (found %v), want the conflict copy carried over", got, found)
	}
	if got, _ := kit.here.body("notes.txt"); got != "my version" {
		t.Errorf("my file is now %q", got)
	}
	if got, _ := kit.there.body("notes.txt"); got != "their version" {
		t.Errorf("their file is now %q", got)
	}
}

// A conflict must not be reported twice as new, and must not be resolved
// behind the user's back either.
func TestAConflictSettlesIntoADivergenceReportedButNotActedOn(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "shared start"}, nil)
	kit.pass(t)
	kit.here.put("notes.txt", "my version", 2000)
	kit.there.put("notes.txt", "their version", 3000)
	kit.pass(t)
	kit.pass(t)

	st := kit.pass(t)

	for _, m := range st.Moved {
		if m.Conflict {
			t.Errorf("a fourth pass made another conflict copy: %+v", m)
		}
	}
	found := false
	for _, s := range st.Skipped {
		if s.Name == "notes.txt" && s.Why == foldersync.WhyUnresolved {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped = %s, want the divergence still reported", joined(skippedNames(st)))
	}
}

// With no history a difference is a conflict. This is what a lost state file
// costs the user, and it is the whole reason a lost one is safe.
func TestWithNoHistoryTwoDifferentFilesBecomeAConflictAndNothingIsLost(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"notes.txt": "mine"},
		map[string]string{"notes.txt": "theirs"},
	)
	kit.there.put("notes.txt", "theirs", 2000)

	st := kit.pass(t)

	if got, _ := kit.here.body("notes.txt"); got != "mine" {
		t.Errorf("my file is now %q", got)
	}
	if got, _ := kit.there.body("notes.txt"); got != "theirs" {
		t.Errorf("their file is now %q", got)
	}
	if len(st.Moved) != 1 || !st.Moved[0].Conflict {
		t.Errorf("moved = %+v, want a conflict copy", st.Moved)
	}
}

// The state file could not be read. The pass still runs, still loses nothing,
// and says so — because the conflict copies it is about to make would
// otherwise be a mystery.
func TestALostHistoryIsReportedRatherThanHidden(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"notes.txt": "mine"},
		map[string]string{"notes.txt": "theirs"},
	)
	kit.state.loadErr = errFake

	st := kit.pass(t)

	if !st.HistoryLost {
		t.Error("HistoryLost = false, want the user told the history was unusable")
	}
	if len(st.Moved) != 1 || !st.Moved[0].Conflict {
		t.Errorf("moved = %+v, want a conflict copy rather than an overwrite", st.Moved)
	}
}

// Rule 5. The file is named, and it is left alone on both machines.
func TestAFileOverTheCapIsSkippedAndNamed(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"big.iso": strings.Repeat("x", 50)}, nil)
	kit.folder.cfg.Limits = foldersync.Limits{MaxBytes: 10}

	st := kit.pass(t)

	if _, found := kit.there.body("big.iso"); found {
		t.Error("a file over the cap was carried anyway")
	}
	if got := skippedNames(st); len(got) != 1 || got[0] != "big.iso:too_big" {
		t.Errorf("skipped = %s, want big.iso named", joined(got))
	}
	if st.Skipped[0].Size != 50 {
		t.Errorf("skipped size = %d, want the real 50", st.Skipped[0].Size)
	}
}

func TestTheExcludeListIsAppliedToBothMachines(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"notes.txt": "keep", ".git/config": "[core]", "junk.tmp": "x"},
		map[string]string{"node_modules/left-pad/index.js": "module"},
	)
	kit.folder.cfg.Limits = foldersync.Limits{Exclude: []string{"*.bak"}}
	kit.here.put("old.bak", "backup", 1000)

	st := kit.pass(t)

	if got := movedNames(st); len(got) != 1 || got[0] != "notes.txt→to_peer" {
		t.Errorf("moved = %s, want only notes.txt", joined(got))
	}
	if got := kit.here.paths(); len(got) != 4 {
		t.Errorf("this machine holds %v, want nothing new pulled in", got)
	}
}

// Rule 4, in the two shapes it takes: the file will not open, and the file
// moves between the listing and the copy.
func TestAFileSomebodyIsWritingIsSkippedAndNotHalfCopied(t *testing.T) {
	t.Parallel()

	t.Run("it will not open", func(t *testing.T) {
		t.Parallel()

		kit := newKit(t, map[string]string{"busy.docx": "being saved"}, nil)
		kit.here.openErr["busy.docx"] = errFake

		st := kit.pass(t)

		if _, found := kit.there.body("busy.docx"); found {
			t.Error("the peer got a file that would not open")
		}
		if got := skippedNames(st); len(got) != 1 || got[0] != "busy.docx:busy" {
			t.Errorf("skipped = %s, want busy.docx left for next time", joined(got))
		}
		if st.Err != "" {
			t.Errorf("Err = %q, want none: a busy file is not a fault", st.Err)
		}
	})

	t.Run("it moved before the copy started", func(t *testing.T) {
		t.Parallel()

		kit := newKit(t, map[string]string{"busy.docx": "short"}, nil)
		// Grow the file after the listing but before the copy: the pass
		// re-checks it against what the decision was made on.
		kit.here.statErr["busy.docx"] = nil
		kit.here.put("busy.docx", "much longer now", 5000)
		kit.folder.deps.Here = movedAfterScan{kit.here}

		st := kit.pass(t)

		if _, found := kit.there.body("busy.docx"); found {
			t.Error("the peer got a file that was moving")
		}
		if got := skippedNames(st); len(got) != 1 || got[0] != "busy.docx:busy" {
			t.Errorf("skipped = %s", joined(got))
		}
	})

	t.Run("it moved while it was being read", func(t *testing.T) {
		t.Parallel()

		kit := newKit(t, map[string]string{"busy.docx": "first half"}, nil)
		kit.here.afterOpen = func(side *fakeSide, rel string) {
			side.put(rel, "first half and then some more", 6000)
		}

		st := kit.pass(t)

		if _, found := kit.there.body("busy.docx"); found {
			t.Error("half a file was published on the peer")
		}
		if got := skippedNames(st); len(got) != 1 || got[0] != "busy.docx:busy" {
			t.Errorf("skipped = %s", joined(got))
		}
		if st.Err != "" {
			t.Errorf("Err = %q, want none", st.Err)
		}
	})
}

// movedAfterScan lists a file as it was and then reports the truth when asked
// again, which is what a file being saved looks like between a scan and a copy.
type movedAfterScan struct{ *fakeSide }

// Scan reports the file one byte shorter than it is, so the pass's own check
// against the listing is the thing that catches it.
func (m movedAfterScan) Scan(
	ctx context.Context, keep func(rel string) bool,
) ([]foldersync.Entry, error) {
	out, err := m.fakeSide.Scan(ctx, keep)
	for i := range out {
		out[i].Size--
	}
	return out, err
}

// A file being weighed that will not give up its bytes is the same case, one
// step earlier: it is on both machines, so the decision needs a hash first.
func TestAFileThatCannotBeWeighedIsLeftForTheNextPass(t *testing.T) {
	t.Parallel()

	kit := newKit(t,
		map[string]string{"notes.txt": "mine"},
		map[string]string{"notes.txt": "theirs different"},
	)
	kit.here.openErr["notes.txt"] = errFake

	st := kit.pass(t)

	if got, _ := kit.here.body("notes.txt"); got != "mine" {
		t.Errorf("my file is now %q", got)
	}
	if got := skippedNames(st); len(got) != 1 || got[0] != "notes.txt:busy" {
		t.Errorf("skipped = %s, want notes.txt left for next time", joined(got))
	}
	if len(st.Moved) != 0 {
		t.Errorf("moved = %s, want nothing", joined(movedNames(st)))
	}
}
