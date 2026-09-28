package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
)

type imageClipService struct {
	fakeClip
	images int
}

func (f *imageClipService) ReceiveImage([]byte) error { f.images++; return nil }

func TestImageRouteRequiresPNGAndHonorsOriginAndPause(t *testing.T) {
	f := &imageClipService{fakeClip: fakeClip{status: app.ClipStatus{On: true}}}
	h := New(Deps{Clip: f})
	for _, tc := range []struct {
		mime, origin string
		want         int
	}{
		{"image/png", "", 200},
		{"application/octet-stream", "", 415},
		{"image/png", "https://example.com", 403},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8731/api/clip/image",
			strings.NewReader("synthetic bytes"))
		r.Header.Set("Content-Type", tc.mime)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("status = %d, want %d", w.Code, tc.want)
		}
	}
	if f.images != 1 {
		t.Fatal("rejected image reached service")
	}
	f.status.On = false
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8731/api/clip/image",
		strings.NewReader("synthetic bytes"))
	r.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || f.images != 1 {
		t.Fatal("paused receiver accepted image")
	}
}
