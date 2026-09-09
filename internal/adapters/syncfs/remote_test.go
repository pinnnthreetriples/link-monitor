package syncfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// Windows OpenSSH's sftp-server treats "C:/Users/user" as relative to the
// login directory and answers only to "/C:/Users/user". Getting this wrong
// makes every remote operation fail with "not found", which is what the work
// PC said until the leading slash went in.
func TestAFolderIsTranslatedIntoThePathSftpAnswersTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{`C:\Users\user\LinkMonitor\shared`, "/C:/Users/user/LinkMonitor/shared"},
		{`C:/Users/user/shared`, "/C:/Users/user/shared"},
		{"/C:/Users/user/shared", "/C:/Users/user/shared"},
		{`C:\Users\user\shared\`, "/C:/Users/user/shared"},
		{"  /home/user/shared  ", "/home/user/shared"},
		{"", "/"},
		{`\`, "/"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			if got := remotePath(tc.in); got != tc.want {
				t.Errorf("remotePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// failDialer is a peer whose SFTP subsystem will not open.
type failDialer struct{}

func (failDialer) SFTP(context.Context) (*sftp.Client, error) {
	return nil, errStub("no session")
}

func TestOpeningThePeerFailsLoudlyWhenThereIsNoSession(t *testing.T) {
	t.Parallel()

	_, _, err := NewPeer(failDialer{}, `C:\Shared`, "peer").Open(context.Background())
	if err == nil {
		t.Fatal("Open succeeded with no SFTP session")
	}
}

// A folder that is not there is a configuration mistake or a drive that failed
// to mount, and both look exactly like an empty folder. With deletion never
// propagating, an empty folder would mean pulling the whole of the other
// machine's copy across — so this fails instead of guessing.
func TestOpeningThePeerFailsWhenTheFolderIsNotThereOrIsNotAFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	notAFolder := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(notAFolder, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	for _, tc := range []struct{ name, root string }{
		{"missing", filepath.Join(dir, "nowhere")},
		{"a file", notAFolder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := NewPeer(pipeDialer{t: t}, tc.root, "peer").Open(context.Background())
			if err == nil {
				t.Fatalf("Open(%s) succeeded, want it refused", tc.root)
			}
		})
	}
}

func TestThePeerReportsTheFolderItIsSharing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if got := peerTree(t, root).Root(); got != remotePath(root) {
		t.Errorf("Root = %q, want %q", got, remotePath(root))
	}
}

// A peer whose SFTP server cannot replace a file in one step is told about,
// not worked around: the workaround would be to remove the destination first,
// and nothing in this feature removes a file somebody wanted.
func TestAPeerWithNoAtomicRenameRefusesToOverwriteRatherThanRemoveAnything(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remote := peerTree(t, root)
	remote.atomic = false
	write(t, root, "notes.txt", "the original")

	err := remote.Receive(context.Background(), "notes.txt", time.Now(), send("new"))
	if !errors.Is(err, ErrNoAtomicRename) {
		t.Fatalf("Receive = %v, want ErrNoAtomicRename", err)
	}
	if got := read(t, root, "notes.txt"); got != "the original" {
		t.Errorf("content = %q, want the original left alone", got)
	}
	if got := leftovers(t, root); len(got) != 0 {
		t.Errorf("temporary files left behind: %v", got)
	}
}

// The same peer writing a file that is not there yet: a plain rename does that
// perfectly well, so it is allowed.
func TestAPeerWithNoAtomicRenameStillWritesAFreshFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remote := peerTree(t, root)
	remote.atomic = false

	if err := remote.Receive(context.Background(), "fresh.txt", time.Now(), send("new")); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got := read(t, root, "fresh.txt"); got != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

// The peer this program was written for. The extension is what rule 3 stands
// on over SFTP, and the test says so out loud so that a peer without it fails
// visibly rather than quietly turning into a remove-then-rename.
func TestThePeerIsAskedWhetherItCanReplaceAFileAtomically(t *testing.T) {
	t.Parallel()

	if !peerTree(t, t.TempDir()).atomic {
		t.Skip("this SFTP server has no posix-rename; the refusal path is tested above")
	}
}

// A copy whose folder cannot be made must not pretend to have worked. A plain
// file sitting where a directory has to go is the honest way to arrange that
// on both machines.
func TestACopyIntoAFolderThatCannotBeCreatedFails(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		write(t, root, "sub", "a file, not a folder")

		err := subject.Receive(context.Background(), "sub/notes.txt", time.Now(), send("x"))
		if err == nil {
			t.Fatal("Receive succeeded through a file, want it refused")
		}
		if got := read(t, root, "sub"); got != "a file, not a folder" {
			t.Errorf("content = %q, want the file in the way left alone", got)
		}
	})
}

// A copy whose destination name is taken by a directory cannot be published,
// and must leave both the directory and the folder as they were.
func TestACopyOntoADirectoryNameFailsAndLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		if err := os.MkdirAll(filepath.Join(root, "notes.txt", "inside"), 0o750); err != nil {
			t.Fatalf("creating the directory: %v", err)
		}

		err := subject.Receive(context.Background(), "notes.txt", time.Now(), send("x"))
		if err == nil {
			t.Fatal("Receive succeeded onto a directory, want it refused")
		}
		if _, statErr := os.Stat(filepath.Join(root, "notes.txt", "inside")); statErr != nil {
			t.Errorf("the directory in the way is gone: %v", statErr)
		}
		if got := leftovers(t, root); len(got) != 0 {
			t.Errorf("temporary files left behind: %v", got)
		}
	})
}

// A scan of a folder that is not there fails rather than reporting an empty
// one, for the reason spelled out on LocalTree: an empty listing and a missing
// folder look the same, and one of them would pull the peer's whole copy back.
func TestScanningAFolderThatIsNotThereFails(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "nowhere")
	if _, err := NewLocal(missing, "here").Scan(context.Background(), keepAll); err == nil {
		t.Error("Scan of a missing folder succeeded")
	}

	// The peer's side refuses one pass earlier, when the session opens, so the
	// same condition is asserted there rather than here.
	root := t.TempDir()
	remote := peerTree(t, root)
	if err := os.Remove(root); err != nil {
		t.Fatalf("removing the folder: %v", err)
	}
	if _, err := remote.Scan(context.Background(), keepAll); err == nil {
		t.Error("Scan of a folder that went away mid-pass succeeded")
	}
}

// A folder that disappears deeper in the walk is not a fault: that is somebody
// working in the folder. The scan carries on with what is left.
func TestAFolderThatGoesAwayMidWalkIsNotAFailureOnThePeer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, root, "notes.txt", "hello")
	write(t, root, "sub/plan.md", "# plan")
	remote := peerTree(t, root)

	// keep is asked about every directory before it is read, which is the seam
	// a folder can vanish through.
	vanishing := func(rel string) bool {
		if rel == "sub" {
			if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
				t.Fatalf("removing the folder: %v", err)
			}
		}
		return true
	}

	got, err := remote.Scan(context.Background(), vanishing)
	if err != nil {
		t.Fatalf("Scan = %v, want the missing folder taken in its stride", err)
	}
	if len(got) != 1 || got[0].Path != "notes.txt" {
		t.Errorf("scan found %+v, want just notes.txt", got)
	}
}

// The same for this machine's side: a file that goes away mid-walk must not
// break the scan.
//
// Note what it does *not* assert. On Windows filepath.WalkDir already holds
// the length and the stamp from the directory listing, so a file removed a
// moment ago is still reported with what it had then — the scan cannot tell.
// That is harmless and is covered by the pass rather than here: the copy step
// re-checks every source file before reading it, and a file that is gone is
// skipped as busy rather than invented.
func TestAFileThatGoesAwayMidWalkDoesNotBreakTheScanHere(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, root, "notes.txt", "hello")
	write(t, root, "going.txt", "bye")
	local := NewLocal(root, "here")

	vanishing := func(rel string) bool {
		if rel == "going.txt" {
			if err := os.Remove(filepath.Join(root, "going.txt")); err != nil {
				t.Fatalf("removing the file: %v", err)
			}
		}
		return true
	}

	got, err := local.Scan(context.Background(), vanishing)
	if err != nil {
		t.Fatalf("Scan = %v, want the vanished file taken in its stride", err)
	}
	if len(got) == 0 {
		t.Error("the scan reported nothing at all")
	}
}

// A caller that has already given up must not have a file read for it.
func TestAReadOnACancelledContextIsNotStarted(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		write(t, root, "notes.txt", "hello")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := subject.Stat(ctx, "notes.txt"); err == nil {
			t.Error("Stat succeeded on a cancelled context")
		}
		if _, err := subject.Open(ctx, "notes.txt"); err == nil {
			t.Error("Open succeeded on a cancelled context")
		}
	})
}

func TestThisMachineReportsTheFolderItIsSharing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if got := NewLocal(root, "here").Root(); got != filepath.Clean(root) {
		t.Errorf("Root = %q, want %q", got, root)
	}
}

// A copy the caller has already given up on must not be started.
func TestACopyOnACancelledContextIsNotStarted(t *testing.T) {
	t.Parallel()

	bothTrees(t, func(t *testing.T, root string, subject tree) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := subject.Receive(ctx, "notes.txt", time.Now(), send("x")); err == nil {
			t.Error("Receive succeeded on a cancelled context")
		}
		if _, err := os.Stat(filepath.Join(root, "notes.txt")); err == nil {
			t.Error("a file was written for a copy nobody was waiting for")
		}
		if got := leftovers(t, root); len(got) != 0 {
			t.Errorf("temporary files left behind: %v", got)
		}
	})
}
