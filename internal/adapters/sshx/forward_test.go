package sshx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// echoServer is the "remote" service a forward points at. The fake SSH peer
// dials it for real over the loopback, so a forwarded byte crosses a TCP
// socket, an SSH channel and a second TCP socket - the same three hops as a
// live ssh -L.
type echoServer struct {
	listener net.Listener
	served   atomic.Int64
	wg       sync.WaitGroup
}

func newEchoServer(t *testing.T) *echoServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the echo server: %v", err)
	}
	e := &echoServer{listener: listener}
	e.wg.Add(1)
	go e.accept()
	t.Cleanup(func() {
		_ = e.listener.Close() // unblocks accept; nothing else to report
		e.wg.Wait()
	})
	return e
}

func (e *echoServer) accept() {
	defer e.wg.Done()
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			return
		}
		e.served.Add(1)
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			defer func() { _ = conn.Close() }()
			scanner := bufio.NewScanner(conn)
			for scanner.Scan() {
				_, _ = fmt.Fprintf(conn, "echo: %s\n", scanner.Text())
			}
		}()
	}
}

func (e *echoServer) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, _, err := net.SplitHostPort(e.listener.Addr().String())
	if err != nil {
		t.Fatalf("splitting the echo address: %v", err)
	}
	return host, listenerPort(t, e.listener)
}

// roundTrip writes one line through the forward and returns the answer. It
// reports errors instead of calling t.Fatal, because it is also used from
// goroutines, where Fatal is not allowed.
func roundTrip(addr, line string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("connecting to the forward: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", fmt.Errorf("setting a deadline: %w", err)
	}
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		return "", fmt.Errorf("writing through the forward: %w", err)
	}
	answer, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading back through the forward: %w", err)
	}
	return strings.TrimRight(answer, "\r\n"), nil
}

// mustRoundTrip is roundTrip for the sequential tests, which may Fatal.
func mustRoundTrip(t *testing.T, addr, line string) string {
	t.Helper()
	got, err := roundTrip(addr, line)
	if err != nil {
		t.Fatalf("round trip %q: %v", line, err)
	}
	return got
}

// asForward unwraps the io.Closer LocalForward returns.
func asForward(t *testing.T, closer io.Closer) *Forward {
	t.Helper()
	forward, ok := closer.(*Forward)
	if !ok {
		t.Fatalf("LocalForward returned %T, expected *Forward", closer)
	}
	return forward
}

func TestLocalForwardCarriesTrafficBothWays(t *testing.T) {
	assertNoLeaks(t)
	client, server := newTestClient(t)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := client.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	forward := asForward(t, closer)
	local := forward.Addr().String()

	if got := mustRoundTrip(t, local, "привет"); got != "echo: привет" {
		t.Errorf("first round trip: got %q", got)
	}
	// A second connection proves the listener keeps serving.
	if got := mustRoundTrip(t, local, "second"); got != "echo: second" {
		t.Errorf("second round trip: got %q", got)
	}
	if want := fmt.Sprintf("%s:%d", host, port); server.forwardTarget() != want {
		t.Errorf("the peer was asked for %q, want %q", server.forwardTarget(), want)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("closing the forward: %v", err)
	}
}

func TestLocalForwardBindsTheLoopbackOnly(t *testing.T) {
	assertNoLeaks(t)
	client, _ := newTestClient(t)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := client.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	defer func() { _ = closer.Close() }()

	addr := asForward(t, closer).Addr().String()
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("splitting %s: %v", addr, err)
	}
	if ip != "127.0.0.1" {
		t.Fatalf("the forward listens on %s; it must stay on the loopback", ip)
	}
}

func TestLocalForwardSurvivesAConnectionThePeerRefuses(t *testing.T) {
	assertNoLeaks(t)
	client, server := newTestClient(t)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := client.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	defer func() { _ = closer.Close() }()
	local := asForward(t, closer).Addr().String()

	// The peer refuses the next channel; the local connection must simply end.
	server.setRejectForwards(true)
	doomed := mustDial(t, local)
	if err := doomed.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting a deadline: %v", err)
	}
	_, _ = io.WriteString(doomed, "lost\n") // may or may not fit in the buffer
	// The read ends one way or another - EOF on a half-close, a reset on
	// Windows. Which one is not the point and is not asserted: what matters is
	// that it ends promptly and the forward stays up.
	_, _ = io.ReadAll(doomed)
	_ = doomed.Close()

	// The forward is still up: the next connection works.
	server.setRejectForwards(false)
	if got := mustRoundTrip(t, local, "still here"); got != "echo: still here" {
		t.Errorf("after a refused connection: got %q", got)
	}
}

