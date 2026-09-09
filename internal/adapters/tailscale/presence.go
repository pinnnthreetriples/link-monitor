package tailscale

import (
	"context"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// PeerPresence reports what the daemon already knows about the peer: whether
// the node is in this tailnet, and whether the control plane sees it connected.
//
// It sends nothing to the peer. The daemon's status is a local read, so this
// answers in milliseconds and it answers about a machine that is switched off —
// which no ping can do. That is the whole reason it exists: a peer the tailnet
// itself reports as offline used to be diagnosed by waiting out a ping deadline
// and then reporting the deadline, while this flag sat in the same status the
// machine card was already drawn from.
//
// A node that is not in the tailnet at all is [core.PresenceAbsent], never
// "offline": the two lead the user to different places, and telling them apart
// is the only reason this returns three values instead of a bool.
func (c *Client) PeerPresence(ctx context.Context, peer core.Machine) (core.Presence, error) {
	peers, err := c.Peers(ctx)
	if err != nil {
		return core.PresenceUnknown, err
	}
	for _, p := range peers {
		if !isMachine(p, peer) {
			continue
		}
		if p.Online {
			return core.PresenceOnline, nil
		}
		return core.PresenceOffline, nil
	}
	return core.PresenceAbsent, nil
}

// isMachine reports whether the tailnet node p is the machine m names.
//
// It matches on two things, the same two the UI's machine card matches on: the
// machine's identifying name, compared case-insensitively because MagicDNS is,
// and its address, compared exactly because it is an address.
//
// One name, not every name field the value carries. That restraint is not
// fussiness, it is a bug that was found by running this: a core.Machine comes
// from configuration where the MagicDNS name and the address are given together
// while WindowsName keeps whatever the defaults left in it, so a run pointed at
// «iphone181 / 100.64.6.3» still carried «WIN-STTM11D02RD». Treating the stale
// field as an alternative identity matched the work PC, reported the peer as
// online, and diagnosed the wrong machine — the same class of mistake the UI
// records in frontend/link.js, where taking the first node in the list named a
// phone while the rows below named the real peer. So the MagicDNS name is the
// identity when there is one, and the Windows name is consulted only when there
// is nothing better.
//
// An empty field never matches, either. A Machine may carry only a name or only
// an address, and a node without MagicDNS has no name at all, so a
// blank-matches-blank rule would report the first such node as the peer.
func isMachine(p Peer, m core.Machine) bool {
	want := m.TailnetName
	if want == "" {
		want = m.WindowsName
	}
	return sameName(p.Name, want) || sameName(p.Host, want) ||
		(p.Addr != "" && p.Addr == m.Addr)
}

// sameName compares two host names the way MagicDNS does, and answers false
// whenever either side has nothing to compare.
func sameName(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(a, b)
}
