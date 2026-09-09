package syncfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// This file stands up a real SFTP server inside the test process, over an
// in-memory pipe, serving the real filesystem beneath a t.TempDir. Every
// remote test in this package talks to it. Nothing here reaches a network, an
// SSH connection or the peer — the same arrangement internal/adapters/sshx
// makes for its own tests, one layer up the protocol stack.

// tree is what both sides of the shared folder look like to the pass. It is
// declared here rather than imported from internal/app, which adapters may not
// see, and asserting both implementations against it is what keeps the two
// from drifting apart.
type tree interface {
	Name() string
	Scan(ctx context.Context, keep func(rel string) bool) ([]foldersync.Entry, error)
	Stat(ctx context.Context, rel string) (foldersync.State, error)
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
	Receive(ctx context.Context, rel string, stamp time.Time, write func(io.Writer) error) error
}

var (
	_ tree = (*LocalTree)(nil)
	_ tree = (*RemoteTree)(nil)
)

// pipeDialer hands out SFTP clients backed by a server in this process.
//
// net.Pipe rather than two io.Pipes on purpose: it is full duplex and closing
// either end unblocks both directions, which is what lets a client shut down
// without waiting on a read that nothing is ever going to answer.
type pipeDialer struct{ t *testing.T }

// SFTP starts one server goroutine and returns the client wired to it.
func (d pipeDialer) SFTP(context.Context) (*sftp.Client, error) {
	clientEnd, serverEnd := net.Pipe()

	server, err := sftp.NewServer(serverEnd)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Serve returns when the client's end goes, which is what the closer
		// from Peer.Open does at the end of every pass.
		_ = server.Serve()
	}()
	d.t.Cleanup(func() {
		_ = clientEnd.Close()
		_ = serverEnd.Close()
		<-done
	})

	client, err := sftp.NewClientPipe(clientEnd, clientEnd)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// peerTree opens the peer's side of a real directory over that server.
func peerTree(t *testing.T, root string) *RemoteTree {
	t.Helper()

	peer := NewPeer(pipeDialer{t: t}, root, "win-sttm11d02rd")
	remote, closer, err := peer.Open(context.Background())
	if err != nil {
		t.Fatalf("opening the peer's folder at %s: %v", root, err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	return remote
}

// bothTrees runs fn once against this machine's filesystem and once against
// the peer's over SFTP, so a rule that holds for one is asserted for both.
func bothTrees(t *testing.T, fn func(t *testing.T, root string, subject tree)) {
	t.Helper()

	t.Run("here", func(t *testing.T) {
		root := t.TempDir()
		fn(t, root, NewLocal(root, "workspace-claude-pc"))
	})
	t.Run("peer", func(t *testing.T) {
		root := t.TempDir()
		fn(t, root, peerTree(t, root))
	})
}

// write puts a file on disk, creating its folder, and gives it a stamp that is
// not the moment the test ran — so a stamp that was not preserved shows up.
func write(t *testing.T, root, rel, body string) time.Time {
	t.Helper()

	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("creating the folder for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
	stamp := time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local)
	if err := os.Chtimes(full, stamp, stamp); err != nil {
		t.Fatalf("stamping %s: %v", rel, err)
	}
	return stamp
}

// read is the content of one file under root.
func read(t *testing.T, root, rel string) string {
	t.Helper()

	blob, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(blob)
}

// leftovers lists every temporary file under root, which must be none once a
// copy has either landed or been abandoned.
func leftovers(t *testing.T, root string) []string {
	t.Helper()

	var out []string
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), foldersync.TempSuffix) {
			out = append(out, d.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

// send is what the pass hands Receive: a writer that copies a string in.
func send(body string) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := io.WriteString(w, body)
		return err
	}
}

// Rule 3, for both machines: the destination is only ever reached by a rename,
// so a finished copy is whole and stamped and nothing is left behind.
func TestACopyArrivesWholeStampedAndWithNoTemporaryFileLeft(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)

		if err := subject.Receive(context.Background(), "sub/notes.txt", stamp,
			send("hello")); err != nil {
			t.Fatalf("Receive: %v", err)
		}

		if got := read(t, root, "sub/notes.txt"); got != "hello" {
			t.Errorf("content = %q, want %q", got, "hello")
		}
		if got := leftovers(t, root); len(got) != 0 {
			t.Errorf("temporary files left behind: %v", got)
		}
		state, err := subject.Stat(context.Background(), "sub/notes.txt")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if state.MTime.Unix() != stamp.Unix() {
			t.Errorf("stamp = %s, want %s", state.MTime, stamp)
		}
		if state.Size != 5 {
			t.Errorf("size = %d, want 5", state.Size)
		}
	})
}

// The same copy over an existing file: it must be replaced in one step, whole,
// with its new stamp.
func TestACopyReplacesAnExistingFileInOneStep(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		write(t, root, "notes.txt", "old content, longer")
		stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)

		if err := subject.Receive(context.Background(), "notes.txt", stamp, send("new")); err != nil {
			t.Fatalf("Receive: %v", err)
		}

		if got := read(t, root, "notes.txt"); got != "new" {
			t.Errorf("content = %q, want %q", got, "new")
		}
		if got := leftovers(t, root); len(got) != 0 {
			t.Errorf("temporary files left behind: %v", got)
		}
	})
}

