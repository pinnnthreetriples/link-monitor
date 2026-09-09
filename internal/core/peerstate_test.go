package core

import "testing"

// TestPeerStateStringIsTheWireToken: these tokens travel over /api/status and
// the machine card keys its wording off them, so a renamed token is a broken
// card and has to fail here first.
func TestPeerStateStringIsTheWireToken(t *testing.T) {
	t.Parallel()

	tests := map[PeerState]string{
		PeerStateUnknown:      "unknown",
		PeerStateAnswers:      "answers",
		PeerStateSilentListed: "silent_listed",
		PeerStateSilent:       "silent",
		PeerStateOffline:      "offline",
		PeerStateAbsent:       "absent",
		PeerState(9):          "PeerState(9)",
	}
	for state, want := range tests {
		if got := state.String(); got != want {
			t.Errorf("PeerState(%d).String() = %q, want %q", int(state), got, want)
		}
	}
}

// TestOnlyAnAnsweringPeerMayBeCalledReachable is the rule the machine card
// rests on: exactly one of the six findings licenses «В сети».
//
// The zero value is in the list of those that do not, and that is the point of
// the list. A consumer that treats "nothing was established" as permission to
// use the control plane's flag instead gets back the defect this type closes —
// the tailnet says "connected" for minutes after a machine is switched off.
func TestOnlyAnAnsweringPeerMayBeCalledReachable(t *testing.T) {
	t.Parallel()

	if !PeerStateAnswers.Answers() {
		t.Error("a peer that round-tripped a ping is reachable")
	}
	for _, state := range []PeerState{
		PeerStateUnknown, PeerStateSilentListed, PeerStateSilent,
		PeerStateOffline, PeerStateAbsent, PeerState(9),
	} {
		if state.Answers() {
			t.Errorf("%v must not license a claim that the peer answers", state)
		}
	}
}
