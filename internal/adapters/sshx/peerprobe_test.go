package sshx

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// A compile-time check that *Client answers PeerCanReachUs with the signature
// core.Probe declares.
var _ interface {
	PeerCanReachUs(ctx context.Context, localAddr string, port int) error
} = (*Client)(nil)

// peerProbe wires the fake peer to answer the probe with one token line, the
// way the real script does. answer receives the decoded PowerShell so a test
// can assert on what was actually sent.
func peerProbe(t *testing.T, answer func(script string) execResult) *Client {
	t.Helper()
	client, server := newTestClient(t)
	server.setHandler(func(cmd string) execResult {
		const prefix = "powershell -NoLogo -NoProfile -NonInteractive -EncodedCommand "
		if !strings.HasPrefix(cmd, prefix) {
			return execResult{stderr: "the peer expected an encoded command", code: 1}
		}
		return answer(decodePowerShell(t, strings.TrimPrefix(cmd, prefix)))
	})
	return client
}

// peerSays answers with exactly the line the real script prints.
func peerSays(status, errno int) func(string) execResult {
	return func(string) execResult {
		return execResult{stdout: peerProbeToken + " " +
			strconv.Itoa(status) + " " + strconv.Itoa(errno) + "\r\n"}
	}
}

// TestPeerCanReachUsClassifiesTheFourOutcomes covers the whole contract: the
// peer connected, a filter on ITS side refused, nothing answered, and the
// probe failed for some other reason. The numbers are the ones a real Windows
// host produced for these situations; only the transport is faked.
func TestPeerCanReachUsClassifiesTheFourOutcomes(t *testing.T) {
	t.Parallel()

	const (
		wsaeconnrefused = 10061 // measured: a refused connect on Windows
		wsahostnotfound = 11001 // measured: an address that does not resolve
	)

	cases := map[string]struct {
		status, errno int
		want          string
	}{
		"connected":                {peerProbeConnected, 0, "ok"},
		"blocked by a filter":      {peerProbeFailed, 10013, "blocked"},
		"no answer in time":        {peerProbeTimedOut, -1, "timeout"},
		"stack gave up waiting":    {peerProbeFailed, 10060, "timeout"},
		"refused":                  {peerProbeFailed, wsaeconnrefused, "other"},
		"address does not resolve": {peerProbeFailed, wsahostnotfound, "other"},
		"failed with no number":    {peerProbeFailed, -1, "other"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := peerProbe(t, peerSays(c.status, c.errno))

			err := client.PeerCanReachUs(t.Context(), "100.124.47.73", 22)

			var blocked *core.BlockedError
			var timeout *core.TimeoutError
			switch {
			case err == nil:
				assertKind(t, "ok", c.want)
			case errors.As(err, &blocked):
				assertKind(t, "blocked", c.want)
				if blocked.Addr != "100.124.47.73" || blocked.Port != 22 {
					t.Errorf("the error should name this machine, got %+v", blocked)
				}
			case errors.As(err, &timeout):
				assertKind(t, "timeout", c.want)
				if timeout.Addr != "100.124.47.73" || timeout.Port != 22 {
					t.Errorf("the error should name this machine, got %+v", timeout)
				}
			default:
				assertKind(t, "other", c.want)
			}
		})
	}
}

// TestPeerCanReachUsKeepsTheWinsockNumberInspectable proves an unclassified
// outcome still carries its number, so a caller never has to read the text.
func TestPeerCanReachUsKeepsTheWinsockNumberInspectable(t *testing.T) {
	t.Parallel()
	client := peerProbe(t, peerSays(peerProbeFailed, 10061))

	err := client.PeerCanReachUs(t.Context(), "100.124.47.73", 22)
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		t.Fatalf("expected a syscall.Errno in the chain, got %T: %v", err, err)
	}
	if errno != syscall.Errno(10061) {
		t.Errorf("errno: got %d, want 10061", uint32(errno))
	}
}

func TestPeerCanReachUsFailsWhenTheProbeCannotRun(t *testing.T) {
	t.Parallel()
	client := peerProbe(t, func(string) execResult {
		return execResult{stderr: "Не удается найти указанный файл.", code: 1}
	})

	err := client.PeerCanReachUs(t.Context(), "100.124.47.73", 22)
	if err == nil {
		t.Fatal("expected an error when powershell exits non-zero")
	}
	var blocked *core.BlockedError
	var timeout *core.TimeoutError
	if errors.As(err, &blocked) || errors.As(err, &timeout) {
		t.Fatalf("a probe that never ran must not be reported as a verdict: %v", err)
	}
	if !strings.Contains(err.Error(), "Не удается найти указанный файл.") {
		t.Errorf("the error should carry the peer's message, got %v", err)
	}
}

func TestPeerCanReachUsFailsOnUnreadableOutput(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"nothing at all":      "",
		"noise but no token":  "Preparing modules for first use.\r\n",
		"token, wrong arity":  peerProbeToken + " 0\r\n",
		"status not a number": peerProbeToken + " ok 0\r\n",
		"errno not a number":  peerProbeToken + " 1 WSAEACCES\r\n",
		"unknown status":      peerProbeToken + " 7 0\r\n",
	}
	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := peerProbe(t, func(string) execResult {
				return execResult{stdout: stdout}
			})
			if err := client.PeerCanReachUs(t.Context(), "100.124.47.73", 22); err == nil {
				t.Fatal("expected an error for output the probe cannot read")
			}
		})
	}
}

