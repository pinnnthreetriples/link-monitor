package localendpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordPath is where these tests pretend the record lives. It never reaches a
// filesystem: fakeFiles keys its map by it.
const recordPath = `C:\Users\pnj\AppData\Local\LinkMonitor\endpoint.json`

// thePID is the process id the fake instance runs under.
const thePID = 4242

// fakeFiles is the filesystem seam, in memory.
type fakeFiles struct {
	data     map[string][]byte
	dirs     []string
	readErr  error
	writeErr error
	removeCn int
	removeEr error
}

func newFakeFiles() *fakeFiles { return &fakeFiles{data: map[string][]byte{}} }

func (f *fakeFiles) mkdirAll(dir string) error {
	f.dirs = append(f.dirs, dir)
	return nil
}

func (f *fakeFiles) writeFile(path string, data []byte) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.data[path] = append([]byte(nil), data...)
	return nil
}

func (f *fakeFiles) readFile(path string) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	data, ok := f.data[path]
	if !ok {
		return nil, fmt.Errorf("reading %s: %w", path, fs.ErrNotExist)
	}
	return data, nil
}

func (f *fakeFiles) remove(path string) error {
	f.removeCn++
	if f.removeEr != nil {
		return f.removeEr
	}
	delete(f.data, path)
	return nil
}

// fakeProcess is the operating system's view of a process, in a map. An absent
// pid is a process that is not running, which is the case that matters most.
type fakeProcess struct {
	live map[int]struct {
		exe       string
		startedAt time.Time
	}
}

func newFakeProcess(pid int, exe string, startedAt time.Time) *fakeProcess {
	p := &fakeProcess{live: map[int]struct {
		exe       string
		startedAt time.Time
	}{}}
	p.set(pid, exe, startedAt)
	return p
}

func (p *fakeProcess) set(pid int, exe string, startedAt time.Time) {
	p.live[pid] = struct {
		exe       string
		startedAt time.Time
	}{exe: exe, startedAt: startedAt}
}

func (p *fakeProcess) kill(pid int) { delete(p.live, pid) }

func (p *fakeProcess) info(pid int) (string, time.Time, error) {
	got, ok := p.live[pid]
	if !ok {
		return "", time.Time{}, fmt.Errorf("looking up process %d: no such process", pid)
	}
	return got.exe, got.startedAt, nil
}

// liveDeps is a machine with our instance running under thePID.
func liveDeps() (deps, *fakeFiles, *fakeProcess) {
	files := newFakeFiles()
	proc := newFakeProcess(thePID, theExe, theStart)
	return deps{files: files, process: proc}, files, proc
}

func TestPublishThenFindRoundTripsTheRecord(t *testing.T) {
	t.Parallel()

	d, files, _ := liveDeps()

	remove, err := publish(d, recordPath, thePID, "http://127.0.0.1:52341/")
	if err != nil {
		t.Fatalf("publish() = %v, want a record", err)
	}
	if remove == nil {
		t.Fatal("publish returned no remover")
	}
	if want := filepath.Dir(recordPath); len(files.dirs) != 1 || files.dirs[0] != want {
		t.Errorf("publish created %v, want just %q", files.dirs, want)
	}

	rec, err := find(d, recordPath)
	if err != nil {
		t.Fatalf("find() = %v, want the published record", err)
	}
	want := Record{
		Version: Version, BaseURL: "http://127.0.0.1:52341/",
		PID: thePID, Exe: theExe, StartedAt: theStart,
	}
	if rec.Version != want.Version || rec.BaseURL != want.BaseURL || rec.PID != want.PID ||
		rec.Exe != want.Exe || !rec.StartedAt.Equal(want.StartedAt) {
		t.Errorf("find() = %+v, want %+v", rec, want)
	}
}