func TestLocalForwardSurvivesTheRemoteServiceGoingAway(t *testing.T) {
	assertNoLeaks(t)
	client, _ := newTestClient(t)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := client.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	defer func() { _ = closer.Close() }()
	local := asForward(t, closer).Addr().String()

	if got := mustRoundTrip(t, local, "before"); got != "echo: before" {
		t.Fatalf("before: got %q", got)
	}
	_ = echo.listener.Close() // the remote service stops

	// Connecting still works locally; the stream just ends with nothing.
	conn := mustDial(t, local)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting a deadline: %v", err)
	}
	_, _ = io.WriteString(conn, "after\n")
	_, _ = io.ReadAll(conn) // ends with EOF or a reset; either is fine
	_ = conn.Close()
}

func TestLocalForwardStopsWhenClosed(t *testing.T) {
	assertNoLeaks(t)
	client, _ := newTestClient(t)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := client.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	forward := asForward(t, closer)
	local := forward.Addr().String()

	// An idle connection is open when Close arrives: Close must break it rather
	// than wait for a pump that will never see EOF.
	idle := mustDial(t, local)
	defer func() { _ = idle.Close() }()
	waitFor(t, "the peer to accept the forwarded channel", func() bool {
		return echo.served.Load() >= 1
	})

	if err := forward.Close(); err != nil {
		t.Fatalf("closing the forward: %v", err)
	}
	// Close is idempotent and every caller sees the same result.
	if err := forward.Close(); err != nil {
		t.Fatalf("closing twice: %v", err)
	}
	if _, err := net.DialTimeout("tcp", local, time.Second); err == nil {
		t.Fatal("the forward still accepts connections after Close")
	}
}

func TestLocalForwardStopsWhenTheContextIsCancelled(t *testing.T) {
	assertNoLeaks(t)
	client, _ := newTestClient(t)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	ctx, cancel := context.WithCancel(t.Context())
	closer, err := client.LocalForward(ctx, 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	defer func() { _ = closer.Close() }()
	local := asForward(t, closer).Addr().String()

	if got := mustRoundTrip(t, local, "alive"); got != "echo: alive" {
		t.Fatalf("before cancellation: got %q", got)
	}

	cancel()
	waitFor(t, "the forward to stop listening", func() bool {
		conn, dialErr := net.DialTimeout("tcp", local, 200*time.Millisecond)
		if dialErr != nil {
			return true
		}
		_ = conn.Close()
		return false
	})
}

func TestLocalForwardHandlesConcurrentConnections(t *testing.T) {
	assertNoLeaks(t)
	client, _ := newTestClient(t)
	echo := newEchoServer(t)
	host, port := echo.hostPort(t)

	closer, err := client.LocalForward(t.Context(), 0, host, port)
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	local := asForward(t, closer).Addr().String()

	const n = 12
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			line := fmt.Sprintf("msg-%02d", i)
			if got := mustRoundTrip(t, local, line); got != "echo: "+line {
				t.Errorf("round trip %d: got %q", i, got)
			}
		}()
	}
	wg.Wait()

	// Closing while several pumps have just finished must still be clean.
	if err := closer.Close(); err != nil {
		t.Fatalf("closing the forward: %v", err)
	}
}

func TestLocalForwardRejectsBadArguments(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	if _, err := client.LocalForward(t.Context(), 0, "", 22); err == nil {
		t.Error("expected an error for an empty remote host")
	}
	if _, err := client.LocalForward(t.Context(), 0, "127.0.0.1", 0); err == nil {
		t.Error("expected an error for remote port 0")
	}
	if _, err := client.LocalForward(t.Context(), -1, "127.0.0.1", 22); err == nil {
		t.Error("expected an error for a negative local port")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.LocalForward(ctx, 0, "127.0.0.1", 22); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestLocalForwardFailsOnAPortAlreadyTaken(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer func() { _ = taken.Close() }()

	_, err = client.LocalForward(t.Context(), listenerPort(t, taken), "127.0.0.1", 22)
	if err == nil {
		t.Fatal("expected an error for a port already in use")
	}
}
