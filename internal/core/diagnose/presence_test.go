package diagnose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// tcpProbe is the dial the engine still makes when the peer is off, as the fake
// records it. It is the kill-switch row's question and nothing else.
var tcpProbe = fmt.Sprintf("TCPReachable:%s:22", workPC.Addr)

// offlinePeer is a probe of the situation this change was written for: the
// tailnet reports the peer as not connected, and the ping is armed to do what
// it really does against a machine that is switched off — burn the deadline.
// Nothing in a correct run may consult it.
func offlinePeer(presence core.Presence) fakeProbe {
	p := healthy()
	p.presence = presence
	p.latency, p.peerErr = 0, context.DeadlineExceeded
	p.tcpErr = &core.TimeoutError{Addr: workPC.Addr, Port: sshPort}
	return p
}

// TestRunWithAPeerTheTailnetSaysIsNotThere covers the two answers the daemon
// can give about a peer that is not available, and what the engine still finds
// out while it is not.
func TestRunWithAPeerTheTailnetSaysIsNotThere(t *testing.T) {
	t.Parallel()

	tests := []scenario{
		{
			// The live run this was reported from: an iPhone at 100.64.6.3 that
			// /api/peers already reported as "online": false, diagnosed for 25
			// seconds and then blamed on the clock.
			name:    "the tailnet says the peer is offline",
			probe:   offlinePeer(core.PresenceOffline),
			states:  []core.State{core.StateOK, core.StateFail, core.StateFail, core.StateUnknown, core.StateOK},
			overall: core.StateFail,
			summary: summaryDown,
			detailHas: []string{
				"не в сети",
				// The point of the sentence: the tailnet said so, we did not
				// deduce it from a wait that ran out.
				"тайлнет",
			},
			fixes: []FixID{FixCheckPeerOnline},
			calls: []string{
				"TailscaleUp",
				presence,
				"LocalServiceRunning:sshd",
				tcpProbe,
			},
		},
		{
			name:      "the peer is not in the tailnet at all",
			probe:     offlinePeer(core.PresenceAbsent),
			states:    []core.State{core.StateOK, core.StateFail, core.StateFail, core.StateUnknown, core.StateOK},
			overall:   core.StateFail,
			summary:   summaryNotInTailnet,
			detailHas: []string{"нет вовсе", "не то имя"},
			fixes:     []FixID{FixPeerNotInTailnet},
			calls: []string{
				"TailscaleUp",
				presence,
				"LocalServiceRunning:sshd",
				tcpProbe,
			},
		},
		{
			// Two faults at once, and the local one is still worth naming: the
			// filter will be there when the machine comes back.
			name: "the peer is off and a local filter refuses sockets as well",
			probe: func() fakeProbe {
				p := offlinePeer(core.PresenceOffline)
				p.tcpErr = &core.BlockedError{Addr: workPC.Addr, Port: sshPort}
				return p
			}(),
			states:    []core.State{core.StateOK, core.StateFail, core.StateFail, core.StateUnknown, core.StateFail},
			overall:   core.StateFail,
			summary:   summaryDown,
			detailHas: []string{"не в сети"},
			fixes:     []FixID{FixCheckPeerOnline, FixDisableKillSwitch, FixSplitTunnelSSH},
		},
		{
			name: "the peer is off and we have no ssh server of our own either",
			probe: func() fakeProbe {
				p := offlinePeer(core.PresenceOffline)
				p.localServiceUp, p.localInstalled = false, false
				return p
			}(),
			states:    []core.State{core.StateOK, core.StateFail, core.StateFail, core.StateUnknown, core.StateOK},
			overall:   core.StateFail,
			summary:   summaryDown,
			detailHas: []string{"не в сети"},
			fixes:     []FixID{FixCheckPeerOnline, FixInstallSSHServer},
			calls: []string{
				"TailscaleUp",
				presence,
				"LocalServiceRunning:sshd",
				"LocalServiceInstalled:sshd",
				tcpProbe,
			},
		},
		{
			// Our own service manager refusing to answer changes nothing: the
			// direction is settled by the peer being away.
			name: "the peer is off and our service manager will not answer",
			probe: func() fakeProbe {
				p := offlinePeer(core.PresenceOffline)
				p.localServiceErr = errors.New("OpenSCManager: access denied")
				return p
			}(),
			states:    []core.State{core.StateOK, core.StateFail, core.StateFail, core.StateUnknown, core.StateOK},
			overall:   core.StateFail,
			summary:   summaryDown,
			detailHas: []string{"не в сети"},
			fixes:     []FixID{FixCheckPeerOnline},
		},
		{
			// The filter dial failed in a way that says nothing about a filter,
			// so the row says so rather than calling the kill switch innocent.
			name: "the peer is off and the filter dial answers nothing useful",
			probe: func() fakeProbe {
				p := offlinePeer(core.PresenceOffline)
				p.tcpErr = errors.New("wsasocket: descriptor is not a socket")
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateFail,
				core.StateUnknown, core.StateUnknown,
			},
			overall:   core.StateFail,
			summary:   summaryDown,
			detailHas: []string{"не в сети"},
			fixes:     []FixID{FixCheckPeerOnline},
		},
		{
			// A dial that establishes rules the filter out just as plainly as
			// one that goes unanswered.
			name: "the peer is off and the socket opens anyway",
			probe: func() fakeProbe {
				p := offlinePeer(core.PresenceOffline)
				p.tcpErr = nil
				return p
			}(),
			states:    []core.State{core.StateOK, core.StateFail, core.StateFail, core.StateUnknown, core.StateOK},
			overall:   core.StateFail,
			summary:   summaryDown,
			detailHas: []string{"не в сети"},
			fixes:     []FixID{FixCheckPeerOnline},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runScenario(t, tt)
		})
	}
}

