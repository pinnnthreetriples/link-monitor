package sshx

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// TestDialUsesOnlyTheHostKeyTypesKnownHostsPins is the regression test for the
// false alarm this file exists to prevent, and it reproduces it exactly as the
// work PC met it.
//
// The laptop, like every Windows OpenSSH install, has three host keys —
// ed25519, ecdsa and rsa — and offers all three. The work PC's known_hosts has
// one entry for it, ssh-ed25519, written by OpenSSH's own client. Nothing is
// wrong with either machine. But x/crypto/ssh's default host key preference
// puts ecdsa-sha2-nistp256 ahead of ssh-ed25519, so with no HostKeyAlgorithms
// of our own the server answered with the ecdsa key, knownhosts reported a
// KeyError whose Want held the ed25519 entry, and this package read a non-empty
// Want as a changed key: «Ключ хоста изменился» about a healthy machine.
//
// The fix is what OpenSSH does — ask only for the key types we have pinned for
// that host — so this dial has to succeed.
func TestDialUsesOnlyTheHostKeyTypesKnownHostsPins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")

	ed := newSigner(t)
	server := newSSHServerWithHostKeys(t, clientPub, ed, newECDSASigner(t))
	// known_hosts pins the ed25519 key only, the way `ssh -o
	// StrictHostKeyChecking=accept-new` leaves it.
	knownHosts := writeKnownHosts(t, dir, server.addr(), ed.PublicKey())

	client, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KnownHostsPath: knownHosts, Timeout: 5 * time.Second,
	})
	if err != nil {
		var mismatch *core.HostKeyMismatchError
		if errors.As(err, &mismatch) {
			t.Fatalf("a healthy host with several key types was called a changed key: %v", err)
		}
		t.Fatalf("dialling a host pinned under one of its key types: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
}

// TestDialStillRefusesAChangedKeyOfAPinnedType is the other half of the pair:
// narrowing the negotiation must not narrow the alarm. The server offers the
// same two key types, but the ed25519 key known_hosts pins is somebody else's,
// so the key we asked for and got is genuinely not the key on file — a
// mismatch, reported as one, with the offered key's own fingerprint.
func TestDialStillRefusesAChangedKeyOfAPinnedType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")

	ed := newSigner(t)
	server := newSSHServerWithHostKeys(t, clientPub, ed, newECDSASigner(t))
	knownHosts := writeKnownHosts(t, dir, server.addr(), newSigner(t).PublicKey())

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KnownHostsPath: knownHosts, TrustOnFirstUse: true, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("a changed key of a pinned type must be refused")
	}
	var mismatch *core.HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected a *core.HostKeyMismatchError, got %#v", err)
	}
	// The fingerprint proves which key was negotiated: the pinned type's, not
	// the ecdsa one the library would have preferred on its own.
	if mismatch.Fingerprint != ssh.FingerprintSHA256(ed.PublicKey()) {
		t.Errorf("fingerprint = %q, want the ed25519 key the pin asked for", mismatch.Fingerprint)
	}
}

