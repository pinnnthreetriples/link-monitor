package sshx

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestRunReturnsBothStreamsAndTheExitCode(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t)

	// The command line travels back over a channel: the handler runs on the
	// server's goroutine, and -race would see a plain variable as a race.
	seen := make(chan string, 1)
	server.setHandler(func(cmd string) execResult {
		seen <- cmd
		return execResult{stdout: "on the wire\n", stderr: "a warning\n", code: 0}
	})

	stdout, stderr, code, err := client.Run(t.Context(), "whoami")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := <-seen; got != "whoami" {
		t.Errorf("the peer saw %q", got)
	}
	if stdout != "on the wire\n" {
		t.Errorf("stdout: got %q", stdout)
	}
	if stderr != "a warning\n" {
		t.Errorf("stderr: got %q", stderr)
	}
	if code != 0 {
		t.Errorf("exit code: got %d", code)
	}
}

func TestRunReportsANonZeroExitWithoutCallingItAnError(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t)
	server.setHandler(func(string) execResult {
		return execResult{stderr: "нет такой службы\n", code: 3}
	})

	stdout, stderr, code, err := client.Run(t.Context(), "sc query nope")
	if err != nil {
		t.Fatalf("a non-zero exit is an answer, not a failure: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code: got %d, want 3", code)
	}
	if stdout != "" {
		t.Errorf("stdout: got %q", stdout)
	}
	// A localised message survives the transport untouched.
	if stderr != "нет такой службы\n" {
		t.Errorf("stderr: got %q", stderr)
	}
}

func TestRunReportsAMissingExitStatusAsAnError(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t)
	server.setHandler(func(string) execResult {
		return execResult{stdout: "partial", noExitStatus: true}
	})

	_, _, code, err := client.Run(t.Context(), "dir")
	if err == nil {
		t.Fatal("expected an error when the peer sends no exit status")
	}
	if code != ExitUnknown {
		t.Errorf("exit code: got %d, want %d", code, ExitUnknown)
	}
}

func TestRunRejectsAnEmptyCommand(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	if _, _, _, err := client.Run(t.Context(), "   "); err == nil {
		t.Fatal("expected an error for an empty command")
	}
}

func TestRunHonoursACancelledContext(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t)

	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server.setHandler(func(string) execResult {
		close(started)
		<-release
		return execResult{stdout: "too late"}
	})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()

	_, _, code, err := client.Run(ctx, "timeout /t 60")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if code != ExitUnknown {
		t.Errorf("exit code: got %d, want %d", code, ExitUnknown)
	}
}

func TestRunRefusesAnAlreadyCancelledContext(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := client.Run(ctx, "dir"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRunFailsAfterTheClientIsClosed(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	if err := client.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if _, _, _, err := client.Run(t.Context(), "dir"); err == nil {
		t.Fatal("expected an error once the connection is closed")
	}
}

// TestEncodePowerShellMatchesKnownVectors pins the encoding against values
// computed by hand, so the test cannot agree with a broken implementation the
// way a round-trip through our own decoder would.
func TestEncodePowerShellMatchesKnownVectors(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ in, want string }{
		"ascii":          {"hi", "aABpAA=="},
		"cyrillic":       {"П", "HwQ="},
		"surrogate pair": {"\U0001F600", "PdgA3g=="},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := encodePowerShell(c.in); got != c.want {
				t.Errorf("encodePowerShell(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestEncodePowerShellEmitsNoByteOrderMark(t *testing.T) {
	t.Parallel()
	raw, err := base64.StdEncoding.DecodeString(encodePowerShell("Get-Service"))
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		t.Fatal("the payload starts with a byte-order mark; PowerShell wants none")
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16 payload has an odd length: %d", len(raw))
	}
}

func TestPowerShellCommandLineShape(t *testing.T) {
	t.Parallel()
	got := powerShellCommandLine("Write-Output 'привет'")
	const prefix = "powershell -NoLogo -NoProfile -NonInteractive -EncodedCommand "
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("command line: got %q", got)
	}
	payload := strings.TrimPrefix(got, prefix)
	if strings.ContainsAny(payload, " '\"\n") {
		t.Fatalf("the payload must be bare base64, got %q", payload)
	}
	script := decodePowerShell(t, payload)
	if !strings.HasSuffix(script, "Write-Output 'привет'") {
		t.Fatalf("the payload does not decode back to the script, got %q", script)
	}
	// The answer has to come back as UTF-8, not as the Russian OEM code page.
	if !strings.Contains(script, "[Console]::OutputEncoding") {
		t.Fatalf("the script does not set the output encoding, got %q", script)
	}
}

func TestRunPowerShellDeliversCyrillicAndQuotesIntact(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t)

	const script = `Write-Output "связь: 'ok' & 100% <ok>"`
	server.setHandler(func(cmd string) execResult {
		const prefix = "powershell -NoLogo -NoProfile -NonInteractive -EncodedCommand "
		if !strings.HasPrefix(cmd, prefix) {
			return execResult{stderr: "unexpected command line", code: 1}
		}
		// Echo back only the caller's half, past our encoding preamble.
		decoded := decodePowerShell(t, strings.TrimPrefix(cmd, prefix))
		return execResult{stdout: strings.TrimPrefix(decoded, powerShellPreamble)}
	})

	stdout, _, code, err := client.RunPowerShell(t.Context(), script)
	if err != nil {
		t.Fatalf("RunPowerShell: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code: got %d", code)
	}
	if stdout != script {
		t.Fatalf("the script did not survive the trip: got %q", stdout)
	}
}

func TestRunPowerShellRejectsAnEmptyScript(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	if _, _, _, err := client.RunPowerShell(t.Context(), "\n\t "); err == nil {
		t.Fatal("expected an error for an empty script")
	}
}

func TestExitCodeOfClassifiesTheOutcomes(t *testing.T) {
	t.Parallel()
	if code, err := exitCodeOf(nil); code != 0 || err != nil {
		t.Errorf("nil: got (%d, %v)", code, err)
	}
	other := errors.New("connection lost")
	code, err := exitCodeOf(other)
	if code != ExitUnknown || !errors.Is(err, other) {
		t.Errorf("other: got (%d, %v)", code, err)
	}
}

// decodePowerShell is the inverse of encodePowerShell, used only to check what
// the peer received.
func decodePowerShell(t *testing.T, payload string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decoding the -EncodedCommand payload: %v", err)
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:i+2]))
	}
	return string(utf16.Decode(units))
}

// waitFor polls until cond holds, so a test never sleeps a fixed amount.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
