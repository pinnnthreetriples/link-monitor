package tailscale

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

func TestPeerReachable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		addr      string
		pingFn    func(n int, ip netip.Addr, pt tailcfg.PingType) (*ipnstate.PingResult, error)
		want      time.Duration
		wantErr   error
		wantPings int
	}{
		{
			name: "the work PC answers",
			addr: workAddr,
			pingFn: func(_ int, ip netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				return &ipnstate.PingResult{IP: ip.String(), LatencySeconds: 0.0125}, nil
			},
			want:      12500 * time.Microsecond,
			wantPings: 1,
		},
		{
			name: "a name is resolved through the tailnet",
			addr: workName,
			pingFn: func(_ int, ip netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				if ip.String() != workAddr {
					return nil, errors.New("pinged the wrong node: " + ip.String())
				}
				return &ipnstate.PingResult{LatencySeconds: 0.02}, nil
			},
			want:      20 * time.Millisecond,
			wantPings: 1,
		},
		{
			name: "a hostname works too",
			addr: workHost,
			pingFn: func(_ int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				return &ipnstate.PingResult{LatencySeconds: 0.03}, nil
			},
			want:      30 * time.Millisecond,
			wantPings: 1,
		},
		{
			name: "the first ping loses the race, the second wins",
			addr: workAddr,
			pingFn: func(n int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				if n == 1 {
					return &ipnstate.PingResult{Err: "no matching peer"}, nil
				}
				return &ipnstate.PingResult{LatencySeconds: 0.05}, nil
			},
			want:      50 * time.Millisecond,
			wantPings: 2,
		},
		{
			name: "peer offline: every attempt is spent",
			addr: workAddr,
			pingFn: func(_ int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				return &ipnstate.PingResult{Err: "peer is not connected"}, nil
			},
			wantErr:   ErrPeerUnreachable,
			wantPings: 3,
		},
		{
			name: "a result with no latency counts as unreachable",
			addr: workAddr,
			pingFn: func(_ int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				return &ipnstate.PingResult{LatencySeconds: 0}, nil
			},
			wantErr:   ErrPeerUnreachable,
			wantPings: 3,
		},
		{
			name: "an empty result counts as unreachable",
			addr: workAddr,
			pingFn: func(_ int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				return nil, nil
			},
			wantErr:   ErrPeerUnreachable,
			wantPings: 3,
		},
		{
			name: "pinging ourselves succeeds instantly",
			addr: laptopAddr,
			pingFn: func(_ int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				return &ipnstate.PingResult{IsLocalIP: true}, nil
			},
			want:      0,
			wantPings: 1,
		},
		{
			name: "a broken daemon is not retried",
			addr: workAddr,
			pingFn: func(_ int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
				return nil, errBoom
			},
			wantErr:   errBoom,
			wantPings: 1,
		},
		{
			name:    "an unknown name",
			addr:    "not-a-machine",
			wantErr: ErrPeerNotFound,
		},
		{
			name:    "an empty address",
			addr:    "  ",
			wantErr: ErrPeerNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := &fakeDaemon{status: runningStatus(t), pingFn: tc.pingFn}
			c := newTestClient(d, &fakeRunner{})

			got, err := c.PeerReachable(context.Background(), tc.addr)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want it to wrap %v", err, tc.wantErr)
				}
				if got != 0 {
					t.Errorf("latency = %v, want 0 on failure", got)
				}
			} else {
				if err != nil {
					t.Fatalf("PeerReachable() error = %v", err)
				}
				if got != tc.want {
					t.Errorf("latency = %v, want %v", got, tc.want)
				}
			}
			if tc.wantPings != 0 && d.pingN != tc.wantPings {
				t.Errorf("sent %d pings, want %d", d.pingN, tc.wantPings)
			}
		})
	}
}

func TestPeerReachableUsesTheTunnelNotASocket(t *testing.T) {
	t.Parallel()
	var seen tailcfg.PingType
	d := &fakeDaemon{
		status: runningStatus(t),
		pingFn: func(_ int, _ netip.Addr, pt tailcfg.PingType) (*ipnstate.PingResult, error) {
			seen = pt
			return &ipnstate.PingResult{LatencySeconds: 0.01}, nil
		},
	}

	if _, err := newTestClient(d, &fakeRunner{}).PeerReachable(context.Background(), workAddr); err != nil {
		t.Fatalf("PeerReachable() error = %v", err)
	}
	if seen != tailcfg.PingDisco {
		t.Errorf("ping type = %q, want %q — a disco ping is what survives a blocked socket",
			seen, tailcfg.PingDisco)
	}
}

func TestPeerReachableStopsOnCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &fakeDaemon{status: runningStatus(t)}
	_, err := newTestClient(d, &fakeRunner{}).PeerReachable(ctx, workAddr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if d.pingN != 0 {
		t.Errorf("sent %d pings on a dead context, want 0", d.pingN)
	}
}

func TestPeerReachableStopsRetryingWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := &fakeDaemon{
		status: runningStatus(t),
		pingFn: func(n int, _ netip.Addr, _ tailcfg.PingType) (*ipnstate.PingResult, error) {
			if n == 1 {
				cancel()
			}
			return &ipnstate.PingResult{Err: "peer is not connected"}, nil
		},
	}
	c := New(WithDaemon(d), WithRunner(&fakeRunner{}), WithPingPolicy(5, time.Minute))

	start := time.Now()
	_, err := c.PeerReachable(ctx, workAddr)
	if err == nil {
		t.Fatal("PeerReachable() error = nil, want one")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v: the backoff ignored the cancelled context", elapsed)
	}
	if d.pingN != 1 {
		t.Errorf("sent %d pings, want 1 before giving up", d.pingN)
	}
}

func TestPeerReachableWhenTheStatusLookupFails(t *testing.T) {
	t.Parallel()
	c := newTestClient(&fakeDaemon{statusErr: errBoom}, &fakeRunner{})

	if _, err := c.PeerReachable(context.Background(), workName); !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want it to wrap %v", err, errBoom)
	}
}

func TestPeerReachableWithAPeerThatHasNoAddress(t *testing.T) {
	t.Parallel()
	st := runningStatus(t)
	for _, ps := range st.Peer {
		ps.TailscaleIPs = nil
	}

	c := newTestClient(&fakeDaemon{status: st}, &fakeRunner{})
	_, err := c.PeerReachable(context.Background(), workName)
	if err == nil {
		t.Fatal("PeerReachable() error = nil, want one")
	}
}

func TestSleepCtxReturnsAfterTheDelay(t *testing.T) {
	t.Parallel()
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepCtx() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx(0) error = %v, want context.Canceled", err)
	}
}
