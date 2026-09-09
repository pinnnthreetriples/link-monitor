package diagnose

import "github.com/pinnnthreetriples/link-monitor/internal/core"

// This file holds one decision: when the two facts this program knows about the
// peer machine disagree, which one does the user get told?
//
// The two facts are these. The local Tailscale daemon carries the control
// plane's word on whether a node is connected — that is what present() reads,
// and it is the fact /api/peers has always reported as "online": true. The
// diagnosis also sends the peer a disco ping through the tunnel and waits for
// it. Normally they agree. For the first minutes after a machine is switched
// off they do not: the control plane has not noticed yet, so the flag still
// says connected while nothing answers.
//
// Both are true, and the program used to report both at once, in two places, as
// if they were about different machines:
//
//	ИТОГ     : fail
//	заголовок: Связи нет
//	пояснение: Вторая машина не отвечает — похоже, она выключена
//	машинная карточка: В сети          ← drawn from the control-plane flag
//
// Whichever half the reader believed, they were misled. So the two are joined
// here, once, in the run that holds both — and the rule is that the diagnosis
// outranks the control plane, because the diagnosis actually tried. A flag is
// what the tailnet last heard; silence is what is happening now.
//
// Note what the join does *not* do: it does not discard the flag. A machine
// that the tailnet still lists and that does not answer is a different and more
// informative state than a machine the tailnet has given up on, so it gets its
// own value ([core.PeerStateSilentListed]) and the card can say both things
// without saying either of them wrongly.
//
// # Why the staleness is not detected from the daemon's timestamps
//
// The obvious alternative is to keep reading the flag and discount it when it
// looks stale. The daemon's per-peer timestamps cannot support that:
//
//   - LastSeen is documented in ipnstate as "last seen to tailcontrol; only
//     present if offline" — it is empty in exactly the state we would need it
//     for. Confirmed live: the work PC, online, reports the zero time, while a
//     phone the tailnet reports as offline carries a real one.
//   - LastHandshake is "with local wireguard", and WireGuard only handshakes
//     when there is traffic; Tailscale also drops idle peers from the WireGuard
//     configuration, in which case the field is zeroed. A healthy machine
//     nobody has spoken to for ten minutes therefore looks identical to one
//     that has been switched off.
//   - LastWrite is "time last packet sent" — sent by *us*. It describes this
//     machine's outbound activity and says nothing about whether the peer
//     replied, and it keeps moving forward while a dead peer is being probed.
//   - Active is defined in the same file as "some packet sent to this peer in
//     the past two minutes", with a comment saying the definition is subject to
//     change. Same objection, and not a promise.
//
// Every one of them is a fact about our own traffic, and reading a peer's
// liveness out of it would be inference dressed as evidence. The ping the
// diagnosis already sends is direct evidence, so the join is decided on that.

// peerListed records that the control plane sees the node as connected.
//
// It is remembered rather than acted on, because on its own it settles nothing:
// being connected to the control plane is not a working path to a machine. What
// it is needed for is the sentence afterwards — a peer that goes on to answer
// is simply reachable, and a peer that stays silent while this flag is set is
// the post-shutdown state, which is worth saying out loud.
func (d *diagnosis) peerListed() { d.presenceListed = true }

// peerAnswered records a peer that round-tripped the tunnel. This is the one
// finding that licenses telling the user the machine is there.
func (d *diagnosis) peerAnswered() { d.peerState = core.PeerStateAnswers }

// peerWentSilent records a peer that was asked and did not answer, keeping the
// control plane's word beside the silence instead of throwing it away.
func (d *diagnosis) peerWentSilent() {
	if d.presenceListed {
		d.peerState = core.PeerStateSilentListed
		return
	}
	d.peerState = core.PeerStateSilent
}
