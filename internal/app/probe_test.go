package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// The routing tests are split by adapter rather than run as one long function:
// each says which of the three the composite is supposed to have reached, so a
// failure names the wire that is crossed.

func TestProbeRoutesTheTailnetQuestionsToTheTailnet(t *testing.T) {
	t.Parallel()

	tn := &fakeTailnet{up: true, note: "v1.102.3 · 3 узла", latency: 12 * time.Millisecond}
	p := NewProbe(tn, &fakeRemote{}, &fakeLocalProbe{})
	ctx := context.Background()

	up, note, err := p.TailscaleUp(ctx)
	if err != nil || !up || note != "v1.102.3 · 3 узла" {
		t.Fatalf("TailscaleUp = (%v, %q, %v)", up, note, err)
	}

	latency, err := p.PeerReachable(ctx, "100.127.188.87")
	if err != nil || latency != 12*time.Millisecond {
		t.Fatalf("PeerReachable = (%v, %v)", latency, err)
	}
	if tn.lastAddr != "100.127.188.87" {
		t.Errorf("PeerReachable got addr %q", tn.lastAddr)
	}

	// Presence is asked about the machine, not about an address: either field
	// may be all the configuration carries, so the whole value goes through.
	tn.presence = core.PresenceOffline
	peer := core.Machine{TailnetName: "iphone181", Addr: "100.64.6.3"}
	presence, err := p.PeerPresence(ctx, peer)
	if err != nil || presence != core.PresenceOffline {
		t.Fatalf("PeerPresence = (%v, %v)", presence, err)
	}
	if tn.lastPeer != peer {
		t.Errorf("PeerPresence got %+v, want %+v", tn.lastPeer, peer)
	}
}

func TestProbeRoutesTheRemoteQuestionsToSSH(t *testing.T) {
	t.Parallel()

	rm := &fakeRemote{running: true}
	p := NewProbe(&fakeTailnet{}, rm, &fakeLocalProbe{})
	ctx := context.Background()

	if err := p.TCPReachable(ctx, "100.127.188.87", 22); err != nil {
		t.Fatalf("TCPReachable: %v", err)
	}
	if rm.lastPort != 22 {
		t.Errorf("TCPReachable got port %d", rm.lastPort)
	}

	running, err := p.ServiceRunning(ctx, "sshd")
	if err != nil || !running || rm.lastService != "sshd" {
		t.Fatalf("ServiceRunning = (%v, %v), name %q", running, err, rm.lastService)
	}

	if err := p.PeerCanReachUs(ctx, "100.124.47.73", 22); err != nil {
		t.Fatalf("PeerCanReachUs: %v", err)
	}
}

func TestProbeRoutesTheLocalServiceQuestionToTheServiceManager(t *testing.T) {
	t.Parallel()

	loc := &fakeLocalProbe{running: true}
	p := NewProbe(&fakeTailnet{}, &fakeRemote{}, loc)

	running, err := p.LocalServiceRunning(context.Background(), "sshd")
	if err != nil || !running || loc.last != "sshd" {
		t.Fatalf("LocalServiceRunning = (%v, %v), name %q", running, err, loc.last)
	}

	loc.installed = true
	installed, err := p.LocalServiceInstalled(context.Background(), "sshd")
	if err != nil || !installed || loc.last != "sshd" {
		t.Fatalf("LocalServiceInstalled = (%v, %v), name %q", installed, err, loc.last)
	}
}

func TestProbeWithoutAdaptersReportsUnavailable(t *testing.T) {
	t.Parallel()

	p := NewProbe(nil, nil, nil)
	ctx := context.Background()

	if _, _, err := p.TailscaleUp(ctx); !errors.Is(err, ErrProbeUnavailable) {
		t.Errorf("TailscaleUp err = %v", err)
	}
	if _, err := p.PeerReachable(ctx, "100.1.1.1"); !errors.Is(err, ErrProbeUnavailable) {
		t.Errorf("PeerReachable err = %v", err)
	}
	if err := p.TCPReachable(ctx, "100.1.1.1", 22); !errors.Is(err, ErrProbeUnavailable) {
		t.Errorf("TCPReachable err = %v", err)
	}
	if _, err := p.ServiceRunning(ctx, "sshd"); !errors.Is(err, ErrProbeUnavailable) {
		t.Errorf("ServiceRunning err = %v", err)
	}
	if _, err := p.LocalServiceRunning(ctx, "sshd"); !errors.Is(err, ErrProbeUnavailable) {
		t.Errorf("LocalServiceRunning err = %v", err)
	}
	if _, err := p.LocalServiceInstalled(ctx, "sshd"); !errors.Is(err, ErrProbeUnavailable) {
		t.Errorf("LocalServiceInstalled err = %v", err)
	}
	if err := p.PeerCanReachUs(ctx, "100.1.1.1", 22); !errors.Is(err, ErrProbeUnavailable) {
		t.Errorf("PeerCanReachUs err = %v", err)
	}
	// A daemon that was never wired has no opinion about the peer, and must not
	// answer "absent": that would turn missing wiring into an accusation about
	// the user's tailnet.
	presence, err := p.PeerPresence(ctx, core.Machine{TailnetName: "win-sttm11d02rd"})
	if !errors.Is(err, ErrProbeUnavailable) || presence != core.PresenceUnknown {
		t.Errorf("PeerPresence = (%v, %v)", presence, err)
	}
}

