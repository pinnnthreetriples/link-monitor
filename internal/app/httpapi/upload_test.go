package httpapi

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

type uploadFake struct{ name, peer, body string }

func (f *uploadFake) Upload(_ context.Context, name string, src io.Reader, peer string) error {
	f.name, f.peer = name, peer
	b, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	f.body = string(b)
	return nil
}

func TestFileUploadSendsSubmittedBytesToConfiguredPeer(t *testing.T) {
	f := &uploadFake{}
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	part, err := m.CreateFormFile("file", "screen.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("real screenshot bytes")); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Uploads: f, DefaultPeer: "work-pc"})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8731/api/files/upload", &body)
	req.Header.Set("Content-Type", m.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || f.name != "screen.png" || f.peer != "work-pc" ||
		f.body != "real screenshot bytes" {
		t.Fatalf("upload status=%d name=%q peer=%q bytes=%q", w.Code, f.name, f.peer, f.body)
	}
}
