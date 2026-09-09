package core

import "fmt"

// PeerState is what one diagnosis established about the peer machine itself:
// not about a port, a service or a key, but about whether the machine at the
// other end is there and answering.
//
// It exists because two facts about the peer are true at the same time and
// disagree, and something had to decide which of them a reader is shown. The
// control plane tells the local daemon which nodes are connected, and it goes
// on saying "connected" for a while after a machine is switched off — the
// notice takes time to travel. Meanwhile the diagnosis sends the peer a disco
// ping and gets nothing. Both are real: the tailnet genuinely still lists the
// node, and the node genuinely does not answer.
//
// Left to two consumers, that pair produced the defect this type closes: the
// machine card drew «В сети» from the control-plane flag while the headline
// three centimetres above it read «Связи нет — похоже, она выключена». The join
// therefore happens once, in [github.com/pinnnthreetriples/link-monitor/internal/core/diagnose],
// where both facts are in hand in the same run, and every consumer reads the
// one value that comes out of it. The rule that join follows is that the
// diagnosis outranks the control plane, because the diagnosis actually tried:
// a flag saying "connected" is what the tailnet last heard, and silence from
// the machine is what is happening now.
//
// The values are not a severity ordering and must not be compared with < or >.
// They are six distinct findings, each with its own sentence.
type PeerState int

const (
	// PeerStateUnknown is a run that established nothing about the peer: the
	// local daemon was not connected to the tailnet, or would not answer, or
	// the probe that would have asked failed for its own reasons, or the
	// context ended first.
	//
	// It is the zero value on purpose. A consumer that reads it must claim
	// nothing about the peer — in particular it must not fall back to the
	// control-plane flag, which is precisely the "still connected" answer that
	// outlives a shutdown.
	PeerStateUnknown PeerState = iota

	// PeerStateAnswers is the peer round-tripping a ping through the tunnel:
	// there is a machine at the other end and it is talking to this one. It is
	// the only value that licenses a claim that the peer is reachable, and it
	// says nothing about the peer's port, service or keys — those are separate
	// rows with separate evidence.
	PeerStateAnswers

	// PeerStateSilentListed is the state this type was written for, and the
	// only one where two true facts contradict each other: the control plane
	// still lists the node as connected, and the node did not answer. That is
	// exactly what a machine looks like for the first minutes after it is
	// switched off, and it is also what a machine looks like when its network
	// dies without a clean disconnect.
	//
	// Neither half may be dropped. "Connected" alone is the lie the machine
	// card told; "not connected" alone throws away a fact the tailnet is still
	// asserting.
	PeerStateSilentListed

	// PeerStateSilent is the peer not answering while the tailnet has no
	// opinion to offer — the daemon answered about itself but not about that
	// node. It is held apart from PeerStateSilentListed because the difference
	// is the whole of what may be said next: without the control plane's word,
	// nothing about the tailnet's view can be claimed.
	PeerStateSilent

	// PeerStateOffline is the tailnet's own answer that the node is not
	// connected: switched off, asleep, or without a network. Nothing was sent
	// to the peer, and nothing needed to be.
	PeerStateOffline

	// PeerStateAbsent is a node this tailnet does not know at all — a wrong
	// name in the settings, a node deleted in the admin console, or a machine
	// logged into somebody else's tailnet. It is not a broken link and must not
	// read like one.
	PeerStateAbsent
)

// String reports the state as a stable lowercase token. It is the token the
// HTTP layer puts on the wire and the machine card keys its wording off, so
// these strings are part of that contract and are not cosmetic.
func (p PeerState) String() string {
	switch p {
	case PeerStateUnknown:
		return "unknown"
	case PeerStateAnswers:
		return "answers"
	case PeerStateSilentListed:
		return "silent_listed"
	case PeerStateSilent:
		return "silent"
	case PeerStateOffline:
		return "offline"
	case PeerStateAbsent:
		return "absent"
	default:
		return fmt.Sprintf("PeerState(%d)", int(p))
	}
}

// Answers reports whether this state licenses saying the peer is reachable.
//
// It is one method rather than a comparison at each call site because it is
// the question every consumer actually asks, and because the honest answer to
// it for five of the six values is "no" — including PeerStateUnknown, where
// the temptation to fall back to weaker evidence is exactly what produced the
// contradiction this type exists to prevent.
func (p PeerState) Answers() bool { return p == PeerStateAnswers }
