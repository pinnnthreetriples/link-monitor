package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
)

// maxPort is the highest TCP port there is.
const maxPort = 65535

// forwardRequest is the body of POST /api/forward.
type forwardRequest struct {
	LocalPort  int    `json:"localPort"`
	RemoteHost string `json:"remoteHost"`
	RemotePort int    `json:"remotePort"`
}

// forwardResponse is its answer. Addr is what the tunnel actually listens on,
// which is how a caller that asked for port 0 learns the port it got.
type forwardResponse struct {
	OK      bool   `json:"ok"`
	ID      string `json:"id"`
	Addr    string `json:"addr"`
	Message string `json:"message"`
}

// handleForwardStart opens an ssh -L style tunnel. The tunnel outlives this
// request: the service holds the process lifetime it is opened against.
func (s *server) handleForwardStart(w http.ResponseWriter, r *http.Request) {
	var req forwardRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RemoteHost == "" {
		writeJSON(w, http.StatusBadRequest, forwardResponse{Message: msgNeedRemoteHost})
		return
	}
	if req.LocalPort < 0 || req.LocalPort > maxPort {
		writeJSON(w, http.StatusBadRequest, forwardResponse{Message: msgBadLocalPort})
		return
	}
	if req.RemotePort < 1 || req.RemotePort > maxPort {
		writeJSON(w, http.StatusBadRequest, forwardResponse{Message: msgBadPort})
		return
	}
	if s.deps.Forwards == nil {
		writeJSON(w, http.StatusServiceUnavailable, forwardResponse{Message: msgForwardFailed})
		return
	}

	fwd, err := s.deps.Forwards.Start(r.Context(), req.LocalPort, req.RemoteHost, req.RemotePort)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, forwardResponse{Message: msgForwardFailed})
		return
	}
	writeJSON(w, http.StatusOK, forwardResponse{
		OK:      true,
		ID:      fwd.ID,
		Addr:    fwd.Addr,
		Message: fmt.Sprintf(msgForwardOKFmt, fwd.Addr, fwd.RemoteHost, fwd.RemotePort),
	})
}

// stopRequest is the body of DELETE /api/forward.
type stopRequest struct {
	ID string `json:"id"`
}

// handleForwardStop closes one tunnel.
func (s *server) handleForwardStop(w http.ResponseWriter, r *http.Request) {
	var req stopRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ID == "" {
		writeJSON(w, http.StatusBadRequest, okMessage{Message: msgNeedForwardID})
		return
	}
	if s.deps.Forwards == nil {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgForwardNotFound})
		return
	}

	err := s.deps.Forwards.Stop(req.ID)
	switch {
	case errors.Is(err, app.ErrForwardNotFound):
		writeJSON(w, http.StatusNotFound, okMessage{Message: msgForwardNotFound})
	case err != nil:
		// The forward is out of the registry either way; only its socket
		// complained on the way out, so the user is told what actually happened.
		writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgForwardStopBad})
	default:
		writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgForwardStopped})
	}
}

// forwardsResponse is the body of GET /api/forward.
type forwardsResponse struct {
	Forwards []ForwardDTO `json:"forwards"`
}

// handleForwardList answers with the live tunnels.
func (s *server) handleForwardList(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Forwards == nil {
		writeJSON(w, http.StatusOK, forwardsResponse{Forwards: []ForwardDTO{}})
		return
	}
	writeJSON(w, http.StatusOK, forwardsResponse{Forwards: toForwards(s.deps.Forwards.List())})
}

// serveRequest is the body of POST /api/serve.
type serveRequest struct {
	Port int `json:"port"`
}

// serveResponse is its answer.
type serveResponse struct {
	OK      bool   `json:"ok"`
	URL     string `json:"url"`
	Message string `json:"message"`
}

// handleServe publishes a local port to the tailnet over HTTPS. Unlike a
// forward this is the Tailscale daemon's job and survives this process.
func (s *server) handleServe(w http.ResponseWriter, r *http.Request) {
	var req serveRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Port < 1 || req.Port > maxPort {
		writeJSON(w, http.StatusBadRequest, serveResponse{Message: msgBadPort})
		return
	}
	if s.deps.Forwards == nil {
		writeJSON(w, http.StatusServiceUnavailable, serveResponse{Message: msgServeFailed})
		return
	}

	url, err := s.deps.Forwards.Serve(r.Context(), req.Port)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, serveResponse{Message: msgServeFailed})
		return
	}
	writeJSON(w, http.StatusOK, serveResponse{
		OK: true, URL: url, Message: fmt.Sprintf(msgServeOKFmt, req.Port),
	})
}
