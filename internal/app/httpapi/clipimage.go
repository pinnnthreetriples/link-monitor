package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// One request body at a time bounds memory even when many local clients post images.
var clipImageSlot = make(chan struct{}, 1)

func (s *server) handleClipImage(w http.ResponseWriter, r *http.Request) {
	receiver, ok := s.deps.Clip.(interface{ ReceiveImage([]byte) error })
	if !ok || !s.deps.Clip.On() {
		writeError(w, http.StatusServiceUnavailable, msgClipIsOff)
		return
	}
	if r.Header.Get("Content-Type") != "image/png" {
		writeError(w, http.StatusUnsupportedMediaType, "Ожидается изображение PNG.")
		return
	}
	select {
	case clipImageSlot <- struct{}{}:
		defer func() { <-clipImageSlot }()
	default:
		writeError(w, http.StatusTooManyRequests, "Предыдущее изображение ещё принимается.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, clipshare.MaxImageBytes)
	text, err := io.ReadAll(r.Body)
	defer clipshare.Zero(text)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, msgClipArrivedTooBig)
		return
	}
	switch err = receiver.ReceiveImage(text); {
	case err == nil:
		writeJSON(w, http.StatusOK, okMessage{OK: true, Message: msgClipReceived})
	case errors.Is(err, app.ErrClipOff), errors.Is(err, app.ErrClipUnavailable):
		writeError(w, http.StatusServiceUnavailable, msgClipIsOff)
	case errors.Is(err, app.ErrClipTooBig):
		writeError(w, http.StatusRequestEntityTooLarge, msgClipArrivedTooBig)
	default:
		writeError(w, http.StatusBadRequest, "Изображение повреждено или формат не поддерживается.")
	}
}
