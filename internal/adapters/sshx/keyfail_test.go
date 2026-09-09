package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// TestLoadSignerNamesAPassphraseProtectedKey is the regression guard for the
// defect that cost a live debugging session. The work PC's key is an ed25519
// key protected by a passphrase — ssh-keygen writes those as aes256-ctr with a
// bcrypt KDF, which is exactly what ssh.MarshalPrivateKeyWithPassphrase
// produces here — and x/crypto answers *ssh.PassphraseMissingError. That is an
// exported type and it says precisely what is wrong; it used to be wrapped into
// prose and flattened five rows later into «результат неизвестен».
func TestLoadSignerNamesAPassphraseProtectedKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, _ := writeEncryptedKeyFile(t, dir, "id_ed25519", []byte("не показывай меня"))

	_, err := loadSigner(keyPath, nil)
	if err == nil {
		t.Fatal("expected a locked key to be refused")
	}

	var unusable *core.KeyUnusableError
	if !errors.As(err, &unusable) {
		t.Fatalf("got %T (%v), want *core.KeyUnusableError", err, err)
	}
	if unusable.Problem != core.KeyPassphraseNeeded {
		t.Errorf("Problem = %v, want KeyPassphraseNeeded", unusable.Problem)
	}
	if unusable.Dir != dir {
		t.Errorf("Dir = %q, want the key's directory %q", unusable.Dir, dir)
	}

	// The library's own typed answer stays reachable: this is classification by
	// type all the way down, not a message match anywhere.
	var locked *ssh.PassphraseMissingError
	if !errors.As(err, &locked) {
		t.Errorf("the library's *ssh.PassphraseMissingError should stay in the chain, got %v", err)
	}
	if strings.Contains(err.Error(), "id_ed25519") {
		t.Errorf("the error names the key file: %v", err)
	}
}

// TestDialReportsALockedKeyBeforeTouchingTheNetwork checks where the answer
// comes from: the key is read first, so a locked key is named without a peer,
// a socket or a timeout being involved at all. Port 1 has nothing on it.
func TestDialReportsALockedKeyBeforeTouchingTheNetwork(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, _ := writeEncryptedKeyFile(t, dir, "id_ed25519", []byte("passphrase"))

	_, err := Dial(t.Context(), Config{
		Addr: "100.127.188.87", Port: 1, User: "user",
		KeyPath: keyPath, KnownHostsPath: filepath.Join(dir, "known_hosts"),
	})

	var unusable *core.KeyUnusableError
	if !errors.As(err, &unusable) || unusable.Problem != core.KeyPassphraseNeeded {
		t.Fatalf("Dial() error = %T (%v), want a locked-key classification", err, err)
	}
}

// TestLoadSignerClassifiesEveryKeyFailure walks the four conditions the domain
// distinguishes. Each leads the user somewhere different, which is why they are
// four values and not one "key error".
func TestLoadSignerClassifiesEveryKeyFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	locked, _ := writeEncryptedKeyFile(t, dir, "locked_key", []byte("right"))
	plain, _ := writeKeyFile(t, dir, "plain_key")
	garbage := filepath.Join(dir, "garbage_key")
	if err := os.WriteFile(garbage, []byte("NOT-A-KEY-AT-ALL"), 0o600); err != nil {
		t.Fatalf("writing the bogus key: %v", err)
	}
	pub := filepath.Join(dir, "public_key")
	if err := os.WriteFile(pub, ssh.MarshalAuthorizedKey(newSigner(t).PublicKey()), 0o600); err != nil {
		t.Fatalf("writing the public key: %v", err)
	}

	tests := map[string]struct {
		path       string
		passphrase []byte
		want       core.KeyProblem
		ok         bool
	}{
		"no file at all":            {filepath.Join(dir, "nowhere"), nil, core.KeyFileMissing, false},
		"locked, no passphrase":     {locked, nil, core.KeyPassphraseNeeded, false},
		"locked, wrong passphrase":  {locked, []byte("wrong"), core.KeyPassphraseWrong, false},
		"locked, right passphrase":  {locked, []byte("right"), 0, true},
		"not a key":                 {garbage, nil, core.KeyFileUnusable, false},
		"a public key by mistake":   {pub, nil, core.KeyFileUnusable, false},
		"a plain key, as it should": {plain, nil, 0, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			signer, err := loadSigner(tt.path, tt.passphrase)
			if tt.ok {
				if err != nil || signer == nil {
					t.Fatalf("loadSigner() = %v, %v, want a signer", signer, err)
				}
				return
			}
			var unusable *core.KeyUnusableError
			if !errors.As(err, &unusable) {
				t.Fatalf("got %T (%v), want *core.KeyUnusableError", err, err)
			}
			if unusable.Problem != tt.want {
				t.Errorf("Problem = %v, want %v", unusable.Problem, tt.want)
			}
		})
	}
}

// TestLoadSignerKeepsTheLibraryErrorReachable: the classification wraps rather
// than replaces, so callers that match the underlying error keep working — a
// missing key file still unwraps to fs.ErrNotExist.
func TestLoadSignerKeepsTheLibraryErrorReachable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_, err := loadSigner(filepath.Join(dir, "id_ed25519"), nil)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false for %v", err)
	}
}

// TestLoadSignerTakesAPassphraseForAKeyThatDoesNotNeedOne. The plain parse is
// tried first, so a configured passphrase against an unencrypted key is
// harmless instead of being the failure the library answers with in that case
// ("ssh: key is not password protected"). A monitor that broke on a *working*
// key because it was told a secret it did not need would be a poor monitor.
func TestLoadSignerTakesAPassphraseForAKeyThatDoesNotNeedOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, _ := writeKeyFile(t, dir, "id_ed25519")

	if _, err := loadSigner(keyPath, []byte("unnecessary")); err != nil {
		t.Fatalf("loadSigner() = %v, want the plain key to load anyway", err)
	}
}

// TestKeyFailuresCarryNoCredential is the rule that outranks helpfulness. Not
// the passphrase, not a byte of the key, not the key's file name — whatever
// path the classification takes.
func TestKeyFailuresCarryNoCredential(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	const passphrase = "correct-horse-battery-staple"
	locked, _ := writeEncryptedKeyFile(t, dir, "id_ed25519", []byte(passphrase))

	// A real key's own bytes, so the test would catch a parser that echoed them.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "link-monitor test")
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	plainBytes := string(pem.EncodeToMemory(block))
	truncated := filepath.Join(dir, "truncated_key")
	if err := os.WriteFile(truncated, []byte(plainBytes[:len(plainBytes)/2]), 0o600); err != nil {
		t.Fatalf("writing the truncated key: %v", err)
	}

	attempts := []struct {
		path       string
		passphrase []byte
	}{
		{locked, nil},
		{locked, []byte(passphrase + "-nope")},
		{truncated, nil},
		{filepath.Join(dir, "nowhere", "id_ed25519"), nil},
	}
	for _, a := range attempts {
		_, err := loadSigner(a.path, a.passphrase)
		if err == nil {
			t.Fatalf("loadSigner(%q) succeeded, want a failure to inspect", a.path)
		}
		text := err.Error()
		for _, secret := range []string{passphrase, "id_ed25519", "BEGIN OPENSSH", "b3BlbnNzaC1rZXktdjE"} {
			if strings.Contains(text, secret) {
				t.Errorf("the error for %q mentions %q: %v", a.path, secret, err)
			}
		}
	}
}
