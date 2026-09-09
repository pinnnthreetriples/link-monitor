//go:build windows

package ui

import (
	"context"
	"strings"
	"testing"
)

// TestExecRunnerRunsACommandAndReportsWhatItSaid covers the one thing in this
// package that actually starts a process. Everything above it takes [runner]
// and is tested against a fake, which leaves this type — the browser hand-off
// and every toast go through it — as the part nothing had ever run.
//
// It uses cmd.exe rather than one of the real commands: rundll32 would open a
// browser on the machine running the tests and powershell would raise a toast
// over whatever the user is doing. What is under test is the runner's own
// contract, not what it is normally pointed at.
func TestExecRunnerRunsACommandAndReportsWhatItSaid(t *testing.T) {
	t.Parallel()

	r := execRunner{}

	if err := r.run(t.Context(), "cmd.exe", "/c", "exit", "0"); err != nil {
		t.Fatalf("a command that succeeds = %v, want no error", err)
	}
}

func TestExecRunnerPutsTheCommandsOwnWordsInTheError(t *testing.T) {
	t.Parallel()

	// A -H=windowsgui build has no console, so whatever the command printed is
	// the only clue that ever reaches the log. Losing it would leave "exit
	// status 3" and nothing else.
	err := execRunner{}.run(t.Context(), "cmd.exe", "/c", "echo", "LINKMON-TEST", "&&", "exit", "3")
	if err == nil {
		t.Fatal("a command that failed = nil, want an error")
	}
	if got := err.Error(); !strings.Contains(got, "LINKMON-TEST") {
		t.Errorf("the error dropped what the command printed: %q", got)
	}
	if got := err.Error(); !strings.Contains(strings.ToLower(got), "cmd.exe") {
		t.Errorf("the error does not name the command: %q", got)
	}
}

func TestExecRunnerReportsAFailureWithNothingToSay(t *testing.T) {
	t.Parallel()

	err := execRunner{}.run(t.Context(), "cmd.exe", "/c", "exit", "3")
	if err == nil {
		t.Fatal("a command that failed silently = nil, want an error")
	}
	if got := err.Error(); !strings.Contains(strings.ToLower(got), "cmd.exe") {
		t.Errorf("the error does not name the command: %q", got)
	}
}

func TestExecRunnerReportsAProgramThatIsNotThere(t *testing.T) {
	t.Parallel()

	if err := (execRunner{}).run(t.Context(), "linkmon-no-such-program.exe"); err == nil {
		t.Fatal("running a program that does not exist = nil, want an error")
	}
}

func TestExecRunnerHonoursACancelledContext(t *testing.T) {
	t.Parallel()

	// This is what shutdown relies on: a toast whose PowerShell is still
	// starting must die with the program rather than hold it up.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := (execRunner{}).run(ctx, "cmd.exe", "/c", "exit", "0"); err == nil {
		t.Fatal("a cancelled context did not stop the command")
	}
}
