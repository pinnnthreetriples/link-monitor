package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// knownHostsPerm and knownHostsDirPerm keep the file readable by its owner
// only, the way OpenSSH writes it.
const (
	knownHostsPerm    os.FileMode = 0o600
	knownHostsDirPerm os.FileMode = 0o700
)

// hostKeyVerifier is what Dial needs in order to establish who answered: the
// callback that checks the key, and the host key algorithms to ask for in the
// first place. The two belong together because they are two halves of one
// decision - see hostkeyalgo.go for why asking for the wrong types is not a
// performance detail but the difference between a true and a false alarm.
type hostKeyVerifier struct {
	verify ssh.HostKeyCallback
	// algorithms goes into ssh.ClientConfig.HostKeyAlgorithms. Nil means leave
	// the library's default preference alone, which is right exactly when
	// known_hosts pins nothing for this host.
	algorithms []string
}

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
//
// known_hosts can say three different things about a key it will not accept,
// and reading it as two is the defect this file was rewritten to remove. What
// it used to claim - "Want is empty only when the host is unknown; a non-empty
// Want is a mismatch" - is false, and the library's own comment on the field
// says it no more carefully. Want holds every entry matching the *address*, of
// any key type; a host with an ed25519 entry that answers with its ecdsa key
// fills Want without one thing having changed. The three cases are:
//
//   - no entry at all -> *core.HostKeyUnknownError, or a recorded key under
//     trust-on-first-use. Nothing contradicts anything; the host has simply
//     never been verified.
//   - an entry of the very type offered, holding a different key ->
//     *core.HostKeyMismatchError. This is the contradiction, and the only one
//     that earns the verdict upstream about the machine answering possibly not
//     being the machine the user means.
//   - entries, but none of the offered type -> unpinnedHostKeyError, which
//     unwraps to *core.HostKeyUnknownError. Nothing is contradicted and nothing
//     is verified either, so it is refused in every mode and never recorded;
//     the type's own comment says why trust-on-first-use does not reach it.
//
// Both typed errors carry the fingerprint of the key that was offered, because
// comparing it with the fingerprint read on the other machine itself is the
// only honest way out of any of these. Neither carries the path of known_hosts:
// that goes into the English error text a developer reads, not into a typed
// value the user's Russian is composed from.
func hostKeyCallback(cfg Config) (hostKeyVerifier, error) {
	path, err := knownHostsFile(cfg)
	if err != nil {
		return hostKeyVerifier{}, err
	}
	if err := ensureKnownHosts(path, cfg.TrustOnFirstUse); err != nil {
		return hostKeyVerifier{}, err
	}

	verify, err := knownhosts.New(path)
	if err != nil {
		return hostKeyVerifier{}, fmt.Errorf("reading known_hosts at %s: %w", path, err)
	}

	return hostKeyVerifier{
		verify:     checkHostKey(cfg, path, verify),
		algorithms: hostKeyAlgorithms(pinnedHostKeys(verify, cfg.hostPort())),
	}, nil
}

// checkHostKey wraps the knownhosts callback in the classification described on
// hostKeyCallback. Anything that is not a knownhosts.KeyError - a revoked key,
// a certificate with no authority behind it - is passed through wrapped, and
// deliberately so: a refusal somebody has already decided is not a question
// about which machine answered, and it keeps its own error.
func checkHostKey(cfg Config, path string, verify ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return fmt.Errorf("verifying the host key of %s against %s: %w", hostname, path, err)
		}

		switch hostKeyVerdictFor(keyErr.Want, key) {
		case hostKeyChanged:
			return &core.HostKeyMismatchError{
				Addr: cfg.Addr, Port: cfg.Port,
				Fingerprint: ssh.FingerprintSHA256(key),
				Err:         err,
			}
		case hostKeyTypeUnpinned:
			return unpinnedHostKey(cfg, keyErr.Want, key, err)
		}

		if !cfg.TrustOnFirstUse {
			return &core.HostKeyUnknownError{
				Addr: cfg.Addr, Port: cfg.Port,
				Fingerprint: ssh.FingerprintSHA256(key),
				Err:         err,
			}
		}
		return addKnownHost(path, hostname, key)
	}
}