// TestADaemonThatWillNotSayFallsBackToThePing: presence is an optimisation over
// the ping, not a replacement for it, and a daemon that cannot answer must not
// cost the engine its old behaviour. Nothing is claimed from a question that
// went unanswered.
func TestADaemonThatWillNotSayFallsBackToThePing(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.presence, probe.presenceErr = core.PresenceUnknown, errors.New("ipn: no status")

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	if snap.Overall != core.StateOK || snap.Summary != summaryOK {
		t.Errorf("verdict = %v / %q, want a healthy link diagnosed the old way",
			snap.Overall, snap.Summary)
	}
	if len(fixes) != 0 {
		t.Errorf("fixes = %v, want none: nothing is wrong", fixIDs(fixes))
	}
	assertCalls(t, probe.calls, []string{
		"TailscaleUp",
		presence,
		"PeerReachable:" + workPC.Addr,
		tcpProbe,
		"ServiceRunning:sshd",
		"LocalServiceRunning:sshd",
		dialBack,
	})
}

// TestAPresenceOfOnlineStillPings: the control plane seeing a node is not a
// working path to it, so an online peer is still round-tripped. Skipping the
// ping there would trade one dishonesty for another — and lose the latency the
// whole dashboard shows.
func TestAPresenceOfOnlineStillPings(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.latency, probe.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	if !containsCall(probe.calls, "PeerReachable:"+workPC.Addr) {
		t.Fatalf("calls = %v, want the ping: online is not the same as reachable", probe.calls)
	}
	if snap.Overall != core.StateFail {
		t.Errorf("verdict = %v, want fail: the peer did not answer in the tunnel", snap.Overall)
	}
	if got := fixIDs(fixes); len(got) != 1 || got[0] != FixCheckPeerOnline {
		t.Errorf("fixes = %v, want just %v", got, FixCheckPeerOnline)
	}
}

