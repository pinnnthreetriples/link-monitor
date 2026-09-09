package quickopen

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeStarter records what would have been started. Every test in this package
// uses it: the real starter opens a window, and a build agent has no desktop.
type fakeStarter struct {
	started []command
	err     error
}

func (f *fakeStarter) start(c command) error {
	f.started = append(f.started, c)
	return f.err
}

var errStartFailed = errors.New("the process would not start")

// fakeProber answers for a peer that is there or a peer that is not, without
// either one existing.
type fakeProber struct {
	asked []string
	err   error
}

func (p *fakeProber) reachable(_ context.Context, host, port string) error {
	p.asked = append(p.asked, host+":"+port)
	return p.err
}

var errPeerSilent = errors.New("nothing answered")

// newTest returns actions over the fake starter and a machine described by
// tls, so a test can say what this machine has without installing anything.
func newTest(t *testing.T, cfg Config, tls tools) (*Actions, *fakeStarter) {
	t.Helper()

	starter := &fakeStarter{}
	return &Actions{
		cfg:     cfg,
		tools:   tls,
		starter: starter,
		prober:  &fakeProber{},
		log:     slog.New(slog.DiscardHandler),
	}, starter
}

func TestEachActionStartsExactlyOneThing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		run  func(*Actions, context.Context) error
		want string
	}{
		{
			name: "the terminal",
			run:  func(a *Actions, ctx context.Context) error { return a.OpenTerminal(ctx) },
			want: realish.conhost,
		},
		{
			name: "the shared folder",
			run:  func(a *Actions, ctx context.Context) error { return a.OpenSharedFolder(ctx) },
			want: realish.explorer,
		},
		{
			name: "the peer's desktop",
			run:  func(a *Actions, ctx context.Context) error { return a.OpenPeerDesktop(ctx) },
			want: realish.rdp,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			actions, starter := newTest(t, peerConfig, realish)
			if err := tc.run(actions, t.Context()); err != nil {
				t.Fatalf("the action failed: %v", err)
			}

			if len(starter.started) != 1 {
				t.Fatalf("started %d processes, want exactly 1", len(starter.started))
			}
			if got := starter.started[0].name; got != tc.want {
				t.Errorf("started %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAFailureToStartIsReportedWithoutNamingTheKey(t *testing.T) {
	t.Parallel()

	actions, starter := newTest(t, peerConfig, realish)
	starter.err = errStartFailed

	err := actions.OpenTerminal(t.Context())
	if !errors.Is(err, errStartFailed) {
		t.Fatalf("OpenTerminal error = %v, want the starter's own failure", err)
	}
	// The argument vector carries the private key's path. A menu click that
	// fails must say so without writing that path into a log file.
	if strings.Contains(err.Error(), peerConfig.KeyPath) {
		t.Errorf("the error names the key path: %v", err)
	}
	if !strings.Contains(err.Error(), "conhost.exe") {
		t.Errorf("the error does not say what would not start: %v", err)
	}
}

func TestAMissingToolIsReportedByTheActionThatNeededIt(t *testing.T) {
	t.Parallel()

	// A machine with only Explorer: the folder still opens, the other two say
	// what is missing rather than failing silently.
	only := tools{explorer: realish.explorer}
	actions, starter := newTest(t, peerConfig, only)

	if err := actions.OpenSharedFolder(t.Context()); err != nil {
		t.Fatalf("the folder should still open on a machine with Explorer: %v", err)
	}
	if err := actions.OpenTerminal(t.Context()); !errors.Is(err, errNoSSH) {
		t.Errorf("OpenTerminal error = %v, want errNoSSH", err)
	}
	if err := actions.OpenPeerDesktop(t.Context()); !errors.Is(err, errNoRDP) {
		t.Errorf("OpenPeerDesktop error = %v, want errNoRDP", err)
	}
	if len(starter.started) != 1 {
		t.Errorf("started %d processes, want only the one that could work", len(starter.started))
	}
}

func TestNothingIsStartedForACallerWhoHasAlreadyGivenUp(t *testing.T) {
	t.Parallel()

	actions, starter := newTest(t, peerConfig, realish)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := actions.OpenTerminal(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("OpenTerminal on a cancelled context = %v, want context.Canceled", err)
	}
	if len(starter.started) != 0 {
		t.Errorf("started %d processes for a caller who had given up", len(starter.started))
	}
}

func TestTheSharedFolderIsReportedSoTheTrayCanSayItIsNotConfigured(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ folder, want string }{
		{folder: `C:\Users\pnj\Общая папка`, want: `C:\Users\pnj\Общая папка`},
		{folder: "  \t ", want: ""},
		{folder: "", want: ""},
	} {
		actions, _ := newTest(t, Config{Folder: tc.folder}, realish)
		if got := actions.SharedFolder(); got != tc.want {
			t.Errorf("SharedFolder() with %q = %q, want %q", tc.folder, got, tc.want)
		}
	}
}

func TestAFolderThatWasNeverConfiguredStartsNothing(t *testing.T) {
	t.Parallel()

	cfg := peerConfig
	cfg.Folder = ""
	actions, starter := newTest(t, cfg, realish)

	if err := actions.OpenSharedFolder(t.Context()); !errors.Is(err, errNoFolder) {
		t.Errorf("OpenSharedFolder with no folder = %v, want errNoFolder", err)
	}
	if len(starter.started) != 0 {
		t.Errorf("started %d processes with no folder configured", len(starter.started))
	}
}

func TestNewRefusesAProgramNoQuickActionCouldHonour(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{name: "no peer user", cfg: Config{PeerAddr: "100.127.188.87", KeyPath: `C:\k`}},
		{name: "no peer address", cfg: Config{PeerUser: "user", KeyPath: `C:\k`}},
		{name: "no key", cfg: Config{PeerUser: "user", PeerAddr: "100.127.188.87"}},
		{
			name: "a key path a command line would read again",
			cfg: Config{
				PeerUser: "user", PeerAddr: "100.127.188.87", KeyPath: `C:\keys & more\id`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			actions, err := New(tc.cfg, slog.New(slog.DiscardHandler))
			if err == nil {
				t.Fatal("New accepted a configuration no quick action could honour")
			}
			if actions != nil {
				t.Error("New returned actions alongside its error")
			}
			if strings.Contains(err.Error(), tc.cfg.KeyPath) && tc.cfg.KeyPath != "" {
				t.Errorf("the error names the key path: %v", err)
			}
		})
	}
}

func TestNewBuildsTheActionsForTheRealPair(t *testing.T) {
	t.Parallel()

	// A nil logger is the ordinary case from cmd/linkmon's point of view, and
	// it must not be the thing that panics on the first click.
	actions, err := New(peerConfig, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if actions.log == nil {
		t.Error("New left the actions with no logger")
	}
	if actions.starter == nil {
		t.Error("New left the actions with nothing to start processes with")
	}
	if actions.prober == nil {
		t.Error("New left the actions with no way to dial the peer")
	}
	if got := actions.SharedFolder(); got != peerConfig.Folder {
		t.Errorf("SharedFolder() = %q, want %q", got, peerConfig.Folder)
	}
}

// TestTheRealStarterStartsAProcessAndLetsGoOfIt covers the one function in
// this package that touches the operating system. It starts the test binary
// itself rather than one of the four real tools: `-test.run` matching nothing
// makes it exit at once, and nothing appears on screen — the point being that
// the process is started, released, and never waited for.
func TestTheRealStarterStartsAProcessAndLetsGoOfIt(t *testing.T) {
	t.Parallel()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding the test binary: %v", err)
	}

	s := execStarter{}
	if err := s.start(command{name: self, args: []string{"-test.run=^$", "-test.timeout=30s"}}); err != nil {
		t.Errorf("start: %v", err)
	}
	// A name nothing answers to is a failure the caller has to hear about,
	// because there is no exit status coming: nothing waits for this process.
	if err := s.start(command{name: filepath.Join(t.TempDir(), "no-such-tool.exe")}); err == nil {
		t.Error("start accepted an executable that does not exist")
	}
}

// TestThePeerIsDialledBeforeTheDesktopClientIsStarted is the guard against the
// hang this feature was asked not to ship: mstsc.exe pointed at a machine that
// is not there shows a spinner and no message. See rdpPort.
func TestThePeerIsDialledBeforeTheDesktopClientIsStarted(t *testing.T) {
	t.Parallel()

	actions, starter := newTest(t, peerConfig, realish)
	probe := actions.prober.(*fakeProber)

	if err := actions.OpenPeerDesktop(t.Context()); err != nil {
		t.Fatalf("OpenPeerDesktop: %v", err)
	}
	if want := []string{"100.127.188.87:3389"}; !slices.Equal(probe.asked, want) {
		t.Errorf("dialled %q, want %q", probe.asked, want)
	}
	if len(starter.started) != 1 {
		t.Errorf("started %d processes, want the desktop client", len(starter.started))
	}
}

func TestAPeerWithNoDesktopToOpenIsReportedRatherThanHandedToMstsc(t *testing.T) {
	t.Parallel()

	actions, starter := newTest(t, peerConfig, realish)
	actions.prober.(*fakeProber).err = errPeerSilent

	err := actions.OpenPeerDesktop(t.Context())
	if !errors.Is(err, errPeerHasNoDesktop) {
		t.Errorf("OpenPeerDesktop error = %v, want errPeerHasNoDesktop", err)
	}
	if len(starter.started) != 0 {
		t.Errorf("started %d processes against a peer that is not answering", len(starter.started))
	}
}

// TestTheRealProberAnswersForAListenerAndForSilence covers the one function
// here that opens a socket. It dials a listener of its own rather than the
// peer, so no test needs a tailnet.
func TestTheRealProberAnswersForAListenerAndForSilence(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("standing up a listener: %v", err)
	}
	// The listener is closed halfway through, on purpose, so the cleanup only
	// has to catch the paths that got there first.
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := ln.Close(); err != nil {
				t.Errorf("closing the listener: %v", err)
			}
		}
	})

	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("splitting %q: %v", ln.Addr(), err)
	}

	p := netProber{}
	if err := p.reachable(t.Context(), host, port); err != nil {
		t.Errorf("reachable said no to a listener that is right there: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("closing the listener: %v", err)
	}
	closed = true
	if err := p.reachable(t.Context(), host, port); err == nil {
		t.Error("reachable said yes to a port nothing is listening on")
	}
}
