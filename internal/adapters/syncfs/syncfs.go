// Package syncfs is the impure half of the shared folder.
//
// It does the four things the decision in internal/core/foldersync cannot do
// for itself: list a directory tree, read a file, write one, and say what a
// file looks like right now. It does them twice over — once on this machine's
// filesystem, once on the peer's over SFTP — behind one shape, so the pass
// above it is written once and the two sides cannot drift apart.
//
// Rule 3 lives here, in [receive], written once for both sides: bytes go to a
// temporary name in the destination's *own* directory and reach the
// destination only by a rename, which inside one directory is atomic on NTFS.
// Nothing is ever written to a destination path directly, so a link that
// breaks mid-copy leaves a visible .lmtmp file and the destination exactly as
// it was — never half a document.
//
// Rule 7 lives here too, at the last moment before a real open: every relative
// path is put through foldersync.SafeRel and then checked to be under the
// chosen root, so a name the peer chose cannot address a file outside the
// folder the user picked.
//
// Nothing here logs a file's contents. Names appear in errors, because a
// failure that will not say which file failed is not much of a failure report.
package syncfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// tempPattern names a half-written file. The suffix is in foldersync's
// built-in exclude list, so a temporary file a broken link left behind is
// never itself carried to the other machine.
const tempPattern = "lm-*" + foldersync.TempSuffix

// dirPerm is the mode a directory this package creates gets. It matters on
// nothing Windows does, and it is the tightest thing that still works when
// this code is compiled for anything else.
const dirPerm = 0o750

// publisher is the handful of operations that differ between a file on this
// machine and a file on the peer. It exists so that rule 3 — never write a
// half file — is written once, in [receive], rather than once per side where
// the two could quietly stop agreeing.
type publisher interface {
	// split separates a path into its directory and its last element, the way
	// the filesystem holding it does: backslashes here, forward slashes there.
	split(path string) (dir, base string)
	// mkdirAll creates dir and every missing parent.
	mkdirAll(dir string) error
	// createTemp makes a new file with an unused name in dir and returns it
	// together with its path.
	createTemp(dir string) (io.WriteCloser, string, error)
	// chtimes gives path the stamp both machines must read for this content.
	chtimes(path string, stamp time.Time) error
	// rename moves from onto to, replacing to if it exists. An implementation
	// that cannot replace atomically must fail rather than remove anything.
	rename(from, to string) error
	// remove deletes one file. It is called on temporary files this package
	// created and on nothing else, ever.
	remove(path string) error
}

// receive writes one file the only way this program is allowed to: into a
// temporary name in the destination's own directory, stamped, and then renamed
// into place.
//
// write is handed the temporary file. It is the caller's chance to say the copy
// must not be published — the pass uses it to re-check that the source has not
// moved while it was being read — and an error from it leaves the destination
// untouched.
func receive(p publisher, dest string, stamp time.Time, write func(io.Writer) error) error {
	dir, base := p.split(dest)
	if err := p.mkdirAll(dir); err != nil {
		return fmt.Errorf("creating the folder for %s: %w", base, err)
	}
	file, tmp, err := p.createTemp(dir)
	if err != nil {
		return fmt.Errorf("opening a temporary file for %s: %w", base, err)
	}

	// From here every path either publishes the temporary file or removes it.
	if err := write(file); err != nil {
		drop(p, file, tmp)
		return fmt.Errorf("copying %s: %w", base, err)
	}
	if err := file.Close(); err != nil {
		drop(p, nil, tmp)
		return fmt.Errorf("finishing %s: %w", base, err)
	}
	// The stamp goes on before the rename, so the destination never exists
	// with the wrong one: what the rename publishes is a finished file.
	if err := p.chtimes(tmp, stamp); err != nil {
		drop(p, nil, tmp)
		return fmt.Errorf("stamping %s: %w", base, err)
	}
	if err := p.rename(tmp, dest); err != nil {
		drop(p, nil, tmp)
		return fmt.Errorf("putting %s into place: %w", base, err)
	}
	return nil
}

// drop gets rid of a temporary file that will not be published.
//
// Both errors are deliberately unreported: the caller is already returning the
// failure that matters, and the worst a leftover .lmtmp costs is one visible
// file the exclude list already ignores. Saying "could not remove the
// temporary file" instead of "the link dropped" would hide the real answer
// behind the tidying-up.
func drop(p publisher, file io.Closer, tmp string) {
	if file != nil {
		_ = file.Close()
	}
	_ = p.remove(tmp)
}

// within reports whether full sits under root.
//
// It is the second of the two locks on rule 7 and not the main one: what
// actually makes an escape impossible is foldersync.SafeRel, which has already
// refused every separator, volume letter and ".." before a path reaches here.
// The comparison folds case because both machines are Windows and a root
// spelled with a different case is the same root there. On a case-sensitive
// filesystem that makes this check slightly more permissive than it looks,
// which costs nothing: SafeRel is what does the work.
func within(root, full string) bool {
	prefix := strings.ToLower(filepath.Clean(root) + string(filepath.Separator))
	return strings.HasPrefix(strings.ToLower(full), prefix)
}

// vanished reports whether an error means the file simply is not there. A file
// that disappeared between the listing and the read is not a fault: it is a
// folder somebody is working in, and the next pass will see whatever is there
// then.
func vanished(err error) bool { return errors.Is(err, fs.ErrNotExist) }
