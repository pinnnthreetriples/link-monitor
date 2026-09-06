package tailscale

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"tailscale.com/client/tailscale/apitype"
)

// The sample file every send test moves.
const (
	sampleName = "report.txt"
	sampleBody = "quarterly numbers"
)

// writeSample puts the sample file in a fresh directory and returns its path.
func writeSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), sampleName)
	if err := os.WriteFile(path, []byte(sampleBody), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

func TestSendFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		peer    string
		targets []apitype.FileTarget
		daemon  func(*fakeDaemon)
		wantErr error
		wantAny bool
	}{
		{
			name:    "by MagicDNS name",
			peer:    workName,
			targets: []apitype.FileTarget{workTarget()},
			wantAny: true,
		},
		{
			name:    "by FQDN",
			peer:    workName + tailnetSuffix,
			targets: []apitype.FileTarget{workTarget()},
			wantAny: true,
		},
		{
			name:    "by address",
			peer:    workAddr,
			targets: []apitype.FileTarget{workTarget()},
			wantAny: true,
		},
		{
			name:    "case does not matter",
			peer:    strings.ToUpper(workName),
			targets: []apitype.FileTarget{workTarget()},
			wantAny: true,
		},
		{
			name:    "a peer that is not a Taildrop target",
			peer:    "some-other-pc",
			targets: []apitype.FileTarget{workTarget()},
			wantErr: ErrPeerNotFound,
		},
		{
			name:    "no targets at all",
			peer:    workName,
			wantErr: ErrPeerNotFound,
		},
		{
			name:    "an empty peer name",
			peer:    "",
			targets: []apitype.FileTarget{workTarget()},
			wantErr: ErrPeerNotFound,
		},
		{
			name:    "a nil node in the target list is skipped",
			peer:    workName,
			targets: []apitype.FileTarget{{}, workTarget()},
			wantAny: true,
		},
		{
			name:    "the daemon cannot list targets",
			peer:    workName,
			daemon:  func(d *fakeDaemon) { d.targetsErr = errBoom },
			wantErr: errBoom,
		},
		{
			name:    "the push itself fails",
			peer:    workName,
			targets: []apitype.FileTarget{workTarget()},
			daemon:  func(d *fakeDaemon) { d.pushErr = errBoom },
			wantErr: errBoom,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeSample(t)
			d := &fakeDaemon{targets: tc.targets}
			if tc.daemon != nil {
				tc.daemon(d)
			}
			c := newTestClient(d, &fakeRunner{})

			err := c.SendFile(context.Background(), path, tc.peer)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want it to wrap %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SendFile() error = %v", err)
			}
			if !tc.wantAny {
				return
			}
			pushed := d.records()
			if len(pushed) != 1 {
				t.Fatalf("pushed %d files, want 1", len(pushed))
			}
			got := pushed[0]
			if got.target != "nWORK" || got.name != sampleName || got.body != sampleBody {
				t.Errorf("pushed %+v, want the report to nWORK", got)
			}
			if got.size != int64(len(sampleBody)) {
				t.Errorf("size = %d, want %d", got.size, len(sampleBody))
			}
		})
	}
}

func TestSendFileRejectsWhatItCannotSend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := &fakeDaemon{targets: []apitype.FileTarget{workTarget()}}
	c := newTestClient(d, &fakeRunner{})

	if err := c.SendFile(context.Background(), filepath.Join(dir, "nope.txt"), workName); err == nil {
		t.Error("SendFile() on a missing file returned nil, want an error")
	}
	if err := c.SendFile(context.Background(), dir, workName); err == nil {
		t.Error("SendFile() on a directory returned nil, want an error")
	}
	if n := len(d.records()); n != 0 {
		t.Errorf("pushed %d files, want none", n)
	}
}

func TestSendFileHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := writeSample(t)
	d := &fakeDaemon{targets: []apitype.FileTarget{workTarget()}}
	err := newTestClient(d, &fakeRunner{}).SendFile(ctx, path, workName)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if n := len(d.records()); n != 0 {
		t.Errorf("pushed %d files on a dead context, want none", n)
	}
}

func TestReceiveFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	d := &fakeDaemon{waiting: []apitype.WaitingFile{
		{Name: "notes.txt", Size: 5},
		{Name: "build.log", Size: 3},
	}}
	c := newTestClient(d, &fakeRunner{})

	got, err := c.ReceiveFiles(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReceiveFiles() error = %v", err)
	}
	want := []string{filepath.Join(dir, "notes.txt"), filepath.Join(dir, "build.log")}
	if len(got) != len(want) {
		t.Fatalf("ReceiveFiles() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], want[i])
		}
		body, readErr := os.ReadFile(got[i])
		if readErr != nil {
			t.Fatalf("reading what was written: %v", readErr)
		}
		if string(body) != "contents of "+filepath.Base(want[i]) {
			t.Errorf("file %q holds %q", got[i], body)
		}
	}
	if deleted := d.deletedNames(); len(deleted) != 2 {
		t.Errorf("cleared %v from the inbox, want both entries", deleted)
	}
}

func TestReceiveFilesKeepsExistingFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("older"), 0o600); err != nil {
		t.Fatalf("seeding the directory: %v", err)
	}

	d := &fakeDaemon{waiting: []apitype.WaitingFile{{Name: "notes.txt"}, {Name: "notes.txt"}}}
	got, err := newTestClient(d, &fakeRunner{}).ReceiveFiles(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReceiveFiles() error = %v", err)
	}
	want := []string{filepath.Join(dir, "notes (1).txt"), filepath.Join(dir, "notes (2).txt")}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ReceiveFiles() = %v, want %v", got, want)
	}
	body, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil || string(body) != "older" {
		t.Errorf("the existing file was disturbed: %q, %v", body, err)
	}
}

func TestReceiveFilesCreatesTheTargetDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "inbox", "today")
	d := &fakeDaemon{waiting: []apitype.WaitingFile{{Name: "notes.txt"}}}

	got, err := newTestClient(d, &fakeRunner{}).ReceiveFiles(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReceiveFiles() error = %v", err)
	}
	if len(got) != 1 || filepath.Dir(got[0]) != dir {
		t.Fatalf("ReceiveFiles() = %v, want one file under %s", got, dir)
	}
}

func TestReceiveFilesEmptyInbox(t *testing.T) {
	t.Parallel()
	got, err := newTestClient(&fakeDaemon{}, &fakeRunner{}).ReceiveFiles(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("ReceiveFiles() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReceiveFiles() = %v, want nothing", got)
	}
}

// failingReader stands in for a stream that dies mid-transfer.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errBoom }

func TestReceiveFilesFailurePaths(t *testing.T) {
	t.Parallel()

	escaping := ".." + string(filepath.Separator) + "Windows" + string(filepath.Separator) + "evil.dll"

	tests := []struct {
		name      string
		daemon    *fakeDaemon
		wantErr   error
		wantPaths int
	}{
		{
			name:    "the daemon cannot list the inbox",
			daemon:  &fakeDaemon{waitingErr: errBoom},
			wantErr: errBoom,
		},
		{
			name:    "a name that tries to escape the directory",
			daemon:  &fakeDaemon{waiting: []apitype.WaitingFile{{Name: escaping}}},
			wantErr: errUnsafeName,
		},
		{
			name:    "a bare parent reference",
			daemon:  &fakeDaemon{waiting: []apitype.WaitingFile{{Name: ".."}}},
			wantErr: errUnsafeName,
		},
		{
			name:    "an absolute path with a volume",
			daemon:  &fakeDaemon{waiting: []apitype.WaitingFile{{Name: "C:evil.dll"}}},
			wantErr: errUnsafeName,
		},
		{
			name:    "an empty name",
			daemon:  &fakeDaemon{waiting: []apitype.WaitingFile{{Name: "   "}}},
			wantErr: errUnsafeName,
		},
		{
			name: "the download breaks",
			daemon: &fakeDaemon{
				waiting: []apitype.WaitingFile{{Name: "notes.txt"}},
				getFn: func(string) (io.ReadCloser, int64, error) {
					return nil, 0, errBoom
				},
			},
			wantErr: errBoom,
		},
		{
			name: "the stream dies halfway",
			daemon: &fakeDaemon{
				waiting: []apitype.WaitingFile{{Name: "notes.txt"}},
				getFn: func(string) (io.ReadCloser, int64, error) {
					return io.NopCloser(failingReader{}), 10, nil
				},
			},
			wantErr: errBoom,
		},
		{
			name: "the inbox entry cannot be cleared",
			daemon: &fakeDaemon{
				waiting:   []apitype.WaitingFile{{Name: "notes.txt"}},
				deleteErr: errBoom,
			},
			wantErr:   errBoom,
			wantPaths: 1,
		},
		{
			name: "the second file fails, the first is still reported",
			daemon: &fakeDaemon{
				waiting: []apitype.WaitingFile{{Name: "notes.txt"}, {Name: ".."}},
			},
			wantErr:   errUnsafeName,
			wantPaths: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := newTestClient(tc.daemon, &fakeRunner{}).
				ReceiveFiles(context.Background(), t.TempDir())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want it to wrap %v", err, tc.wantErr)
			}
			if len(got) != tc.wantPaths {
				t.Errorf("ReceiveFiles() = %v, want %d path(s)", got, tc.wantPaths)
			}
		})
	}
}

func TestReceiveFilesHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := &fakeDaemon{waiting: []apitype.WaitingFile{{Name: "notes.txt"}}}
	_, err := newTestClient(d, &fakeRunner{}).ReceiveFiles(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFreePathGivesUpEventually(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := range 101 {
		name := "notes.txt"
		if i > 0 {
			name = "notes (" + strconv.Itoa(i) + ").txt"
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	if _, err := freePath(dir, "notes.txt"); err == nil {
		t.Error("freePath() found a free name among 101 taken ones, want an error")
	}
}
