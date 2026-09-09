// Package quickopen starts the three windows the tray's quick actions hand the
// user: a terminal already logged in to the peer, the shared folder on this
// machine, and the peer's desktop over RDP.
//
// # Why this is an adapter and not another line in internal/ui
//
// internal/ui has a documented licence to run two processes of its own —
// rundll32 for a link and powershell for a toast — and CLAUDE.md asks that
// nothing be added to it without re-reading the argument first. That argument
// is not "no syscalls in ui" but *nothing the domain could have an opinion
// about*: a browser hand-off and a notification are pixels, and an adapter
// that returned one would be an adapter that returned a UI.
//
// These three are the other kind. Which account to log in as, which private
// key to offer, which host-key policy to accept, which folder is shared, which
// address the peer answers on — every one of them is a decision this program
// already makes elsewhere, and the terminal has to make the *same* decisions
// as internal/adapters/sshx or the window it opens is not the link the monitor
// is monitoring. There is also a real choice of tool here (Windows Terminal or
// a console, the OpenSSH client Windows ships or the MSYS one on PATH first)
// and real refusals to make. So it goes where the rule sends it: behind an
// interface declared in the package that consumes it — [ui.QuickActions] — and
// implemented out here, with the decisions lifted into plan.go where they can
// be tested on a machine with no desktop and no peer.
//
// # Why the terminal shells out to ssh.exe when sshx never does
//
// internal/adapters/sshx says why it speaks SSH in-process and never runs
// ssh.exe: on these machines a VPN split-tunnel rule once matched that image
// name specifically and broke it silently, which is undiagnosable from the
// outside. Every word of that still holds — and it is an argument about a
// *silent* failure in an unattended probe, which is not this.
//
// An interactive terminal is a window the user is looking at. If ssh.exe is
// blocked, it prints its refusal into a console that cmd.exe /k holds open,
// and the user reads it. Nothing in this package parses ssh's output or
// decides anything by it, so there is no diagnosis to be misled. And the
// alternative is not "use the library instead": golang.org/x/crypto/ssh can
// open a shell but it cannot draw a terminal, so taking that road means this
// program growing a terminal emulator, a console host and a resize protocol to
// avoid a program Windows already ships. The link's health is still judged by
// sshx and only by sshx; this is a window.
//
// Nothing here logs the key path, and no error carries it: the values that
// could go wrong are named by field, never by content.
package quickopen

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config describes the peer to reach and the folder to open. Every field
// except Folder is required.
type Config struct {
	// PeerName is what the peer is called in a log line — "WIN-STTM11D02RD".
	// Nothing is decided by it.
	PeerName string
	// PeerUser is the account the terminal logs in as, and PeerAddr is the
	// address it dials: "user" at "100.127.188.87" on these machines.
	PeerUser string
	PeerAddr string
	// KeyPath is the private key the terminal offers — the same key the
	// monitor itself uses, which is the whole point of the item. Its path is
	// never logged and never put in an error.
	KeyPath string
	// Folder is the shared folder on this machine, as -sync-folder named it.
	// Empty is the folder feature switched off, and the tray says so rather
	// than opening something else.
	Folder string
}

// starter starts a process and does not wait for it. It is the seam the tests
// watch: they assert on the command line that would have run without a window
// appearing on the build agent.
type starter interface {
	start(c command) error
}

// Actions is the three quick actions for one peer and one shared folder. It
// implements the tray's QuickActions port.
type Actions struct {
	cfg     Config
	tools   tools
	starter starter
	prober  prober
	log     *slog.Logger
}

// New works out which tools this machine has and returns the actions over
// them. A nil logger means slog.Default().
//
// An error means the program is configured in a way no quick action could
// honour — no peer, or a value a terminal host would read again — and
// cmd/linkmon then leaves the whole block out of the menu. A *missing tool* is
// not an error here: it is reported by the one action that needed it, when the
// user clicks it, so that a machine without mstsc.exe still offers a terminal.
func New(cfg Config, logger *slog.Logger) (*Actions, error) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, field := range []struct{ what, value string }{
		{"the peer's user name", cfg.PeerUser},
		{"the peer's address", cfg.PeerAddr},
		{"the SSH key", cfg.KeyPath},
	} {
		if err := checkValue(field.what, field.value); err != nil {
			return nil, fmt.Errorf("the quick actions cannot be offered: %w", err)
		}
	}
	return &Actions{
		cfg:     cfg,
		tools:   locate(osFinder{}),
		starter: execStarter{},
		prober:  netProber{},
		log:     logger,
	}, nil
}

