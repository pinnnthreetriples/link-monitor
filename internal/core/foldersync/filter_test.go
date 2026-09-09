package foldersync

import (
	"errors"
	"strings"
	"testing"
)

// Rule 7. This is the same reasoning internal/adapters/tailscale's safeBase
// applies to a Taildrop inbox entry, applied to a path rather than one name.
func TestSafeRelRefusesEveryWayOutOfTheFolder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, rel string }{
		{"empty", ""},
		{"only spaces", "   "},
		{"the folder itself", "."},
		{"the parent", ".."},
		{"a parent in the middle", "sub/../../secret.txt"},
		{"a leading parent", "../secret.txt"},
		{"a trailing parent", "sub/.."},
		{"absolute", "/windows/system32/evil.dll"},
		{"a windows separator", `sub\evil.dll`},
		{"a bare windows path", `C:\Windows\evil.dll`},
		{"a drive letter", "C:/Windows/evil.dll"},
		{"an alternate data stream", "notes.txt:hidden"},
		{"an empty component", "sub//notes.txt"},
		{"a trailing dot", "notes.txt."},
		{"a trailing space inside", "sub /notes.txt"},
		{"a trailing dot inside", "sub./notes.txt"},
		{"a newline", "notes\n.txt"},
		{"a delete character", "notes\x7f.txt"},
		{"a nul", "notes\x00.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := SafeRel(tc.rel)
			if !errors.Is(err, ErrUnsafeName) {
				t.Fatalf("SafeRel(%q) = %q, %v; want ErrUnsafeName", tc.rel, got, err)
			}
		})
	}
}

func TestSafeRelAcceptsTheNamesPeopleActuallyUse(t *testing.T) {
	t.Parallel()

	for _, rel := range []string{
		"notes.txt",
		"заметки.txt",
		"sub/notes.txt",
		"a/b/c/deep.txt",
		".gitignore",
		"a file with spaces.txt",
		"archive.tar.gz",
		"заметки (с win-sttm11d02rd, 21-04).txt",
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()

			got, err := SafeRel(rel)
			if err != nil {
				t.Fatalf("SafeRel(%q) = %v, want it accepted", rel, err)
			}
			if got != rel {
				t.Errorf("SafeRel(%q) = %q, want it unchanged", rel, got)
			}
		})
	}
}

// A refusal has to name the path, because one the user cannot connect to a
// file is one they cannot act on.
func TestARefusedNameIsNamedInTheError(t *testing.T) {
	t.Parallel()

	_, err := SafeRel("../secret.txt")
	if err == nil || !strings.Contains(err.Error(), "../secret.txt") {
		t.Errorf("error = %v, want it to name ../secret.txt", err)
	}
}

// The space around a whole path is trimmed rather than refused: Windows would
// open the same file either way, so the trimmed form is the honest name for
// it. A space *inside* the path is a different matter and is refused above.
func TestSafeRelTrimsTheSurroundingSpaceAndKeepsTheRest(t *testing.T) {
	t.Parallel()

	got, err := SafeRel("  sub/notes.txt  ")
	if err != nil {
		t.Fatalf("SafeRel = %v", err)
	}
	if got != "sub/notes.txt" {
		t.Errorf("SafeRel = %q, want the path without its surrounding space", got)
	}
}

// Rule 5's exclude list. Every component is tested, so a directory prunes its
// whole subtree — which is what keeps .git and node_modules free.
func TestTheBuiltInExcludeListPrunesWholeSubtrees(t *testing.T) {
	t.Parallel()

	for _, rel := range []string{
		".git", ".git/config", "sub/.git/HEAD",
		"node_modules", "node_modules/left-pad/index.js", "app/node_modules/x/y.js",
		"~$report.docx", "sub/~$report.docx",
		"scratch.tmp", "sub/scratch.tmp",
		"desktop.ini", "Desktop.ini", "sub/DESKTOP.INI",
		"Thumbs.db", "thumbs.db",
		"lm-abc" + TempSuffix, "sub/lm-abc" + TempSuffix,
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()

			if !Excluded(rel, Limits{}) {
				t.Errorf("Excluded(%q) = false, want it excluded", rel)
			}
		})
	}
}

func TestTheExcludeListLeavesOrdinaryFilesAlone(t *testing.T) {
	t.Parallel()

	for _, rel := range []string{
		"notes.txt", "sub/notes.txt", "gitignore.txt", "my-node_modules-notes.txt",
		"report.docx", "temporary.txt", "заметки.txt",
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()

			if Excluded(rel, Limits{}) {
				t.Errorf("Excluded(%q) = true, want it kept", rel)
			}
		})
	}
}

func TestTheUsersOwnPatternsAreAppliedToo(t *testing.T) {
	t.Parallel()

	lim := Limits{Exclude: []string{"*.iso", "  кэш  ", "", "logs/*.log", "[bad"}}
	for _, tc := range []struct {
		rel  string
		want bool
	}{
		{"windows.iso", true},
		{"sub/windows.iso", true},
		{"WINDOWS.ISO", true},
		{"кэш/anything.txt", true},
		{"logs/today.log", true},
		{"deeper/logs/today.log", false}, // the pattern names a whole path
		{"notes.txt", false},
		// A malformed pattern matches nothing rather than refusing the folder.
		{"[bad", false},
	} {
		t.Run(tc.rel, func(t *testing.T) {
			t.Parallel()

			if got := Excluded(tc.rel, lim); got != tc.want {
				t.Errorf("Excluded(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

func TestTheSizeCapFallsBackToTheDefault(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		lim  Limits
		want int64
	}{
		{"unset", Limits{}, DefaultMaxBytes},
		{"negative", Limits{MaxBytes: -1}, DefaultMaxBytes},
		{"chosen", Limits{MaxBytes: 4096}, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := maxBytes(tc.lim); got != tc.want {
				t.Errorf("maxBytes(%+v) = %d, want %d", tc.lim, got, tc.want)
			}
		})
	}
}

// A file at exactly the cap is carried; the cap is a limit, not a target.
func TestAFileExactlyAtTheCapStillTravels(t *testing.T) {
	t.Parallel()

	in := input([]Entry{file("edge.bin", 1000, 100, "")}, nil, nil)
	in.Limits = Limits{MaxBytes: 1000}

	plan := Decide(in)
	if len(plan.Actions) != 1 || len(plan.Skips) != 0 {
		t.Errorf("plan = %+v, want the file carried", plan)
	}
}