// TestThePublishedFileIsReadableJSONAndHoldsNothingSecret is the standing check
// on the package comment's promise: a person can open the file, and there is
// nothing in it that would matter if they did.
func TestThePublishedFileIsReadableJSONAndHoldsNothingSecret(t *testing.T) {
	t.Parallel()

	d, files, _ := liveDeps()
	if _, err := publish(d, recordPath, thePID, "http://127.0.0.1:52341/"); err != nil {
		t.Fatalf("publish() = %v, want a record", err)
	}

	raw := string(files.data[recordPath])
	if !strings.HasSuffix(raw, "\n") {
		t.Error("the record does not end in a newline; a person will read this in a terminal")
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("the record is not JSON: %v\n%s", err, raw)
	}
	want := map[string]bool{"version": true, "baseURL": true, "pid": true, "exe": true, "startedAt": true}
	for name := range fields {
		if !want[name] {
			t.Errorf("the record carries an unexpected field %q; only a signpost belongs here", name)
		}
	}
	for name := range want {
		if _, ok := fields[name]; !ok {
			t.Errorf("the record is missing %q", name)
		}
	}
	for _, forbidden := range []string{"token", "secret", "key", "password"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Errorf("the record mentions %q; this file is a signpost, not a keyring:\n%s", forbidden, raw)
		}
	}
}

func TestFindRefusesEveryRecordThatIsNotALiveInstance(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// arrange leaves the machine in the state under test, having already
		// published a good record.
		arrange func(*fakeFiles, *fakeProcess)
		wantErr error
	}{
		{
			name:    "nothing was ever published",
			arrange: func(f *fakeFiles, _ *fakeProcess) { delete(f.data, recordPath) },
			wantErr: ErrNoRecord,
		},
		{
			name:    "the instance was killed and the file survived",
			arrange: func(_ *fakeFiles, p *fakeProcess) { p.kill(thePID) },
			wantErr: ErrStale,
		},
		{
			name: "the machine rebooted and the pid came round again",
			arrange: func(_ *fakeFiles, p *fakeProcess) {
				p.set(thePID, theExe, theStart.Add(-3*time.Hour))
			},
			wantErr: ErrStale,
		},
		{
			name: "the pid now belongs to an unrelated program",
			arrange: func(_ *fakeFiles, p *fakeProcess) {
				p.set(thePID, `C:\Windows\System32\svchost.exe`, theStart)
			},
			wantErr: ErrStale,
		},
		{
			name: "the file was truncated mid-write",
			arrange: func(f *fakeFiles, _ *fakeProcess) {
				f.data[recordPath] = []byte(`{"version":1,"baseURL":"http`)
			},
			wantErr: ErrStale,
		},
		{
			name: "the file was written by a newer build",
			arrange: func(f *fakeFiles, _ *fakeProcess) {
				f.data[recordPath] = mustJSON(t, Record{
					Version: Version + 1, BaseURL: "http://127.0.0.1:52341/",
					PID: thePID, Exe: theExe, StartedAt: theStart,
				})
			},
			wantErr: ErrStale,
		},
		{
			name: "somebody edited the address to point off the machine",
			arrange: func(f *fakeFiles, _ *fakeProcess) {
				f.data[recordPath] = mustJSON(t, Record{
					Version: Version, BaseURL: "http://198.51.100.7:8080/",
					PID: thePID, Exe: theExe, StartedAt: theStart,
				})
			},
			wantErr: ErrStale,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d, files, proc := liveDeps()
			if _, err := publish(d, recordPath, thePID, "http://127.0.0.1:52341/"); err != nil {
				t.Fatalf("publish() = %v, want a record", err)
			}
			tc.arrange(files, proc)

			rec, err := find(d, recordPath)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("find() = (%+v, %v), want %v", rec, err, tc.wantErr)
			}
			if !NotRunning(err) {
				t.Errorf("NotRunning(%v) = false; the caller would report a fault instead of doing the job", err)
			}
			if rec != (Record{}) {
				t.Errorf("find() returned %+v alongside its refusal", rec)
			}
		})
	}
}

