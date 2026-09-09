package ui

import (
	"errors"
	"testing"
)

func TestCheckLoopbackURLAccepts(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:8731/",
		"http://localhost:8731/#log",
		"https://127.0.0.1/",
		"http://[::1]:8731/",
		"http://127.0.0.2:9/",
	} {
		if err := checkLoopbackURL(raw); err != nil {
			t.Errorf("checkLoopbackURL(%q) = %v, want nil", raw, err)
		}
	}
}

func TestCheckLoopbackURLRejects(t *testing.T) {
	tests := map[string]string{
		"empty":            "",
		"blank":            "   ",
		"another machine":  "http://100.127.188.87:8731/",
		"a name":           "http://example.com/",
		"a file":           "file:///C:/Windows/System32/calc.exe",
		"a program":        "calc.exe",
		"no scheme":        "127.0.0.1:8731",
		"unparseable":      "http://[::1",
		"a foreign scheme": "javascript:alert(1)",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if err := checkLoopbackURL(raw); err == nil {
				t.Fatalf("checkLoopbackURL(%q) accepted it", raw)
			}
		})
	}
}

func TestSystemOpenerRefusesToLaunchAnythingButTheWindow(t *testing.T) {
	r := &fakeRunner{}
	o := &SystemOpener{runner: r}

	got := o.Open(t.Context(), "http://example.com/")
	if got == nil {
		t.Fatal("Open accepted a URL off this machine")
	}
	if !errors.Is(got, errNotLoopback) {
		t.Errorf("Open = %v, want it to wrap errNotLoopback", got)
	}
	if r.runs != 0 {
		t.Errorf("ran %d commands for a URL it should have refused", r.runs)
	}
}

func TestSystemOpenerReportsARunnerFailure(t *testing.T) {
	o := &SystemOpener{runner: &fakeRunner{err: errFake}}

	got := o.Open(t.Context(), "http://127.0.0.1:8731/")
	if got == nil {
		t.Fatal("Open hid the failure")
	}
	if !errors.Is(got, errFake) {
		t.Errorf("Open = %v, want it to wrap the runner's error", got)
	}
}

func TestNewSystemOpenerUsesTheRealRunner(t *testing.T) {
	if _, ok := NewSystemOpener().runner.(execRunner); !ok {
		t.Error("NewSystemOpener did not wire in the real runner")
	}
}