func TestProbeForwardsAdapterErrors(t *testing.T) {
	t.Parallel()

	p := NewProbe(
		&fakeTailnet{upErr: errBoom, pingErr: errBoom, presenceErr: errBoom},
		&fakeRemote{tcpErr: errBoom, runningErr: errBoom, dialBackErr: errBoom},
		&fakeLocalProbe{err: errBoom, installedErr: errBoom},
	)
	ctx := context.Background()

	if _, _, err := p.TailscaleUp(ctx); !errors.Is(err, errBoom) {
		t.Errorf("TailscaleUp err = %v", err)
	}
	if _, err := p.PeerReachable(ctx, "x"); !errors.Is(err, errBoom) {
		t.Errorf("PeerReachable err = %v", err)
	}
	if err := p.TCPReachable(ctx, "x", 22); !errors.Is(err, errBoom) {
		t.Errorf("TCPReachable err = %v", err)
	}
	if _, err := p.ServiceRunning(ctx, "sshd"); !errors.Is(err, errBoom) {
		t.Errorf("ServiceRunning err = %v", err)
	}
	if _, err := p.LocalServiceRunning(ctx, "sshd"); !errors.Is(err, errBoom) {
		t.Errorf("LocalServiceRunning err = %v", err)
	}
	if _, err := p.LocalServiceInstalled(ctx, "sshd"); !errors.Is(err, errBoom) {
		t.Errorf("LocalServiceInstalled err = %v", err)
	}
	if _, err := p.PeerPresence(ctx, core.Machine{}); !errors.Is(err, errBoom) {
		t.Errorf("PeerPresence err = %v", err)
	}
	if err := p.PeerCanReachUs(ctx, "x", 22); !errors.Is(err, errBoom) {
		t.Errorf("PeerCanReachUs err = %v", err)
	}
}

// The composite has to be usable as the one interface the diagnosis takes;
// this is the assertion that the three adapters really do cover all eight.
//
// The declaration below is the compile-time half of that. The calls are the
// half worth running: each goes through the interface, so a method that
// compiles but was left routed at no adapter answers ErrProbeUnavailable and
// is named here. Asserting that the interface value is non-nil, which is what
// this test used to do, could not fail — NewProbe returns a concrete pointer,
// and staticcheck said so (SA4023).
func TestProbeSatisfiesCoreProbe(t *testing.T) {
	t.Parallel()

	var p core.Probe = NewProbe(
		&fakeTailnet{up: true},
		&fakeRemote{running: true},
		&fakeLocalProbe{running: true},
	)
	ctx := context.Background()

	asked := map[string]func() error{
		"TailscaleUp":         func() error { _, _, err := p.TailscaleUp(ctx); return err },
		"PeerReachable":       func() error { _, err := p.PeerReachable(ctx, "100.127.188.87"); return err },
		"TCPReachable":        func() error { return p.TCPReachable(ctx, "100.127.188.87", 22) },
		"ServiceRunning":      func() error { _, err := p.ServiceRunning(ctx, "sshd"); return err },
		"LocalServiceRunning": func() error { _, err := p.LocalServiceRunning(ctx, "sshd"); return err },
		"PeerCanReachUs":      func() error { return p.PeerCanReachUs(ctx, "100.124.47.73", 22) },
		"PeerPresence": func() error {
			_, err := p.PeerPresence(ctx, core.Machine{TailnetName: "win-sttm11d02rd"})
			return err
		},
		"LocalServiceInstalled": func() error {
			_, err := p.LocalServiceInstalled(ctx, "sshd")
			return err
		},
	}
	if len(asked) != 8 {
		t.Fatalf("the table covers %d methods, core.Probe has 8", len(asked))
	}
	for name, ask := range asked {
		if err := ask(); err != nil {
			t.Errorf("%s through core.Probe: %v", name, err)
		}
	}
}