// TestDialRefusesAKeyTypeKnownHostsDoesNotPin covers the third thing
// known_hosts can say, the one the old two-way reading had nowhere to put: it
// has entries for this host, and none of them is of the type the host answered
// with. Here the server has only an ecdsa key while known_hosts pins ed25519,
// so the type we asked for first cannot be had and the ecdsa key arrives
// unverifiable.
//
// That is not a mismatch - nothing contradicts anything - so it must not reach
// the verdict about the machine possibly having been replaced. It is not an
// acceptance either, not even with trust-on-first-use on: we hold a pin for
// this host, so this is not first contact, and recording a second key type on
// sight would hand an entry to anyone who can answer on that address.
func TestDialRefusesAKeyTypeKnownHostsDoesNotPin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath, clientPub := writeKeyFile(t, dir, "id_ed25519")

	server := newSSHServerWithHostKeys(t, clientPub, newECDSASigner(t))
	pinned := newSigner(t).PublicKey()
	knownHosts := writeKnownHosts(t, dir, server.addr(), pinned)

	_, err := Dial(t.Context(), Config{
		Addr: "127.0.0.1", Port: server.port(), User: "pnj", KeyPath: keyPath,
		KnownHostsPath: knownHosts, TrustOnFirstUse: true, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("a key of a type known_hosts does not pin must not be accepted")
	}
	var mismatch *core.HostKeyMismatchError
	if errors.As(err, &mismatch) {
		t.Fatalf("an unpinned key type must never be read as a changed key: %v", err)
	}
	var unknown *core.HostKeyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected the unverified-host condition, got %#v", err)
	}
	if unknown.Fingerprint != ssh.FingerprintSHA256(server.hostKey.PublicKey()) {
		t.Errorf("fingerprint = %q, want the key the server actually offered", unknown.Fingerprint)
	}

	// The developer's sentence names both key types and no path at all.
	var unpinned *unpinnedHostKeyError
	if !errors.As(err, &unpinned) {
		t.Fatalf("expected an *unpinnedHostKeyError, got %#v", err)
	}
	if !strings.Contains(unpinned.Error(), ssh.KeyAlgoECDSA256) ||
		!strings.Contains(unpinned.Error(), ssh.KeyAlgoED25519) {
		t.Errorf("the error should name the offered and pinned types: %q", unpinned.Error())
	}
	if strings.Contains(unpinned.Error(), dir) {
		t.Errorf("the typed error names the known_hosts path: %q", unpinned.Error())
	}

	// Trust-on-first-use recorded nothing: the file still holds its one pin.
	after, err := os.ReadFile(knownHosts) //nolint:gosec // a path this test just built
	if err != nil {
		t.Fatalf("reading known_hosts: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(after)), "\n") + 1; lines != 1 {
		t.Errorf("known_hosts grew to %d lines: an unpinned key type must never be recorded", lines)
	}
	if !strings.Contains(string(after), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pinned)))) {
		t.Error("the pinned entry was disturbed")
	}
}

// TestHostKeyVerdictFor pins the three-way classification itself, away from a
// handshake. The verdict is decided by key type, read from Key.Type(), and by
// nothing else - never by the text of an error.
func TestHostKeyVerdictFor(t *testing.T) {
	t.Parallel()
	ed := newSigner(t).PublicKey()
	other := newSigner(t).PublicKey()
	ecdsaKey := newECDSASigner(t).PublicKey()

	tests := []struct {
		name    string
		want    []knownhosts.KnownKey
		offered ssh.PublicKey
		verdict hostKeyVerdict
	}{
		{
			name:    "no entry at all is an unknown host",
			offered: ed,
			verdict: hostUnknown,
		},
		{
			name:    "an entry of the same type holding another key is a mismatch",
			want:    []knownhosts.KnownKey{{Key: other}},
			offered: ed,
			verdict: hostKeyChanged,
		},
		{
			name:    "entries of other types only are not a mismatch",
			want:    []knownhosts.KnownKey{{Key: ed}, {Key: other}},
			offered: ecdsaKey,
			verdict: hostKeyTypeUnpinned,
		},
		{
			name:    "one entry of the offered type among others is a mismatch",
			want:    []knownhosts.KnownKey{{Key: ecdsaKey}, {Key: other}},
			offered: ed,
			verdict: hostKeyChanged,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hostKeyVerdictFor(tc.want, tc.offered); got != tc.verdict {
				t.Errorf("verdict = %d, want %d", got, tc.verdict)
			}
		})
	}
}

// TestHostKeyAlgorithmsPutsPinnedTypesFirst checks the list we offer: the
// pinned type's own algorithm ahead of everything else, and nothing dropped.
// A server that has since gained a new key type must still be able to
// negotiate, so that the classification, and not the algorithm list, is what
// decides what to say about it.
func TestHostKeyAlgorithmsPutsPinnedTypesFirst(t *testing.T) {
	t.Parallel()
	ed := newSigner(t).PublicKey()

	if got := hostKeyAlgorithms(nil); got != nil {
		t.Errorf("with nothing pinned the library default must stand, got %v", got)
	}

	got := hostKeyAlgorithms([]knownhosts.KnownKey{{Key: ed}})
	if len(got) == 0 || got[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("the pinned type must come first, got %v", got)
	}
	all := offerableHostKeyAlgos()
	if len(got) != len(all) {
		t.Errorf("offered %d algorithms, want all %d of them reordered", len(got), len(all))
	}
	for _, algo := range all {
		if !slices.Contains(got, algo) {
			t.Errorf("algorithm %q was dropped from the list we offer", algo)
		}
	}
}

