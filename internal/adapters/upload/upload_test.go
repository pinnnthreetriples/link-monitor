package upload

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

type fakeSender struct {
	path, peer, body string
	err              error
}

func (s *fakeSender) SendFile(_ context.Context, path, peer string) error {
	s.path, s.peer = path, peer
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s.body = string(b)
	return s.err
}

func TestUploadSendsActualBytesAndRemovesStaging(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			s := &fakeSender{}
			if fail {
				s.err = errors.New("sender failed")
			}
			n, err := New(s).SendUpload(t.Context(), "снимок экрана.png", strings.NewReader("image bytes"), "peer")
			if !errors.Is(err, s.err) || n != 11 {
				t.Fatalf("SendUpload = %d, %v", n, err)
			}
			if filepath.Base(s.path) != "снимок экрана.png" || s.peer != "peer" || s.body != "image bytes" {
				t.Fatalf("sender = %+v", s)
			}
			if _, err := os.Stat(filepath.Dir(s.path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("staging directory remains: %v", err)
			}
		})
	}
}

func TestUploadRejectsUnsafeNamesBeforeSending(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../a", `..\a`, `C:\a`, "a:b", "a.", "CON", "nul.txt"} {
		t.Run(name, func(t *testing.T) {
			s := &fakeSender{}
			if _, err := New(s).SendUpload(t.Context(), name, strings.NewReader("x"), "peer"); err == nil {
				t.Fatal("unsafe name accepted")
			}
			if s.path != "" {
				t.Fatal("invalid upload was sent")
			}
		})
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestUploadNeverSendsIncompleteOrCancelledInput(t *testing.T) {
	s := &fakeSender{}
	u := New(s)
	u.tempRoot = t.TempDir()
	if _, err := u.SendUpload(t.Context(), "file.png", brokenReader{}, "peer"); err == nil {
		t.Fatal("broken upload accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := u.SendUpload(ctx, "file.png", strings.NewReader("x"), "peer"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upload: %v", err)
	}
	entries, err := os.ReadDir(u.tempRoot)
	if err != nil || len(entries) != 0 || s.path != "" {
		t.Fatalf("staging or send leaked: %v, %v", entries, err)
	}
}

func TestStageRejectsOversizeBeforePeerSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.png")
	n, err := stage(t.Context(), path, strings.NewReader("12345"), 3)
	if !errors.Is(err, core.ErrUploadTooLarge) || n != 4 {
		t.Fatalf("stage = %d, %v", n, err)
	}
}
