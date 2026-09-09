package sshx

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// A compile-time check that *Lazy answers all three interfaces the app
// declares, with the signatures it declares them with. app cannot be imported
// here - adapters never climb the stack - so the shapes are restated.
var _ interface {
	// app.RemoteProbe
	TCPReachable(ctx context.Context, addr string, port int) error
	ServiceRunning(ctx context.Context, name string) (bool, error)
	PeerCanReachUs(ctx context.Context, localAddr string, port int) error
	// app.RemoteShell
	RunPowerShell(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error)
	// app.Forwarder
	LocalForward(ctx context.Context, localPort int, remoteHost string, remotePort int) (io.Closer, error)
	io.Closer
} = (*Lazy)(nil)

// sessionAlive reports whether Lazy is holding a live session right now. It
// exists for the tests: the library signals a dead transport on its own
// goroutine, so a test that hangs up on the peer needs somewhere to wait for
// that to be noticed rather than guessing with a sleep. It is defined here so
// no test-only helper lives in the production file.
func (l *Lazy) sessionAlive() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sess != nil && alive(l.sess)
}

// newLazyAgainst builds a Lazy pointed at a fresh in-process peer, with a short
// backoff so a test does not spend seconds waiting one out.
func newLazyAgainst(t *testing.T, backoff time.Duration) (*Lazy, *sshServer) {
	t.Helper()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")
	server := newSSHServer(t, clientPub)
	knownHosts := writeKnownHosts(t, dir, server.addr(), server.hostKey.PublicKey())

	lazy := NewLazy(Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KnownHostsPath: knownHosts, Timeout: 5 * time.Second, DialBackoff: backoff,
	})
	t.Cleanup(func() { _ = lazy.Close() })
	return lazy, server
}

// newUnreachableLazy points a Lazy at a peer that accepts TCP but always fails
// the handshake, because known_hosts names somebody else's host key. Every
// attempt is therefore a counted dial that fails.
func newUnreachableLazy(t *testing.T, backoff time.Duration) (*Lazy, *sshServer) {
	t.Helper()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")
	server := newSSHServer(t, clientPub)
	wrongKey := writeKnownHosts(t, dir, server.addr(), newSigner(t).PublicKey())

	lazy := NewLazy(Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KnownHostsPath: wrongKey, Timeout: 5 * time.Second, DialBackoff: backoff,
	})
	t.Cleanup(func() { _ = lazy.Close() })
	return lazy, server
}

func TestNewLazyTouchesNothing(t *testing.T) {
	assertNoLeaks(t)
	// A port nothing listens on. Constructing must still succeed: the whole
	// point is that the app can be built while the peer is unreachable.
	lazy := NewLazy(Config{Addr: "100.127.188.87", Port: 22, User: "user", KeyPath: "nowhere"})
	if lazy == nil {
		t.Fatal("NewLazy returned nil")
	}
	if err := lazy.Close(); err != nil {
		t.Fatalf("closing a client that never connected: %v", err)
	}
}

