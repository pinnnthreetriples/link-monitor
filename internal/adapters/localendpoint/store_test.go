package localendpoint

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOsFilesWritesReadsAndRemoves(t *testing.T) {
	t.Parallel()

	f := osFiles{}
	dir := filepath.Join(t.TempDir(), dirName)
	path := filepath.Join(dir, fileName)

	if err := f.mkdirAll(dir); err != nil {
		t.Fatalf("mkdirAll(%q) = %v", dir, err)
	}
	// Creating a directory that is already there must not be a failure: the
	// instance publishes on every start, not only on the first one.
	if err := f.mkdirAll(dir); err != nil {
		t.Fatalf("mkdirAll(%q) a second time = %v", dir, err)
	}

	if err := f.writeFile(path, []byte("first")); err != nil {
		t.Fatalf("writeFile() = %v", err)
	}
	if err := f.writeFile(path, []byte("second")); err != nil {
		t.Fatalf("writeFile() over an existing record = %v", err)
	}

	got, err := f.readFile(path)
	if err != nil {
		t.Fatalf("readFile() = %v", err)
	}
	if string(got) != "second" {
		t.Errorf("readFile() = %q, want the second write", got)
	}

	// The rename is what makes the write atomic, and a rename that left its
	// temporary file behind would litter %LOCALAPPDATA% once per start.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %q: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("writeFile left %q behind", e.Name())
		}
	}

	if err := f.remove(path); err != nil {
		t.Fatalf("remove() = %v", err)
	}
	// Removing what is not there is how shutdown behaves after a failed
	// publish, and it must not report a failure.
	if err := f.remove(path); err != nil {
		t.Fatalf("remove() a second time = %v", err)
	}
}

func TestOsFilesReportsAnAbsentRecordAsAbsent(t *testing.T) {
	t.Parallel()

	_, err := osFiles{}.readFile(filepath.Join(t.TempDir(), fileName))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("readFile() = %v, want an error satisfying fs.ErrNotExist", err)
	}
}

func TestOsFilesReportsAWriteWithNowhereToGo(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "not-created", fileName)
	if err := (osFiles{}).writeFile(missing, []byte("x")); err == nil {
		t.Fatal("writeFile() into a directory that does not exist = nil, want a failure")
	}
}
