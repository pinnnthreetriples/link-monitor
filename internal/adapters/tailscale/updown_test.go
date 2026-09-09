package tailscale

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
)

const authURL = "https://login.tailscale.com/a/0f1e2d3c4b5a"

// stoppedStatus is the daemon connected to nothing.
func stoppedStatus() *ipnstate.Status {
	return &ipnstate.Status{Version: longVersion, BackendState: "Stopped"}
}

func TestUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   *ipnstate.Status
		run      func(args []string) (CommandResult, error)
		wantErr  error
		wantText string
		wantRun  bool
		wantURL  string
	}{
		{
			name:    "a stopped daemon is connected",
			status:  stoppedStatus(),
			wantRun: true,
		},
		{
			name:   "already connected: the CLI is not even called",
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{}, errors.New("the CLI should not have run")
			},
		},
		{
			name:    "a daemon that will not answer is left to the CLI",
			run:     func([]string) (CommandResult, error) { return CommandResult{}, nil },
			wantRun: true,
		},
		{
			name:   "login required, with the URL on stderr",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{
					Stderr: "\nTo authenticate, visit:\n\n\t" + authURL + "\n\n",
					Code:   1,
				}, nil
			},
			wantErr: ErrLoginRequired,
			wantURL: authURL,
			wantRun: true,
		},
		{
			name:   "login required, the URL printed on stdout instead",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stdout: "visit " + authURL + " to continue", Code: 1}, nil
			},
			wantErr: ErrLoginRequired,
			wantURL: authURL,
			wantRun: true,
		},
		{
			name:   "login required with no URL at all",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "Error: the node is logged out", Code: 1}, nil
			},
			wantErr: ErrLoginRequired,
			wantRun: true,
		},
		{
			name:   "an expired node key also wants a person",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "the node key has expired", Code: 1}, nil
			},
			wantErr: ErrLoginRequired,
			wantRun: true,
		},
		{
			name:   "access denied",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "Access is denied.", Code: 1}, nil
			},
			wantErr: ErrAccessDenied,
			wantRun: true,
		},
		{
			name:   "a non-zero exit nobody predicted",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "flag provided but not defined: -timeout", Code: 2}, nil
			},
			wantText: "exit 2: flag provided but not defined",
			wantRun:  true,
		},
		{
			name:   "the CLI could not be started",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{}, errBoom
			},
			wantErr: errBoom,
			wantRun: true,
		},
		{
			name:   "the daemon never came up",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{
					Stderr: "timeout waiting for Tailscale service to enter a Running state",
					Code:   1,
				}, nil
			},
			wantText: "exit 1: timeout waiting",
			wantRun:  true,
		},
		{
			name:   "the process outlived its own budget",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{}, context.DeadlineExceeded
			},
			wantErr: context.DeadlineExceeded,
			wantRun: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &fakeRunner{fn: tc.run}
			c := newTestClient(&fakeDaemon{status: tc.status}, r)

			err := c.Up(context.Background())
			checkOutcome(t, "Up", err, tc.wantErr, tc.wantText)

			if tc.wantURL != "" {
				var lre *LoginRequiredError
				if !errors.As(err, &lre) {
					t.Fatalf("Up() error = %v, want a *LoginRequiredError", err)
				}
				if lre.URL != tc.wantURL {
					t.Errorf("login URL = %q, want %q", lre.URL, tc.wantURL)
				}
				if strings.Contains(err.Error(), tc.wantURL) {
					t.Error("the login URL leaked into the error text, where logs would keep it")
				}
			}
			if ran := r.lastCall() != nil; ran != tc.wantRun {
				t.Errorf("the CLI ran = %v, want %v", ran, tc.wantRun)
			}
		})
	}
}

// checkOutcome asserts one expected failure shape, or none at all.
func checkOutcome(t *testing.T, what string, err, wantErr error, wantText string) {
	t.Helper()
	switch {
	case wantErr != nil:
		if !errors.Is(err, wantErr) {
			t.Fatalf("%s() error = %v, want it to wrap %v", what, err, wantErr)
		}
	case wantText != "":
		if err == nil || !strings.Contains(err.Error(), wantText) {
			t.Fatalf("%s() error = %v, want it to mention %q", what, err, wantText)
		}
	default:
		if err != nil {
			t.Fatalf("%s() error = %v, want none", what, err)
		}
	}
}

func TestUpRunsABoundedCommand(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	c := New(WithDaemon(&fakeDaemon{status: stoppedStatus()}), WithRunner(r), WithUpTimeout(9*time.Second))

	if err := c.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	got := r.lastCall()
	if len(got) != 2 || got[0] != "up" || got[1] != "--timeout=9s" {
		t.Fatalf("ran %v, want [up --timeout=9s]", got)
	}
}

func TestUpBoundsAContextWithoutADeadline(t *testing.T) {
	t.Parallel()
	var deadline time.Time
	var hasDeadline bool
	r := &fakeRunner{}
	r.fn = func([]string) (CommandResult, error) { return CommandResult{}, nil }

	d := &fakeDaemon{status: stoppedStatus()}
	c := New(WithDaemon(d), WithRunner(&deadlineRunner{
		inner: r,
		seen:  func(dl time.Time, ok bool) { deadline, hasDeadline = dl, ok },
	}), WithUpTimeout(time.Second))

	if err := c.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !hasDeadline {
		t.Fatal("the command ran with no deadline: Up() could wedge the caller")
	}
	if left := time.Until(deadline); left > time.Second+upGrace+time.Second {
		t.Errorf("deadline is %v away, want about %v", left, time.Second+upGrace)
	}
}

