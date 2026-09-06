package tailscale

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The helper process stands in for tailscale.exe: it prints what the test asks
// for and exits with the code the test asks for. No real CLI is involved.
const (
	helperEnv    = "LINKMON_HELPER"
	helperStdout = "LINKMON_HELPER_STDOUT"
	helperStderr = "LINKMON_HELPER_STDERR"
	helperCode   = "LINKMON_HELPER_CODE"
	helperArg    = "-test.run=TestHelperProcess"
)

// TestHelperProcess is not a test. It is re-executed as a child process by the
// tests below, which is how execRunner gets exercised without tailscale.exe.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("not the helper process")
	}
	fmt.Fprint(os.Stdout, os.Getenv(helperStdout))
	fmt.Fprint(os.Stderr, os.Getenv(helperStderr))
	code, err := strconv.Atoi(os.Getenv(helperCode))
	if err != nil {
		code = 0
	}
	os.Exit(code)
}

// armHelper makes the next child process behave as described.
func armHelper(t *testing.T, stdout, stderr string, code int) {
	t.Helper()
	t.Setenv(helperEnv, "1")
	t.Setenv(helperStdout, stdout)
	t.Setenv(helperStderr, stderr)
	t.Setenv(helperCode, strconv.Itoa(code))
}

func TestExecRunnerRun(t *testing.T) {
	tests := []struct {
		name       string
		stdout     string
		stderr     string
		code       int
		wantStdout string
		wantStderr string
		wantCode   int
	}{
		{name: "success", stdout: "served", wantStdout: "served"},
		{name: "both streams", stdout: "out", stderr: "err", wantStdout: "out", wantStderr: "err"},
		{
			name:       "a non-zero exit is a result, not an error",
			stderr:     "serve not available",
			code:       1,
			wantStderr: "serve not available",
			wantCode:   1,
		},
		{name: "a larger exit code", code: 3, wantCode: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			armHelper(t, tc.stdout, tc.stderr, tc.code)
			r := newExecRunner(os.Args[0])

			res, err := r.Run(context.Background(), helperArg)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if res.Stdout != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", res.Stdout, tc.wantStdout)
			}
			if res.Stderr != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", res.Stderr, tc.wantStderr)
			}
			if res.Code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", res.Code, tc.wantCode)
			}
		})
	}
}

func TestExecRunnerHonoursContext(t *testing.T) {
	armHelper(t, "", "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := newExecRunner(os.Args[0]).Run(ctx, helperArg); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// emptyPath removes every directory from PATH, so that looking a program up
// there is guaranteed to fail whatever is installed on the machine.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestExecRunnerWithoutAnExecutable(t *testing.T) {
	emptyPath(t)
	missing := filepath.Join(t.TempDir(), "tailscale.exe")

	r := newExecRunner(missing)
	_, err := r.Run(context.Background(), "serve", "reset")
	if err == nil {
		t.Fatal("Run() error = nil, want one: the CLI is not there")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Run() error = %v, want it to say the CLI was not found", err)
	}

	// The answer is cached, and stays the same on a second call.
	if _, second := r.Run(context.Background(), "serve", "reset"); second == nil ||
		second.Error() != err.Error() {
		t.Errorf("second Run() error = %v, want the same as the first", second)
	}
}

func TestExecRunnerRefusesADirectory(t *testing.T) {
	emptyPath(t)

	if _, err := newExecRunner(t.TempDir()).Run(context.Background(), "serve"); err == nil {
		t.Fatal("Run() error = nil, want one: a directory is not the CLI")
	}
}

func TestExecRunnerFallsBackToPath(t *testing.T) {
	dir := t.TempDir()
	name := "tailscale"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	stand := filepath.Join(dir, name)
	// Any executable will do: resolution is what is under test, not the CLI.
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatalf("reading the test binary: %v", err)
	}
	if err := os.WriteFile(stand, body, 0o700); err != nil { //nolint:gosec // a throwaway copy in a temp dir
		t.Fatalf("planting a stand-in on PATH: %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := newExecRunner(filepath.Join(t.TempDir(), "nowhere.exe")).resolve()
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if !strings.EqualFold(got, stand) {
		t.Errorf("resolve() = %q, want the copy on PATH %q", got, stand)
	}
}

func TestNewUsesRealImplementations(t *testing.T) {
	c := New()
	if c.daemon == nil || c.runner == nil {
		t.Fatalf("New() left a nil dependency: %+v", c)
	}
	if c.pingAttempts != defaultPingAttempts || c.pingBackoff != defaultPingBackoff {
		t.Errorf("New() ping policy = (%d, %v), want the defaults", c.pingAttempts, c.pingBackoff)
	}

	withPath := New(WithExePath(`D:\tools\tailscale.exe`))
	r, ok := withPath.runner.(*execRunner)
	if !ok {
		t.Fatalf("WithExePath left runner as %T, want *execRunner", withPath.runner)
	}
	if r.configured != `D:\tools\tailscale.exe` {
		t.Errorf("configured path = %q, want the one given", r.configured)
	}
}

func TestWithPingPolicyIgnoresNonsense(t *testing.T) {
	c := New(WithPingPolicy(0, -1))
	if c.pingAttempts != defaultPingAttempts || c.pingBackoff != defaultPingBackoff {
		t.Errorf("ping policy = (%d, %v), want the defaults kept", c.pingAttempts, c.pingBackoff)
	}
}

func TestPeerReachableWithAZeroAttemptPolicy(t *testing.T) {
	d := &fakeDaemon{status: runningStatus(t)}
	c := &Client{daemon: d, runner: &fakeRunner{}} // pingAttempts stays zero

	if _, err := c.PeerReachable(context.Background(), workAddr); err != nil {
		t.Fatalf("PeerReachable() error = %v", err)
	}
	if d.pingN != 1 {
		t.Errorf("sent %d pings, want exactly 1", d.pingN)
	}
}
