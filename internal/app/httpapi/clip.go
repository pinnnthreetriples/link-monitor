package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// maxClipBodyBytes is the body limit for the receiving route alone.
//
// It is larger than [maxBodyBytes] because this is the one route whose body is
// not a handful of fields: it carries a clipboard item, and a 64 KiB item can
// become several hundred kilobytes once JSON has escaped every quote and every
// newline in it. It is still a limit, and still smaller than any item the
// sending side would produce.
const maxClipBodyBytes = 1 << 20

// ClipEventDTO is one line of the shared clipboard's list: when something
// happened, what it was, how big it was, and the sentence the window shows.
//
// Kind is the machine-readable token and Label the Russian; Size is
// preformatted, as every size on this wire is. There is deliberately no field
// for the content, and there never may be — see tools/gates.
type ClipEventDTO struct {
	At    string `json:"at"`
	Kind  string `json:"kind"`
	Size  string `json:"size"`
	Label string `json:"label"`
}

// ClipCountsDTO is the tally rules 2 and 3 ask to be visible: what travelled,
// and what was deliberately left behind.
type ClipCountsDTO struct {
	Sent     int `json:"sent"`
	Received int `json:"received"`
	TooBig   int `json:"tooBig"`
	Marked   int `json:"marked"`
	NotText  int `json:"notText"`
	Failed   int `json:"failed"`
}

// clipResponse is the body of GET /api/clip.
//
// Available and On are separate answers to separate questions: a machine that
// cannot share its clipboard at all is not the same as one where the user has
// not switched sharing on, and a window that conflated them would offer a
// switch that does nothing.
type clipResponse struct {
	Available bool   `json:"available"`
	On        bool   `json:"on"`
	Peer      string `json:"peer"`
	MaxSize   string `json:"maxSize"`
	// PeerMissing says the program is not running on the other machine. It is
	// a normal state and not a fault, which is why it is its own field rather
	// than a failure.
	PeerMissing bool `json:"peerMissing"`
	// Failed says something actually went wrong, so the window can colour the
	// message without reading the Russian in it.
	Failed  bool           `json:"failed"`
	Counts  ClipCountsDTO  `json:"counts"`
	Events  []ClipEventDTO `json:"events"`
	Message string         `json:"message"`
	// Note is the standing guarantee: text and PNG, only while it is on, and
	// nothing written down anywhere. It is served rather than written into the
	// page because the sentence and the behaviour it promises belong together.
	Note string `json:"note"`
}

// handleClipStatus answers with the shared clipboard's state.
func (s *server) handleClipStatus(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Clip == nil {
		writeJSON(w, http.StatusOK, clipUnavailable())
		return
	}
	writeJSON(w, http.StatusOK, toClip(s.deps.Clip.Status()))
}

// handleClipOn resumes sharing after a manual pause. Startup activation uses
// the same Clip service without an HTTP request.
func (s *server) handleClipOn(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Clip == nil {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgClipNoClipboard})
		return
	}
	if err := s.deps.Clip.TurnOn(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgClipNoClipboard})
		return
	}
	writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgClipTurnedOn})
}

// handleClipOff switches sharing off. Rule 5 asks for one action that cannot
// fail, so this answers OK even when there was nothing to switch off.
func (s *server) handleClipOff(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Clip != nil {
		s.deps.Clip.TurnOff()
	}
	writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgClipTurnedOff})
}

// handleClipReceive is the route the *peer's* instance posts an item to,
// through a port forwarded over the SSH session that program already holds.
//
// # What protects it
//
// Not a token. The API has no authentication by design — see this package's
// own comment: the listener binds 127.0.0.1, so only a process already running
// as this user can reach it, and such a process could read this user's files
// and keys anyway. What is defended against is the attacker loopback does not
// exclude, a web page in the user's browser, and [guardOrigin] does that for
// every route here.
//
// Two things specific to this route are worth writing down. It is refused
// outright while sharing is paused, so a machine whose user paused it cannot
// have its clipboard written by anything that finds this port. And the
// capability it grants, to a local process that is already
// running as this user, is *setting the clipboard* — which any such process can
// already do by calling SetClipboardData directly, with less effort and no
// dependency on this program. It grants no read: nothing here, and nothing in
// GET /api/clip, ever answers with what is on the clipboard.
func (s *server) handleClipReceive(w http.ResponseWriter, r *http.Request) {
	if s.deps.Clip == nil {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgClipNoClipboard})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeClipBody(w, r, &body) {
		return
	}

	item := []byte(body.Text)
	// The decoded string cannot be overwritten — strings are immutable, and
	// net/http has already buffered the request besides — but the copy this
	// package makes can be, and is.
	defer clipshare.Zero(item)

	switch err := s.deps.Clip.Receive(item); {
	case err == nil:
		writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgClipReceived})
	case errors.Is(err, app.ErrClipOff):
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgClipIsOff})
	case errors.Is(err, app.ErrClipTooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, okMessage{Message: msgClipArrivedTooBig})
	default:
		writeJSON(w, http.StatusInternalServerError, okMessage{Message: msgClipPutFailed})
	}
}

