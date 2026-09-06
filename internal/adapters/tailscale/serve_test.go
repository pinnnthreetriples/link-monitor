package tailscale

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tailscale.com/ipn/ipnstate"
)

const laptopURL = "https://" + laptopName + ".tail1a2b.ts.net/"

func TestServe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		port    int
		status  *ipnstate.Status
		run     func(args []string) (CommandResult, error)
		wantURL string
		wantErr string
	}{
		{
			name:    "the daemon names this node",
			port:    8080,
			status:  runningStatus(t),
			wantURL: laptopURL,
		},
		{
			name:   "the printed URL is the fallback when the daemon goes quiet",
			port:   8080,
			status: nil,
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stdout: "Available within your tailnet:\n" + laptopURL + "\n"}, nil
			},
			wantURL: laptopURL,
		},
		{
			name:   "a URL on stderr is found too",
			port:   3000,
			status: &ipnstate.Status{BackendState: "Running"},
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stderr: "serving " + laptopURL}, nil
			},
			wantURL: laptopURL,
		},
		{
			name:    "port zero never reaches the CLI",
			port:    0,
			status:  runningStatus(t),
			wantErr: "outside",
		},
		{
			name:    "a port past the end of the range",
			port:    70000,
			status:  runningStatus(t),
			wantErr: "outside",
		},
		{
			name:   "the CLI refuses",
			port:   8080,
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{Code: 1, Stderr: "serve not available\nsecond line"}, nil
			},
			wantErr: "exit 1: serve not available",
		},
		{
			name:   "the CLI cannot be started",
			port:   8080,
			status: runningStatus(t),
			run: func([]string) (CommandResult, error) {
				return CommandResult{}, errBoom
			},
			wantErr: errBoom.Error(),
		},
		{
			name:   "nothing says what the URL is",
			port:   8080,
			status: &ipnstate.Status{BackendState: "Running"},
			run: func([]string) (CommandResult, error) {
				return CommandResult{Stdout: "done"}, nil
			},
			wantErr: "could not be determined",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &fakeRunner{fn: tc.run}
			c := newTestClient(&fakeDaemon{status: tc.status}, r)

			got, err := c.Serve(context.Background(), tc.port)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Serve() error = nil, want one about %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("Serve() error = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Serve() error = %v", err)
			}
			if got != tc.wantURL {
				t.Errorf("Serve() = %q, want %q", got, tc.wantURL)
			}
		})
	}
}

func TestServeRunsTheRightCommand(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	c := newTestClient(&fakeDaemon{status: runningStatus(t)}, r)

	if _, err := c.Serve(context.Background(), 8080); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	want := []string{"serve", "--bg", "8080"}
	got := r.lastCall()
	if len(got) != len(want) {
		t.Fatalf("ran %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ran %v, want %v", got, want)
		}
	}
}

func TestServeHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newTestClient(&fakeDaemon{status: runningStatus(t)}, &fakeRunner{})
	if _, err := c.Serve(ctx, 8080); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve() error = %v, want context.Canceled", err)
	}
}

func TestServeReset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		run     func(args []string) (CommandResult, error)
		wantErr string
	}{
		{name: "it resets"},
		{
			name: "the CLI refuses",
			run: func([]string) (CommandResult, error) {
				return CommandResult{Code: 2}, nil
			},
			wantErr: "exit 2: no output",
		},
		{
			name: "the CLI cannot be started",
			run: func([]string) (CommandResult, error) {
				return CommandResult{}, errBoom
			},
			wantErr: errBoom.Error(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &fakeRunner{fn: tc.run}
			err := newTestClient(&fakeDaemon{}, r).ServeReset(context.Background())

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ServeReset() error = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ServeReset() error = %v", err)
			}
			got := r.lastCall()
			if len(got) != 2 || got[0] != "serve" || got[1] != "reset" {
				t.Errorf("ran %v, want [serve reset]", got)
			}
		})
	}
}

func TestServeResetHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := newTestClient(&fakeDaemon{}, &fakeRunner{}).ServeReset(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeReset() error = %v, want context.Canceled", err)
	}
}

func TestSelfURLWithoutAName(t *testing.T) {
	t.Parallel()
	st := &ipnstate.Status{BackendState: "Running", Self: &ipnstate.PeerStatus{HostName: laptopHost}}

	if got := newTestClient(&fakeDaemon{status: st}, &fakeRunner{}).selfURL(context.Background()); got != "" {
		t.Errorf("selfURL() = %q, want empty when the node has no MagicDNS name", got)
	}
}

func TestCLIMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		res  CommandResult
		want string
	}{
		{name: "stderr wins", res: CommandResult{Stdout: "out", Stderr: "err"}, want: "err"},
		{name: "stdout stands in", res: CommandResult{Stdout: "out"}, want: "out"},
		{name: "silence", res: CommandResult{}, want: "no output"},
		{name: "first line only", res: CommandResult{Stderr: "first\r\nsecond"}, want: "first"},
		{
			name: "long output is cut",
			res:  CommandResult{Stderr: strings.Repeat("x", 300)},
			want: strings.Repeat("x", 200) + "…",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cliMessage(tc.res); got != tc.want {
				t.Errorf("cliMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}