// deadlineRunner reports the context deadline the command was given.
type deadlineRunner struct {
	inner Runner
	seen  func(time.Time, bool)
}

func (d *deadlineRunner) Run(ctx context.Context, args ...string) (CommandResult, error) {
	dl, ok := ctx.Deadline()
	d.seen(dl, ok)
	return d.inner.Run(ctx, args...)
}

func TestUpHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &fakeRunner{}
	err := newTestClient(&fakeDaemon{status: stoppedStatus()}, r).Up(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Up() error = %v, want context.Canceled", err)
	}
	if r.lastCall() != nil {
		t.Error("the CLI ran on a dead context")
	}
}

func TestUpReportsACancelledParentRatherThanTheKill(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	c := newTestClient(&fakeDaemon{status: stoppedStatus()}, &fakeRunner{
		fn: func([]string) (CommandResult, error) {
			cancel()
			return CommandResult{Code: 1, Stderr: "signal: killed"}, errBoom
		},
	})
	defer cancel()

	if err := c.Up(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Up() error = %v, want context.Canceled", err)
	}
}

func TestDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   *ipnstate.Status
		run      func(args []string) (CommandResult, error)
		wantErr  error
		wantText string
		wantRun  bool
	}{
		{
			name:    "a running daemon is disconnected",
			status:  runningStatus(t),
			wantRun: true,
		},
		{
			name:   "already disconnected: the CLI is not even called",
			status: stoppedStatus(),
			run: func([]string) (CommandResult, error) {
				return CommandResult{}, errors.New("the CLI should not have run")
			},
		},
		{
			name: "the CLI says it was already stopped, which is still success",
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "Tailscale was already stopped.\n"}, nil
			},
			wantRun: true,
		},
		{
			name:   "access denied",
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "Access is denied.", Code: 1}, nil
			},
			wantErr: ErrAccessDenied,
			wantRun: true,
		},
		{
			name:   "elevation asked for in other words",
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "this command must be run as administrator", Code: 1}, nil
			},
			wantErr: ErrAccessDenied,
			wantRun: true,
		},
		{
			name:   "a non-zero exit nobody predicted",
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stdout: "unexpected answer from the daemon", Code: 7}, nil
			},
			wantText: "exit 7: unexpected answer",
			wantRun:  true,
		},
		{
			name:   "silence with a bad exit code still explains itself",
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Code: 1}, nil
			},
			wantText: "exit 1: no output",
			wantRun:  true,
		},
		{
			name:   "the CLI could not be started",
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{}, errBoom
			},
			wantErr: errBoom,
			wantRun: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &fakeRunner{fn: tc.run}
			c := newTestClient(&fakeDaemon{status: tc.status}, r)

			err := c.Down(context.Background())
			checkOutcome(t, "Down", err, tc.wantErr, tc.wantText)

			got := r.lastCall()
			if ran := got != nil; ran != tc.wantRun {
				t.Fatalf("the CLI ran = %v, want %v", ran, tc.wantRun)
			}
			if tc.wantRun && (len(got) != 1 || got[0] != "down") {
				t.Errorf("ran %v, want [down]", got)
			}
		})
	}
}

func TestDownHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &fakeRunner{}
	err := newTestClient(&fakeDaemon{status: runningStatus(t)}, r).Down(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Down() error = %v, want context.Canceled", err)
	}
	if r.lastCall() != nil {
		t.Error("the CLI ran on a dead context")
	}
}

func TestLoginRequiredErrorText(t *testing.T) {
	t.Parallel()
	withURL := &LoginRequiredError{URL: authURL}
	if strings.Contains(withURL.Error(), authURL) {
		t.Errorf("Error() = %q, want it to keep the one-time URL out of the text", withURL.Error())
	}
	if !errors.Is(withURL, ErrLoginRequired) {
		t.Error("a LoginRequiredError does not match ErrLoginRequired")
	}
	bare := &LoginRequiredError{}
	if bare.Error() != ErrLoginRequired.Error() {
		t.Errorf("Error() = %q, want the plain sentinel text", bare.Error())
	}
}

func TestWithUpTimeoutIgnoresNonsense(t *testing.T) {
	t.Parallel()
	if c := New(WithUpTimeout(0)); c.upTimeout != defaultUpTimeout {
		t.Errorf("up timeout = %v, want the default kept", c.upTimeout)
	}
	if c := New(WithUpTimeout(-time.Second)); c.upTimeout != defaultUpTimeout {
		t.Errorf("up timeout = %v, want the default kept", c.upTimeout)
	}
}

func TestUpFallsBackToTheDefaultTimeout(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	c := &Client{daemon: &fakeDaemon{status: stoppedStatus()}, runner: r} // upTimeout stays zero

	if err := c.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	got := r.lastCall()
	if len(got) != 2 || got[1] != "--timeout="+defaultUpTimeout.String() {
		t.Errorf("ran %v, want the default timeout", got)
	}
}
