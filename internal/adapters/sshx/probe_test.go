package sshx

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// A compile-time check that *Client answers the two questions of core.Probe
// that belong to SSH, with the signatures core declares. The other two -
// TailscaleUp and PeerReachable - are the Tailscale adapter's, so nothing here
// claims to implement core.Probe whole; app composes the two. The assertion
// lives in the test file because the interface belongs to its consumer, not to
// this adapter, but it still breaks CI the moment the signatures drift.
var _ interface {
	TCPReachable(ctx context.Context, addr string, port int) error
	ServiceRunning(ctx context.Context, name string) (bool, error)
} = (*Client)(nil)

func TestTCPReachableSucceedsAgainstAListener(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	port := listenerPort(t, listener)
	if err := TCPReachable(t.Context(), "127.0.0.1", port); err != nil {
		t.Fatalf("TCPReachable: %v", err)
	}
}

func TestClientTCPReachableDelegates(t *testing.T) {
	t.Parallel()
	client, server := newTestClient(t)
	// The SSH server is itself a plain TCP listener, which is all this probe
	// needs: it deliberately does not use the SSH connection.
	if err := client.TCPReachable(t.Context(), "127.0.0.1", server.port()); err != nil {
		t.Fatalf("TCPReachable: %v", err)
	}
}

func TestTCPReachableRejectsBadArguments(t *testing.T) {
	t.Parallel()
	if err := TCPReachable(t.Context(), "", 22); err == nil {
		t.Error("expected an error for an empty address")
	}
	for _, port := range []int{0, -1, 70000} {
		if err := TCPReachable(t.Context(), "100.124.47.73", port); err == nil {
			t.Errorf("expected an error for port %d", port)
		}
	}
}

// TestTCPReachableReportsATimeout drives the real dialler, but makes the
// deadline expire rather than wait for a black hole: a test must not depend on
// how this machine's network treats an unroutable address, nor send a packet
// anywhere. The dialler reports its own expiry as a timeout, which is exactly
// what the classifier has to recognise.
func TestTCPReachableReportsATimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	err := TCPReachable(ctx, "100.127.188.87", 22)
	var timeout *core.TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("expected *core.TimeoutError, got %T: %v", err, err)
	}
	if timeout.Addr != "100.127.188.87" || timeout.Port != 22 {
		t.Errorf("the error should name the destination, got %+v", timeout)
	}
}

// TestClassifyDialErrorMapsTheOutcomes is where the classification is really
// pinned down. Producing a genuine WSAEACCES needs a packet filter, so the
// error chains are built the way net builds them and matched by errno, never by
// message text - these machines run a Russian Windows and every system message
// is translated.
func TestClassifyDialErrorMapsTheOutcomes(t *testing.T) {
	t.Parallel()

	opErr := func(inner error) error {
		return &net.OpError{
			Op:   "dial",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.ParseIP("100.127.188.87"), Port: 22},
			Err:  inner,
		}
	}

	cases := map[string]struct {
		err    error
		expect string
	}{
		"wsaeacces through os.SyscallError": {
			opErr(os.NewSyscallError("connectex", wsaEACCES)), "blocked",
		},
		"bare errno": {
			opErr(wsaEACCES), "blocked",
		},
		// A reset is not a block and not a timeout: the far end is up and
		// nothing is listening. It used to fall through to "other", which is
		// how the commonest SSH failure of all came out as «результат
		// неизвестен» five rows down.
		"connection refused is a closed port": {
			opErr(os.NewSyscallError("connectex", syscall.Errno(10061))), "closed",
		},
		"i/o timeout": {
			opErr(os.ErrDeadlineExceeded), "timeout",
		},
		"context deadline": {
			context.DeadlineExceeded, "timeout",
		},
		"context cancelled is not a timeout": {
			context.Canceled, "other",
		},
		"unclassifiable": {
			errors.New("no such host"), "other",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := classifyDialError(c.err, "100.127.188.87", 22)
			var blocked *core.BlockedError
			var timeout *core.TimeoutError
			var closed *core.PortClosedError
			switch {
			case errors.As(got, &blocked):
				assertKind(t, "blocked", c.expect)
			case errors.As(got, &closed):
				assertKind(t, "closed", c.expect)
			case errors.As(got, &timeout):
				assertKind(t, "timeout", c.expect)
			default:
				assertKind(t, "other", c.expect)
				if !errors.Is(got, c.err) {
					t.Errorf("an unclassified error must wrap the cause, got %v", got)
				}
			}
		})
	}
}

func TestClassifyDialErrorPassesNilThrough(t *testing.T) {
	t.Parallel()
	if err := classifyDialError(nil, "100.124.47.73", 22); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestBlockedErrorCarriesTheDestination(t *testing.T) {
	t.Parallel()
	err := classifyDialError(
		&net.OpError{Op: "dial", Err: os.NewSyscallError("connectex", wsaEACCES)},
		"100.127.188.87", 22,
	)
	var blocked *core.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected *core.BlockedError, got %T", err)
	}
	if blocked.Addr != "100.127.188.87" || blocked.Port != 22 {
		t.Errorf("got %+v", blocked)
	}
}

func assertKind(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("classified as %s, want %s", got, want)
	}
}

func listenerPort(t *testing.T, l net.Listener) int {
	t.Helper()
	_, p, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("splitting %s: %v", l.Addr(), err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("parsing the port: %v", err)
	}
	return n
}
