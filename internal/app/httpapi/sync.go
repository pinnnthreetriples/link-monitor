package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// SyncMovedDTO is one file the shared folder carried between the machines.
// Size is preformatted for a Russian reader, as every size on the wire is.
type SyncMovedDTO struct {
	Name string `json:"name"`
	Size string `json:"size"`
	// Way is "to_peer" or "to_us" — foldersync's own tokens, so the UI does
	// not have to guess which end a file came from.
	Way string `json:"way"`
	// Conflict marks a copy written *beside* the local file because both
	// machines had changed it. Saved is the name it landed under.
	Conflict bool   `json:"conflict"`
	Saved    string `json:"saved"`
}

// SyncSkippedDTO is one file the pass left alone. Why is the machine-readable
// token and Reason is the sentence the window shows.
type SyncSkippedDTO struct {
	Name   string `json:"name"`
	Size   string `json:"size"`
	Why    string `json:"why"`
	Reason string `json:"reason"`
}

// syncResponse is the body of GET /api/sync.
//
// At is empty until a pass has finished, which is how the UI tells "never run"
// from "ran and found nothing" without a second flag.
type syncResponse struct {
	On          bool   `json:"on"`
	Folder      string `json:"folder"`
	PeerFolder  string `json:"peerFolder"`
	Peer        string `json:"peer"`
	MaxSize     string `json:"maxSize"`
	Running     bool   `json:"running"`
	HistoryLost bool   `json:"historyLost"`
	At          string `json:"at"`
	TookMs      int64  `json:"tookMs"`
	// Failed says the last pass could not finish, so Message is about
	// something going wrong rather than a tally. It is a field rather than
	// something the UI works out from the sentence: a window that classified
	// the pass by reading Russian prose would be one sentence away from
	// drawing a working sync in red.
	Failed  bool             `json:"failed"`
	Moved   []SyncMovedDTO   `json:"moved"`
	Skipped []SyncSkippedDTO `json:"skipped"`
	Message string           `json:"message"`
	// Note is the guarantee rule 1 asks to be said out loud, so that a file
	// coming back after a deletion surprises nobody. It is served rather than
	// written into the page because the sentence and the behaviour it promises
	// belong together.
	Note string `json:"note"`
}

// handleSyncStatus answers with the shared folder's state.
func (s *server) handleSyncStatus(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Sync == nil {
		writeJSON(w, http.StatusOK, syncOff())
		return
	}
	writeJSON(w, http.StatusOK, toSync(s.deps.Sync.Status()))
}

// handleSyncRun asks for a pass now. It answers at once rather than waiting:
// a pass over a large folder takes minutes, and a request that hangs for
// minutes is a request the window has already given up on.
func (s *server) handleSyncRun(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Sync == nil || !s.deps.Sync.On() {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgSyncOff})
		return
	}
	s.deps.Sync.SyncNow()
	writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgSyncStarted})
}

// syncOff is the answer when no folder was chosen, or the feature was never
// wired up. Off is the default and is not a fault.
func syncOff() syncResponse {
	return syncResponse{
		Moved:   []SyncMovedDTO{},
		Skipped: []SyncSkippedDTO{},
		Message: msgSyncOff,
		Note:    msgSyncNoDeletes,
	}
}

// toSync renders the shared folder's state for the wire.
func toSync(st app.SyncStatus) syncResponse {
	if !st.On {
		return syncOff()
	}
	out := syncResponse{
		On:          true,
		Folder:      st.Folder,
		PeerFolder:  st.PeerFolder,
		Peer:        st.PeerName,
		MaxSize:     humanSize(st.MaxBytes),
		Running:     st.Running,
		HistoryLost: st.HistoryLost,
		TookMs:      st.Took.Milliseconds(),
		Failed:      st.Err != "",
		Moved:       toSyncMoved(st.Moved),
		Skipped:     toSyncSkipped(st.Skipped),
		Note:        msgSyncNoDeletes,
	}
	if st.HasRun {
		out.At = st.At.Format(time.RFC3339)
	}
	out.Message = syncMessage(st)
	return out
}

// toSyncMoved renders what moved.
func toSyncMoved(items []app.SyncMoved) []SyncMovedDTO {
	out := make([]SyncMovedDTO, 0, len(items))
	for _, m := range items {
		out = append(out, SyncMovedDTO{
			Name:     m.Name,
			Size:     humanSize(m.Size),
			Way:      string(m.Way),
			Conflict: m.Conflict,
			Saved:    m.Saved,
		})
	}
	return out
}

// toSyncSkipped renders what was left alone, each with its reason in Russian.
func toSyncSkipped(items []app.SyncSkipped) []SyncSkippedDTO {
	out := make([]SyncSkippedDTO, 0, len(items))
	for _, sk := range items {
		out = append(out, SyncSkippedDTO{
			Name:   sk.Name,
			Size:   humanSize(sk.Size),
			Why:    string(sk.Why),
			Reason: skipReason(sk.Why),
		})
	}
	return out
}

// skipReason is why one file was left alone, in the words the user reads.
//
// An unrecognised token gets the honest fallback rather than nothing: a reason
// this function has no wording for is a gap to fix, and a blank line in the
// window would hide it.
func skipReason(why foldersync.Why) string {
	switch why {
	case foldersync.WhyTooBig:
		return msgSkipTooBig
	case foldersync.WhyBusy:
		return msgSkipBusy
	case foldersync.WhyUnresolved:
		return msgSkipUnresolved
	case foldersync.WhyUnsafeName:
		return msgSkipUnsafeName
	default:
		return msgSkipUnknown
	}
}

// syncMessage is the one sentence at the top of the section.
//
// A conflict comes before everything else on purpose: rule 6 says conflicts
// are the one thing that must be impossible to miss, and burying them under
// «Перенесено файлов: 12» is exactly how they would be missed.
func syncMessage(st app.SyncStatus) string {
	if st.Err != "" {
		return st.Err
	}
	if !st.HasRun {
		return msgSyncNotYet
	}
	conflicts := 0
	for _, m := range st.Moved {
		if m.Conflict {
			conflicts++
		}
	}
	switch {
	case conflicts > 0:
		return fmt.Sprintf(msgSyncConflictsFmt, conflicts)
	case len(st.Moved) > 0:
		return fmt.Sprintf(msgSyncMovedFmt, len(st.Moved))
	case len(st.Skipped) > 0:
		return fmt.Sprintf(msgSyncSkippedFmt, len(st.Skipped))
	default:
		return msgSyncSame
	}
}
