package sshx

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// serviceProbe wires the fake peer to answer a Get-Service query the way
// PowerShell would, by decoding the -EncodedCommand payload the client sent.
func serviceProbe(t *testing.T, answer func(script string) execResult) (*Client, *sshServer) {
	t.Helper()
	client, server := newTestClient(t)
	server.setHandler(func(cmd string) execResult {
		const prefix = "powershell -NoLogo -NoProfile -NonInteractive -EncodedCommand "
		if !strings.HasPrefix(cmd, prefix) {
			return execResult{stderr: "the peer expected an encoded command", code: 1}
		}
		return answer(decodePowerShell(t, strings.TrimPrefix(cmd, prefix)))
	})
	return client, server
}

// serviceSays renders the token line exactly as the real script prints it.
func serviceSays(status int) string {
	return serviceToken + " " + strconv.Itoa(status)
}

func TestServiceRunningReadsTheNumericStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		stdout string
		want   bool
	}{
		"running":            {serviceSays(serviceStatusRunning) + "\n", true},
		"stopped":            {serviceSays(1) + "\r\n", false},
		"start pending":      {serviceSays(2), false},
		"not installed":      {serviceSays(serviceStatusMissing) + "\r\n", false},
		"padded with spaces": {"  " + serviceSays(4) + "  ", true},
		// The lesson from the probe, applied here: a cold PowerShell buries the
		// verdict in a CLIXML blob, and the parser has to find its line.
		"buried in cold-start noise": {buriedInNoise(serviceSays(4)), true},
		"stopped, buried":            {buriedInNoise(serviceSays(1)), false},
		"not installed, buried":      {buriedInNoise(serviceSays(serviceStatusMissing)), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client, _ := serviceProbe(t, func(string) execResult {
				return execResult{stdout: c.stdout}
			})
			got, err := client.ServiceRunning(t.Context(), "sshd")
			if err != nil {
				t.Fatalf("ServiceRunning: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestServiceRunningTrustsTheVerdictOverTheExitCode pins the precedence: a
// status on the stream is an answer however the shell felt about the run.
func TestServiceRunningTrustsTheVerdictOverTheExitCode(t *testing.T) {
	t.Parallel()
	client, _ := serviceProbe(t, func(string) execResult {
		return execResult{stdout: buriedInNoise(serviceSays(4)), stderr: "шум", code: 1}
	})

	got, err := client.ServiceRunning(t.Context(), "sshd")
	if err != nil {
		t.Fatalf("a verdict on the stream should outrank a non-zero exit: %v", err)
	}
	if !got {
		t.Error("expected the service to be reported as running")
	}
}

// TestServiceRunningSendsATokenLine checks the script asks for the answer in
// the shape the parser looks for, so the two cannot drift apart.
func TestServiceRunningSendsATokenLine(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 1)
	client, _ := serviceProbe(t, func(script string) execResult {
		seen <- script
		return execResult{stdout: serviceSays(4)}
	})
	if _, err := client.ServiceRunning(t.Context(), "sshd"); err != nil {
		t.Fatalf("ServiceRunning: %v", err)
	}

	script := <-seen
	if !strings.Contains(script, "Write-Output ('"+serviceToken+" ' + [int]$s.Status)") {
		t.Errorf("the script should print a token line, got:\n%s", script)
	}
	if !strings.Contains(script, serviceSays(serviceStatusMissing)) {
		t.Errorf("the script should report a missing service on the same line, got:\n%s", script)
	}
}

func TestServiceRunningSendsAScriptThatCannotBeLocalised(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 1)
	client, _ := serviceProbe(t, func(script string) execResult {
		seen <- script
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	if _, err := client.ServiceRunning(t.Context(), "sshd"); err != nil {
		t.Fatalf("ServiceRunning: %v", err)
	}
	script := <-seen

	// The status must be read as a number: the printed name of the enum is the
	// only locale-proof alternative, and the number is safer still.
	if !strings.Contains(script, "[int]$s.Status") {
		t.Errorf("the script should print the numeric status, got:\n%s", script)
	}
	if !strings.Contains(script, "Get-Service -Name 'sshd'") {
		t.Errorf("the script should query sshd by name, got:\n%s", script)
	}
}

func TestServiceRunningFailsWhenPowerShellFails(t *testing.T) {
	t.Parallel()
	client, _ := serviceProbe(t, func(string) execResult {
		return execResult{stderr: "Отказано в доступе.", code: 1}
	})

	got, err := client.ServiceRunning(t.Context(), "sshd")
	if err == nil {
		t.Fatal("expected an error when powershell exits non-zero")
	}
	if got {
		t.Error("a failed query must not report the service as running")
	}
	// The peer's own message is worth keeping, localised or not.
	if !strings.Contains(err.Error(), "Отказано в доступе.") {
		t.Errorf("the error should carry the peer's message, got %v", err)
	}
}

func TestServiceRunningFailsOnUnreadableOutput(t *testing.T) {
	t.Parallel()
	client, _ := serviceProbe(t, func(string) execResult {
		return execResult{stdout: "Выполняется\n"}
	})

	if _, err := client.ServiceRunning(t.Context(), "sshd"); err == nil {
		t.Fatal("expected an error for output that is not a number")
	}
}

func TestServiceRunningRejectsABadName(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)
	for _, name := range []string{"", "ssh\nd", "ssh\x00d"} {
		if _, err := client.ServiceRunning(t.Context(), name); err == nil {
			t.Errorf("expected %q to be refused", name)
		}
	}
}

func TestServiceRunningHonoursACancelledContext(t *testing.T) {
	t.Parallel()
	client, _ := serviceProbe(t, func(string) execResult {
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := client.ServiceRunning(ctx, "sshd")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got {
		t.Error("a cancelled query must not report the service as running")
	}
}

func TestQuotePowerShellLiteralDoublesQuotes(t *testing.T) {
	t.Parallel()
	got, err := quotePowerShellLiteral("it's odd")
	if err != nil {
		t.Fatalf("quoting: %v", err)
	}
	if got != "'it''s odd'" {
		t.Errorf("got %q", got)
	}
}
