package quickopen

import (
	"path/filepath"
	"strings"
	"testing"
)

// fakeFinder answers for a machine that has exactly the files listed in
// present, plus whatever onPath names. It records every search so a test can
// assert that a tool was asked for by absolute path before PATH was consulted.
type fakeFinder struct {
	present map[string]bool
	onPath  map[string]string
	asked   [][]string
}

func (f *fakeFinder) find(paths []string, bare string) string {
	f.asked = append(f.asked, append(append([]string(nil), paths...), bare))
	for _, path := range paths {
		if f.present[path] {
			return path
		}
	}
	return f.onPath[bare]
}

func TestEveryToolIsAskedForByPathBeforePath(t *testing.T) {
	t.Parallel()

	// The laptop this was written for: OpenSSH and mstsc where Windows puts
	// them, no Windows Terminal, and a Git installation whose own ssh.exe
	// comes first on PATH. Locating by path is what keeps the MSYS client —
	// which reads C:\Users\... as an MSYS path — out of the terminal.
	system := envDir("SystemRoot", `C:\Windows`)
	f := &fakeFinder{
		present: map[string]bool{
			filepath.Join(system, "System32", "OpenSSH", "ssh.exe"): true,
			filepath.Join(system, "System32", "conhost.exe"):        true,
			filepath.Join(system, "System32", "cmd.exe"):            true,
			filepath.Join(system, "explorer.exe"):                   true,
			filepath.Join(system, "System32", "mstsc.exe"):          true,
		},
		onPath: map[string]string{"ssh.exe": `C:\Program Files\Git\usr\bin\ssh.exe`},
	}

	got := locate(f)

	if want := filepath.Join(system, "System32", "OpenSSH", "ssh.exe"); got.ssh != want {
		t.Errorf("ssh = %q, want the client Windows ships at %q", got.ssh, want)
	}
	if strings.Contains(got.ssh, "Git") {
		t.Error("locate picked the MSYS ssh.exe that PATH answers with first")
	}
	if got.terminal != "" {
		t.Errorf("locate found Windows Terminal at %q on a machine without it", got.terminal)
	}
	if got.conhost == "" || got.shell == "" || got.explorer == "" || got.rdp == "" {
		t.Errorf("locate missed a tool every Windows install has: %+v", got)
	}
	if len(f.asked) != 6 {
		t.Errorf("locate ran %d searches, want one per tool", len(f.asked))
	}
	for _, search := range f.asked {
		if len(search) < 2 {
			t.Errorf("a tool was looked up on PATH with no absolute path tried first: %q", search)
		}
	}
}

func TestWindowsTerminalIsFoundThroughItsExecutionAlias(t *testing.T) {
	t.Parallel()

	// Windows Terminal installs as a packaged app, and what a caller can start
	// is the alias under WindowsApps rather than a file in Program Files.
	f := &fakeFinder{onPath: map[string]string{"wt.exe": `C:\...\WindowsApps\wt.exe`}}

	if got := locate(f).terminal; got == "" {
		t.Error("locate did not find Windows Terminal on a machine that has it")
	}
}

func TestAMachineWithNoneOfTheToolsReportsNoneOfThem(t *testing.T) {
	t.Parallel()

	// The build agent, and any Linux cross-build: nothing is found, nothing is
	// invented, and every action says what is missing at the click.
	if got := locate(&fakeFinder{}); got != (tools{}) {
		t.Errorf("locate invented tools on a machine that has none: %+v", got)
	}
}

func TestTheRealFinderLooksAtTheFilesystemAndThenPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "tool.exe")
	if err := writeStub(file); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}

	f := osFinder{}
	if got := f.find([]string{"", filepath.Join(dir, "missing.exe"), file}, "nothing-like-this"); got != file {
		t.Errorf("find returned %q, want the file that exists at %q", got, file)
	}
	// A directory is not a program, and neither is a name nothing answers to.
	if got := f.find([]string{dir}, "definitely-not-a-real-program-xyz"); got != "" {
		t.Errorf("find returned %q for a directory and an unknown name", got)
	}
}

func TestADirectoryFromTheEnvironmentFallsBackToWhatWindowsAlwaysHas(t *testing.T) {
	// Not parallel: t.Setenv writes the process's own environment.
	t.Setenv("QUICKOPEN_TEST_DIR", "")
	if got := envDir("QUICKOPEN_TEST_DIR", `C:\Windows`); got != `C:\Windows` {
		t.Errorf("envDir with an empty variable = %q, want the fallback", got)
	}
	t.Setenv("QUICKOPEN_TEST_DIR", `D:\elsewhere`)
	if got := envDir("QUICKOPEN_TEST_DIR", `C:\Windows`); got != `D:\elsewhere` {
		t.Errorf("envDir = %q, want the variable's value", got)
	}
}