// decodeClipBody is [decodeBody] with this route's own, larger limit. It is
// written out rather than parameterised so that the ordinary limit stays a
// constant nobody has to think about, and so that the one route with a bigger
// body is the one route that says so.
func decodeClipBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxClipBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return true
	case isTooLarge(err):
		writeError(w, http.StatusRequestEntityTooLarge, msgClipArrivedTooBig)
		return false
	default:
		writeError(w, http.StatusBadRequest, msgBadJSON)
		return false
	}
}

// clipUnavailable is the answer when there is no clipboard to share — a build
// or a machine that cannot reach one — as opposed to one switched off.
func clipUnavailable() clipResponse {
	return clipResponse{
		Events:  []ClipEventDTO{},
		Message: msgClipNoClipboard,
		Note:    msgClipNote,
	}
}

// toClip renders the shared clipboard's state for the wire.
func toClip(st app.ClipStatus) clipResponse {
	if !st.Available {
		return clipUnavailable()
	}
	return clipResponse{
		Available:   true,
		On:          st.On,
		Peer:        st.PeerName,
		MaxSize:     humanSize(int64(st.MaxBytes)),
		PeerMissing: st.On && st.PeerMissing,
		Failed:      st.On && st.Err != "",
		Counts: ClipCountsDTO{
			Sent:     st.Counts.Sent,
			Received: st.Counts.Received,
			TooBig:   st.Counts.TooBig,
			Marked:   st.Counts.Marked,
			NotText:  st.Counts.NotText,
			Failed:   st.Counts.Failed,
		},
		Events:  toClipEvents(st.Events),
		Message: clipMessage(st),
		Note:    msgClipNote,
	}
}

// toClipEvents renders the recent items, newest first — which is the order a
// person reads a list of "what just happened" in.
func toClipEvents(items []app.ClipEvent) []ClipEventDTO {
	out := make([]ClipEventDTO, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		out = append(out, ClipEventDTO{
			At:    items[i].At.Format(time.RFC3339),
			Kind:  string(items[i].Kind),
			Size:  clipEventSize(items[i]),
			Label: clipEventLabel(items[i].Kind),
		})
	}
	return out
}

// clipEventSize is the size to show for one line, and neither refusal gets
// one.
//
// An item Windows was asked not to record is never measured, so there is no
// size to show and showing one would be reporting something about it after
// all. An item past the cap is never read out either: the reader stops as soon
// as it knows the item is too large, so what it can report is a lower bound
// and not a size. A lower bound printed in the window would simply be a wrong
// number — 130 Б beside an item of 400 — and the line already says what
// happened and the section already says what the cap is.
func clipEventSize(e app.ClipEvent) string {
	if e.Kind == app.ClipMarked || e.Kind == app.ClipTooBig || e.Bytes <= 0 {
		return ""
	}
	return humanSize(int64(e.Bytes))
}

// clipEventLabel is the sentence beside one line.
//
// An unrecognised kind gets an honest fallback rather than a blank: a kind the
// window has no wording for is a gap to fix, and an empty line would hide it.
func clipEventLabel(kind app.ClipEventKind) string {
	switch kind {
	case app.ClipSent:
		return msgClipEventSent
	case app.ClipReceived:
		return msgClipEventReceived
	case app.ClipTooBig:
		return msgClipEventTooBig
	case app.ClipMarked:
		return msgClipEventMarked
	case app.ClipFailed:
		return msgClipEventFailed
	default:
		return msgClipEventUnknown
	}
}

// clipMessage is the one sentence at the top of the section.
//
// The order is what the user needs to hear first. Off comes before everything,
// because off is the answer to every other question. The peer's program not
// running comes next, because until it is running nothing can arrive and
// nothing can leave, and that is not a fault anybody should be hunting for. A
// real failure comes after those, and the tally last.
func clipMessage(st app.ClipStatus) string {
	switch {
	case !st.On:
		return msgClipOff
	case st.PeerMissing:
		return fmt.Sprintf(msgClipPeerMissingFmt, st.PeerName)
	case st.Err != "":
		return st.Err
	case st.Counts.Sent == 0 && st.Counts.Received == 0:
		return msgClipOnNothingYet
	default:
		return fmt.Sprintf(msgClipCountsFmt, st.Counts.Sent, st.Counts.Received)
	}
}
