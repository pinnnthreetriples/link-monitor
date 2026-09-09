package syncfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// statePath is a store in a folder that does not exist yet, so Save has to
// create it — which is what happens on the first run of a fresh install.
func statePath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "LinkMonitor", "sync-state.json")
}

// baseline is a small history to write and read back.
func baseline() foldersync.Baseline {
	return foldersync.Baseline{
		"notes.txt": {
			Local:  foldersync.State{Size: 10, MTime: time.Unix(1000, 0), Hash: "aaa"},
			Remote: foldersync.State{Size: 10, MTime: time.Unix(1001, 0), Hash: "aaa"},
		},
		"sub/plan.md": {
			Local:  foldersync.State{Size: 20, MTime: time.Unix(2000, 0), Hash: "mine"},
			Remote: foldersync.State{Size: 22, MTime: time.Unix(2100, 0), Hash: "theirs"},
		},
	}
}

func TestTheStateSurvivesARoundTrip(t *testing.T) {
	t.Parallel()

	store := NewStore(statePath(t))
	want := baseline()

	if err := store.Save(`C:\Shared`, "win-sttm11d02rd", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(`C:\Shared`, "win-sttm11d02rd")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("loaded %d rows, want %d", len(got), len(want))
	}
	for key, wantRec := range want {
		gotRec := got[key]
		if gotRec.Local != wantRec.Local || gotRec.Remote != wantRec.Remote {
			t.Errorf("%s = %+v, want %+v", key, gotRec, wantRec)
		}
	}
	if store.Path() == "" {
		t.Error("Path is empty")
	}
}

// The first run. There is no state file, and that is not a fault: the answer
// is an empty history, which makes every difference a conflict rather than a
// copy — rule 5's "no silent first sync".
func TestNoStateFileYetIsNotAFailure(t *testing.T) {
	t.Parallel()

	got, err := NewStore(statePath(t)).Load(`C:\Shared`, "peer")
	if err != nil {
		t.Fatalf("Load = %v, want the first run treated as normal", err)
	}
	if len(got) != 0 {
		t.Errorf("Load = %+v, want an empty history", got)
	}
}

// Everything a state file can be that is not usable. Each answers with
// ErrNoHistory *and* an empty baseline, so a caller that logged the error and
// carried on still gets the safe behaviour rather than the wrong one.
func TestAStateFileThatCannotBeUsedCostsAConflictAndNeverAFile(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, body, folder, peer string }{
		{"truncated mid-write", `{"version":1,"folder":"C:\\Shared","fil`, `C:\Shared`, "peer"},
		{"not json at all", "\x00\x01\x02 rubbish", `C:\Shared`, "peer"},
		{"an empty file", "", `C:\Shared`, "peer"},
		{
			"written by a newer version",
			`{"version":99,"folder":"C:\\Shared","peer":"peer","files":{}}`,
			`C:\Shared`, "peer",
		},
		{
			"about another folder",
			`{"version":1,"folder":"C:\\Other","peer":"peer","files":{}}`,
			`C:\Shared`, "peer",
		},
		{
			"about another machine",
			`{"version":1,"folder":"C:\\Shared","peer":"laptop","files":{}}`,
			`C:\Shared`, "peer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := statePath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatalf("creating the folder: %v", err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("writing the state: %v", err)
			}

			got, err := NewStore(path).Load(tc.folder, tc.peer)
			if !errors.Is(err, ErrNoHistory) {
				t.Fatalf("Load = %v, want ErrNoHistory", err)
			}
			if len(got) != 0 {
				t.Errorf("Load = %+v, want an empty history alongside the error", got)
			}
		})
	}
}

// The folder and the machine are compared the way Windows compares them, so a
// path the user typed in a different case is still the same folder and the
// history is still theirs.
func TestTheFolderAndTheMachineAreMatchedTheWayWindowsWould(t *testing.T) {
	t.Parallel()

	store := NewStore(statePath(t))
	if err := store.Save(`C:\Shared`, "win-sttm11d02rd", baseline()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(`c:\shared`, "WIN-STTM11D02RD")
	if err != nil {
		t.Fatalf("Load = %v, want the same history", err)
	}
	if len(got) == 0 {
		t.Error("the history was thrown away over a difference of case")
	}
}

// The state is written the way a transferred file is: to a temporary name and
// then renamed. A kill mid-write therefore leaves the old state or the new one
// and never half of either — and half a state file is exactly the input that
// would make the next pass believe a copy had happened.
func TestTheStateIsPublishedByARenameAndLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	path := statePath(t)
	store := NewStore(path)
	if err := store.Save(`C:\Shared`, "peer", baseline()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Save(`C:\Shared`, "peer", foldersync.Baseline{}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	if got := leftovers(t, filepath.Dir(path)); len(got) != 0 {
		t.Errorf("temporary files left behind: %v", got)
	}
	got, err := store.Load(`C:\Shared`, "peer")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load = %+v, want the second Save to have replaced the first", got)
	}
}

// A file that says which rows are a conflict the user still has to resolve.
// It is written for a person reading the state, not read back, so this asserts
// only that it is there and honest.
func TestTheStateSaysWhichRowsAreStillADivergence(t *testing.T) {
	t.Parallel()

	path := statePath(t)
	if err := NewStore(path).Save(`C:\Shared`, "peer", baseline()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the state: %v", err)
	}
	body := string(blob)
	if !contains(body, `"diverged": true`) {
		t.Errorf("the state does not mark the divergence:\n%s", body)
	}
	// The row the two machines agree on must not be marked.
	if count(body, `"diverged": true`) != 1 {
		t.Errorf("the state marks more rows than diverge:\n%s", body)
	}
}

func TestSaveReportsAFolderItCannotCreate(t *testing.T) {
	t.Parallel()

	// A file where the state's folder should be: MkdirAll cannot get past it.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "LinkMonitor")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatalf("writing the blocker: %v", err)
	}

	err := NewStore(filepath.Join(blocker, "sync-state.json")).Save(`C:\Shared`, "peer", baseline())
	if err == nil {
		t.Fatal("Save succeeded, want the folder failure reported")
	}
}

func TestLoadReportsAStateItCannotEvenOpen(t *testing.T) {
	t.Parallel()

	// A directory where the file should be: ReadFile fails with something
	// other than "not there", which is the branch this covers.
	dir := filepath.Join(t.TempDir(), "sync-state.json")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}

	got, err := NewStore(dir).Load(`C:\Shared`, "peer")
	if !errors.Is(err, ErrNoHistory) {
		t.Fatalf("Load = %v, want ErrNoHistory", err)
	}
	if len(got) != 0 {
		t.Errorf("Load = %+v, want an empty history", got)
	}
}

func TestTheDefaultStateLivesUnderTheLocalApplicationData(t *testing.T) {
	t.Parallel()

	got, err := DefaultStatePath()
	if err != nil {
		t.Fatalf("DefaultStatePath: %v", err)
	}
	if filepath.Base(got) != "sync-state.json" {
		t.Errorf("DefaultStatePath = %q, want it to end in sync-state.json", got)
	}
	if filepath.Base(filepath.Dir(got)) != "LinkMonitor" {
		t.Errorf("DefaultStatePath = %q, want it under a LinkMonitor folder", got)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("DefaultStatePath = %q, want an absolute path", got)
	}
}

// contains and count keep the assertions above readable without pulling in a
// dependency for two lines.
func contains(haystack, needle string) bool { return count(haystack, needle) > 0 }

func count(haystack, needle string) int {
	var n int
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			n++
		}
	}
	return n
}
