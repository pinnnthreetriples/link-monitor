package httpapi

import (
	"errors"
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/diagnose"
)

// handleStatus answers with the last finished diagnosis, or an honest "not
// checked yet" before the first one.
func (s *server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.currentStatus())
}

// currentStatus renders whatever the poller last saw.
func (s *server) currentStatus() Status {
	if s.deps.Status == nil {
		return emptyStatus(false)
	}
	checking := s.deps.Status.Checking()
	res, ok := s.deps.Status.Latest()
	if !ok {
		return emptyStatus(checking)
	}
	return toStatus(res, checking)
}

// handleCheck probes now and answers with the result. It waits on the poller
// rather than probing itself, so two impatient clicks cost one probe.
func (s *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if s.deps.Status == nil {
		writeError(w, http.StatusServiceUnavailable, msgPollerDown)
		return
	}
	res, err := s.deps.Status.Check(r.Context())
	switch {
	case errors.Is(err, app.ErrPollerStopped):
		writeError(w, http.StatusServiceUnavailable, msgPollerDown)
	case err != nil:
		writeError(w, http.StatusGatewayTimeout, msgCheckFailed)
	default:
		writeJSON(w, http.StatusOK, toStatus(res, false))
	}
}

// fixRequest is the body of POST /api/fix.
type fixRequest struct {
	ID string `json:"id"`
}

// fixResponse is its answer.
type fixResponse struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	NeedsAdmin bool   `json:"needsAdmin"`
}

// handleFix carries out one repair. An unknown id is not an HTTP error: the
// fixer has a Russian sentence for it, and the UI shows outcomes the same way
// whatever they are.
func (s *server) handleFix(w http.ResponseWriter, r *http.Request) {
	var req fixRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if s.deps.Fixes == nil {
		writeJSON(w, http.StatusServiceUnavailable, fixResponse{Message: msgPollerDown})
		return
	}

	out := s.deps.Fixes.Apply(r.Context(), diagnose.FixID(req.ID))
	writeJSON(w, http.StatusOK, fixResponse{
		OK: out.OK, Message: out.Message, NeedsAdmin: out.NeedsAdmin,
	})
}

// historyResponse is the body of GET /api/history.
type historyResponse struct {
	Points []HistoryPoint `json:"points"`
}

// handleHistory answers with the 24-hour uptime strip.
func (s *server) handleHistory(w http.ResponseWriter, _ *http.Request) {
	var points []app.Point
	if s.deps.History != nil {
		points = s.deps.History.Points()
	}
	writeJSON(w, http.StatusOK, historyResponse{Points: toPoints(points)})
}

// peersResponse is the body of GET /api/peers.
//
// ConfiguredPeer is the tailnet name of the machine this install was set up to
// watch. It is a top-level field rather than a flag on one list entry because
// it is a configuration fact, not a property of the tailnet: the list can hold
// any number of nodes — a phone, a tablet, a second laptop — and no entry
// carries anything saying which one was meant, so a UI left to guess picks
// whatever happens to come first. It is reported whether or not it matches an
// entry: "the configured machine is not in the tailnet at all" is precisely
// the state the user most needs to see, and it can only be told by name. Empty
// means no peer is configured.
type peersResponse struct {
	Peers          []PeerDTO `json:"peers"`
	ConfiguredPeer string    `json:"configuredPeer"`
}

// handlePeers answers with the tailnet members and names the configured one.
func (s *server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if s.deps.Peers == nil {
		writeError(w, http.StatusServiceUnavailable, msgPeersFailed)
		return
	}
	peers, err := s.deps.Peers.List(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, msgPeersFailed)
		return
	}
	writeJSON(w, http.StatusOK, peersResponse{
		Peers: toPeers(peers), ConfiguredPeer: s.deps.DefaultPeer,
	})
}
