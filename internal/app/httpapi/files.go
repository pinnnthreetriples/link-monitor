package httpapi

import (
	"fmt"
	"net/http"
	"path/filepath"
)

// sendRequest is the body of POST /api/files/send.
type sendRequest struct {
	Path string `json:"path"`
	Peer string `json:"peer"`
}

// okMessage is the shape shared by the routes that only report success or
// failure with a sentence.
type okMessage struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// handleFilesSend pushes one file to a peer over Taildrop. When the request
// names no peer, the configured one is used — with two machines in the tailnet
// that is what the user meant.
func (s *server) handleFilesSend(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, okMessage{Message: msgNeedPath})
		return
	}
	peer := req.Peer
	if peer == "" {
		peer = s.deps.DefaultPeer
	}
	if peer == "" {
		writeJSON(w, http.StatusBadRequest, okMessage{Message: msgNeedPeer})
		return
	}
	if s.deps.Files == nil {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgSendFailed})
		return
	}

	if err := s.deps.Files.Send(r.Context(), req.Path, peer); err != nil {
		writeJSON(w, http.StatusBadGateway, okMessage{Message: msgSendFailed})
		return
	}
	writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgSendOK})
}

// receiveResponse is the body of POST /api/files/receive. Files are base names,
// not full paths: where they landed is this machine's business, and the inbox
// path is not something to put on a screen.
type receiveResponse struct {
	OK      bool     `json:"ok"`
	Files   []string `json:"files"`
	Message string   `json:"message"`
}

// handleFilesReceive drains the Taildrop inbox. Files that landed before an
// error are still reported: they really did arrive.
func (s *server) handleFilesReceive(w http.ResponseWriter, r *http.Request) {
	if s.deps.Files == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			receiveResponse{Files: []string{}, Message: msgReceiveFailed})
		return
	}

	paths, err := s.deps.Files.Receive(r.Context())
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway,
			receiveResponse{Files: names, Message: msgReceiveFailed})
		return
	}

	message := msgReceiveEmpty
	if len(names) > 0 {
		message = fmt.Sprintf(msgReceiveOKFmt, len(names))
	}
	writeJSON(w, http.StatusOK, receiveResponse{OK: true, Files: names, Message: message})
}

// filesResponse is the body of GET /api/files.
type filesResponse struct {
	Transfers []TransferDTO `json:"transfers"`
}

// handleFilesList answers with the recent transfers, newest first.
func (s *server) handleFilesList(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Files == nil {
		writeJSON(w, http.StatusOK, filesResponse{Transfers: []TransferDTO{}})
		return
	}
	writeJSON(w, http.StatusOK, filesResponse{Transfers: toTransfers(s.deps.Files.List())})
}
