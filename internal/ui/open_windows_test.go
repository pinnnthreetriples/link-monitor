//go:build windows

package ui

import (
	"slices"
	"strings"
	"testing"
)

func TestOpenCommandHandsTheURLOverWhole(t *testing.T) {
	const raw = "http://127.0.0.1:8731/?tab=связь&x=1"

	name, args, err := openCommand(raw)
	if err != nil {
		t.Fatalf("openCommand: %v", err)
	}
	if !strings.EqualFold(name, "rundll32.exe") {
		t.Errorf("command = %q, want rundll32.exe", name)
	}
	if len(args) != 2 || args[0] != "url.dll,FileProtocolHandler" {
		t.Fatalf("args = %q, want the FileProtocolHandler entry point", args)
	}
	// The URL must arrive as one argument. Splitting it, or routing it through
	// a shell, is how an ampersand turns into a second command.
	if args[1] != raw {
		t.Errorf("URL argument = %q, want %q", args[1], raw)
	}
}

func TestSystemOpenerRunsTheBrowserCommand(t *testing.T) {
	r := &fakeRunner{}
	o := &SystemOpener{runner: r}

	if err := o.Open(t.Context(), "http://127.0.0.1:8731/"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r.runs != 1 {
		t.Fatalf("ran %d commands, want 1", r.runs)
	}
	if !strings.EqualFold(r.name, "rundll32.exe") {
		t.Errorf("ran %q, want rundll32.exe", r.name)
	}
	if !slices.Contains(r.args, "http://127.0.0.1:8731/") {
		t.Errorf("args %q do not carry the URL", r.args)
	}
}
