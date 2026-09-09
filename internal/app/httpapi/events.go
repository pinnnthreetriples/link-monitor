package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// heartbeat keeps an idle SSE stream from being collected by an intermediary.
// At the default poll interval a real event arrives more often than this, so in
// practice it only fires when the poller has stopped.
const heartbeat = 25 * time.Second

// handleEvents streams one `data:` line per new Status, as Server-Sent Events.
//
// The current status goes out immediately, so the UI can open the stream and
// draw from it alone rather than racing a separate GET /api/status.
//
// The handler returns when the client disconnects or when the poller stops, and
// it always drops its subscription on the way out: a stream that leaked one
// would keep a channel in the poller forever.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, msgPollerDown)
		return
	}
	if s.deps.Status == nil {
		writeError(w, http.StatusServiceUnavailable, msgPollerDown)
		return
	}

	updates, unsubscribe := s.deps.Status.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	// Not every deployment has a proxy in front, but the one that does must not
	// buffer this stream into uselessness.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sendEvent(w, flusher, s.currentStatus())

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case res, open := <-updates:
			if !open {
				return
			}
			sendEvent(w, flusher, toStatus(res, false))
		case <-ticker.C:
			// A comment line: valid SSE that no client turns into an event.
			writeAndFlush(w, flusher, ": ping\n\n")
		}
	}
}

// sendEvent writes one Status as an SSE data frame.
func sendEvent(w http.ResponseWriter, flusher http.Flusher, status Status) {
	// The value is a plain struct of strings, numbers and slices, so json cannot
	// refuse it; and a half-written stream has nobody left to report an error to.
	payload, err := json.Marshal(status)
	if err != nil {
		return
	}
	writeAndFlush(w, flusher, "data: "+string(payload)+"\n\n")
}

// writeAndFlush pushes one frame out to the client.
//
// A failed write means the client is gone, which the next loop turn learns from
// the request context anyway — there is nowhere to report it to.
func writeAndFlush(w http.ResponseWriter, flusher http.Flusher, frame string) {
	if _, err := w.Write([]byte(frame)); err != nil {
		return
	}
	flusher.Flush()
}