func TestLazyDialsOnFirstUseAndReusesAfterwards(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, 50*time.Millisecond)
	server.setHandler(func(string) execResult {
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	if got := server.dials(); got != 0 {
		t.Fatalf("NewLazy should not have connected, but the peer saw %d dials", got)
	}

	for i := range 3 {
		running, err := lazy.ServiceRunning(t.Context(), "sshd")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !running {
			t.Errorf("call %d: expected the service to be running", i)
		}
	}
	if got := server.dials(); got != 1 {
		t.Errorf("three calls should share one connection, the peer saw %d dials", got)
	}
}

func TestLazyReconnectsAfterTheSessionDies(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)
	server.setHandler(func(string) execResult {
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// The peer hangs up, as it does when it reboots or loses its address.
	server.dropConnections()
	waitFor(t, "the client to notice the session is gone", func() bool {
		return !lazy.sessionAlive()
	})

	running, err := lazy.ServiceRunning(t.Context(), "sshd")
	if err != nil {
		t.Fatalf("the call after a drop should have reconnected: %v", err)
	}
	if !running {
		t.Error("expected the service to be running after the reconnect")
	}
	if got := server.dials(); got != 2 {
		t.Errorf("expected exactly one redial, the peer saw %d dials in total", got)
	}
}

// TestLazyDoesNotRedialOnARemoteFailure is the other half of the contract: a
// command that failed on a working connection failed on its own account.
func TestLazyDoesNotRedialOnARemoteFailure(t *testing.T) {
	assertNoLeaks(t)

	t.Run("powershell exits non-zero", func(t *testing.T) {
		lazy, server := newLazyAgainst(t, time.Millisecond)
		server.setHandler(func(string) execResult {
			return execResult{stderr: "Отказано в доступе.", code: 1}
		})

		if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err == nil {
			t.Fatal("expected the failure to be reported")
		}
		if got := server.dials(); got != 1 {
			t.Errorf("a remote failure must not redial, the peer saw %d dials", got)
		}
	})

	t.Run("the peer sends no exit status", func(t *testing.T) {
		lazy, server := newLazyAgainst(t, time.Millisecond)
		server.setHandler(func(string) execResult {
			return execResult{stdout: "partial", noExitStatus: true}
		})

		if _, _, _, err := lazy.RunPowerShell(t.Context(), "Get-Date"); err == nil {
			t.Fatal("expected an error when the peer sends no status")
		}
		if got := server.dials(); got != 1 {
			t.Errorf("a remote failure must not redial, the peer saw %d dials", got)
		}
	})

	t.Run("a non-zero exit is not an error at all", func(t *testing.T) {
		lazy, server := newLazyAgainst(t, time.Millisecond)
		server.setHandler(func(string) execResult {
			return execResult{stdout: "out", stderr: "err", code: 3}
		})

		stdout, stderr, code, err := lazy.RunPowerShell(t.Context(), "Get-Date")
		if err != nil {
			t.Fatalf("a non-zero exit is an answer, not a failure: %v", err)
		}
		if stdout != "out" || stderr != "err" || code != 3 {
			t.Errorf("got (%q, %q, %d)", stdout, stderr, code)
		}
		if got := server.dials(); got != 1 {
			t.Errorf("the peer saw %d dials", got)
		}
	})
}

// killTransport hangs up on the peer and waits until the client has been told,
// so a test that needs the retry decision to be deterministic can have one.
func killTransport(t *testing.T, lazy *Lazy, server *sshServer) {
	t.Helper()
	server.dropConnections()
	waitFor(t, "the client to notice the session is gone", func() bool {
		return !lazy.sessionAlive()
	})
}

// TestWithSessionRetriesOnceWhenTheTransportDiesMidCall covers the other half
// of reconnecting: not the call that finds a dead session waiting for it, but
// the one whose connection dies underneath it. The work is driven directly so
// the transport can be killed at a known moment - the library reports a death
// on its own goroutine, and a test that raced it would prove nothing.
func TestWithSessionRetriesOnceWhenTheTransportDiesMidCall(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)

	calls := 0
	_, err := withSession(t.Context(), lazy, func(*Client) (struct{}, error) {
		calls++
		if calls == 1 {
			killTransport(t, lazy, server)
			return struct{}{}, errors.New("the transport went away mid-call")
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("the retry should have succeeded: %v", err)
	}
	if calls != 2 {
		t.Errorf("the work ran %d times, want exactly 2", calls)
	}
	if got := server.dials(); got != 2 {
		t.Errorf("expected exactly one redial, the peer saw %d dials", got)
	}
}

func TestWithSessionRetriesNoMoreThanOnce(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)

	calls := 0
	// Every attempt loses its transport, so a client that kept retrying would
	// never stop. It must give up after the second.
	_, err := withSession(t.Context(), lazy, func(*Client) (struct{}, error) {
		calls++
		killTransport(t, lazy, server)
		return struct{}{}, errors.New("gone again")
	})
	if err == nil {
		t.Fatal("expected the failure to be reported")
	}
	if calls != 2 {
		t.Errorf("the work ran %d times, want exactly 2", calls)
	}
	if got := server.dials(); got != 2 {
		t.Errorf("the peer saw %d dials, want 2", got)
	}
}

// TestWithSessionReportsAFailedReconnect covers the case where the session died
// and the peer has gone away entirely, so there is nothing to retry against.
func TestWithSessionReportsAFailedReconnect(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)

	calls := 0
	_, err := withSession(t.Context(), lazy, func(*Client) (struct{}, error) {
		calls++
		killTransport(t, lazy, server)
		server.closeListener() // the peer is not coming back
		return struct{}{}, errors.New("gone")
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("the work ran %d times; with nothing to reconnect to it should run once", calls)
	}
	if !strings.Contains(err.Error(), "reconnecting to") {
		t.Errorf("the error should say the reconnect failed, got %v", err)
	}
}

func TestLazyPeerCanReachUsSucceedsThroughTheSession(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)
	server.setHandler(func(string) execResult {
		return execResult{stdout: buriedInNoise(peerProbeToken + " 0 0")}
	})

	if err := lazy.PeerCanReachUs(t.Context(), "100.124.47.73", 22); err != nil {
		t.Fatalf("PeerCanReachUs: %v", err)
	}
	if got := server.dials(); got != 1 {
		t.Errorf("the peer saw %d dials, want 1", got)
	}
}

func TestLazyBacksOffAfterAFailedDial(t *testing.T) {
	assertNoLeaks(t)
	const backoff = 150 * time.Millisecond
	lazy, server := newUnreachableLazy(t, backoff)

	first := lazy.PeerCanReachUs(t.Context(), "100.124.47.73", 22)
	if first == nil {
		t.Fatal("expected the dial to fail")
	}
	if got := server.dials(); got != 1 {
		t.Fatalf("expected one attempt, the peer saw %d", got)
	}

	// Inside the backoff every caller gets the same answer and nobody dials.
	for i := range 5 {
		if err := lazy.PeerCanReachUs(t.Context(), "100.124.47.73", 22); err == nil {
			t.Fatalf("call %d should still fail", i)
		}
	}
	if got := server.dials(); got != 1 {
		t.Errorf("the backoff should have suppressed the retries, the peer saw %d dials", got)
	}

	// Once it elapses, one more attempt is allowed.
	time.Sleep(backoff + 50*time.Millisecond)
	if err := lazy.PeerCanReachUs(t.Context(), "100.124.47.73", 22); err == nil {
		t.Fatal("the host key is still wrong, so this must fail too")
	}
	if got := server.dials(); got != 2 {
		t.Errorf("expected a second attempt after the backoff, the peer saw %d dials", got)
	}
}

func TestLazyOpensOneConnectionForConcurrentCallers(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, 50*time.Millisecond)
	server.setHandler(func(string) execResult {
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	const callers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := range callers {
		go func() {
			defer wg.Done()
			<-start // all of them arrive at once, which is the point
			if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := server.dials(); got != 1 {
		t.Errorf("%d concurrent callers opened %d connections, want 1", callers, got)
	}
}

func TestLazyTCPReachableNeedsNoSession(t *testing.T) {
	assertNoLeaks(t)
	// Deliberately unreachable over SSH: this answer must survive that.
	lazy, server := newUnreachableLazy(t, time.Hour)

	// The peer's listener is an ordinary TCP listener, which is all this asks.
	if err := lazy.TCPReachable(t.Context(), "127.0.0.1", server.port()); err != nil {
		t.Fatalf("TCPReachable should work without a session: %v", err)
	}
	if got := server.dials(); got != 1 {
		// One dial, and it is the probe's own TCP connection, not an SSH one.
		t.Logf("the peer counted %d connections, which is the probe's own", got)
	}

	// And it still works once SSH is known to be broken.
	if err := lazy.PeerCanReachUs(t.Context(), "100.124.47.73", 22); err == nil {
		t.Fatal("expected the ssh side to be broken")
	}
	if err := lazy.TCPReachable(t.Context(), "127.0.0.1", server.port()); err != nil {
		t.Fatalf("TCPReachable should still work with ssh down: %v", err)
	}
}

func TestLazyCloseIsIdempotent(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)
	server.setHandler(func(string) execResult {
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	for i := range 3 {
		if err := lazy.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	// A closed client does not quietly reconnect.
	if _, err := lazy.ServiceRunning(t.Context(), "sshd"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
	if got := server.dials(); got != 1 {
		t.Errorf("a closed client must not dial again, the peer saw %d dials", got)
	}
}

func TestLazyForwardsThroughTheSession(t *testing.T) {
	assertNoLeaks(t)
	lazy, _ := newLazyAgainst(t, time.Millisecond)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := lazy.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	defer func() { _ = closer.Close() }()

	// app.listenAddr type-asserts this to learn the port when 0 was asked for,
	// so Lazy must not wrap the closer in something that hides Addr.
	addressable, ok := closer.(interface{ Addr() net.Addr })
	if !ok {
		t.Fatalf("the closer no longer reports its address; app.listenAddr needs it")
	}
	local := addressable.Addr().String()

	if got := mustRoundTrip(t, local, "привет"); got != "echo: привет" {
		t.Errorf("through the forward: got %q", got)
	}
}

// TestLazyForwardDoesNotSurviveTheConnectionDropping documents the limit
// honestly: the tunnel dies with its transport and does not heal. What must
// not happen is a reconnect closing a forward that still works - see the test
// below.
func TestLazyForwardDoesNotSurviveTheConnectionDropping(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := lazy.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	defer func() { _ = closer.Close() }()
	local := closer.(*Forward).Addr().String()

	if got := mustRoundTrip(t, local, "before"); got != "echo: before" {
		t.Fatalf("before the drop: got %q", got)
	}

	server.dropConnections()
	waitFor(t, "the client to notice the session is gone", func() bool {
		return !lazy.sessionAlive()
	})

	// The local port still accepts, but nothing comes back: the tunnel is gone.
	if _, err := roundTrip(local, "after"); err == nil {
		t.Error("the forward should be dead once its transport is")
	}
	// Closing it is still clean, and leaves nothing running.
	if err := closer.Close(); err != nil {
		t.Errorf("closing a dead forward: %v", err)
	}
}

// TestLazyReconnectDoesNotKillALiveForward is the guarantee that matters: Lazy
// only ever drops a session whose transport has already died, so reconnecting
// cannot take a working tunnel with it.
func TestLazyReconnectDoesNotKillALiveForward(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)
	server.setHandler(func(string) execResult {
		return execResult{stderr: "Отказано в доступе.", code: 1}
	})

	closer, err := lazy.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	defer func() { _ = closer.Close() }()
	local := closer.(*Forward).Addr().String()

	// A failing call on a working session must not disturb anything.
	for range 3 {
		if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err == nil {
			t.Fatal("expected the call to fail")
		}
	}
	if got := server.dials(); got != 1 {
		t.Fatalf("nothing should have reconnected, the peer saw %d dials", got)
	}
	if got := mustRoundTrip(t, local, "still here"); got != "echo: still here" {
		t.Errorf("the forward should be untouched: got %q", got)
	}
}

func TestLazyReportsTheDialFailure(t *testing.T) {
	assertNoLeaks(t)
	lazy, _ := newUnreachableLazy(t, time.Hour)

	_, err := lazy.ServiceRunning(t.Context(), "sshd")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "ssh handshake") {
		t.Errorf("the error should explain the dial failed, got %v", err)
	}
}

func TestLazyHonoursACancelledContext(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := lazy.ServiceRunning(ctx, "sshd"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := server.dials(); got != 0 {
		t.Errorf("a cancelled call should not have dialled, the peer saw %d", got)
	}
}

// TestLazyDoesNotRedialForACallerThatGaveUp covers the other half of the same
// rule: the connection is up and it is the *caller's* context that ended. That
// is not the connection's fault, so the session is kept and nothing is
// redialled — a retry would only make the wait longer for somebody who has
// already stopped waiting.
//
// It is deterministic because a cached session is handed out without consulting
// the context: the call gets as far as the peer's session and fails there.
func TestLazyDoesNotRedialForACallerThatGaveUp(t *testing.T) {
	assertNoLeaks(t)
	lazy, server := newLazyAgainst(t, time.Millisecond)
	server.setHandler(func(string) execResult {
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err != nil {
		t.Fatalf("establishing the session: %v", err)
	}
	dialsBefore := server.dials()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := lazy.ServiceRunning(ctx, "sshd"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := server.dials(); got != dialsBefore {
		t.Errorf("the peer saw %d dials, want %d: a cancelled caller must not cause a redial",
			got, dialsBefore)
	}
	if !lazy.sessionAlive() {
		t.Error("the session should survive: nothing was wrong with it")
	}
}

func TestLazyDefaultDialBackoff(t *testing.T) {
	t.Parallel()
	lazy := NewLazy(Config{Addr: "100.127.188.87", User: "user", KeyPath: "k"})
	if lazy.cfg.DialBackoff != DefaultDialBackoff {
		t.Errorf("DialBackoff: got %v, want %v", lazy.cfg.DialBackoff, DefaultDialBackoff)
	}
}
