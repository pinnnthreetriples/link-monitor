package localendpoint

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// These are the seams the shared clipboard reads the *peer's* record through.
// The record arrives over SSH as bytes and the process behind its pid is one
// only the peer can be asked about, so the two halves are exercised here on
// their own — with no filesystem and no process at all.

func TestRelativePathIsWhereTheRecordSitsUnderLocalAppData(t *testing.T) {
	t.Parallel()

	got := RelativePath()
	if !strings.Contains(got, dirName) || !strings.Contains(got, fileName) {
		t.Errorf("RelativePath() = %q, want it to name %s and %s", got, dirName, fileName)
	}
	// A reader on the peer joins this onto that machine's %LOCALAPPDATA%, so
	// it must be relative and spelled the way Windows spells a path.
	if strings.HasPrefix(got, `\`) || strings.Contains(got, "/") || strings.Contains(got, ":") {
		t.Errorf("RelativePath() = %q, want a relative Windows path", got)
	}
}

// The relative path must agree with the absolute one this package publishes
// to, or a reader on the peer would look somewhere the writer never writes.
// Not parallel: it sets an environment variable.
func TestRelativePathAgreesWithThePathThisPackagePublishesTo(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\somebody\AppData\Local`)

	absolute, err := Path()
	if err != nil {
		t.Fatalf("Path() = %v", err)
	}
	if !strings.HasSuffix(absolute, RelativePath()) {
		t.Errorf("Path() = %q does not end in RelativePath() = %q", absolute, RelativePath())
	}
}

func TestParseAcceptsAPublishedRecordAndRefusesAnythingElse(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(good())
	if err != nil {
		t.Fatalf("marshalling the record: %v", err)
	}
	rec, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() = %v, want the record", err)
	}
	if rec.PID != good().PID || rec.BaseURL != good().BaseURL {
		t.Errorf("Parse() = %+v, want %+v", rec, good())
	}

	// Both refusals are ErrStale, because to a caller they mean the same
	// thing: there is no instance over there to hand anything to.
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"not JSON at all", "this is not json"},
		{"JSON that is not a record", `{"version":1}`},
		{"a record naming somebody else's address", `{"version":1,"baseURL":` +
			`"http://10.0.0.5:8080/","pid":42,"exe":"x","startedAt":"2026-09-08T18:10:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse([]byte(tc.raw)); !errors.Is(err, ErrStale) {
				t.Errorf("Parse(%q) = %v, want ErrStale", tc.raw, err)
			}
		})
	}
}

// Parse must be exactly as strict as Find is, or a record refused on this
// machine would be believed when it came from the peer.
func TestParseIsAsStrictAsTheLocalReader(t *testing.T) {
	t.Parallel()

	rec := good()
	rec.BaseURL = "http://127.0.0.1:52341/api/files/send"
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if _, err := Parse(data); !errors.Is(err, ErrStale) {
		t.Errorf("Parse() accepted a base URL with a path on it: %v", err)
	}
}

func TestConfirmAcceptsOnlyWhatTheOperatingSystemAgreesWith(t *testing.T) {
	t.Parallel()

	rec := good()
	if err := rec.Confirm(theExe, theStart); err != nil {
		t.Errorf("Confirm() with the same process = %v, want it accepted", err)
	}
	for _, tc := range []struct {
		name      string
		exe       string
		startedAt time.Time
	}{
		{"the pid now belongs to something else", `C:\Windows\notepad.exe`, theStart},
		{"the pid was reused by another copy of us", theExe, theStart.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := rec.Confirm(tc.exe, tc.startedAt)
			if !errors.Is(err, ErrStale) {
				t.Errorf("Confirm() = %v, want ErrStale", err)
			}
			if !NotRunning(err) {
				t.Errorf("NotRunning(%v) = false, want true", err)
			}
		})
	}
}

func TestPortIsReadOutOfTheOneAddressTheRecordCarries(t *testing.T) {
	t.Parallel()

	rec := good()
	port, err := rec.Port()
	if err != nil {
		t.Fatalf("Port() = %v", err)
	}
	if port != 52341 {
		t.Errorf("Port() = %d, want 52341", port)
	}

	for _, tc := range []struct {
		name string
		url  string
	}{
		{"no port", "http://127.0.0.1/"},
		{"not a number", "http://127.0.0.1:порт/"},
		{"not a URL at all", "http://127.0.0.1:%zz/"},
		{"zero", "http://127.0.0.1:0/"},
		{"past the range", "http://127.0.0.1:70000/"},
		{"nothing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assembled := Record{BaseURL: tc.url}
			if _, err := assembled.Port(); !errors.Is(err, ErrStale) {
				t.Errorf("Port() for %q = %v, want ErrStale", tc.url, err)
			}
		})
	}
}