// hostKeyVerdict is what known_hosts says about a key it did not accept.
type hostKeyVerdict int

const (
	// hostUnknown: nothing in known_hosts matches this host.
	hostUnknown hostKeyVerdict = iota
	// hostKeyChanged: known_hosts has an entry of the offered key's own type,
	// and it holds a different key. Every entry reported failed to equal the
	// key offered - a match would have ended the check with no error at all -
	// so an entry of the offered type is a genuine contradiction.
	hostKeyChanged
	// hostKeyTypeUnpinned: known_hosts has entries for this host but none of
	// the offered type, so there is nothing to compare the key against.
	hostKeyTypeUnpinned
)

// hostKeyVerdictFor classifies the entries knownhosts reported against the key
// the host actually offered. It compares key *types*, read from Key.Type(), and
// never the text of an error message.
func hostKeyVerdictFor(want []knownhosts.KnownKey, offered ssh.PublicKey) hostKeyVerdict {
	if len(want) == 0 {
		return hostUnknown
	}
	offeredType := hostKeyType(offered.Type())
	for _, known := range want {
		if hostKeyType(known.Key.Type()) == offeredType {
			return hostKeyChanged
		}
	}
	return hostKeyTypeUnpinned
}

// unpinnedHostKeyError is the third thing known_hosts can say: it knows this
// host, and it knows nothing about a key of the type the host just offered.
//
// It is not a mismatch and is never reported as one. Nothing has been
// contradicted: a machine that gained a key type since the entry was written,
// or an entry another tool wrote under one type while the server prefers
// another, produce this with nothing whatever being wrong. Telling the user
// their machine may have been replaced on that evidence is the false alarm this
// package was fixed to stop. After the negotiation in hostkeyalgo.go it should
// be rare - reachable mainly when the server has no key of any pinned type at
// all - but rare is not never, and the classification has to be right on its
// own terms rather than because the negotiation usually hides it.
//
// It is not an acceptance either, in any mode. The key is unverifiable: we hold
// a pin for this host and this key cannot be checked against it.
// Trust-on-first-use deliberately does not reach here, because its licence is
// first contact - accept and record a host with *no* entry - and a host with
// entries is past first contact. Recording a second key type on sight would let
// anyone who can answer on that address earn an entry of their own next to the
// real one, which is most of the protection the pin was there to give.
//
// So it unwraps to *core.HostKeyUnknownError, whose verdict upstream says both
// true things - nothing is broken, nothing is proven - and whose remedy is the
// right one: read the fingerprint on the machine itself and record it. The
// English sentence here is the precise one, for the developer reading a log;
// like every error in this package it names no path, only key types.
type unpinnedHostKeyError struct {
	unknown *core.HostKeyUnknownError
	offered string
	pinned  []string
}

// unpinnedHostKey builds that error from the entries known_hosts held.
func unpinnedHostKey(cfg Config, want []knownhosts.KnownKey, key ssh.PublicKey, err error) error {
	pinned := make([]string, 0, len(want))
	for _, known := range want {
		if t := hostKeyType(known.Key.Type()); !slices.Contains(pinned, t) {
			pinned = append(pinned, t)
		}
	}
	return &unpinnedHostKeyError{
		unknown: &core.HostKeyUnknownError{
			Addr: cfg.Addr, Port: cfg.Port,
			Fingerprint: ssh.FingerprintSHA256(key),
			Err:         err,
		},
		offered: hostKeyType(key.Type()),
		pinned:  pinned,
	}
}

func (e *unpinnedHostKeyError) Error() string {
	return fmt.Sprintf("%s:%d offered a %s host key and known_hosts records only %s for it, "+
		"so the key is neither confirmed nor contradicted (%s)",
		e.unknown.Addr, e.unknown.Port, e.offered, strings.Join(e.pinned, ", "),
		e.unknown.Fingerprint)
}

// Unwrap exposes the unverified-host condition, and through it the library's
// own error, so that errors.As upstream classifies this as what it is.
func (e *unpinnedHostKeyError) Unwrap() error { return e.unknown }

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
