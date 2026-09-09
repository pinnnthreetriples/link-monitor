package window

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// loginURL is the shape of a Tailscale login URL: everything after the host is
// a one-time credential. No test may find it in a log or an error.
const loginURL = "https://login.tailscale.com/a/6f3e1c9b2d4a8e57"

// loginSecret is the part of it that must never be written down.
const loginSecret = "6f3e1c9b2d4a8e57"

// recorder is a logger that keeps everything it was told, so a test can check
// what would have reached the journal.
type recorder struct {
	buf bytes.Buffer
	log *slog.Logger
}

func newRecorder() *recorder {
	r := &recorder{}
	r.log = slog.New(slog.NewTextHandler(&r.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return r
}

func (r *recorder) text() string { return r.buf.String() }

func TestAnExternalLinkGoesToTheOpenerUntouched(t *testing.T) {
	t.Parallel()

	var got string
	rec := newRecorder()
	links := newExternalLinks(Config{
		Logger: rec.log,
		OpenExternal: func(rawURL string) error {
			got = rawURL
			return nil
		},
	})

	if err := links.handle(loginURL); err != nil {
		t.Fatalf("handle(%s) = %v, want no error", linkHost(loginURL), err)
	}
	if got != loginURL {
		t.Errorf("the opener was handed a different URL than the page gave")
	}
	if strings.Contains(rec.text(), loginSecret) {
		t.Errorf("the login credential reached the log:\n%s", rec.text())
	}
	if !strings.Contains(rec.text(), "login.tailscale.com") {
		t.Errorf("the log does not name the host, so a failure could not be traced:\n%s", rec.text())
	}
}

func TestWithNoOpenerNothingHappensAndNothingIsRecorded(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	links := newExternalLinks(Config{Logger: rec.log})

	err := links.handle(loginURL)
	if !errors.Is(err, errNoExternalOpener) {
		t.Fatalf("handle() = %v, want errNoExternalOpener", err)
	}
	if strings.Contains(err.Error(), loginSecret) {
		t.Errorf("the credential reached the error returned to the page: %v", err)
	}
	if strings.Contains(rec.text(), loginSecret) {
		t.Errorf("the credential reached the log:\n%s", rec.text())
	}
}

func TestAFailedOpenNeverRepeatsTheURL(t *testing.T) {
	t.Parallel()

	rec := newRecorder()
	links := newExternalLinks(Config{
		Logger: rec.log,
		// Every opener in this program quotes the URL it was handed. That is
		// exactly why its message is not passed on.
		OpenExternal: func(rawURL string) error {
			return fmt.Errorf("opening %q: %w", rawURL, errors.New("rundll32 failed"))
		},
	})

	err := links.handle(loginURL)
	if !errors.Is(err, errExternalOpenFailed) {
		t.Fatalf("handle() = %v, want errExternalOpenFailed", err)
	}
	if strings.Contains(err.Error(), loginSecret) {
		t.Errorf("the credential reached the error returned to the page: %v", err)
	}
	if strings.Contains(rec.text(), loginSecret) {
		t.Errorf("the credential reached the log:\n%s", rec.text())
	}
	if !strings.Contains(rec.text(), "login.tailscale.com") {
		t.Errorf("a failed open left nothing traceable in the log:\n%s", rec.text())
	}
}

func TestOnlyWebLinksLeaveTheWindow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "a local file", url: `file:///C:/Windows/System32/cmd.exe`},
		{name: "a shell handler", url: "ms-settings:windowsupdate"},
		{name: "a mail link", url: "mailto:someone@example.com"},
		{name: "a javascript URL", url: "javascript:alert(1)"},
		{name: "a scheme with no host", url: "http:///just-a-path"},
		{name: "nothing at all", url: ""},
		{name: "something unparseable", url: "http://%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			called := false
			rec := newRecorder()
			links := newExternalLinks(Config{
				Logger:       rec.log,
				OpenExternal: func(string) error { called = true; return nil },
			})

			if err := links.handle(tc.url); !errors.Is(err, errNotWebLink) {
				t.Errorf("handle(%q) = %v, want errNotWebLink", tc.url, err)
			}
			if called {
				t.Errorf("handle(%q) handed the address to the opener", tc.url)
			}
		})
	}
}

func TestAnOrdinaryWebLinkIsAccepted(t *testing.T) {
	t.Parallel()

	for _, url := range []string{
		loginURL,
		"http://100.124.47.73:8080/",
		"https://laptop.tailnet-1234.ts.net/",
		"http://localhost:8765/",
	} {
		links := newExternalLinks(Config{Logger: quiet(), OpenExternal: func(string) error { return nil }})
		if err := links.handle(url); err != nil {
			t.Errorf("handle(%q) = %v, want it opened", url, err)
		}
	}
}

func TestLinkHostNamesOnlyWhatIsSafeToLog(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{name: "a login URL keeps only its host", url: loginURL, want: "login.tailscale.com"},
		{name: "the port is part of the host", url: "http://127.0.0.1:8765/x", want: "127.0.0.1:8765"},
		{name: "a query string is dropped", url: "https://h/x?token=secret", want: "h"},
		{name: "a fragment is dropped", url: "https://h/x#secret", want: "h"},
		{name: "an unparseable URL has no host", url: "http://%zz", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := linkHost(tc.url); got != tc.want {
				t.Errorf("linkHost(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// TestTheBoundNameIsTheOneThePageCalls guards the string the two halves have
// to agree on. frontend/desktop.js calls window.openExternal; a rename here
// would silently send every external link to its fallback.
func TestTheBoundNameIsTheOneThePageCalls(t *testing.T) {
	t.Parallel()

	if externalBindName != "openExternal" {
		t.Errorf("the bound name is %q, but the page calls window.openExternal", externalBindName)
	}
}
