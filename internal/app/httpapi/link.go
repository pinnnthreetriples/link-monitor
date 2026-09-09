package httpapi

import (
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
)

// linkRequest is the body of POST /api/link.
type linkRequest struct {
	Action string `json:"action"`
}

// linkResponse is its answer. LoginURL is present only when the daemon wants an
// interactive browser login, which is the one case the UI can act on itself by
// offering to open it.
type linkResponse struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	LoginURL   string `json:"loginURL"`
	NeedsAdmin bool   `json:"needsAdmin"`
}

// handleLink connects or disconnects the tailnet.
//
// Pressing a button twice is harmless: an action the link is already in comes
// back OK with a message that says so. Only a request this API cannot make
// sense of is an HTTP error — an action that ran and failed is 200 with ok
// false and a sentence to read, the same shape as /api/fix.
func (s *server) handleLink(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if !decodeBody(w, r, &req) {
		return
	}

	action, ok := app.ParseLinkAction(req.Action)
	if !ok {
		writeJSON(w, http.StatusBadRequest, linkResponse{Message: msgBadLinkAction})
		return
	}
	if s.deps.Link == nil {
		writeJSON(w, http.StatusServiceUnavailable, linkResponse{Message: msgLinkUnavailable})
		return
	}

	out := s.deps.Link.Apply(r.Context(), action)
	writeJSON(w, http.StatusOK, linkResponse{
		OK: out.OK, Message: out.Message, LoginURL: out.LoginURL, NeedsAdmin: out.NeedsAdmin,
	})
}
