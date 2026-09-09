package diagnose

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// headlinesForPeer lists the headlines that may stand over one finding about
// the peer machine, and it is the same kind of contract as headlinesFor: the
// machine card is drawn from Snapshot.Peer and the headline is printed beside
// it, so a pair outside this table is not an imprecision but two sentences on
// one screen contradicting each other.
//
// The reported defect is the first row's absence from the last two lists. The
// card said «В сети», drawn from the control plane's flag, while the headline
// said «Связи нет — похоже, она выключена», drawn from a ping that got nothing.
//
//   - PeerStateAnswers may sit under anything the run can find wrong with a
//     port, a service or a key. It may never sit under the two headlines that
//     say the machine itself is not there.
//   - The three findings that mean silence admit «Связи нет» and nothing else:
//     that is the sentence for a machine that does not answer.
//   - PeerStateAbsent keeps its own headline, as it always has — a node this
//     tailnet never heard of is not a broken link.
//   - PeerStateUnknown normally admits only «Состояние неизвестно». It admits
//     «Связи нет» for exactly one reason, which assertCardAgreesWithVerdict
//     checks separately: the local daemon is not in the tailnet, which is a
//     statement about this machine and claims nothing about the peer.
var headlinesForPeer = map[core.PeerState][]string{
	core.PeerStateAnswers: {
		summaryOK, summaryBlocked, summaryPartial, summaryRefused,
		summaryKeyUnusable, summaryHostKeyChanged, summaryHostUnverified,
		summaryUnknown,
	},
	core.PeerStateSilentListed: {summaryDown},
	core.PeerStateSilent:       {summaryDown},
	core.PeerStateOffline:      {summaryDown},
	core.PeerStateAbsent:       {summaryNotInTailnet},
	core.PeerStateUnknown:      {summaryUnknown, summaryDown},
}

// TestTheMachineCardCannotContradictTheVerdict is the guard for the defect a
// user found by switching a machine off and watching this program.
//
// What was on the screen, both halves true at once:
//
//	ИТОГ     : fail
//	заголовок: Связи нет
//	пояснение: Вторая машина не отвечает — похоже, она выключена
//	карточка : win-sttm11d02rd · В сети      ← the control plane's flag
//
// The tailnet really did still list the node as connected — the control plane
// takes minutes to notice a shutdown — and the machine really did not answer.
// Two consumers each picked a different fact and printed it, so whichever half
// the reader believed, the program had misled them.
//
// The fix was to join the two facts once, in the run that holds both, and this
// test is the reason the join cannot come apart again. It does not check the
// case that was reported: it runs every combination of answers the six probes
// can give — the same cross product TestHeadlineMatchesTheVerdict uses — and
// holds the card and the headline to one story across all of them. A branch
// that finds the peer silent and forgets to say so fails here rather than on
// somebody's screen.
func TestTheMachineCardCannotContradictTheVerdict(t *testing.T) {
	t.Parallel()

	shapes := everyProbeShape()
	if len(shapes) < 1000 {
		t.Fatalf("only %d probe shapes; the cross product is not being built", len(shapes))
	}

	seen := make(map[core.PeerState]bool, len(headlinesForPeer))
	for _, shape := range shapes {
		probe := shape.probe
		snap, fixes := Run(context.Background(), &probe, laptop, workPC)

		seen[snap.Peer] = true
		assertCardAgreesWithVerdict(t, shape.name, snap, fixes)
		if t.Failed() {
			t.Fatalf("first failing shape: %s", shape.name)
		}
	}
	// A run that ended before the first probe claims nothing about the peer.
	assertCardAgreesWithVerdict(t, "context already done", cancelledRun(), nil)

	// The guard is worth only as much as the range it covers, so the range is
	// asserted too: every finding the card can draw must have been produced by
	// some shape above, or this test is quietly checking five of six.
	for finding := range headlinesForPeer {
		if !seen[finding] {
			t.Errorf("no probe shape produced %v; this guard does not cover it", finding)
		}
	}
}

// assertCardAgreesWithVerdict holds one snapshot to the contract above, plus
// the two rules that the table alone cannot express.
func assertCardAgreesWithVerdict(t *testing.T, name string, snap core.Snapshot, fixes []Fix) {
	t.Helper()

	allowed, known := headlinesForPeer[snap.Peer]
	if !known {
		t.Errorf("%s: the finding %v has no agreed headline; a state the card cannot draw",
			name, snap.Peer)
		return
	}
	if !slices.Contains(allowed, snap.Summary) {
		t.Errorf("%s: the card would say %v under the headline %q, want one of %q",
			name, snap.Peer, snap.Summary, allowed)
	}
	// «Связи нет» over a card that claims nothing about the peer is only honest
	// when the sentence is about this machine's own daemon. Anywhere else it is
	// a statement that the peer is not there, and the card has to be saying the
	// same thing — a branch that finds silence and forgets to record it lands
	// here.
	if snap.Summary == summaryDown && snap.Peer == core.PeerStateUnknown && snap.Detail != detailNoTS {
		t.Errorf("%s: the headline says %q (%q) while the card claims nothing about the peer",
			name, snap.Summary, snap.Detail)
	}
	// The reported defect, stated directly: the card may not call the peer
	// reachable under a headline that says the machine is not there.
	if snap.Peer.Answers() && (snap.Summary == summaryDown || snap.Summary == summaryNotInTailnet) {
		t.Errorf("%s: the card would say «В сети» under the headline %q", name, snap.Summary)
	}
	// The advice is read on the same screen as the card. Telling the user to go
	// and check whether the machine is switched on, beside a card saying it
	// answers, is the same contradiction in a third place.
	if snap.Peer.Answers() && slicesContainsFix(fixes, FixCheckPeerOnline) {
		t.Errorf("%s: the card would say «В сети» while the advice is %q", name, FixCheckPeerOnline)
	}
}

