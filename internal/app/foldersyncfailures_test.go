package app

import (
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// What one pass makes of things going wrong: the copy that failed, the folder
// that could not be listed, the state that could not be written. None of them
// may lose a file, and none may be recorded as a copy that happened.

func TestACopyThatFailsIsReportedAndNotRecordedAsDone(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.there.receiveErr["notes.txt"] = errFake

	st := kit.pass(t)

	if st.Err == "" || !strings.Contains(st.Err, "не удалось") {
		t.Errorf("Err = %q, want a Russian sentence about the failure", st.Err)
	}
	if _, recorded := kit.state.record("notes.txt"); recorded {
		t.Error("a copy that failed was recorded as synced — the next pass would believe it")
	}
	// The next pass must try again rather than give up.
	kit.there.receiveErr["notes.txt"] = nil
	if st = kit.pass(t); len(st.Moved) != 1 {
		t.Errorf("the second pass moved %s, want the retry", joined(movedNames(st)))
	}
}

func TestACopyThatArrivesTheWrongLengthIsNotBelieved(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.there.shrink = true

	st := kit.pass(t)

	if st.Err == "" {
		t.Error("Err is empty, want the short copy reported")
	}
	if _, recorded := kit.state.record("notes.txt"); recorded {
		t.Error("a copy that arrived short was recorded as synced")
	}
}

func TestACopyWeCannotLookAtAfterwardsIsNotBelieved(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.there.statErr["notes.txt"] = errFake

	st := kit.pass(t)

	if st.Err == "" {
		t.Error("Err is empty, want the failure reported")
	}
	if _, recorded := kit.state.record("notes.txt"); recorded {
		t.Error("a copy nobody could confirm was recorded as synced")
	}
}

func TestAPeerThatCannotBeOpenedIsReportedAndNothingElseHappens(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.peer.openErr = errFake

	st := kit.pass(t)

	if st.Err != msgSyncNoPeer {
		t.Errorf("Err = %q, want %q", st.Err, msgSyncNoPeer)
	}
	if got := kit.there.writes(); len(got) != 0 {
		t.Errorf("the peer was written to: %v", got)
	}
}

func TestEveryPassClosesItsSession(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.peer.closeErr = errFake // a noisy close must not fail the pass

	st := kit.pass(t)
	kit.pass(t)

	if st.Err != "" {
		t.Errorf("Err = %q, want none: a close that complained is not a lost file", st.Err)
	}
	opens, closes := kit.peer.counts()
	if opens != 2 || closes != 2 {
		t.Errorf("opened %d sessions and closed %d, want two of each", opens, closes)
	}
}

func TestAFolderThatCannotBeListedIsReportedPerMachine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		set  func(k *folderKit)
		want string
	}{
		{"here", func(k *folderKit) { k.here.scanErr = errFake }, msgSyncScanHere},
		{"the peer", func(k *folderKit) { k.there.scanErr = errFake }, msgSyncScanThere},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
			tc.set(kit)

			if st := kit.pass(t); st.Err != tc.want {
				t.Errorf("Err = %q, want %q", st.Err, tc.want)
			}
			if kit.state.rows() != 0 {
				t.Error("the history was written for a pass that could not even look")
			}
		})
	}
}

func TestAStateThatCannotBeSavedIsReportedWithoutLosingTheCopy(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.state.saveErr = errFake

	st := kit.pass(t)

	if st.Err != msgSyncStateFailed {
		t.Errorf("Err = %q, want %q", st.Err, msgSyncStateFailed)
	}
	if got, _ := kit.there.body("notes.txt"); got != "hello" {
		t.Errorf("the peer has %q — the copy itself did happen", got)
	}
}

// A copy that failed *and* a state that could not be saved: the copy's own
// sentence is the one that matters, because it is the one the user can act on.
func TestACopyFailureOutranksAStateFailure(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.there.receiveErr["notes.txt"] = errFake
	kit.state.saveErr = errFake

	if st := kit.pass(t); st.Err != msgSyncCopyFailed {
		t.Errorf("Err = %q, want %q", st.Err, msgSyncCopyFailed)
	}
}

func TestTheHistoryIsKeptPerFolderAndPerMachine(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	kit.pass(t)

	kit.state.mu.Lock()
	folder, peer := kit.state.folder, kit.state.peer
	kit.state.mu.Unlock()

	if folder != `C:\Shared` || peer != "win-sttm11d02rd" {
		t.Errorf("the history was asked for %q/%q, want the configured folder and machine",
			folder, peer)
	}
}

func TestTheStatusSaysWhatTheUserTurnedOn(t *testing.T) {
	t.Parallel()

	kit := newKit(t, nil, nil)

	st := kit.pass(t)

	switch {
	case !st.On:
		t.Error("On = false for a folder that was chosen")
	case st.Folder != `C:\Shared`:
		t.Errorf("Folder = %q", st.Folder)
	case st.PeerFolder != `C:\Users\user\LinkMonitor\shared`:
		t.Errorf("PeerFolder = %q", st.PeerFolder)
	case st.PeerName != "win-sttm11d02rd":
		t.Errorf("PeerName = %q", st.PeerName)
	case st.MaxBytes != foldersync.DefaultMaxBytes:
		t.Errorf("MaxBytes = %d, want the default", st.MaxBytes)
	case !st.HasRun:
		t.Error("HasRun = false after a pass")
	case !st.At.Equal(kit.now):
		t.Errorf("At = %s, want %s", st.At, kit.now)
	case st.Running:
		t.Error("Running = true after the pass finished")
	}
}

func TestAChosenCapIsWhatTheStatusReports(t *testing.T) {
	t.Parallel()

	kit := newKit(t, nil, nil)
	kit.folder.cfg.Limits = foldersync.Limits{MaxBytes: 4096}

	if got := kit.pass(t).MaxBytes; got != 4096 {
		t.Errorf("MaxBytes = %d, want 4096", got)
	}
}

// A first sync of a large folder must not fill the window with a row per file.
func TestWhatOnePassReportsIsBounded(t *testing.T) {
	t.Parallel()

	here := map[string]string{}
	for i := range defaultSyncKeep * 2 {
		here[string(rune('a'+i%26))+strings.Repeat("x", i)+".txt"] = "body"
	}
	kit := newKit(t, here, nil)
	kit.folder.cfg.Limits = foldersync.Limits{MaxBytes: 1}

	st := kit.pass(t)

	if len(st.Skipped) != defaultSyncKeep {
		t.Errorf("skipped %d rows, want them trimmed to %d", len(st.Skipped), defaultSyncKeep)
	}
}

func TestTheStatusIsACopyAndNotAViewOfTheLoop(t *testing.T) {
	t.Parallel()

	kit := newKit(t, map[string]string{"notes.txt": "hello"}, nil)
	st := kit.pass(t)
	if len(st.Moved) == 0 {
		t.Fatal("nothing moved")
	}

	st.Moved[0].Name = "tampered"
	if again := kit.folder.Status(); again.Moved[0].Name != "notes.txt" {
		t.Error("the status handed out the loop's own slice")
	}
}
