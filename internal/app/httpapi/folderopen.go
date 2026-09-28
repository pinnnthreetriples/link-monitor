package httpapi

import (
	"context"
	"net/http"
	"strings"
)

// FolderOpener opens only the configured shared folder, never a client-supplied path.
type FolderOpener interface {
	SharedFolder() string
	OpenSharedFolder(context.Context) error
}

func (s *server) handleFolderOpen(w http.ResponseWriter, r *http.Request) {
	folder := s.deps.FolderOpener
	if folder == nil || strings.TrimSpace(folder.SharedFolder()) == "" {
		writeJSON(w, http.StatusServiceUnavailable, okMessage{
			Message: "Общая папка не настроена. Укажите её при запуске приложения.",
		})
		return
	}
	if err := folder.OpenSharedFolder(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, okMessage{
			Message: "Не удалось открыть общую папку в Проводнике.",
		})
		return
	}
	writeJSON(w, http.StatusOK, okMessage{OK: true, Message: "Общая папка открыта."})
}
