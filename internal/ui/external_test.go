package ui

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// loginURL stands in for what `tailscale up` prints: a one-time credential
// whose whole secret is the path. Nothing about it beyond the host may reach a
// log line, an error string or a window title.
const (
	loginURL   = "https://login.tailscale.com/a/9f3c1d7e5b2a4086"
	loginHost  = "login.tailscale.com"
	loginToken = "9f3c1d7e5b2a4086"
	loginPath  = "/a/" + loginToken
)

// recordingOpener returns an [ExternalOpener] with a fake runner and a logger
// writing into a buffer, so a test can read back everything it said.
func recordingOpener(err error) (*ExternalOpener, *fakeRunner, *bytes.Buffer) {
	var logged bytes.Buffer
	r := &fakeRunner{err: err}
	o := &ExternalOpener{
		runner: r,
		log:    slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return o, r, &logged
}

func TestExternalOpenerRefusesEveryOtherScheme(t *testing.T) {
	// The addresses in the page come from `tailscale status` and `tailscale
	// serve`. A scheme this does not recognise must never become a way to
	// start a program through the shell's URL handler.
	tests := map[string]string{
		"a file":          "file:///C:/Windows/System32/calc.exe",
		"a script":        "javascript:alert(1)",
		"windows setting": "ms-settings:windowsupdate",
		"mail":            "mailto:someone@example.com",
		"a shell verb":    "shell:startup",
		"a UNC path":      `\\WIN-STTM11D02RD\C$`,
		"a bare program":  "calc.exe",
		"no host":         "http:///a/b",
		"empty":           "",
		"unparseable":     "http://[::1",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			o, r, _ := recordingOpener(nil)

			err := o.Open(t.Context(), raw)
			if err == nil {
				t.Fatalf("Open(%q) accepted it", raw)
			}
			if !errors.Is(err, errNotWebURL) {
				t.Errorf("Open(%q) = %v, want it to wrap errNotWebURL", raw, err)
			}
			if r.runs != 0 {
				t.Errorf("Open(%q) ran %d commands for a URL it refused", raw, r.runs)
			}
		})
	}
}

func TestExternalOpenerNeverWritesDownAOneTimeURL(t *testing.T) {
	// A Tailscale login URL is a credential. Whether the launch succeeds or
	// fails, neither the path nor the query may appear in the log or in the
	// error — which is why this asserts about the output rather than about the
	// outcome, and holds on a platform that cannot open anything at all.
	for _, runnerErr := range []error{nil, errFake} {
		o, _, logged := recordingOpener(runnerErr)

		err := o.Open(t.Context(), loginURL+"?next=%2Fadmin")

		for _, secret := range []string{loginToken, loginPath, "next=", loginURL} {
			if strings.Contains(logged.String(), secret) {
				t.Errorf("the log leaked %q:\n%s", secret, logged.String())
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Errorf("the error leaked %q: %v", secret, err)
			}
		}
		// The host is the part worth knowing and the part that is safe.
		if !strings.Contains(logged.String(), loginHost) {
			t.Errorf("the log does not say which host was opened:\n%s", logged.String())
		}
	}
}

func TestExternalOpenerReportsAFailureWithoutTheRunnersWords(t *testing.T) {
	// The runner's error names the command line, which carries the URL, so it
	// must not be wrapped — only the fact of the failure is passed on.
	o, _, _ := recordingOpener(errFake)

	err := o.Open(t.Context(), loginURL)
	if err == nil {
		t.Fatal("Open hid the failure")
	}
	if !errors.Is(err, errExternalOpenFailed) {
		t.Errorf("Open = %v, want errExternalOpenFailed", err)
	}
	if errors.Is(err, errFake) {
		t.Error("Open wrapped the runner's error, whose text quotes the URL")
	}
}

func TestExternalOpenerDoesNotWidenTheLoopbackRule(t *testing.T) {
	// The two openers are separate on purpose: the narrow one must stay
	// narrow, and only the external one may leave this machine.
	if err := NewSystemOpener().Open(t.Context(), loginURL); err == nil {
		t.Error("SystemOpener accepted an address off this machine")
	}
}

func TestNewExternalOpenerUsesTheRealRunnerAndAWorkingLogger(t *testing.T) {
	o := NewExternalOpener(nil)
	if _, ok := o.runner.(execRunner); !ok {
		t.Errorf("runner = %T, want the real one", o.runner)
	}
	if o.log == nil {
		t.Error("a nil logger was left nil rather than defaulted")
	}
}