// TestHostKeyTypeCollapsesSignatureAlgorithms is the guard on the one mapping
// this fix rests on. If a later x/crypto adds a host key algorithm whose name
// follows neither pattern hostKeyType knows, a pin of that type would look
// unpinned, and this test is what notices.
func TestHostKeyTypeCollapsesSignatureAlgorithms(t *testing.T) {
	t.Parallel()
	pairs := []struct{ algo, keyType string }{
		{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA},
		{ssh.CertAlgoRSASHA256v01, ssh.KeyAlgoRSA},
		{ssh.CertAlgoED25519v01, ssh.KeyAlgoED25519},
		{ssh.KeyAlgoED25519, ssh.KeyAlgoED25519},
		{ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA256},
	}
	for _, p := range pairs {
		if got := hostKeyType(p.algo); got != p.keyType {
			t.Errorf("hostKeyType(%q) = %q, want %q", p.algo, got, p.keyType)
		}
	}

	// Whatever the library implements, the type it collapses to has to be a
	// plain key type: known_hosts holds nothing else.
	for _, algo := range offerableHostKeyAlgos() {
		keyType := hostKeyType(algo)
		if strings.Contains(keyType, "cert") {
			t.Errorf("hostKeyType(%q) = %q, which is not a known_hosts key type", algo, keyType)
		}
		if strings.HasPrefix(keyType, "rsa-sha2-") {
			t.Errorf("hostKeyType(%q) = %q, but known_hosts writes ssh-rsa", algo, keyType)
		}
	}
}

// TestPinnedHostKeysReadsWhatKnownHostsHolds covers the probe on its own: it
// reports the entries for the host it asks about, and nothing at all for a
// host the file does not mention.
func TestPinnedHostKeysReadsWhatKnownHostsHolds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ed := newSigner(t).PublicKey()
	path := writeKnownHosts(t, dir, "100.124.47.73:22", ed)

	verify, err := knownhosts.New(path)
	if err != nil {
		t.Fatalf("reading the known_hosts this test wrote: %v", err)
	}

	pinned := pinnedHostKeys(verify, "100.124.47.73:22")
	if len(pinned) != 1 || pinned[0].Key.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("pinned = %v, want the one ed25519 entry", pinned)
	}
	if other := pinnedHostKeys(verify, "100.127.188.87:22"); len(other) != 0 {
		t.Errorf("a host the file does not mention must pin nothing, got %v", other)
	}
}

// TestTheKnownHostsProbeMatchesNothing covers the one property the probe rests
// on: whatever known_hosts holds, no entry in it can equal the key we ask
// about, or a pinned host would come back looking unpinned and every alarm in
// this file would be pointed the wrong way.
func TestTheKnownHostsProbeMatchesNothing(t *testing.T) {
	t.Parallel()
	for _, real := range []ssh.PublicKey{
		newSigner(t).PublicKey(),
		newECDSASigner(t).PublicKey(),
	} {
		if bytes.Equal(probeKey{}.Marshal(), real.Marshal()) {
			t.Fatalf("the probe key collides with a real %s key", real.Type())
		}
	}
	if got := (probeKey{}).Type(); got != probeKeyType {
		t.Errorf("probe key type = %q, want %q", got, probeKeyType)
	}
	if err := (probeKey{}).Verify(nil, nil); err == nil {
		t.Error("the probe key must not claim to verify a signature")
	}

	addr := probeAddr("100.124.47.73:22")
	if addr.Network() != "tcp" || addr.String() != "100.124.47.73:22" {
		t.Errorf("probe address = %s/%s, want the destination on tcp", addr.Network(), addr.String())
	}
}

// TestPinnedHostKeysPinsNothingForAnUnusableAddress covers the guard: an
// address knownhosts cannot even split into host and port comes back as an
// error of its own rather than a KeyError, and the honest answer to "what is
// pinned for it" is nothing - which leaves the library's default preference in
// place instead of a list derived from a misread file.
func TestPinnedHostKeysPinsNothingForAnUnusableAddress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeKnownHosts(t, dir, "100.124.47.73:22", newSigner(t).PublicKey())

	verify, err := knownhosts.New(path)
	if err != nil {
		t.Fatalf("reading the known_hosts this test wrote: %v", err)
	}
	if pinned := pinnedHostKeys(verify, "no-port-here"); len(pinned) != 0 {
		t.Errorf("pinned = %v, want nothing at all", pinned)
	}
}
