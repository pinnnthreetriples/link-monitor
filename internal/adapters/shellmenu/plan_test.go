package shellmenu

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// testRoot stands in for Software\Classes in the tests that only look at the
// shape of a path.
const testRoot = `Software\Classes`

// theExe is a plausible install location, chosen because it has a space in it:
// this program is meant to survive C:\Program Files.
const theExe = `C:\Program Files\Link Monitor\linkmon.exe`

func TestPlanWritesTheVerbAndItsCommandAndNothingElse(t *testing.T) {
	t.Parallel()

	entries, err := plan(testRoot, theExe)
	if err != nil {
		t.Fatalf("plan() = %v, want a plan", err)
	}

	want := []entry{
		{
			path: `Software\Classes\*\shell\LinkMonitor.SendToPC`,
			values: map[string]string{
				"MUIVerb":          "Отправить на ПК",
				"Icon":             `"C:\Program Files\Link Monitor\linkmon.exe",0`,
				"MultiSelectModel": "Document",
			},
		},
		{
			path: `Software\Classes\*\shell\LinkMonitor.SendToPC\command`,
			values: map[string]string{
				"": `"C:\Program Files\Link Monitor\linkmon.exe" -send "%1"`,
			},
		},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("plan() =\n%+v\nwant\n%+v", entries, want)
	}
}

// TestTheLabelIsTheRussianTheUserWillRead pins the one string that appears on
// screen. It is a test rather than a comment because a typo here is a typo in
// the product, and because the tray names the same string.
func TestTheLabelIsTheRussianTheUserWillRead(t *testing.T) {
	t.Parallel()

	if verbLabel != "Отправить на ПК" {
		t.Errorf("verbLabel = %q, want «Отправить на ПК»", verbLabel)
	}
	if got := New(theExe).Label(); got != verbLabel {
		t.Errorf("Menu.Label() = %q, want %q", got, verbLabel)
	}
}

func TestCommandLineQuotesThePathsExplorerWillMeet(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		exe  string
		want string
	}{
		{
			name: "a plain path",
			exe:  `C:\tools\linkmon.exe`,
			want: `"C:\tools\linkmon.exe" -send "%1"`,
		},
		{
			name: "a path with spaces",
			exe:  `C:\Program Files\Link Monitor\linkmon.exe`,
			want: `"C:\Program Files\Link Monitor\linkmon.exe" -send "%1"`,
		},
		{
			name: "a path with an ampersand",
			exe:  `C:\Rock & Roll\linkmon.exe`,
			want: `"C:\Rock & Roll\linkmon.exe" -send "%1"`,
		},
		{
			name: "a Cyrillic path",
			exe:  `C:\Программы\Мой монитор\linkmon.exe`,
			want: `"C:\Программы\Мой монитор\linkmon.exe" -send "%1"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := commandLine(tc.exe); got != tc.want {
				t.Errorf("commandLine(%q) =\n%s\nwant\n%s", tc.exe, got, tc.want)
			}
		})
	}
}

func TestCheckExeRefusesAPathThatWouldMakeADeadMenuItem(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		exe     string
		wantErr bool
	}{
		{name: "the install location", exe: theExe},
		{name: "a Cyrillic install location", exe: `C:\Программы\Монитор связи\linkmon.exe`},
		{name: "an ampersand in the path", exe: `C:\Rock & Roll\linkmon.exe`},
		{name: "a UNC path is absolute too", exe: `\\server\share\linkmon.exe`},
		{name: "the extension in capitals", exe: `C:\tools\LINKMON.EXE`},
		{name: "nothing at all", exe: "", wantErr: true},
		{name: "whitespace", exe: "   ", wantErr: true},
		{name: "a relative path", exe: `linkmon.exe`, wantErr: true},
		{name: "a path relative to the drive", exe: `\tools\linkmon.exe`, wantErr: true},
		{name: "a double quote", exe: `C:\to"ols\linkmon.exe`, wantErr: true},
		{name: "a per cent sign", exe: `C:\100% done\linkmon.exe`, wantErr: true},
		{name: "an Explorer placeholder", exe: `C:\tools\%1\linkmon.exe`, wantErr: true},
		{name: "a newline", exe: "C:\\tools\\link\nmon.exe", wantErr: true},
		{name: "a NUL", exe: "C:\\tools\\link\x00mon.exe", wantErr: true},
		{name: "not an executable", exe: `C:\tools\linkmon.dll`, wantErr: true},
		{name: "no extension", exe: `C:\tools\linkmon`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := checkExe(tc.exe)
			if tc.wantErr {
				if !errors.Is(err, errBadExePath) {
					t.Fatalf("checkExe(%q) = %v, want an errBadExePath", tc.exe, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkExe(%q) = %v, want no error", tc.exe, err)
			}
		})
	}
}

func TestPlanRefusesWhatCheckExeRefuses(t *testing.T) {
	t.Parallel()

	entries, err := plan(testRoot, `linkmon.exe`)
	if !errors.Is(err, errBadExePath) {
		t.Fatalf("plan() with a relative exe = %v, want an errBadExePath", err)
	}
	if entries != nil {
		t.Errorf("plan() returned %d entries alongside its refusal", len(entries))
	}
}

func TestAncestorsNamesTheContainersRemovalMayOfferBack(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		root string
		path string
		want []string
	}{
		{
			name: "the verb under the all-files class",
			root: `Software\Classes`,
			path: `Software\Classes\*\shell\LinkMonitor.SendToPC`,
			want: []string{`Software\Classes\*\shell`, `Software\Classes\*`},
		},
		{
			name: "a key directly under the root has no ancestors to offer",
			root: `Software\Classes`,
			path: `Software\Classes\*`,
			want: nil,
		},
		{
			name: "the root itself is never offered",
			root: `Software\Classes`,
			path: `Software\Classes`,
			want: nil,
		},
		{
			name: "a deeper root shortens the walk",
			root: `Software\Classes\*`,
			path: `Software\Classes\*\shell\LinkMonitor.SendToPC\command`,
			want: []string{
				`Software\Classes\*\shell\LinkMonitor.SendToPC`,
				`Software\Classes\*\shell`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ancestors(tc.root, tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ancestors(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

// TestNothingIsWrittenOutsideTheClassesRoot is a standing check on the one
// promise this package makes about where it writes. Every path in the plan has
// to sit under the root it was given, or a caller passing a scratch root — the
// Windows tests do — would be writing into the real one.
func TestNothingIsWrittenOutsideTheClassesRoot(t *testing.T) {
	t.Parallel()

	entries, err := plan(testRoot, theExe)
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.path, testRoot+`\`) {
			t.Errorf("plan() writes %q, which is not under %q", e.path, testRoot)
		}
	}
}
