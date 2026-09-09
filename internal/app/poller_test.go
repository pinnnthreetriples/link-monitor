package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// stubProbe is the cheapest possible core.Probe: it reports the daemon down, so
// diagnose.Run stops after one question and every test here costs one call.
type stubProbe struct {
	runs chan struct{}
	// hold, when set, keeps a probe inside TailscaleUp until it is closed.
	hold chan struct{}
}

func newStubProbe() *stubProbe { return &stubProbe{runs: make(chan struct{}, 128)} }

func (s *stubProbe) TailscaleUp(context.Context) (bool, string, error) {
	select {
	case s.runs <- struct{}{}:
	default:
	}
	if s.hold != nil {
		<-s.hold
	}
	return false, "", nil
}

func (s *stubProbe) PeerReachable(context.Context, string) (time.Duration, error) { return 0, nil }
func (s *stubProbe) TCPReachable(context.Context, string, int) error              { return nil }
func (s *stubProbe) ServiceRunning(context.Context, string) (bool, error)         { return true, nil }

func (s *stubProbe) LocalServiceRunning(context.Context, string) (bool, error) { return true, nil }

func (s *stubProbe) LocalServiceInstalled(context.Context, string) (bool, error) { return true, nil }

func (s *stubProbe) PeerPresence(context.Context, core.Machine) (core.Presence, error) {
	return core.PresenceOnline, nil
}
func (s *stubProbe) PeerCanReachUs(context.Context, string, int) error { return nil }

var _ core.Probe = (*stubProbe)(nil)

// testMachines are the two real machines from CLAUDE.md.
func testMachines() (core.Machine, core.Machine) {
	local := core.Machine{
		WindowsName: "DESKTOP-L9DJSE9", TailnetName: "workspace-claude-pc",
		Addr: "100.124.47.73", User: "pnj",
	}
	peer := core.Machine{
		WindowsName: "WIN-STTM11D02RD", TailnetName: "win-sttm11d02rd",
		Addr: "100.127.188.87", User: "user",
	}
	return local, peer
}

// waitFor polls cond until it holds or the test runs out of patience.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestPoller(t *testing.T, probe core.Probe, interval time.Duration, h *History) *Poller {
	t.Helper()

	local, peer := testMachines()
	return NewPoller(PollerConfig{
		Probe: probe, Local: local, Peer: peer, Interval: interval,
		Timeout: time.Second, History: h,
	})
}

func TestPollerProbesOnceAtStartupAndPublishes(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)
	updates, unsubscribe := p.Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	select {
	case res := <-updates:
		if res.Snapshot.Overall != core.StateFail {
			t.Errorf("Overall = %v, want fail", res.Snapshot.Overall)
		}
		if len(res.Fixes) == 0 {
			t.Error("a downed daemon should have offered a fix")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no snapshot arrived")
	}

	latest, ok := p.Latest()
	if !ok || latest.Snapshot.Summary == "" {
		t.Errorf("Latest = (%+v, %v)", latest, ok)
	}

	cancel()
	p.Wait()
}

func TestPollerKeepsProbingOnTheInterval(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, 5*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	for i := range 3 {
		select {
		case <-probe.runs:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d probes ran", i)
		}
	}
	cancel()
	p.Wait()
}

func TestPollerCheckNowProbesImmediately(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	<-probe.runs // the startup probe
	p.CheckNow()
	select {
	case <-probe.runs:
	case <-time.After(2 * time.Second):
		t.Fatal("CheckNow did not cause a probe")
	}

	cancel()
	p.Wait()
}

func TestPollerCheckReturnsAFreshResult(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	<-probe.runs

	res, err := p.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Snapshot.Taken.IsZero() {
		t.Error("Check returned a snapshot with no timestamp")
	}

	cancel()
	p.Wait()
}

