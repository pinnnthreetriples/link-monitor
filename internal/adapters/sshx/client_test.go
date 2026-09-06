package sshx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestDialSucceedsWithAKnownHostKey(t *testing.T) {
	t.Parallel()
	client, _ := newTestClient(t)

	if err := client.Close(); err != nil {
		t.Fatalf("closing the client: %v", err)
	}
	// Close is idempotent and reports the same thing to every caller.
	if err := client.Close(); err != nil {
		t.Fatalf("closing the client twice: %v", err)
	}
}

func TestDialRejectsAMissingRequiredField(t *testing.T) {
	t.Parallel()
	cases := map[string]Config{
		"no address": {User: "pnj", KeyPath: "k"},
		"no user":    {Addr: "100.124.47.73", KeyPath: "k"},
		"no key":     {Addr: "100.124.47.73", User: "pnj"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Dial(t.Context(), cfg); err == nil {
				t.Fatal("expected an error for an incomplete config")
			}
		})
	}
}

func TestDialReportsAMissingKeyWithoutNamingTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "id_ed25519")

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: 1, User: "pnj",
		KeyPath: missing, KnownHostsPath: filepath.Join(dir, "known_hosts"),
	})
	if err == nil {
		t.Fatal("expected an error for a missing key file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected the error to unwrap to fs.ErrNotExist, got %v", err)
	}
	// The directory is fine to name; the key file itself is not.
	if strings.Contains(err.Error(), "id_ed25519") {
		t.Fatalf("the error names the key file: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("the error should name the key's directory, got %v", err)
	}
}

func TestDialRejectsAMalformedKeyWithoutLeakingItsBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	secret := "NOT-A-KEY-BUT-SECRET-LOOKING-0123456789"
	if err := os.WriteFile(keyPath, []byte(secret), 0o600); err != nil {
		t.Fatalf("writing the bogus key: %v", err)
	}

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: 1, User: "pnj",
		KeyPath: keyPath, KnownHostsPath: filepath.Join(dir, "known_hosts"),
	})
	if err == nil {
		t.Fatal("expected an error for a malformed key")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("the error carries key bytes")
	}
}

func TestDialRejectsAnUnknownHostWhenTrustOnFirstUseIsOff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")
	server := newSSHServer(t, clientPub)

	// A known_hosts that exists but names some other host.
	other := writeKnownHosts(t, dir, "100.127.188.87:22", newSigner(t).PublicKey())

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj",
		KeyPath: keyPath, KnownHostsPath: other, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected the unknown host key to be refused")
	}
	if !strings.Contains(err.Error(), "trust-on-first-use is off") {
		t.Fatalf("expected the refusal to explain itself, got %v", err)
	}
}

func TestDialRejectsAMissingKnownHostsWhenTrustOnFirstUseIsOff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, _ := writeKeyFile(t, dir, "id_ed25519")

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: 1, User: "pnj", KeyPath: keyPath,
		KnownHostsPath: filepath.Join(dir, "nowhere", "known_hosts"),
	})
	if err == nil {
		t.Fatal("expected a missing known_hosts to be a configuration error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestTrustOnFirstUseAcceptsAndRecordsANewHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")
	server := newSSHServer(t, clientPub)
	knownHosts := filepath.Join(dir, "fresh", "known_hosts")

	cfg := Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KnownHostsPath: knownHosts, TrustOnFirstUse: true, Timeout: 5 * time.Second,
	}
	client, err := Dial(t.Context(), cfg)
	if err != nil {
		t.Fatalf("trust-on-first-use should have accepted a new host: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	recorded, err := os.ReadFile(knownHosts) //nolint:gosec // a path this test just built
	if err != nil {
		t.Fatalf("reading the recorded known_hosts: %v", err)
	}
	want := string(ssh.MarshalAuthorizedKey(server.hostKey.PublicKey()))
	if !strings.Contains(string(recorded), strings.TrimSpace(want)) {
		t.Fatal("the host key was not recorded in known_hosts")
	}

	// Second dial: the entry now exists, so strict verification must pass.
	cfg.TrustOnFirstUse = false
	again, err := Dial(t.Context(), cfg)
	if err != nil {
		t.Fatalf("the recorded host key should verify strictly: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
}

func TestTrustOnFirstUseStillRejectsAChangedHostKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")
	server := newSSHServer(t, clientPub)

	// known_hosts names this host, but with somebody else's key.
	knownHosts := writeKnownHosts(t, dir, server.addr(), newSigner(t).PublicKey())

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KnownHostsPath: knownHosts, TrustOnFirstUse: true, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("a changed host key must be refused even with trust-on-first-use")
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		t.Fatalf("expected a knownhosts mismatch, got %v", err)
	}
}

func TestDialRejectsAnUnauthorizedKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, authorized := writeKeyFile(t, dir, "authorized")
	strangerPath, _ := writeKeyFile(t, dir, "stranger")
	server := newSSHServer(t, authorized)
	knownHosts := writeKnownHosts(t, dir, server.addr(), server.hostKey.PublicKey())

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: server.port(), User: "user",
		KeyPath: strangerPath, KnownHostsPath: knownHosts, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected the peer to refuse an unauthorized key")
	}
	if !strings.Contains(err.Error(), "ssh handshake") {
		t.Fatalf("expected a handshake error, got %v", err)
	}
}

func TestDialHonoursACancelledContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")
	server := newSSHServer(t, clientPub)
	knownHosts := writeKnownHosts(t, dir, server.addr(), server.hostKey.PublicKey())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Dial(ctx, Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj",
		KeyPath: keyPath, KnownHostsPath: knownHosts,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDialAcceptsAnEncryptedKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	passphrase := []byte("не показывай меня")
	keyPath, clientPub := writeEncryptedKeyFile(t, dir, "id_ed25519", passphrase)
	server := newSSHServer(t, clientPub)
	knownHosts := writeKnownHosts(t, dir, server.addr(), server.hostKey.PublicKey())

	cfg := Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KeyPassphrase: passphrase, KnownHostsPath: knownHosts, Timeout: 5 * time.Second,
	}
	client, err := Dial(t.Context(), cfg)
	if err != nil {
		t.Fatalf("dialling with an encrypted key: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	// The wrong passphrase must fail without echoing what was tried.
	cfg.KeyPassphrase = []byte("wrong-passphrase-xyzzy")
	if _, err := Dial(t.Context(), cfg); err == nil {
		t.Fatal("expected the wrong passphrase to be refused")
	} else if strings.Contains(err.Error(), "xyzzy") {
		t.Fatalf("the error carries the passphrase: %v", err)
	}
}

func TestHostKeyCallbackRejectsAnUnreadableKnownHosts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte("this is not a known_hosts line\n"), 0o600); err != nil {
		t.Fatalf("writing the broken known_hosts: %v", err)
	}

	if _, err := hostKeyCallback(Config{KnownHostsPath: path}); err == nil {
		t.Fatal("expected a malformed known_hosts to be reported")
	}
}

func TestConfigDefaults(t *testing.T) {
	t.Parallel()
	got := Config{Addr: "100.127.188.87"}.withDefaults()
	if got.Port != DefaultPort {
		t.Errorf("port: got %d, want %d", got.Port, DefaultPort)
	}
	if got.Timeout != DefaultTimeout {
		t.Errorf("timeout: got %v, want %v", got.Timeout, DefaultTimeout)
	}
	if addr := got.hostPort(); addr != "100.127.188.87:22" {
		t.Errorf("hostPort: got %q", addr)
	}
}

func TestKnownHostsFileFallsBackToTheHomeDirectory(t *testing.T) {
	t.Parallel()
	explicit, err := knownHostsFile(Config{KnownHostsPath: `C:\tmp\kh`})
	if err != nil {
		t.Fatalf("explicit path: %v", err)
	}
	if explicit != `C:\tmp\kh` {
		t.Errorf("explicit path: got %q", explicit)
	}

	fallback, err := knownHostsFile(Config{})
	if err != nil {
		t.Fatalf("fallback path: %v", err)
	}
	if !strings.HasSuffix(fallback, filepath.Join(".ssh", "known_hosts")) {
		t.Errorf("fallback path: got %q", fallback)
	}
}
