// Package gates holds repository-wide guardrails that run as ordinary tests, so
// a violation fails `go test ./...` locally and in CI without a separate step.
package gates

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	maxSourceLines = 400
	maxTestLines   = 700
)

// skipDirs are trees we do not own or do not want to police.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"build":        true,
	"dist":         true,
}

// skipPaths are trees excluded by where they sit rather than by their name.
// frontend/fonts holds woff2 binaries: there are no lines in them to count, and
// counting bytes as if there were would be theatre.
var skipPaths = map[string]bool{
	"frontend/fonts": true,
}

// frontendExts are the front-end sources the rule covers. The rule says
// "source file", not "Go file", and a stylesheet or a tab module outgrows what
// fits in one's head exactly the way a package does.
var frontendExts = map[string]bool{
	".js":   true,
	".css":  true,
	".html": true,
}

// TestFileLengths keeps source files small enough to hold in one's head. A file
// that outgrows the limit wants splitting; raising the limit is not the fix.
func TestFileLengths(t *testing.T) {
	root := repoRoot(t)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || skipPaths[filepath.ToSlash(rel)] {
				return filepath.SkipDir
			}
			return nil
		}

		limit := limitFor(rel)
		if limit == 0 {
			return nil
		}

		n, countErr := countLines(path)
		if countErr != nil {
			return countErr
		}
		if n > limit {
			t.Errorf("%s is %d lines, limit is %d — split it", rel, n, limit)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
}

// limitFor returns the line limit for one file, or 0 for a file the size rule
// says nothing about. rel is the path relative to the repository root.
func limitFor(rel string) int {
	switch {
	case strings.HasSuffix(rel, "_test.go"):
		return maxTestLines
	case strings.HasSuffix(rel, ".go"):
		return maxSourceLines
	case isFrontend(rel) && frontendExts[filepath.Ext(rel)]:
		return maxSourceLines
	}
	return 0
}

// isFrontend answers whether a file belongs to the window's own sources, which
// is where the front-end extensions are policed and nowhere else.
func isFrontend(rel string) bool {
	return strings.HasPrefix(filepath.ToSlash(rel), "frontend/")
}

func countLines(path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from our own walk
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	n := 0
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		n++
	}
	return n, s.Err()
}

// repoRoot climbs until it finds go.mod, so the test does not care where it runs.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}
