package localendpoint

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// dirMode and fileMode are the modes the record and its directory are created
// with. On Windows the protection that actually applies is the ACL inherited
// from %LOCALAPPDATA% — this user, SYSTEM and Administrators — because Windows
// does not derive a DACL from a Unix mode. The modes are still spelled out so
// that the file is not world-readable if this package is ever built or pointed
// somewhere else, and so the intent is written down where the code is.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

// files is the filesystem as this package needs it. It is a seam so that
// publishing, finding and removing can all be tested without a %LOCALAPPDATA%
// and without leaving anything on the machine running the tests.
type files interface {
	// mkdirAll creates dir and its parents.
	mkdirAll(dir string) error
	// writeFile replaces path's contents. It must not leave a half-written
	// record behind: a reader can arrive at any moment.
	writeFile(path string, data []byte) error
	// readFile returns path's contents. When there is no such file the error
	// must satisfy errors.Is(err, fs.ErrNotExist), which is how the caller
	// tells "nothing is running" from "something is wrong".
	readFile(path string) ([]byte, error)
	// remove deletes path. Removing what is not there is not an error: a
	// shutdown after a failed publish must not report one.
	remove(path string) error
}

// osFiles is the real filesystem.
type osFiles struct{}

func (osFiles) mkdirAll(dir string) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	return nil
}

// writeFile writes the record to a temporary file beside its destination and
// renames it into place, so that a reader arriving mid-write sees either the
// old record or the new one and never half of either. Both files are in the
// same directory, which is what makes the rename atomic.
func (osFiles) writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	name := tmp.Name()

	if err := writeAndClose(tmp, data); err != nil {
		// The temporary file is ours and now useless; a failure to remove it
		// must not hide the failure that got us here, so it is reported
		// alongside rather than instead.
		return errors.Join(fmt.Errorf("writing %s: %w", name, err), removeFile(name))
	}
	if err := os.Rename(name, path); err != nil {
		return errors.Join(fmt.Errorf("renaming %s to %s: %w", name, path, err), removeFile(name))
	}
	return nil
}

// writeAndClose writes data, sets the mode and closes the file, reporting the
// first thing that went wrong. The close is not deferred: on Windows a rename
// over a still-open handle fails, so the handle has to be gone before the
// caller renames.
func writeAndClose(f *os.File, data []byte) error {
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close() // the mode failure is the one worth reporting
		return fmt.Errorf("setting the mode: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close() // the write failure is the one worth reporting
		return fmt.Errorf("writing: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing: %w", err)
	}
	return nil
}

// removeFile deletes a file this package created, reporting only a failure that
// left it there.
func removeFile(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", name, err)
	}
	return nil
}

// readFile reads the record. The path is this package's own — %LOCALAPPDATA%
// joined with two constants — or, in a test, a temporary directory; it never
// comes from a request.
func (osFiles) readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is this package's own, see above
	if err != nil {
		// Wrapped, not replaced: the caller asks errors.Is about fs.ErrNotExist
		// to tell an absent record from an unreadable one.
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return data, nil
}

func (osFiles) remove(path string) error { return removeFile(path) }