// TestPeerCanReachUsIgnoresNoiseAroundItsToken is the lesson from a real run:
// a cold PowerShell prints a CLIXML progress blob before anything else, so the
// parser must find its line rather than trust the whole stream. The same blob
// is fed to ServiceRunning in service_test.go.
func TestPeerCanReachUsIgnoresNoiseAroundItsToken(t *testing.T) {
	t.Parallel()
	client := peerProbe(t, func(string) execResult {
		return execResult{stdout: buriedInNoise(peerProbeToken + " 0 0")}
	})

	if err := client.PeerCanReachUs(t.Context(), "100.124.47.73", 22); err != nil {
		t.Fatalf("the token line should have been found: %v", err)
	}
}

// TestPeerCanReachUsTrustsTheVerdictOverTheExitCode matches ServiceRunning: a
// verdict on the stream outranks how the shell felt about the run.
func TestPeerCanReachUsTrustsTheVerdictOverTheExitCode(t *testing.T) {
	t.Parallel()
	client := peerProbe(t, func(string) execResult {
		return execResult{
			stdout: buriedInNoise(peerProbeToken + " 1 10013"),
			stderr: "шум",
			code:   1,
		}
	})

	err := client.PeerCanReachUs(t.Context(), "100.124.47.73", 22)
	var blocked *core.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected *core.BlockedError, got %T: %v", err, err)
	}
}

// TestPeerProbeScriptShape pins the parts of the script that carry the
// contract. The script itself was run against a real Windows host during
// development; this keeps it from drifting.
func TestPeerProbeScriptShape(t *testing.T) {
	t.Parallel()
	script, err := peerProbeScript("100.127.188.87", 22, 1500*time.Millisecond)
	if err != nil {
		t.Fatalf("building the script: %v", err)
	}

	for _, want := range []string{
		"System.Net.Sockets.TcpClient",
		"$client.ConnectAsync('100.127.188.87', 22)",
		"[System.Threading.Tasks.Task]::WaitAny($tasks, 1500)",
		"$e.NativeErrorCode",
		"$task.IsFaulted",
		"$client.Close()",
		peerProbeToken,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script is missing %q:\n%s", want, script)
		}
	}
	// Test-NetConnection prints a localised object with no error number in it.
	if strings.Contains(script, "Test-NetConnection") {
		t.Error("the script must not depend on Test-NetConnection")
	}
	// Task.Wait throws on a faulted task, and PowerShell's handling of that
	// inside an "if" condition is not something to depend on.
	if strings.Contains(script, "$task.Wait(") {
		t.Error("the script must use WaitAny, which does not throw")
	}

	// A sub-millisecond timeout is clamped rather than sent as 0, which WaitAny
	// would read as "do not wait at all".
	tiny, err := peerProbeScript("100.127.188.87", 22, time.Microsecond)
	if err != nil {
		t.Fatalf("building the script: %v", err)
	}
	if !strings.Contains(tiny, "WaitAny($tasks, 1)") {
		t.Errorf("a sub-millisecond timeout should clamp to 1ms, got:\n%s", tiny)
	}
}

func TestPeerCanReachUsUsesTheConfiguredTimeout(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 1)
	client := peerProbe(t, func(script string) execResult {
		seen <- script
		return peerSays(peerProbeConnected, 0)("")
	})
	// Set before the call and not read concurrently with it, so the peer sees
	// this deadline in the script rather than the default.
	client.cfg.PeerProbeTimeout = 2500 * time.Millisecond

	if err := client.PeerCanReachUs(t.Context(), "100.124.47.73", 22); err != nil {
		t.Fatalf("PeerCanReachUs: %v", err)
	}
	if script := <-seen; !strings.Contains(script, "WaitAny($tasks, 2500)") {
		t.Errorf("the script should carry the configured timeout, got:\n%s", script)
	}
}

func TestPeerCanReachUsRejectsBadArguments(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	for _, port := range []int{0, -1, 70000} {
		if err := client.PeerCanReachUs(t.Context(), "100.124.47.73", port); err == nil {
			t.Errorf("expected an error for port %d", port)
		}
	}
	// A control character would break out of the PowerShell literal.
	if err := client.PeerCanReachUs(t.Context(), "100.124.47.73\nWrite-Output x", 22); err == nil {
		t.Error("expected an injected newline to be refused")
	}
	if err := client.PeerCanReachUs(t.Context(), "", 22); err == nil {
		t.Error("expected an empty address to be refused")
	}
}

func TestPeerCanReachUsHonoursACancelledContext(t *testing.T) {
	t.Parallel()
	client := peerProbe(t, peerSays(peerProbeConnected, 0))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.PeerCanReachUs(ctx, "100.124.47.73", 22); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPeerCanReachUsDefaultTimeout(t *testing.T) {
	t.Parallel()
	got := Config{Addr: "100.127.188.87"}.withDefaults()
	if got.PeerProbeTimeout != DefaultPeerProbeTimeout {
		t.Errorf("PeerProbeTimeout: got %v, want %v", got.PeerProbeTimeout, DefaultPeerProbeTimeout)
	}
}
