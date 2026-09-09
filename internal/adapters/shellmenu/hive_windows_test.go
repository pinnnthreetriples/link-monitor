//go:build windows

package shellmenu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// nextScratch numbers the throwaway roots these tests write under, so two
// parallel tests never share one.
var nextScratch atomic.Uint64

// scratchParent holds every throwaway root. It is deleted on the way out too —
// a test suite that leaves an empty key behind is exactly the litter this
// package refuses to leave in Software\Classes.
const scratchParent = `Software\LinkMonitorSelfTest`

// scratchRoot returns a key path under HKCU that no other program uses, and
// arranges for the whole subtree to be gone when the test ends.
//
// The tests write to the real registry — that is the point of them — but never
// to Software\Classes: an install that half-succeeded in a test run must not
// leave a verb in the user's own Explorer menu. The root is injected exactly so
// that these tests can be honest about the registry and harmless to the
// machine.
func scratchRoot(t *testing.T) string {
	t.Helper()

	root := fmt.Sprintf(`%s\%d-%d`, scratchParent, os.Getpid(), nextScratch.Add(1))
	t.Cleanup(func() {
		if err := deleteTree(root); err != nil {
			t.Errorf("cleaning up HKCU\\%s: %v", root, err)
		}
		// And the parent, once the last test has let go of it. DeleteKey
		// refuses a key that still has subkeys, so a sibling test still
		// running keeps it — which is why the error is ignored here rather
		// than reported: "not yet" is the expected answer most of the time.
		_ = registry.DeleteKey(registry.CURRENT_USER, scratchParent)
	})
	return root
}