// This is the other half of rule 4, at the adapter's own boundary: the pass
// refuses the copy at the last moment, and what was there before must still be
// there, untouched, with nothing half-written beside it.
func TestACopyRefusedAtTheLastMomentLeavesTheDestinationExactlyAsItWas(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		before := write(t, root, "notes.txt", "the original")
		refuse := func(w io.Writer) error {
			if _, err := io.WriteString(w, "half of something else"); err != nil {
				return err
			}
			return errRefused
		}

		err := subject.Receive(context.Background(), "notes.txt", time.Now(), refuse)
		if err == nil {
			t.Fatal("Receive succeeded, want the refusal reported")
		}

		if got := read(t, root, "notes.txt"); got != "the original" {
			t.Errorf("content = %q, want the original untouched", got)
		}
		state, statErr := subject.Stat(context.Background(), "notes.txt")
		if statErr != nil {
			t.Fatalf("Stat: %v", statErr)
		}
		if state.MTime.Unix() != before.Unix() {
			t.Errorf("stamp = %s, want the original %s", state.MTime, before)
		}
		if got := leftovers(t, root); len(got) != 0 {
			t.Errorf("temporary files left behind: %v", got)
		}
	})
}

// errRefused stands in for the pass deciding the source moved.
var errRefused = errStub("the source moved")

// errStub is a plain error for the tests.
type errStub string

func (e errStub) Error() string { return string(e) }

// Rule 7 at the last moment before a real open, on both machines.
func TestNoPathCanReachOutsideTheSharedFolder(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		for _, rel := range []string{
			"../escape.txt", "sub/../../escape.txt", `..\escape.txt`,
			"C:/Windows/escape.txt", "/etc/passwd", "", "..", "notes.txt:stream",
		} {
			t.Run(rel, func(t *testing.T) {
				if _, err := subject.Stat(context.Background(), rel); err == nil {
					t.Errorf("Stat(%q) succeeded, want it refused", rel)
				}
				if _, err := subject.Open(context.Background(), rel); err == nil {
					t.Errorf("Open(%q) succeeded, want it refused", rel)
				}
				err := subject.Receive(context.Background(), rel, time.Now(), send("x"))
				if err == nil {
					t.Errorf("Receive(%q) succeeded, want it refused", rel)
				}
			})
		}
		// Nothing must have been created anywhere near the folder.
		if got := leftovers(t, root); len(got) != 0 {
			t.Errorf("temporary files left behind: %v", got)
		}
	})
}

func TestScanListsEveryFileWithItsLengthAndStamp(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		stamp := write(t, root, "notes.txt", "hello")
		write(t, root, "sub/deep/plan.md", "# plan")
		write(t, root, "заметки.txt", "привет")

		got, err := subject.Scan(context.Background(), keepAll)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		byPath := map[string]foldersync.Entry{}
		for _, e := range got {
			byPath[e.Path] = e
		}
		if len(byPath) != 3 {
			t.Fatalf("scan found %+v, want three files", got)
		}
		notes := byPath["notes.txt"]
		if notes.Size != 5 || notes.MTime.Unix() != stamp.Unix() {
			t.Errorf("notes.txt = %+v, want 5 bytes stamped %s", notes, stamp)
		}
		if _, found := byPath["sub/deep/plan.md"]; !found {
			t.Errorf("scan found %+v, want the nested file under a forward-slash path", got)
		}
	})
}

func TestScanDoesNotDescendIntoAFolderKeepRefuses(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		write(t, root, "notes.txt", "hello")
		write(t, root, ".git/config", "[core]")
		write(t, root, ".git/objects/ab/cdef", "blob")
		write(t, root, "node_modules/left-pad/index.js", "module")

		got, err := subject.Scan(context.Background(),
			func(rel string) bool { return !foldersync.Excluded(rel, foldersync.Limits{}) })
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if len(got) != 1 || got[0].Path != "notes.txt" {
			t.Errorf("scan found %+v, want only notes.txt", got)
		}
	})
}

func TestScanHonoursACancelledContext(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		write(t, root, "notes.txt", "hello")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := subject.Scan(ctx, keepAll); err == nil {
			t.Error("Scan succeeded on a cancelled context, want it to give up")
		}
	})
}

func TestOpenReadsWhatIsThere(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		write(t, root, "sub/notes.txt", "the content")

		body, err := subject.Open(context.Background(), "sub/notes.txt")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = body.Close() }()

		sum := sha256.New()
		if _, err := io.Copy(sum, body); err != nil {
			t.Fatalf("reading: %v", err)
		}
		want := sha256.Sum256([]byte("the content"))
		if hex.EncodeToString(sum.Sum(nil)) != hex.EncodeToString(want[:]) {
			t.Error("the bytes read are not the bytes written")
		}
	})
}

func TestAFileThatIsNotThereIsReportedAndNotInvented(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, _ string, subject tree) {
		if _, err := subject.Stat(context.Background(), "missing.txt"); err == nil {
			t.Error("Stat of a missing file succeeded")
		}
		if _, err := subject.Open(context.Background(), "missing.txt"); err == nil {
			t.Error("Open of a missing file succeeded")
		}
	})
}

func TestEachSideKnowsWhatItIsCalled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if got := NewLocal(root, "workspace-claude-pc").Name(); got != "workspace-claude-pc" {
		t.Errorf("local name = %q", got)
	}
	if got := peerTree(t, t.TempDir()).Name(); got != "win-sttm11d02rd" {
		t.Errorf("peer name = %q", got)
	}
}

// keepAll is the predicate that excludes nothing.
func keepAll(string) bool { return true }
