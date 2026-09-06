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
	"frontend":     true,
}

// TestFileLengths keeps Go files small enough to hold in one's head. A file that
// outgrows the limit wants splitting; raising the limit is not the fix.
func TestFileLengths(t *testing.T) {
	root := repoRoot(t)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		limit := maxSourceLines
		if strings.HasSuffix(path, "_test.go") {
			limit = maxTestLines
		}

		n, countErr := countLines(path)
		if countErr != nil {
			return countErr
		}
		if n > limit {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			t.Errorf("%s is %d lines, limit is %d — split it", rel, n, limit)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
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
