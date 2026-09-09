package syncfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// LocalTree is this machine's side of the shared folder.
//
// The root is never created. A folder that is not there is a configuration
// mistake or a drive that failed to mount, and both of those look exactly like
// an empty folder — which, with deletion never propagating, would mean pulling
// the whole of the peer's copy back over the link. Failing loudly is the
// cheaper answer.
type LocalTree struct {
	root string
	name string
}

// NewLocal opens this machine's side. root must be an absolute path; name is
// what the UI calls this machine.
func NewLocal(root, name string) *LocalTree {
	return &LocalTree{root: filepath.Clean(root), name: name}
}

// Name is what the UI calls this side.
func (t *LocalTree) Name() string { return t.name }

// Root is the folder being shared.
func (t *LocalTree) Root() string { return t.root }

// Scan lists every regular file under the root.
//
// keep is asked about every directory and every file; a directory it refuses is
// not descended into, which is what makes .git and node_modules cost nothing.
//
// A file that disappears between the listing and the stat is left out rather
// than reported: that is a folder somebody is working in. Anything else stops
// the scan, because a listing that is missing files for a reason we do not
// understand is not a listing this feature may act on.
func (t *LocalTree) Scan(ctx context.Context, keep func(rel string) bool) ([]foldersync.Entry, error) {
	var out []foldersync.Entry
	walk := func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		rel := t.rel(full)
		if d.IsDir() {
			if rel != "" && !keep(rel) {
				return fs.SkipDir
			}
			return nil
		}
		// Not a regular file: a symlink, a junction, a pipe. Following one
		// would be a way out of the folder the user chose, and copying one is
		// not a thing this feature claims to do.
		if !d.Type().IsRegular() || !keep(rel) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			if vanished(statErr) {
				return nil
			}
			return fmt.Errorf("looking at %s: %w", rel, statErr)
		}
		out = append(out, foldersync.Entry{Path: rel, Size: info.Size(), MTime: info.ModTime()})
		return nil
	}
	if err := filepath.WalkDir(t.root, walk); err != nil {
		return nil, fmt.Errorf("listing %s: %w", t.name, err)
	}
	return out, nil
}

// Stat reports one file's length and stamp without reading it.
func (t *LocalTree) Stat(ctx context.Context, rel string) (foldersync.State, error) {
	if err := ctx.Err(); err != nil {
		return foldersync.State{}, fmt.Errorf("looking at %s on %s: %w", rel, t.name, err)
	}
	full, err := t.abs(rel)
	if err != nil {
		return foldersync.State{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return foldersync.State{}, fmt.Errorf("looking at %s on %s: %w", rel, t.name, err)
	}
	return foldersync.State{Size: info.Size(), MTime: info.ModTime()}, nil
}

// Open opens one file for reading. The caller closes it.
func (t *LocalTree) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("reading %s on %s: %w", rel, t.name, err)
	}
	full, err := t.abs(rel)
	if err != nil {
		return nil, err
	}
	// full is under the root and holds no separator, volume letter or ".."
	// that did not come from the root itself: abs refuses all of those.
	file, err := os.Open(full) //nolint:gosec // G304: see abs, which is the whole guard
	if err != nil {
		return nil, fmt.Errorf("reading %s on %s: %w", rel, t.name, err)
	}
	return file, nil
}

// Receive writes one file through a temporary name in its own directory and
// renames it into place. See [receive] for the rule and [publisher] for why
// the two sides share one implementation of it.
func (t *LocalTree) Receive(
	ctx context.Context, rel string, stamp time.Time, write func(io.Writer) error,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("writing %s on %s: %w", rel, t.name, err)
	}
	full, err := t.abs(rel)
	if err != nil {
		return err
	}
	if err := receive(localPublisher{}, full, stamp, write); err != nil {
		return fmt.Errorf("on %s: %w", t.name, err)
	}
	return nil
}

// rel is full's path relative to the root, with forward slashes. The root
// itself answers "".
func (t *LocalTree) rel(full string) string {
	trimmed := strings.TrimPrefix(full, t.root)
	return filepath.ToSlash(strings.TrimPrefix(trimmed, string(filepath.Separator)))
}

// abs turns a relative path into one on this machine, refusing any that could
// address a file outside the shared folder — rule 7, at the last moment before
// a real open.
func (t *LocalTree) abs(rel string) (string, error) {
	clean, err := foldersync.SafeRel(rel)
	if err != nil {
		return "", fmt.Errorf("on %s: %w", t.name, err)
	}
	full := filepath.Join(t.root, filepath.FromSlash(clean))
	if !within(t.root, full) {
		return "", fmt.Errorf("%q leaves %s: %w", rel, t.name, foldersync.ErrUnsafeName)
	}
	return full, nil
}

// localPublisher is the filesystem half of [publisher].
type localPublisher struct{}

func (localPublisher) split(path string) (string, string) {
	return filepath.Dir(path), filepath.Base(path)
}

func (localPublisher) mkdirAll(dir string) error {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	return nil
}

func (localPublisher) createTemp(dir string) (io.WriteCloser, string, error) {
	file, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return nil, "", fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	return file, file.Name(), nil
}

func (localPublisher) chtimes(path string, stamp time.Time) error {
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		return fmt.Errorf("setting the stamp on %s: %w", filepath.Base(path), err)
	}
	return nil
}

// rename replaces to with from. os.Rename is MoveFileEx with
// MOVEFILE_REPLACE_EXISTING on Windows, so inside one directory this is the
// atomic publish rule 3 asks for: there is no instant at which the destination
// is missing or short.
func (localPublisher) rename(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("renaming onto %s: %w", filepath.Base(to), err)
	}
	return nil
}

// remove deletes a temporary file this package created, and is called on
// nothing else. See the guard in tools/gates.
func (localPublisher) remove(path string) error {
	if err := os.Remove(path); err != nil && !vanished(err) {
		return fmt.Errorf("removing %s: %w", filepath.Base(path), err)
	}
	return nil
}
