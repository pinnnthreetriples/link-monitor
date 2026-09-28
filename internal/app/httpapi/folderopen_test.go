package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenSharedFolderWithoutConfiguration(t *testing.T) {
	r := do(t, New(Deps{}), http.MethodPost, "/api/sync/open", "")
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured folder returned %d, want 503", r.Code)
	}
}

type folderOpenFake struct {
	path  string
	err   error
	calls int
}

func (f *folderOpenFake) SharedFolder() string { return f.path }
func (f *folderOpenFake) OpenSharedFolder(context.Context) error {
	f.calls++
	return f.err
}

func TestOpenSharedFolderUsesConfiguredAction(t *testing.T) {
	f := &folderOpenFake{path: `C:\Shared`}
	h := New(Deps{FolderOpener: f})
	r := do(t, h, http.MethodPost, "/api/sync/open", `{"path":"C:\\Other"}`)
	if r.Code != http.StatusOK || f.calls != 1 {
		t.Fatalf("open: status=%d calls=%d", r.Code, f.calls)
	}
	f.err = errors.New("private details")
	r = do(t, h, http.MethodPost, "/api/sync/open", "")
	if r.Code != http.StatusInternalServerError || strings.Contains(r.Body.String(), "private details") {
		t.Fatalf("failed open exposed details or returned wrong status: %s", r.Body.String())
	}
}

func TestOpenSharedFolderRejectsForeignOriginAndGet(t *testing.T) {
	f := &folderOpenFake{path: `C:\Shared`}
	h := New(Deps{FolderOpener: f})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/open", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || f.calls != 0 {
		t.Fatalf("foreign origin: status=%d calls=%d", w.Code, f.calls)
	}
	w = do(t, h, http.MethodGet, "/api/sync/open", "")
	if w.Code != http.StatusMethodNotAllowed || f.calls != 0 {
		t.Fatalf("GET: status=%d calls=%d", w.Code, f.calls)
	}
}