// deleteTree removes a key and everything under it. The registry refuses to
// delete a key with subkeys, so the children go first.
func deleteTree(path string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	names, err := key.ReadSubKeyNames(-1)
	if closeErr := key.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("listing %s: %w", path, err)
	}

	for _, name := range names {
		if err := deleteTree(path + `\` + name); err != nil {
			return err
		}
	}
	if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil &&
		!errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("deleting %s: %w", path, err)
	}
	return nil
}

// readValues returns every value of a key, so a test can assert on what was
// really written rather than on what was asked for.
func readValues(t *testing.T, path string) map[string]string {
	t.Helper()

	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf(`opening HKCU\%s: %v`, path, err)
	}
	t.Cleanup(func() {
		if err := key.Close(); err != nil {
			t.Errorf(`closing HKCU\%s: %v`, path, err)
		}
	})

	names, err := key.ReadValueNames(-1)
	if err != nil {
		t.Fatalf(`listing the values of HKCU\%s: %v`, path, err)
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		value, _, err := key.GetStringValue(name)
		if err != nil {
			t.Fatalf(`reading HKCU\%s\%s: %v`, path, valueName(name), err)
		}
		out[name] = value
	}
	return out
}

func TestTheRealRegistryTakesTheItemAndGivesItBack(t *testing.T) {
	t.Parallel()

	root := scratchRoot(t)
	h := winHive{}
	verb := verbPath(root)

	for round := 1; round <= 2; round++ {
		if err := install(h, root, theExe); err != nil {
			t.Fatalf("install() in round %d = %v", round, err)
		}

		wantVerb := map[string]string{
			"MUIVerb":          "Отправить на ПК",
			"Icon":             `"C:\Program Files\Link Monitor\linkmon.exe",0`,
			"MultiSelectModel": "Document",
		}
		if got := readValues(t, verb); !reflect.DeepEqual(got, wantVerb) {
			t.Errorf("round %d: the verb key holds %+v, want %+v", round, got, wantVerb)
		}
		wantCommand := map[string]string{
			"": `"C:\Program Files\Link Monitor\linkmon.exe" -send "%1"`,
		}
		if got := readValues(t, verb+`\`+commandKey); !reflect.DeepEqual(got, wantCommand) {
			t.Errorf("round %d: the command key holds %+v, want %+v", round, got, wantCommand)
		}

		ok, err := installed(h, root)
		if err != nil || !ok {
			t.Fatalf("round %d: installed() = (%t, %v), want (true, nil)", round, ok, err)
		}

		if err := remove(h, root); err != nil {
			t.Fatalf("round %d: remove() = %v", round, err)
		}
		if left := subkeysUnder(t, root); len(left) != 0 {
			t.Fatalf("round %d: remove() left %v under HKCU\\%s", round, left, root)
		}
		ok, err = installed(h, root)
		if err != nil || ok {
			t.Fatalf("round %d: installed() after remove = (%t, %v), want (false, nil)", round, ok, err)
		}
	}
}

// TestRemoveKeepsARealNeighboursVerb repeats the pruning rule against the
// registry rather than the fake, because the fake could be wrong about what
// "empty" means and only the registry can settle it.
func TestRemoveKeepsARealNeighboursVerb(t *testing.T) {
	t.Parallel()

	root := scratchRoot(t)
	h := winHive{}
	neighbour := root + `\*\shell\SomeoneElse.DoAThing`

	if err := h.set(neighbour, map[string]string{"MUIVerb": "Что-то ещё"}); err != nil {
		t.Fatalf("planting a neighbour: %v", err)
	}
	if err := install(h, root, theExe); err != nil {
		t.Fatalf("install() = %v", err)
	}
	if err := remove(h, root); err != nil {
		t.Fatalf("remove() = %v", err)
	}

	if got := readValues(t, neighbour); got["MUIVerb"] != "Что-то ещё" {
		t.Errorf("the neighbour's verb holds %+v after our removal", got)
	}
	if ok, err := h.exists(verbPath(root)); err != nil || ok {
		t.Errorf("our verb is still there: (%t, %v)", ok, err)
	}
}

// subkeysUnder lists the immediate children of a key, or nothing when the key
// itself is gone.
func subkeysUnder(t *testing.T, path string) []string {
	t.Helper()

	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf(`opening HKCU\%s: %v`, path, err)
	}
	defer func() {
		if err := key.Close(); err != nil {
			t.Errorf(`closing HKCU\%s: %v`, path, err)
		}
	}()

	names, err := key.ReadSubKeyNames(-1)
	if err != nil {
		t.Fatalf(`listing HKCU\%s: %v`, path, err)
	}
	return names
}

// TestTheCommandLineSurvivesWindowsOwnArgvParser is the quoting test that
// matters. Explorer substitutes the clicked file's path into %1 and hands the
// result to CreateProcess; the program on the other end gets its arguments back
// through CommandLineToArgvW. So the check is not "does the string look right"
// but "does Windows's own parser hand back exactly the path that went in".
func TestTheCommandLineSurvivesWindowsOwnArgvParser(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		exe  string
		file string
	}{
		{
			name: "a plain path",
			exe:  `C:\tools\linkmon.exe`,
			file: `C:\tmp\notes.txt`,
		},
		{
			name: "spaces on both sides",
			exe:  `C:\Program Files\Link Monitor\linkmon.exe`,
			file: `C:\Users\pnj\My Documents\the big report.pdf`,
		},
		{
			name: "an ampersand, which cmd.exe would have eaten",
			exe:  `C:\Program Files\Link Monitor\linkmon.exe`,
			file: `C:\tmp\rock & roll.mp3`,
		},
		{
			name: "a per cent sign, which cmd.exe would have expanded",
			exe:  `C:\Program Files\Link Monitor\linkmon.exe`,
			file: `C:\tmp\100%25 %PATH% done.txt`,
		},
		{
			name: "a Cyrillic name",
			exe:  `C:\Program Files\Link Monitor\linkmon.exe`,
			file: `C:\Users\pnj\Загрузки\Отчёт за сентябрь.docx`,
		},
		{
			name: "Cyrillic with a space and an ampersand",
			exe:  `C:\Программы\Монитор связи\linkmon.exe`,
			file: `C:\Мои файлы\счёт & акт (копия).pdf`,
		},
		{
			name: "the characters a shell would treat as operators",
			exe:  `C:\Program Files\Link Monitor\linkmon.exe`,
			file: `C:\tmp\a^b;c,d=e!f#g$h.txt`,
		},
		{
			name: "a bracket and a single quote",
			exe:  `C:\Program Files\Link Monitor\linkmon.exe`,
			file: `C:\tmp\[draft] don't lose this.txt`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// This is Explorer's substitution, and nothing else: the template
			// already carries the quotes, so the raw path goes straight in.
			line := strings.Replace(commandLine(tc.exe), commandTarget, tc.file, 1)

			argv, err := windows.DecomposeCommandLine(line)
			if err != nil {
				t.Fatalf("Windows could not parse\n%s\n%v", line, err)
			}
			want := []string{tc.exe, sendFlag, tc.file}
			if !reflect.DeepEqual(argv, want) {
				t.Errorf("Windows parsed\n%s\ninto %q, want %q", line, argv, want)
			}
		})
	}
}

// TestValueNameNamesTheDefaultValue covers the one thing that would otherwise
// be tested only by a failure: an error message about the unnamed value must
// not read as though a word were missing.
func TestValueNameNamesTheDefaultValue(t *testing.T) {
	t.Parallel()

	if got := valueName(""); got != "(default)" {
		t.Errorf("valueName(\"\") = %q, want %q", got, "(default)")
	}
	if got := valueName("MUIVerb"); got != "MUIVerb" {
		t.Errorf("valueName(%q) = %q, want it unchanged", "MUIVerb", got)
	}
}

func TestCurrentDescribesTheRunningExecutable(t *testing.T) {
	t.Parallel()

	m, err := Current()
	if err != nil {
		t.Fatalf("Current() = %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v", err)
	}
	if m.ExePath() != self {
		t.Errorf("Current().ExePath() = %q, want %q", m.ExePath(), self)
	}
	// The test binary is a real .exe in a real directory, so the path a live
	// install would be built from has to pass the same check.
	if err := checkExe(m.ExePath()); err != nil {
		t.Errorf("checkExe(%q) = %v; this program cannot register itself", filepath.Base(self), err)
	}
}
