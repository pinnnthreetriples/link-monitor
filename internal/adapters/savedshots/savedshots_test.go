package savedshots

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

func writeOldPNG(t *testing.T, dir, name string, contents []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestSourceIgnoresOldFilesAndReadsNewestNewScreenshot(t *testing.T) {
	dir := t.TempDir()
	writeOldPNG(t, dir, "before.png", []byte("old"))
	s := New(dir)
	if err := s.Baseline(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Next(); err != nil || ok {
		t.Fatalf("old screenshot visible: ok=%t err=%v", ok, err)
	}
	writeOldPNG(t, dir, "first.png", []byte("first"))
	writeOldPNG(t, dir, "second.png", []byte("second"))
	shot, ok, err := s.Next()
	if err != nil || !ok || shot.ID != "second.png" {
		t.Fatalf("newest screenshot: %+v ok=%t err=%v", shot, ok, err)
	}
	data, err := s.Read(shot)
	if err != nil || !bytes.Equal(data, []byte("second")) {
		t.Fatalf("read newest screenshot: %q, %v", data, err)
	}
	s.Mark(shot)
	if _, ok, err := s.Next(); err != nil || ok {
		t.Fatalf("marked screenshots reappeared: ok=%t err=%v", ok, err)
	}
}

func TestSourceRejectsPathOutsideScreenshotsFolder(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Read(clipshare.SavedScreenshot{ID: "../secret.png"}); err == nil {
		t.Fatal("screenshot source read outside its folder")
	}
}

func TestSourceBaselinesBeforeFirstScan(t *testing.T) {
	dir := t.TempDir()
	writeOldPNG(t, dir, "already-there.png", []byte("old"))
	s := New(dir)
	if _, ok, err := s.Next(); err != nil || ok {
		t.Fatalf("screenshot existing before first scan was sent: ok=%t err=%v", ok, err)
	}
	writeOldPNG(t, dir, "new.png", []byte("new"))
	if shot, ok, err := s.Next(); err != nil || !ok || shot.ID != "new.png" {
		t.Fatalf("new screenshot after baseline: %+v ok=%t err=%v", shot, ok, err)
	}
}
