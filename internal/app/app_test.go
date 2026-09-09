package app

import (
	"context"
	"testing"
	"time"
)

// assertEveryPieceWired names the piece that is missing rather than printing
// the whole App and leaving the reader to find the nil, and keeps the eight-way
// condition out of the test's own branch count.
func assertEveryPieceWired(t *testing.T, a *App) {
	t.Helper()

	pieces := []struct {
		name  string
		isNil bool
	}{
		{"Probe", a.Probe == nil},
		{"Poller", a.Poller == nil},
		{"History", a.History == nil},
		{"Fixer", a.Fixer == nil},
		{"Transfers", a.Transfers == nil},
		{"Forwards", a.Forwards == nil},
		{"Peers", a.Peers == nil},
		{"Link", a.Link == nil},
	}
	var unwired []string
	for _, p := range pieces {
		if p.isNil {
			unwired = append(unwired, p.name)
		}
	}
	if len(unwired) > 0 {
		t.Fatalf("New left these unwired: %v", unwired)
	}
}

func TestNewWiresEveryPieceAndCloseLeavesNothingRunning(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwarder{withAddr: true}
	local, peer := testMachines()
	ctx, cancel := context.WithCancel(context.Background())

	a := New(ctx, Config{
		Local: local, Peer: peer,
		Interval: 5 * time.Millisecond, ProbeTimeout: time.Second,
		HistoryWindow: time.Hour, InboxDir: `C:\in`, MaxTransfers: 5,
	}, Deps{
		Tailnet:   &fakeTailnet{up: false},
		Remote:    &fakeRemote{},
		Local:     &fakeLocalProbe{},
		Services:  &fakeServiceControl{},
		Shell:     &fakeShell{stdout: "LINKMON-FIX 0"},
		Files:     &fakeMover{},
		Forwarder: fwd,
		Publisher: &fakePublisher{url: "https://x.ts.net/"},
		Peers:     &fakePeerLister{},
		LinkCtl:   &fakeLinkControl{},
	})

	assertEveryPieceWired(t, a)
	if a.Transfers.Inbox() != `C:\in` {
		t.Errorf("inbox = %q", a.Transfers.Inbox())
	}

	a.Start()
	waitFor(t, "the first snapshot", func() bool { _, ok := a.Poller.Latest(); return ok })
	waitFor(t, "a history point", func() bool { return a.History.Len() > 0 })

	if _, err := a.Forwards.Start(context.Background(), 0, "100.127.188.87", 22); err != nil {
		t.Fatalf("Start a forward: %v", err)
	}

	// The link controller is wired to the same Tailscale client that answers the
	// probe, so it sees the daemon as down and connects rather than shrugging.
	if out := a.Link.Apply(context.Background(), LinkUp); !out.OK {
		t.Errorf("Link.Apply = %+v", out)
	}

	cancel()
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fwd.opened[0].closes() != 1 {
		t.Error("Close left a forward open")
	}
	if len(a.Forwards.List()) != 0 {
		t.Error("Close left something in the forward registry")
	}
}

func TestNewWithNoAdaptersStillBuilds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := New(ctx, Config{}, Deps{})
	if a.Poller.Interval() != DefaultInterval {
		t.Errorf("Interval = %v", a.Poller.Interval())
	}
	if a.History.Window() != DefaultHistoryWindow {
		t.Errorf("HistoryWindow = %v", a.History.Window())
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close on an unstarted app = %v", err)
	}
}