// TestFindReportsARealFailureAsItself keeps the refusals apart from the faults:
// a directory this user cannot read is not "the program is not running", and a
// caller must not quietly do the job twice because of one.
func TestFindReportsARealFailureAsItself(t *testing.T) {
	t.Parallel()

	d, files, _ := liveDeps()
	files.readErr = errors.New("the hive is not readable")

	_, err := find(d, recordPath)
	if err == nil {
		t.Fatal("find() accepted an unreadable record file")
	}
	if NotRunning(err) {
		t.Fatalf("find() = %v, reported as \"not running\"; an unreadable file is a fault", err)
	}
}

func TestRemoveTakesTheRecordBackAndIsIdempotent(t *testing.T) {
	t.Parallel()

	d, files, _ := liveDeps()
	remove, err := publish(d, recordPath, thePID, "http://127.0.0.1:52341/")
	if err != nil {
		t.Fatalf("publish() = %v, want a record", err)
	}

	for range 3 {
		if err := remove(); err != nil {
			t.Fatalf("remove() = %v, want no error", err)
		}
	}
	if files.removeCn != 1 {
		t.Errorf("remove() reached the filesystem %d times, want exactly 1", files.removeCn)
	}
	if _, err := find(d, recordPath); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("find() after remove = %v, want ErrNoRecord", err)
	}
}

func TestRemoveReportsTheSameFailureEveryTime(t *testing.T) {
	t.Parallel()

	d, files, _ := liveDeps()
	remove, err := publish(d, recordPath, thePID, "http://127.0.0.1:52341/")
	if err != nil {
		t.Fatalf("publish() = %v, want a record", err)
	}
	files.removeEr = errors.New("the file is held open")

	first := remove()
	if first == nil {
		t.Fatal("remove() = nil, want the failure")
	}
	if second := remove(); second == nil || second.Error() != first.Error() {
		t.Errorf("the second remove() = %v, want the same answer as the first (%v)", second, first)
	}
}

func TestPublishRefusesWhatAReaderWouldRefuse(t *testing.T) {
	t.Parallel()

	d, files, _ := liveDeps()

	if _, err := publish(d, recordPath, thePID, "http://100.127.188.87:8080/"); err == nil {
		t.Fatal("publish() accepted an address off this machine")
	}
	if len(files.data) != 0 {
		t.Errorf("publish() wrote %d files after refusing the address", len(files.data))
	}
}

func TestPublishFailsWhenTheOperatingSystemWillNotDescribeThisProcess(t *testing.T) {
	t.Parallel()

	d, files, proc := liveDeps()
	proc.kill(thePID)

	if _, err := publish(d, recordPath, thePID, "http://127.0.0.1:52341/"); err == nil {
		t.Fatal("publish() wrote a record it could not have confirmed")
	}
	if len(files.data) != 0 {
		t.Errorf("publish() wrote %d files after failing", len(files.data))
	}
}

func TestPublishReportsAFailedWrite(t *testing.T) {
	t.Parallel()

	d, files, _ := liveDeps()
	files.writeErr = errors.New("the disk is full")

	if _, err := publish(d, recordPath, thePID, "http://127.0.0.1:52341/"); err == nil {
		t.Fatal("publish() = nil, want the write failure")
	}
}

func TestNotRunningAnswersOnlyForTheTwoRefusals(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "no record", err: fmt.Errorf("looking: %w", ErrNoRecord), want: true},
		{name: "a stale record", err: fmt.Errorf("confirming: %w", ErrStale), want: true},
		{name: "a real failure", err: errors.New("the hive is not readable"), want: false},
		{name: "no error at all", err: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := NotRunning(tc.err); got != tc.want {
				t.Errorf("NotRunning(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// mustJSON encodes a record the way publish does, for the tests that need to
// plant one directly.
func mustJSON(t *testing.T, rec Record) []byte {
	t.Helper()

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("encoding %+v: %v", rec, err)
	}
	return append(data, '\n')
}
