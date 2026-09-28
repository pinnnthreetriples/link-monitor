// Package savedshots reads new PNGs from Windows' Screenshots folder when a
// capture was saved but Snipping Tool failed to copy it to the clipboard.
package savedshots

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

const settle = time.Second

type Source struct {
	dir       string
	seen      map[string]struct{}
	baselined bool
}

func New(dir string) *Source { return &Source{dir: dir} }

// Baseline ignores every screenshot already present when sharing starts.
func (s *Source) Baseline() error {
	s.baselined = false
	s.seen = make(map[string]struct{})
	if s.dir == "" {
		s.baselined = true
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		s.baselined = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading screenshots baseline: %w", err)
	}
	for _, e := range entries {
		if imageFile(e) {
			s.seen[e.Name()] = struct{}{}
		}
	}
	s.baselined = true
	return nil
}

// Next returns the newest new PNG after it has had time to finish writing.
func (s *Source) Next() (clipshare.SavedScreenshot, bool, error) {
	if !s.baselined {
		return clipshare.SavedScreenshot{}, false, s.Baseline()
	}
	if s.dir == "" {
		return clipshare.SavedScreenshot{}, false, nil
	}
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return clipshare.SavedScreenshot{}, false, nil
	}
	if err != nil {
		return clipshare.SavedScreenshot{}, false, fmt.Errorf("scanning screenshots: %w", err)
	}
	var newest clipshare.SavedScreenshot
	for _, e := range entries {
		if !imageFile(e) {
			continue
		}
		if _, seen := s.seen[e.Name()]; seen {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return clipshare.SavedScreenshot{}, false, fmt.Errorf("examining screenshot: %w", err)
		}
		newest.Names = append(newest.Names, e.Name())
		if newest.ID == "" || info.ModTime().After(newest.Modified) ||
			(info.ModTime().Equal(newest.Modified) && e.Name() > newest.ID) {
			newest.ID, newest.Modified, newest.Size = e.Name(), info.ModTime(), info.Size()
		}
	}
	if newest.ID == "" || time.Since(newest.Modified) < settle {
		return clipshare.SavedScreenshot{}, false, nil
	}
	return newest, true, nil
}

// Read takes at most one bounded PNG into memory and never writes a file.
func (s *Source) Read(shot clipshare.SavedScreenshot) ([]byte, error) {
	if shot.ID == "" || filepath.Base(shot.ID) != shot.ID {
		return nil, fmt.Errorf("reading screenshot: invalid file name")
	}
	f, err := os.Open(filepath.Join(s.dir, shot.ID))
	if err != nil {
		return nil, fmt.Errorf("opening screenshot: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, clipshare.MaxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading screenshot: %w", err)
	}
	return data, nil
}

func (s *Source) Mark(shot clipshare.SavedScreenshot) {
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	for _, name := range shot.Names {
		s.seen[name] = struct{}{}
	}
}

func imageFile(e os.DirEntry) bool {
	return !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".png")
}