// SharedFolder reports the folder [Actions.OpenSharedFolder] would open, or ""
// when the user configured none. The tray asks before it offers to open
// anything, so that a machine with no shared folder gets a sentence saying so
// instead of a click that opens the wrong thing.
func (a *Actions) SharedFolder() string { return strings.TrimSpace(a.cfg.Folder) }

// OpenTerminal opens a terminal window already logged in to the peer.
func (a *Actions) OpenTerminal(ctx context.Context) error {
	c, err := terminalCommand(a.tools, a.cfg)
	if err != nil {
		return fmt.Errorf("opening a terminal on %s: %w", a.cfg.PeerName, err)
	}
	a.log.Info("opening a terminal on the peer", "peer", a.cfg.PeerName,
		"host", filepath.Base(c.name), "client", filepath.Base(a.tools.ssh))
	return a.start(ctx, c)
}

// OpenSharedFolder opens the shared folder on this machine in Explorer.
func (a *Actions) OpenSharedFolder(ctx context.Context) error {
	c, err := folderCommand(a.tools, a.cfg.Folder)
	if err != nil {
		return fmt.Errorf("opening the shared folder: %w", err)
	}
	a.log.Info("opening the shared folder", "folder", c.args[0])
	return a.start(ctx, c)
}

// OpenPeerDesktop opens the peer's desktop in the Remote Desktop client.
//
// The port is dialled before the client is started, and a peer that does not
// answer is reported rather than handed to mstsc.exe. See [rdpPort] for the
// measurement that argument rests on: mstsc's own answer to a machine that is
// not there is a spinner that was still going two minutes later.
func (a *Actions) OpenPeerDesktop(ctx context.Context) error {
	c, err := desktopCommand(a.tools, a.cfg.PeerAddr)
	if err != nil {
		return fmt.Errorf("opening the desktop of %s: %w", a.cfg.PeerName, err)
	}
	if err := a.prober.reachable(ctx, a.cfg.PeerAddr, rdpPort); err != nil {
		a.log.Warn("the peer is not answering on the remote-desktop port",
			"peer", a.cfg.PeerName, "addr", a.cfg.PeerAddr, "port", rdpPort, "err", err)
		return fmt.Errorf("opening the desktop of %s: %w", a.cfg.PeerName, errPeerHasNoDesktop)
	}
	a.log.Info("opening the peer's desktop", "peer", a.cfg.PeerName, "addr", a.cfg.PeerAddr)
	return a.start(ctx, c)
}

// start launches one command.
//
// ctx bounds the decisions above this line and nothing below it. The process
// is started with exec.Command rather than exec.CommandContext on purpose: the
// tray gives a menu click fifteen seconds, and CommandContext would kill the
// terminal the user had just been handed the moment that deadline passed. A
// quick action gives the user a window and then has no further business with
// it — which is also why nothing here waits for an exit status.
func (a *Actions) start(ctx context.Context, c command) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("starting %s: %w", filepath.Base(c.name), err)
	}
	if err := a.starter.start(c); err != nil {
		return fmt.Errorf("starting %s: %w", filepath.Base(c.name), err)
	}
	return nil
}

// execStarter is the real starter, and the only thing in this package that
// touches the world outside the process.
type execStarter struct{}

// start starts one process and lets go of it.
//
// Stdin, Stdout and Stderr are left nil, which os/exec turns into the null
// device. That is the right answer for all three of these — explorer.exe and
// mstsc.exe are GUI programs, and the terminal gets its handles from
// conhost.exe rather than from us — and [terminalCommand] explains at length
// why it is also the reason conhost.exe has to be in the chain at all.
func (execStarter) start(c command) error {
	// G204: name is one of the executables locate() found on this machine by
	// absolute path or on PATH — never a value from outside — and argv is
	// passed as a vector, so the values that do come from configuration are
	// not re-read by anything here. conhost.exe, cmd.exe and Windows Terminal
	// do read their own tails again, which is what checkValue in plan.go
	// refuses characters for.
	//
	// noctx: exec.CommandContext would tie the user's terminal window to the
	// fifteen-second deadline of the menu click that opened it, and kill it.
	// See [Actions.start].
	cmd := exec.Command(c.name, c.args...) //nolint:gosec,noctx // G204/noctx: see above
	if err := cmd.Start(); err != nil {
		// The error names the executable and not the arguments: one of them is
		// the path of a private key.
		return fmt.Errorf("starting %s: %w", filepath.Base(c.name), err)
	}
	// Nothing will ever call Wait, so the handle is given back here rather than
	// held for the life of this program.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("letting go of %s: %w", filepath.Base(c.name), err)
	}
	return nil
}
