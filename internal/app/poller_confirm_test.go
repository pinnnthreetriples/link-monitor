package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// blipProbe is a healthy link whose daemon fails to answer on the listed
// calls, counted from one.
type blipProbe struct {
	stubProbe
	calls atomic.Int32
	fail  map[int32]bool
}

func (b *blipProbe) TailscaleUp(context.Context) (bool, string, error) {
	if b.fail[b.calls.Add(1)] {
		return false, "", errors.New("daemon busy")
	}
	return true, "v1.102.3", nil
}

func newConfirmPoller(probe core.Probe) *Poller {
	local, peer := testMachines()
	return NewPoller(PollerConfig{
		Probe: probe, Local: local, Peer: peer, Interval: time.Hour,
		Timeout: time.Second, ConfirmDelay: time.Millisecond,
	})
}

// secondResult starts p, lets its startup probe publish, asks for one more
// probe and returns what that one published.
func secondResult(t *testing.T, p *Poller) core.State {
	t.Helper()
	updates, unsubscribe := p.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); p.Wait() }()
	p.Start(ctx)

	next := func() core.State {
		select {
		case res := <-updates:
			return res.Snapshot.Overall
		case <-time.After(5 * time.Second):
			t.Fatal("no result arrived")
			return core.StateUnknown
		}
	}
	if first := next(); first != core.StateOK {
		t.Fatalf("startup probe = %v, want ok", first)
	}
	p.CheckNow()
	return next()
}

func TestPollerLooksTwiceBeforeCallingAHealthyLinkDown(t *testing.T) {
	t.Parallel()
	probe := &blipProbe{fail: map[int32]bool{2: true}}
	if got := secondResult(t, newConfirmPoller(probe)); got != core.StateOK {
		t.Errorf("Overall = %v, want ok: one failed probe on a healthy link is a blip", got)
	}
	if got := probe.calls.Load(); got != 3 {
		t.Errorf("probed %d times, want 3: the blip looked at a second time", got)
	}
}

func TestPollerReportsAnOutageThatSurvivesTheSecondLook(t *testing.T) {
	t.Parallel()
	probe := &blipProbe{fail: map[int32]bool{2: true, 3: true}}
	if got := secondResult(t, newConfirmPoller(probe)); got == core.StateOK {
		t.Error("an outage seen twice in a row was hidden")
	}
}
