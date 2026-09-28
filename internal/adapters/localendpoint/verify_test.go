package localendpoint

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// theExe and theStart are one plausible instance, used wherever a test needs a
// record that ought to be accepted.
const theExe = `C:\Apps\LinkMonitor\linkmon.exe`

var theStart = time.Date(2026, 9, 8, 18, 10, 0, 1234500, time.UTC)

// good returns a record a reader should accept.
func good() Record {
	return Record{
		Version:   Version,
		BaseURL:   "http://127.0.0.1:52341/",
		PID:       4242,
		Exe:       theExe,
		StartedAt: theStart,
	}
}

func TestValidateAcceptsOnlyARecordWorthActingOn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		mutate  func(*Record)
		wantErr bool
	}{
		{name: "a freshly published record", mutate: func(*Record) {}},
		{
			name:   "localhost is this machine too",
			mutate: func(r *Record) { r.BaseURL = "http://localhost:52341/" },
		},
		{
			name:   "IPv6 loopback is this machine too",
			mutate: func(r *Record) { r.BaseURL = "http://[::1]:52341/" },
		},
		{
			name:   "no trailing slash is still a base URL",
			mutate: func(r *Record) { r.BaseURL = "http://127.0.0.1:52341" },
		},
		{
			name:    "a version this build does not know",
			mutate:  func(r *Record) { r.Version = Version + 1 },
			wantErr: true,
		},
		{
			name:    "a version this build has outgrown",
			mutate:  func(r *Record) { r.Version = 0 },
			wantErr: true,
		},
		{name: "no process id", mutate: func(r *Record) { r.PID = 0 }, wantErr: true},
		{name: "a negative process id", mutate: func(r *Record) { r.PID = -1 }, wantErr: true},
		{name: "no executable", mutate: func(r *Record) { r.Exe = "" }, wantErr: true},
		{name: "a blank executable", mutate: func(r *Record) { r.Exe = "   " }, wantErr: true},
		{name: "no start time", mutate: func(r *Record) { r.StartedAt = time.Time{} }, wantErr: true},
		{
			name:    "an address on the network",
			mutate:  func(r *Record) { r.BaseURL = "http://100.127.188.87:52341/" },
			wantErr: true,
		},
		{
			name:    "an address somebody else owns",
			mutate:  func(r *Record) { r.BaseURL = "http://example.com/" },
			wantErr: true,
		},
		{
			name:    "a scheme this program does not serve",
			mutate:  func(r *Record) { r.BaseURL = "https://127.0.0.1:52341/" },
			wantErr: true,
		},
		{
			name:    "a scheme that is not a web address at all",
			mutate:  func(r *Record) { r.BaseURL = `file:///C:/Windows/System32/calc.exe` },
			wantErr: true,
		},
		{
			name:    "no port to talk to",
			mutate:  func(r *Record) { r.BaseURL = "http://127.0.0.1/" },
			wantErr: true,
		},
		{
			name:    "a path routes would be joined onto",
			mutate:  func(r *Record) { r.BaseURL = "http://127.0.0.1:52341/somewhere/else" },
			wantErr: true,
		},
		{
			name:    "a query string",
			mutate:  func(r *Record) { r.BaseURL = "http://127.0.0.1:52341/?to=elsewhere" },
			wantErr: true,
		},
		{
			name:    "credentials in the address",
			mutate:  func(r *Record) { r.BaseURL = "http://someone:secret@127.0.0.1:52341/" },
			wantErr: true,
		},
		{name: "no address at all", mutate: func(r *Record) { r.BaseURL = "" }, wantErr: true},
		{
			name:    "an address that cannot be parsed",
			mutate:  func(r *Record) { r.BaseURL = "http://[::1" },
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := good()
			tc.mutate(&rec)

			err := validate(rec)
			if tc.wantErr {
				if !errors.Is(err, errBadRecord) {
					t.Fatalf("validate(%+v) = %v, want an errBadRecord", rec, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate(%+v) = %v, want no error", rec, err)
			}
		})
	}
}

func TestConfirmAcceptsOnlyTheProcessThatPublishedTheRecord(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		exe       string
		startedAt time.Time
		wantErr   bool
	}{
		{
			name:      "the same process is still running",
			exe:       theExe,
			startedAt: theStart,
		},
		{
			name:      "the path Windows reports differs only in case",
			exe:       `c:\apps\linkmonitor\LINKMON.EXE`,
			startedAt: theStart,
		},
		{
			name:      "a difference finer than a FILETIME tick is not a difference",
			exe:       theExe,
			startedAt: theStart.Add(37 * time.Nanosecond),
			wantErr:   false,
		},
		{
			name:      "the pid now belongs to something else",
			exe:       `C:\Windows\System32\notepad.exe`,
			startedAt: theStart,
			wantErr:   true,
		},
		{
			name:      "the pid now belongs to another copy of this exe",
			exe:       theExe,
			startedAt: theStart.Add(time.Second),
			wantErr:   true,
		},
		{
			name:      "the machine rebooted and the pid came round again",
			exe:       theExe,
			startedAt: theStart.Add(-90 * time.Minute),
			wantErr:   true,
		},
		{
			name:      "one tick apart is a different process",
			exe:       theExe,
			startedAt: theStart.Add(tick),
			wantErr:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := confirm(good(), tc.exe, tc.startedAt)
			if tc.wantErr {
				if !errors.Is(err, errWrongProcess) {
					t.Fatalf("confirm(exe=%q, startedAt=%s) = %v, want an errWrongProcess",
						tc.exe, tc.startedAt, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("confirm(exe=%q, startedAt=%s) = %v, want no error", tc.exe, tc.startedAt, err)
			}
		})
	}
}

// TestConfirmErrorsSayNothingSecret is a standing check on the one rule this
// package could break by accident: the record is not a secret, but the error
// text goes to a log file, so it names the exe by base name and never repeats
// the address.
func TestConfirmErrorsSayNothingSecret(t *testing.T) {
	t.Parallel()

	err := confirm(good(), `C:\Windows\System32\notepad.exe`, theStart)
	if err == nil {
		t.Fatal("confirm accepted a different executable")
	}
	if got := err.Error(); strings.Contains(got, "52341") {
		t.Errorf("confirm's error repeats the port: %q", got)
	}
	if got := err.Error(); strings.Contains(got, `C:\Windows\System32`) {
		t.Errorf("confirm's error repeats a full path where a base name would do: %q", got)
	}
}
