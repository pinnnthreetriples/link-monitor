package gates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// TestTheMachineCardKnowsEveryPeerFinding closes the last gap in the join that
// keeps the machine card and the verdict telling one story.
//
// core/diagnose decides what is true about the peer, /api/status carries
// core.PeerState's token, and frontend/link.js turns that token into the state
// line the user reads. The Go side of that chain is held by
// TestTheMachineCardCannotContradictTheVerdict; this holds the last link, which
// crosses a language boundary and so cannot be type-checked: a finding the card
// has no wording for.
//
// The card is deliberately fail-safe about it — an unrecognised token draws
// «Неизвестно», never «В сети», so a gap is honest rather than a new lie — but
// honest is not the same as right. A state the engine can produce and the card
// cannot name is a state the user is never told about, and adding one is a
// decision, not an accident. It fails here.
//
// This lives in tools/gates for the same reason the size rule does: it is a
// rule about the repository rather than about a package, and neither side can
// import the other to assert it.
func TestTheMachineCardKnowsEveryPeerFinding(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "frontend", "link.js")

	source, err := os.ReadFile(path) //nolint:gosec // a fixed path under the repository root
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	table, ok := peerStateTable(string(source))
	if !ok {
		t.Fatalf("frontend/link.js has no PEER_STATE table; the card's wording is keyed off it")
	}

	for _, finding := range []core.PeerState{
		core.PeerStateUnknown, core.PeerStateAnswers, core.PeerStateSilentListed,
		core.PeerStateSilent, core.PeerStateOffline, core.PeerStateAbsent,
	} {
		if !strings.Contains(table, "\n    "+finding.String()+":") {
			t.Errorf("frontend/link.js has no wording for %q; the card would say «Неизвестно» "+
				"about a state the engine reports", finding)
		}
	}
}

// peerStateTable cuts out the PEER_STATE object literal, so a token that merely
// appears in a comment elsewhere in the file does not count as wording.
func peerStateTable(source string) (string, bool) {
	const opening = "var PEER_STATE = {"

	start := strings.Index(source, opening)
	if start < 0 {
		return "", false
	}
	rest := source[start+len(opening):]
	end := strings.Index(rest, "\n  };")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