// TestAnOfflinePeerIsNamedNotTimedOut is the guard for the defect this change
// was written for, and it is built to bite the two ways that defect can come
// back: the engine forgetting to ask the daemon, and the engine asking and then
// waiting for the network anyway.
//
// What was really printed against a phone the tailnet already reported as
// offline:
//
//	ИТОГ     : unknown
//	заголовок: Состояние неизвестно
//	[unknown] SSH: workspace-claude-pc → iphone181  Проверку не удалось
//	                                                завершить: истекло время
//	                                                ожидания
//	починок: 0
//
// Twenty-five seconds to report the clock, with the answer in the daemon's
// status the whole time. So: the daemon is asked before anything is sent, the
// ping is never sent, no row blames a deadline, the row names the cause, the
// verdict is a definite failure rather than «unknown», and the advice that
// already existed for this exact case is finally offered.
func TestAnOfflinePeerIsNamedNotTimedOut(t *testing.T) {
	t.Parallel()

	// The deadline note, obtained the way the engine obtains it, so a reworded
	// sentence cannot slip past this test.
	deadline := unknownNote(context.DeadlineExceeded)

	tests := map[string]struct {
		presence core.Presence
		says     string
		fix      FixID
	}{
		"switched off": {core.PresenceOffline, "не в сети", FixCheckPeerOnline},
		"not a member": {core.PresenceAbsent, "в tailnet", FixPeerNotInTailnet},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := offlinePeer(tt.presence)
			snap, fixes := Run(context.Background(), &probe, laptop, workPC)

			assertInvariants(t, snap)
			assertAskedTheDaemonFirst(t, probe.calls)
			assertNoDeadlineWasWaitedFor(t, snap, deadline)
			assertTheCauseIsNamed(t, snap, tt.says)
			if !slicesContainsFix(fixes, tt.fix) {
				t.Errorf("fixes = %v, want %v among them", fixIDs(fixes), tt.fix)
			}
		})
	}
}

// assertAskedTheDaemonFirst holds the ordering the whole change rests on: the
// question that is already answered is asked before the one that costs a wait,
// and once it is answered the wait is not taken.
func assertAskedTheDaemonFirst(t *testing.T, calls []string) {
	t.Helper()

	if len(calls) < 2 || calls[0] != "TailscaleUp" || calls[1] != presence {
		t.Fatalf("calls = %v, want the daemon asked about the peer before anything is sent", calls)
	}
	if containsCall(calls, "PeerReachable:"+workPC.Addr) {
		t.Error("the peer was pinged after the tailnet had already said it is not there; " +
			"that ping is the fifteen seconds this change removed")
	}
	// The one dial that remains is the local-filter question, and it is about
	// this machine. Asking the peer's service, or for a call back, would need a
	// login to a machine that is not there.
	for _, call := range calls {
		if strings.HasPrefix(call, "ServiceRunning:") || strings.HasPrefix(call, "PeerCanReachUs") {
			t.Errorf("calls = %v; %q needs a session to a machine that is not there", calls, call)
		}
	}
}

// assertNoDeadlineWasWaitedFor: not one row may explain itself by the clock,
// and none may still be holding the note it was born with.
func assertNoDeadlineWasWaitedFor(t *testing.T, snap core.Snapshot, deadline string) {
	t.Helper()

	for _, row := range snap.Checks {
		switch row.Note {
		case deadline:
			t.Errorf("%s = %q; the tailnet had already answered this without a deadline",
				row.ID, row.Note)
		case noteNotChecked:
			t.Errorf("%s still carries the note it started with: the engine forgot it", row.ID)
		}
	}
	if snap.Overall == core.StateUnknown {
		t.Errorf("verdict = unknown over %q; a peer the tailnet reports as away is a "+
			"known cause, not a missing answer", snap.Summary)
	}
}

// assertTheCauseIsNamed: the outbound row and the headline both say what is
// wrong, in the words the machine card has always used for the same fact.
func assertTheCauseIsNamed(t *testing.T, snap core.Snapshot, says string) {
	t.Helper()

	outbound := snap.Checks[1]
	if outbound.ID != core.CheckSSHOut {
		t.Fatalf("Checks[1] is %q, want the outbound row", outbound.ID)
	}
	if outbound.State != core.StateFail {
		t.Errorf("outbound = %v (%q), want fail", outbound.State, outbound.Note)
	}
	if !strings.Contains(outbound.Note, says) {
		t.Errorf("outbound note = %q, want it to say %q", outbound.Note, says)
	}
	if !strings.Contains(snap.Detail, says) {
		t.Errorf("Detail = %q, want it to say %q", snap.Detail, says)
	}
	// The kill-switch row is a fact about this machine and stays answerable.
	filter := snap.Checks[4]
	if filter.ID != core.CheckKillSwitch {
		t.Fatalf("Checks[4] is %q, want the kill-switch row", filter.ID)
	}
	if filter.State == core.StateUnknown {
		t.Errorf("the local filter row = unknown (%q), but nothing about it needed the peer",
			filter.Note)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func slicesContainsFix(fixes []Fix, want FixID) bool {
	for _, f := range fixes {
		if f.ID == want {
			return true
		}
	}
	return false
}