func TestPollerCheckOnAStoppedPollerSaysSo(t *testing.T) {
	t.Parallel()

	p := newTestPoller(t, newStubProbe(), time.Hour, nil)
	if _, err := p.Check(context.Background()); !errors.Is(err, ErrPollerStopped) {
		t.Fatalf("Check before Start = %v, want ErrPollerStopped", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	cancel()
	p.Wait()

	if _, err := p.Check(context.Background()); !errors.Is(err, ErrPollerStopped) {
		t.Fatalf("Check after shutdown = %v, want ErrPollerStopped", err)
	}
}

func TestPollerCheckHonoursTheCallersContext(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	probe.hold = make(chan struct{})
	p := newTestPoller(t, probe, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	callCtx, callCancel := context.WithCancel(context.Background())
	callCancel()
	if _, err := p.Check(callCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check with a dead context = %v", err)
	}

	close(probe.hold)
	cancel()
	p.Wait()
}

func TestPollerCancellationClosesSubscribersAndLeavesNothingRunning(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)
	updates, unsubscribe := p.Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	<-probe.runs
	cancel()
	p.Wait()

	// Drain whatever is buffered, then the channel must be closed.
	for range 2 {
		if _, open := <-updates; !open {
			return
		}
	}
	t.Fatal("the subscriber channel was never closed")
}

func TestPollerSubscribeAfterShutdownYieldsAClosedChannel(t *testing.T) {
	t.Parallel()

	p := newTestPoller(t, newStubProbe(), time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	cancel()
	p.Wait()

	updates, unsubscribe := p.Subscribe()
	unsubscribe()
	if _, open := <-updates; open {
		t.Error("a late subscriber got a live channel")
	}
	// Starting again after shutdown must not resurrect the loop.
	p.Start(context.Background())
	p.Wait()
}

func TestPollerDropsStaleValuesForASlowSubscriber(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)
	updates, unsubscribe := p.Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Never read: the buffer of one fills at once and every later publish has to
	// replace it rather than block the poller.
	for range 5 {
		p.CheckNow()
		<-probe.runs
	}
	waitFor(t, "the poller to keep publishing", func() bool { return len(updates) == 1 })

	cancel()
	p.Wait()
}

func TestPollerFeedsTheHistory(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	h := NewHistory(time.Hour)
	p := newTestPoller(t, probe, time.Hour, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	<-probe.runs
	waitFor(t, "a history point", func() bool { return h.Len() >= 1 })

	if h.Points()[0].State != core.StateFail {
		t.Errorf("history state = %v, want fail", h.Points()[0].State)
	}

	cancel()
	p.Wait()
}

func TestPollerReportsCheckingWhileAProbeRuns(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	probe.hold = make(chan struct{})
	p := newTestPoller(t, probe, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	waitFor(t, "the probe to start", func() bool { return p.Checking() })
	close(probe.hold)
	waitFor(t, "the probe to finish", func() bool { return !p.Checking() })

	cancel()
	p.Wait()
}

func TestPollerFillsInDefaults(t *testing.T) {
	t.Parallel()

	p := NewPoller(PollerConfig{Probe: newStubProbe()})
	if p.Interval() != DefaultInterval {
		t.Errorf("Interval = %v, want %v", p.Interval(), DefaultInterval)
	}
	if p.cfg.Timeout != DefaultProbeTimeout {
		t.Errorf("Timeout = %v, want %v", p.cfg.Timeout, DefaultProbeTimeout)
	}
	if _, ok := p.Latest(); ok {
		t.Error("a poller that never ran reports a latest result")
	}
	if p.Checking() {
		t.Error("a poller that never ran reports a probe in flight")
	}
}

func TestPollerStartIsIdempotent(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	p.Start(ctx)
	<-probe.runs

	cancel()
	p.Wait()

	// One loop means one startup probe; a second Start would have made two.
	if len(probe.runs) != 0 {
		t.Errorf("%d extra probes ran — Start started a second loop", len(probe.runs))
	}
}
