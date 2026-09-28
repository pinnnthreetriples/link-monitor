package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// UploadService sends the bytes submitted by the browser, not a guessed path.
type UploadService interface {
	Upload(context.Context, string, io.Reader, string) error
}

func (s *server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Uploads == nil {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{Message: msgSendFailed})
		return
	}
	peer := r.URL.Query().Get("peer")
	if peer == "" {
		peer = s.deps.DefaultPeer
	}
	if peer == "" {
		writeError(w, http.StatusBadRequest, msgNeedPeer)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, core.MaxUploadBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Ожидается файл для отправки.")
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		writeError(w, http.StatusBadRequest, "Выберите файл для отправки.")
		return
	}
	defer func() { _ = part.Close() }()
	// LimitReader prevents an oversized file from reaching the transport even
	// when the multipart envelope fits inside the HTTP limit.
	err = s.deps.Uploads.Upload(r.Context(), part.FileName(), part, peer)
	var tooLarge *http.MaxBytesError
	if errors.Is(err, core.ErrUploadTooLarge) || errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "Файл слишком большой (максимум 100 МБ).")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, msgSendFailed)
		return
	}
	writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgSendOK})
}
