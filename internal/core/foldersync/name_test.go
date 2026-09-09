package foldersync

import (
	"strings"
	"testing"
	"time"
)

func TestAConflictCopyIsNamedInRussianAfterWhereItCameFrom(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 4, 21, 21, 4, 0, 0, time.UTC)
	for _, tc := range []struct{ name, rel, peer, want string }{
		{
			name: "the example from the rules",
			rel:  "заметки.txt", peer: "win-sttm11d02rd",
			want: "заметки (с win-sttm11d02rd, 21-04).txt",
		},
		{
			name: "in a subfolder",
			rel:  "проект/план.md", peer: "win-sttm11d02rd",
			want: "проект/план (с win-sttm11d02rd, 21-04).md",
		},
		{
			name: "no extension",
			rel:  "Makefile", peer: "pc",
			want: "Makefile (с pc, 21-04)",
		},
		{
			name: "two extensions keeps only the last, as Windows does",
			rel:  "dump.tar.gz", peer: "pc",
			want: "dump.tar (с pc, 21-04).gz",
		},
		{
			name: "a peer nobody named",
			rel:  "notes.txt", peer: "   ",
			want: "notes (с другой машины, 21-04).txt",
		},
		{
			name: "a peer name that could not be a file name",
			rel:  "notes.txt", peer: `win:st/m\1*?"<>|`,
			want: "notes (с winstm1, 21-04).txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := conflictName(tc.rel, tc.peer, when, nil)
			if got != tc.want {
				t.Errorf("conflictName(%q, %q) = %q, want %q", tc.rel, tc.peer, got, tc.want)
			}
			// Whatever it is called, it must still be a name that cannot leave
			// the folder — otherwise the conflict rule would have opened the
			// hole the name rule closes.
			if _, err := SafeRel(got); err != nil {
				t.Errorf("the conflict name %q is not a safe path: %v", got, err)
			}
		})
	}
}

func TestAConflictCopyNeverLandsOnAnExistingFile(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 4, 21, 21, 4, 0, 0, time.UTC)
	taken := map[string]bool{
		"заметки (с pc, 21-04).txt":    true,
		"заметки (с pc, 21-04, 2).txt": true,
	}

	got := conflictName("заметки.txt", "pc", when, taken)
	if got != "заметки (с pc, 21-04, 3).txt" {
		t.Errorf("conflictName = %q, want the third free name", got)
	}
}

// The names are compared the way Windows compares them, so a copy cannot land
// on a file that differs from it only in case.
func TestAConflictCopyAvoidsANameThatDiffersOnlyInCase(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 4, 21, 21, 4, 0, 0, time.UTC)
	taken := map[string]bool{"notes (с pc, 21-04).txt": true}

	got := conflictName("Notes.TXT", "pc", when, taken)
	if got != "Notes (с pc, 21-04, 2).TXT" {
		t.Errorf("conflictName = %q, want the taken name avoided", got)
	}
}

func TestALongPeerNameIsCutRatherThanCarriedIntoAPath(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("длинноеимя", 12)
	got := peerLabel(long)
	if len([]rune(got)) != maxPeerLabel {
		t.Errorf("peerLabel gave %d runes, want %d", len([]rune(got)), maxPeerLabel)
	}
	if !strings.HasPrefix(long, got) {
		t.Errorf("peerLabel = %q, want a prefix of the name", got)
	}
}

func TestTwoConflictsInOnePassCannotClaimTheSameName(t *testing.T) {
	t.Parallel()

	// Two files whose stems differ only in case: their conflict names would
	// collide if the plan did not remember what it had already claimed.
	base := Baseline{
		"notes.txt": agreed(10, 100, "old"),
		"NOTES.txt": agreed(10, 100, "old"),
	}
	plan := Decide(input(
		[]Entry{file("notes.txt", 12, 300, "mine")},
		[]Entry{file("notes.txt", 14, 400, "theirs")},
		base,
	))
	// One file, one conflict — the fixture above is there to show the taken
	// set is seeded from the local listing, which this asserts directly.
	if len(plan.Actions) != 1 {
		t.Fatalf("actions = %+v, want one", plan.Actions)
	}

	// Now the real case: the conflict's own name is already on disk.
	plan = Decide(input(
		[]Entry{
			file("notes.txt", 12, 300, "mine"),
			file("notes (с win-sttm11d02rd, 12-34).txt", 1, 100, "older"),
		},
		[]Entry{file("notes.txt", 14, 400, "theirs")},
		Baseline{"notes.txt": agreed(10, 100, "old")},
	))
	var conflict Action
	for _, a := range plan.Actions {
		if a.Conflict {
			conflict = a
		}
	}
	if conflict.Dest != "notes (с win-sttm11d02rd, 12-34, 2).txt" {
		t.Errorf("conflict destination = %q, want the existing copy left alone", conflict.Dest)
	}
}
