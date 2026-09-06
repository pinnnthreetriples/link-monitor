package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// knownHostsPerm and knownHostsDirPerm keep the file readable by its owner
// only, the way OpenSSH writes it.
const (
	knownHostsPerm    os.FileMode = 0o600
	knownHostsDirPerm os.FileMode = 0o700
)

// hostKeyCallback builds the host key verifier for cfg.
//
// The normal path is strict verification against known_hosts. The only
// weakening is Config.TrustOnFirstUse, which accepts a host known_hosts has
// never seen and appends its key, exactly as OpenSSH does when
// StrictHostKeyChecking is "accept-new". A host that already has an entry is
// still checked strictly, so a *changed* key is refused whether or not
// trust-on-first-use is on. ssh.InsecureIgnoreHostKey is never used, in any
// mode: it would accept a man in the middle silently, and the whole point of
// this program is to tell a broken link from a redirected one.
func hostKeyCallback(cfg Config) (ssh.HostKeyCallback, error) {
	path, err := knownHostsFile(cfg)
	if err != nil {
		return nil, err
	}
	if err := ensureKnownHosts(path, cfg.TrustOnFirstUse); err != nil {
		return nil, err
	}

	verify, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("reading known_hosts at %s: %w", path, err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		// Want is empty only when the host is unknown; a non-empty Want is a
		// mismatch and must never be accepted.
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			if !cfg.TrustOnFirstUse {
				return fmt.Errorf("host %s is not in %s and trust-on-first-use is off: %w",
					hostname, path, err)
			}
			return addKnownHost(path, hostname, key)
		}
		return fmt.Errorf("verifying the host key of %s against %s: %w", hostname, path, err)
	}, nil
}

// knownHostsFile resolves the configured path, falling back to
// %USERPROFILE%\.ssh\known_hosts.
func knownHostsFile(cfg Config) (string, error) {
	if cfg.KnownHostsPath != "" {
		return cfg.KnownHostsPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory for known_hosts: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// ensureKnownHosts makes sure the file exists, because knownhosts.New refuses a
// missing one. A missing file is only created under trust-on-first-use: without
// it, an absent known_hosts is a configuration error the operator should see
// rather than a blank slate we quietly fill in.
func ensureKnownHosts(path string, tofu bool) error {
	switch _, err := os.Stat(path); {
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("checking known_hosts at %s: %w", path, err)
	case !tofu:
		return fmt.Errorf("known_hosts not found at %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), knownHostsDirPerm); err != nil {
		return fmt.Errorf("creating the directory for known_hosts at %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, knownHostsPerm) //nolint:gosec // operator-supplied path
	if err != nil {
		return fmt.Errorf("creating known_hosts at %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("creating known_hosts at %s: %w", path, err)
	}
	return nil
}

// addKnownHost appends one trust-on-first-use entry. The callback built by
// knownhosts.New holds a snapshot taken at Dial time, so the new entry takes
// effect for the next Dial, not for the connection in progress - which is
// harmless, since this call is what accepted that connection.
func addKnownHost(path, hostname string, key ssh.PublicKey) error {
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)

	f, err := os.OpenFile(path, //nolint:gosec // operator-supplied path
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, knownHostsPerm)
	if err != nil {
		return fmt.Errorf("opening known_hosts at %s to record a new host key: %w", path, err)
	}
	// The explicit Close below reports a real failure; this deferred one only
	// covers the error return between here and it.
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("recording the host key of %s in %s: %w", hostname, path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing known_hosts at %s: %w", path, err)
	}
	return nil
}