// TestTheDiagnosisOutranksTheControlPlane is the other half of the fix: which
// of the two facts wins, in each of the six situations they can be in.
//
// The middle row is the one the defect was reported from, and the one where the
// two disagree: the control plane says connected, the ping gets nothing, and
// the finding is that the machine is silent *and* still listed — neither fact
// dropped, because dropping either was how the program came to state both at
// once in two different places.
func TestTheDiagnosisOutranksTheControlPlane(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		probe fakeProbe
		want  core.PeerState
	}{
		"the peer answers the tunnel": {healthy(), core.PeerStateAnswers},
		"the tailnet lists it as connected and it does not answer": {
			func() fakeProbe {
				p := healthy()
				p.latency, p.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}
				return p
			}(),
			core.PeerStateSilentListed,
		},
		"it does not answer and the daemon would not say anything about it": {
			func() fakeProbe {
				p := healthy()
				p.presence, p.presenceErr = core.PresenceUnknown, errors.New("ipn: no status")
				p.latency, p.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}
				return p
			}(),
			core.PeerStateSilent,
		},
		"it does not answer and the daemon settled nothing about it": {
			func() fakeProbe {
				p := healthy()
				p.presence = core.PresenceUnknown
				p.latency, p.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}
				return p
			}(),
			core.PeerStateSilent,
		},
		"the tailnet says it is not connected": {offlinePeer(core.PresenceOffline), core.PeerStateOffline},
		"the tailnet has never heard of it":    {offlinePeer(core.PresenceAbsent), core.PeerStateAbsent},
		"our own daemon is not in the tailnet": {fakeProbe{up: false}, core.PeerStateUnknown},
		"our own daemon will not say either way": {
			fakeProbe{upErr: errors.New("ipn: connection refused")}, core.PeerStateUnknown,
		},
		"our ping probe is broken, so nothing was established": {
			func() fakeProbe {
				p := healthy()
				p.latency, p.peerErr = 0, errors.New("tailscaled: no route")
				return p
			}(),
			core.PeerStateUnknown,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := tt.probe
			snap, fixes := Run(context.Background(), &probe, laptop, workPC)

			if snap.Peer != tt.want {
				t.Errorf("Peer = %v, want %v (headline %q / %q)",
					snap.Peer, tt.want, snap.Summary, snap.Detail)
			}
			assertInvariants(t, snap)
			assertCardAgreesWithVerdict(t, name, snap, fixes)
		})
	}
}

// TestASilentPeerStillAnswersForItsOwnMachine is the guard for the second
// defect: the same local fact checked in one no-answer case and skipped in the
// other.
//
// When the tailnet said the peer was offline, the kill-switch row was already
// answered by a short local dial — Windows refuses a blocked socket inside
// connect(), before a packet leaves, so nobody at the far end is needed. When
// the peer merely did not answer the tunnel, the identical row read «Не
// проверялось: вторая машина не отвечает». Same question, same evidence
// available, two different answers.
func TestASilentPeerStillAnswersForItsOwnMachine(t *testing.T) {
	t.Parallel()

	silent := func() fakeProbe {
		p := healthy()
		p.latency, p.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}
		return p
	}

	// Two faults at once, which is where the row earns its keep: the filter
	// will still be there when the machine comes back, and it is named now
	// rather than hidden behind the peer's silence.
	runScenario(t, scenario{
		name: "the peer is silent and a local filter refuses sockets as well",
		probe: func() fakeProbe {
			p := silent()
			p.tcpErr = &core.BlockedError{Addr: workPC.Addr, Port: sshPort}
			return p
		}(),
		states: []core.State{
			core.StateOK, core.StateFail, core.StateFail,
			core.StateUnknown, core.StateFail,
		},
		overall:   core.StateFail,
		summary:   summaryDown,
		detailHas: []string{"не отвечает"},
		fixes:     []FixID{FixCheckPeerOnline, FixDisableKillSwitch, FixSplitTunnelSSH},
		calls: []string{
			"TailscaleUp",
			presence,
			"PeerReachable:" + workPC.Addr,
			tcpProbe,
		},
	})

	tests := map[string]struct {
		probe fakeProbe
		state core.State
		note  string
	}{
		"no local filter refused the socket": {silent(), core.StateOK, noteNoLocalRefusal},
		"a local filter did refuse it": {
			func() fakeProbe {
				p := silent()
				p.tcpErr = &core.BlockedError{Addr: workPC.Addr, Port: sshPort}
				return p
			}(),
			core.StateFail,
			blockedSocketNote(&core.BlockedError{Addr: workPC.Addr, Port: sshPort}),
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := tt.probe
			snap, _ := Run(context.Background(), &probe, laptop, workPC)

			assertInvariants(t, snap)
			filter := snap.Checks[4]
			if filter.ID != core.CheckKillSwitch {
				t.Fatalf("Checks[4] is %q, want the kill-switch row", filter.ID)
			}
			if filter.State != tt.state || filter.Note != tt.note {
				t.Errorf("the kill-switch row = %v (%q), want %v (%q)",
					filter.State, filter.Note, tt.state, tt.note)
			}
			// The dial is the local one, cut short: nothing else about the peer
			// was asked, because everything else needs a login to it.
			if !containsCall(probe.calls, tcpProbe) {
				t.Errorf("calls = %v, want the local dial that answers this row", probe.calls)
			}
			for _, call := range probe.calls {
				if call == "ServiceRunning:sshd" {
					t.Errorf("calls = %v; %q needs a session to a machine that is silent",
						probe.calls, call)
				}
			}
		})
	}
}
