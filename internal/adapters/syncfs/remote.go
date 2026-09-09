package syncfs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// posixRename is the OpenSSH extension that makes a rename replace an existing
// file. Without it the only way to publish over an existing file would be to
// remove it first, which is a deletion and a window in which the file is
// simply gone — so this package refuses instead. Windows OpenSSH's sftp-server
// advertises it; verified against the work PC on 2026-09-08.
const posixRename = "posix-rename@openssh.com"

// ErrNoAtomicRename means the peer's SFTP server cannot replace a file in one
// step. Nothing is removed to work around it: an overwrite is refused, and the
// user is told.
var ErrNoAtomicRename = errors.New("syncfs: the peer's sftp server has no posix-rename extension")

// Dialer hands out SFTP clients over the SSH connection this program already
// keeps open. *sshx.Lazy implements it, and it is the only way this package
// reaches the peer: nothing here dials, listens or shells out.
type Dialer interface {
	SFTP(ctx context.Context) (*sftp.Client, error)
}

// Peer is the peer's side of the shared folder, not yet opened.
type Peer struct {
	dialer Dialer
	root   string
	name   string
}

// NewPeer describes the peer's side. root is the folder on that machine, given
// the way a person would write it — "C:\Users\user\Shared" — and translated
// here; name is what the UI calls the peer.
func NewPeer(d Dialer, root, name string) *Peer {
	return &Peer{dialer: d, root: remotePath(root), name: name}
}

// Open starts one SFTP session and hands back the peer's tree together with
// the closer that ends the session.
//
// One session per pass, closed at the end of it. An SFTP client is bound to
// the SSH connection that carried it and does not heal when that connection
// dies, exactly as a port forward does not; a pass is the natural place to
// find that out and start again.
func (p *Peer) Open(ctx context.Context) (*RemoteTree, io.Closer, error) {
	client, err := p.dialer.SFTP(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("opening sftp to %s: %w", p.name, err)
	}
	info, err := client.Stat(p.root)
	if err != nil {
		_ = client.Close() // the failure below is the one worth reporting
		return nil, nil, fmt.Errorf("looking at %s on %s: %w", p.root, p.name, err)
	}
	if !info.IsDir() {
		_ = client.Close() // as above
		return nil, nil, fmt.Errorf("%s on %s is not a folder", p.root, p.name)
	}
	_, atomic := client.HasExtension(posixRename)
	return &RemoteTree{client: client, root: p.root, name: p.name, atomic: atomic}, client, nil
}

// RemoteTree is the peer's side of the shared folder, over one SFTP session.
type RemoteTree struct {
	client *sftp.Client
	root   string
	name   string
	// atomic says whether the peer can replace a file with a rename. When it
	// cannot, an overwrite is refused rather than turned into a remove.
	atomic bool
}

// Name is what the UI calls this side.
func (t *RemoteTree) Name() string { return t.name }

// Root is the folder being shared, as SFTP addresses it.
func (t *RemoteTree) Root() string { return t.root }

// Scan lists every regular file under the peer's root.
//
// It walks by hand rather than through sftp's Walker so that a directory keep
// refuses costs no round trips at all, which over a tailnet is the difference
// that matters. Symbolic links are not followed and not listed: a link is a
// way out of the folder the user chose.
func (t *RemoteTree) Scan(ctx context.Context, keep func(rel string) bool) ([]foldersync.Entry, error) {
	var out []foldersync.Entry
	// Breadth first, with an explicit queue: recursion over a remote tree of
	// unknown depth is a stack this program does not control.
	queue := []string{""}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("listing %s: %w", t.name, err)
		}
		dir := queue[0]
		queue = queue[1:]

		entries, err := t.client.ReadDir(t.join(dir))
		if err != nil {
			// A folder deeper in the tree that went away while we walked is
			// somebody working in the folder, and the next pass will see
			// whatever is there then. The root going away is a different
			// matter and is never taken in our stride: an empty listing where
			// a shared folder should be is exactly what a drive that failed to
			// mount looks like, and acting on one is how a sync loses a whole
			// tree.
			if dir != "" && vanished(err) {
				continue
			}
			return nil, fmt.Errorf("listing %s on %s: %w", t.join(dir), t.name, err)
		}
		for _, e := range entries {
			rel := path.Join(dir, e.Name())
			if !keep(rel) {
				continue
			}
			switch {
			case e.IsDir():
				queue = append(queue, rel)
			case e.Mode().IsRegular():
				out = append(out, foldersync.Entry{
					Path: rel, Size: e.Size(), MTime: e.ModTime(),
				})
			}
		}
	}
	return out, nil
}

// Stat reports one file's length and stamp without reading it.
func (t *RemoteTree) Stat(ctx context.Context, rel string) (foldersync.State, error) {
	full, err := t.abs(ctx, rel)
	if err != nil {
		return foldersync.State{}, err
	}
	info, err := t.client.Stat(full)
	if err != nil {
		return foldersync.State{}, fmt.Errorf("looking at %s on %s: %w", rel, t.name, err)
	}
	return foldersync.State{Size: info.Size(), MTime: info.ModTime()}, nil
}

