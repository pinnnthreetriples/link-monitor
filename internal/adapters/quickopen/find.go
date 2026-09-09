package quickopen

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

// tools are the executables the quick actions need, as this machine actually
// has them. An empty field is a tool that is not here, and the action that
// needs it says so at the click rather than failing silently.
type tools struct {
	// ssh is the OpenSSH client that logs in to the peer.
	ssh string
	// terminal is Windows Terminal, empty when it is not installed.
	terminal string
	// conhost is the console host, and shell is cmd.exe. Together they are
	// the terminal on a machine with no Windows Terminal: see
	// [terminalCommand] for why both are needed and why neither can be
	// dropped.
	conhost string
	shell   string
	// explorer opens a folder; rdp is mstsc.exe.
	explorer string
	rdp      string
}

// finder locates an executable. It is this package's only seam onto the
// filesystem and PATH, so a test can describe a machine that has Windows
// Terminal, a machine that has not, and a build agent that has neither
// without installing anything.
type finder interface {
	// find returns the first of paths that exists. When none of them does it
	// falls back to looking bare up on PATH, and returns "" when that fails
	// too.
	find(paths []string, bare string) string
}

// osFinder is the real finder.
type osFinder struct{}

func (osFinder) find(paths []string, bare string) string {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	// LookPath's error says only that the name is not on PATH, which is what
	// an empty result already says; the callers turn it into their own
	// sentinel so the message names the action rather than the search.
	if found, err := exec.LookPath(bare); err == nil {
		return found
	}
	return ""
}

// locate works out which tools this machine has.
//
// Every candidate is an absolute path first and a PATH lookup only as a last
// resort, because PATH is not a reliable way to find these particular
// programs. `where ssh.exe` on the laptop this was written for answers
// C:\Program Files\Git\usr\bin\ssh.exe before the Windows one: an MSYS build
// that reads C:\Users\... as an MSYS path and handles the key file
// differently. The tool the item needs is the OpenSSH client Windows ships,
// and asking for it by name is how it gets it.
func locate(f finder) tools {
	system := envDir("SystemRoot", `C:\Windows`)
	programFiles := envDir("ProgramFiles", `C:\Program Files`)
	localAppData := os.Getenv("LOCALAPPDATA")

	return tools{
		ssh: f.find([]string{
			filepath.Join(system, "System32", "OpenSSH", "ssh.exe"),
			filepath.Join(programFiles, "OpenSSH", "ssh.exe"),
		}, "ssh.exe"),
		// Windows Terminal installs as a packaged app whose launcher is an
		// execution alias under WindowsApps. The alias is on an interactive
		// user's PATH, but this program is started from the Run key and a
		// service-like environment may not have it, so the alias is looked for
		// by path as well.
		terminal: f.find([]string{
			filepath.Join(localAppData, "Microsoft", "WindowsApps", "wt.exe"),
		}, "wt.exe"),
		conhost:  f.find([]string{filepath.Join(system, "System32", "conhost.exe")}, "conhost.exe"),
		shell:    f.find([]string{filepath.Join(system, "System32", "cmd.exe")}, "cmd.exe"),
		explorer: f.find([]string{filepath.Join(system, "explorer.exe")}, "explorer.exe"),
		rdp:      f.find([]string{filepath.Join(system, "System32", "mstsc.exe")}, "mstsc.exe"),
	}
}

// envDir reads a directory out of the environment, falling back to what it is
// on every Windows install when the variable is missing.
func envDir(name, fallback string) string {
	if dir := os.Getenv(name); dir != "" {
		return dir
	}
	return fallback
}

// prober asks whether the peer is answering on a port. It is a seam for the
// same reason [finder] is: a test must be able to describe a peer that is
// there and a peer that is not, without either one existing.
type prober interface {
	reachable(ctx context.Context, host, port string) error
}

// netProber is the real prober.
type netProber struct{}

// reachable opens a TCP connection and closes it again, which is the whole
// question: nothing is sent, nothing is read, and nothing on the other machine
// is touched beyond the handshake its listener would log anyway.
func (netProber) reachable(ctx context.Context, host, port string) error {
	ctx, cancel := context.WithTimeout(ctx, rdpProbeTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("dialling %s: %w", net.JoinHostPort(host, port), err)
	}
	if err := conn.Close(); err != nil {
		// The answer is already known — the connection was made — so a failure
		// to hang up is worth reporting and not worth failing the action for.
		return nil
	}
	return nil
}