// Open opens one of the peer's files for reading. The caller closes it.
func (t *RemoteTree) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	full, err := t.abs(ctx, rel)
	if err != nil {
		return nil, err
	}
	file, err := t.client.Open(full)
	if err != nil {
		return nil, fmt.Errorf("reading %s on %s: %w", rel, t.name, err)
	}
	return file, nil
}

// Receive writes one file on the peer through a temporary name in its own
// directory and renames it into place. See [receive].
func (t *RemoteTree) Receive(
	ctx context.Context, rel string, stamp time.Time, write func(io.Writer) error,
) error {
	full, err := t.abs(ctx, rel)
	if err != nil {
		return err
	}
	if err := receive(remotePublisher{t}, full, stamp, write); err != nil {
		return fmt.Errorf("on %s: %w", t.name, err)
	}
	return nil
}

// join puts a relative path under the peer's root. SFTP speaks forward slashes
// whatever the far end's filesystem thinks.
func (t *RemoteTree) join(rel string) string {
	if rel == "" {
		return t.root
	}
	return t.root + "/" + rel
}

// abs turns a relative path into one on the peer, refusing any that could
// address a file outside the shared folder. This is the boundary rule 7 names:
// a path in the peer's listing is a name the peer chose, so it is checked
// here, with foldersync.SafeRel, and not merely trusted because it arrived
// over an authenticated connection.
func (t *RemoteTree) abs(ctx context.Context, rel string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("on %s: %w", t.name, err)
	}
	clean, err := foldersync.SafeRel(rel)
	if err != nil {
		return "", fmt.Errorf("on %s: %w", t.name, err)
	}
	return t.join(clean), nil
}

// remotePath turns a folder as a person writes it into the path SFTP wants.
// Windows OpenSSH's sftp-server treats "C:/Users/user" as *relative* — it
// joins it onto the login directory and then fails — and answers only to
// "/C:/Users/user". Verified against the work PC on 2026-09-08.
func remotePath(root string) string {
	slashed := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(root), `\`, "/"), "/")
	if slashed == "" {
		return "/"
	}
	if !strings.HasPrefix(slashed, "/") {
		return "/" + slashed
	}
	return slashed
}

// remotePublisher is the SFTP half of [publisher].
type remotePublisher struct{ t *RemoteTree }

func (remotePublisher) split(p string) (string, string) { return path.Dir(p), path.Base(p) }

func (r remotePublisher) mkdirAll(dir string) error {
	if err := r.t.client.MkdirAll(dir); err != nil {
		return fmt.Errorf("creating %s on %s: %w", dir, r.t.name, err)
	}
	return nil
}

// createTemp opens a new file under a name nothing else holds.
//
// sftp has neither CreateTemp nor O_TMPFILE, so the name is random and O_EXCL
// enforces it. Random rather than a counter on purpose: there is then no retry
// loop whose exit nobody can reach, and a failure here is a real failure
// rather than a collision. O_EXCL also means an existing entry — a symlink
// somebody put there, most of all — is an error instead of something to write
// through, which is the reasoning internal/adapters/tailscale already applies
// to a Taildrop inbox file.
func (r remotePublisher) createTemp(dir string) (io.WriteCloser, string, error) {
	name := dir + "/" + strings.Replace(tempPattern, "*", token(), 1)
	file, err := r.t.client.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return nil, "", fmt.Errorf("creating %s on %s: %w", path.Base(name), r.t.name, err)
	}
	return file, name, nil
}

// token is a random fragment for a temporary file's name. crypto/rand.Text
// returns no error — it panics rather than hand back weak randomness — so
// there is nothing here to check and nothing to ignore.
func token() string { return strings.ToLower(rand.Text()) }

func (r remotePublisher) chtimes(p string, when time.Time) error {
	if err := r.t.client.Chtimes(p, when, when); err != nil {
		return fmt.Errorf("setting the stamp on %s: %w", path.Base(p), err)
	}
	return nil
}

// rename replaces to with from, in one step or not at all.
//
// posix-rename@openssh.com is what makes that possible; plain SFTP rename
// fails when the destination exists. A peer without the extension is told
// about rather than worked around: the workaround would be to remove the
// destination first, and this feature does not remove files.
func (r remotePublisher) rename(from, to string) error {
	if r.t.atomic {
		if err := r.t.client.PosixRename(from, to); err != nil {
			return fmt.Errorf("renaming onto %s on %s: %w", path.Base(to), r.t.name, err)
		}
		return nil
	}
	if _, err := r.t.client.Stat(to); err == nil {
		return fmt.Errorf("replacing %s on %s: %w", path.Base(to), r.t.name, ErrNoAtomicRename)
	}
	if err := r.t.client.Rename(from, to); err != nil {
		return fmt.Errorf("renaming onto %s on %s: %w", path.Base(to), r.t.name, err)
	}
	return nil
}

// remove deletes a temporary file this package created on the peer, and is
// called on nothing else, ever. See the guard in tools/gates.
func (r remotePublisher) remove(p string) error {
	if err := r.t.client.Remove(p); err != nil && !vanished(err) {
		return fmt.Errorf("removing %s on %s: %w", path.Base(p), r.t.name, err)
	}
	return nil
}
