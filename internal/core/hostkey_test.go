package core

import (
	"errors"
	"strings"
	"testing"
)

// theFingerprint is a real ed25519 host key fingerprint shape. It is public
// information — a fingerprint is what you compare, not what you keep secret —
// and it is here so the tests below can prove it survives into the message the
// user is meant to compare.
const theFingerprint = "SHA256:9qO1kQxUKZBk3H4tR2u6VXbmYcJ7d8fLpNa0sEgWiTo"

// TestHostKeyErrorsAreTwoThingsNotOne is the guard for the distinction this
// pair of types exists to keep. An unknown host and a changed key are not the
// same condition, they do not have the same remedy, and no amount of
// convenience justifies matching one and getting the other.
func TestHostKeyErrorsAreTwoThingsNotOne(t *testing.T) {
	t.Parallel()

	mismatch := &HostKeyMismatchError{
		Addr: "100.127.188.87", Port: 22, Fingerprint: theFingerprint,
		Err: errors.New("knownhosts: key mismatch"),
	}
	unknown := &HostKeyUnknownError{
		Addr: "100.127.188.87", Port: 22, Fingerprint: theFingerprint,
		Err: errors.New("knownhosts: key is unknown"),
	}

	var asMismatch *HostKeyMismatchError
	if errors.As(error(unknown), &asMismatch) {
		t.Error("an unknown host must never be matched as a changed key")
	}
	var asUnknown *HostKeyUnknownError
	if errors.As(error(mismatch), &asUnknown) {
		t.Error("a changed key must never be matched as an unknown host")
	}
	if mismatch.Error() == unknown.Error() {
		t.Error("the two conditions must not read as the same sentence")
	}
}

// TestHostKeyErrorsSayWhatHappened: both name the destination and carry the
// fingerprint, because the fingerprint is the whole remedy.
func TestHostKeyErrorsSayWhatHappened(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			"mismatch",
			&HostKeyMismatchError{Addr: "100.127.188.87", Port: 22, Fingerprint: theFingerprint},
			[]string{"100.127.188.87:22", "does not match", theFingerprint},
		},
		{
			"unknown host",
			&HostKeyUnknownError{Addr: "100.64.6.3", Port: 22, Fingerprint: theFingerprint},
			[]string{"100.64.6.3:22", "known_hosts", theFingerprint},
		},
	}
	for _, tt := range tests {
		text := tt.err.Error()
		for _, want := range tt.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: Error() = %q, want it to mention %q", tt.name, text, want)
			}
		}
	}
}

// TestHostKeyErrorsKeepTheLibraryError: wrapping still works, so a person
// debugging can reach what the verifier said, and a nil cause unwraps to nil
// rather than to something invented.
func TestHostKeyErrorsKeepTheLibraryError(t *testing.T) {
	t.Parallel()

	cause := errors.New("knownhosts: key mismatch")
	if got := errors.Unwrap(&HostKeyMismatchError{Err: cause}); !errors.Is(got, cause) {
		t.Errorf("Unwrap() = %v, want %v", got, cause)
	}
	if got := errors.Unwrap(&HostKeyUnknownError{Err: cause}); !errors.Is(got, cause) {
		t.Errorf("Unwrap() = %v, want %v", got, cause)
	}
	if got := errors.Unwrap(&HostKeyMismatchError{Addr: "a"}); got != nil {
		t.Errorf("Unwrap() of a bare mismatch = %v, want nil", got)
	}
	if got := errors.Unwrap(&HostKeyUnknownError{Addr: "a"}); got != nil {
		t.Errorf("Unwrap() of a bare unknown host = %v, want nil", got)
	}
}

// TestHostKeyErrorsNameNoCredential is the absolute rule again. A fingerprint
// is public and has to be there; the private key, its passphrase and the path
// of known_hosts have no business in a value that gets formatted into logs.
func TestHostKeyErrorsNameNoCredential(t *testing.T) {
	t.Parallel()

	texts := []string{
		(&HostKeyMismatchError{
			Addr: "100.127.188.87", Port: 22, Fingerprint: theFingerprint,
		}).Error(),
		(&HostKeyUnknownError{
			Addr: "100.127.188.87", Port: 22, Fingerprint: theFingerprint,
		}).Error(),
	}
	for _, text := range texts {
		for _, secret := range []string{"PRIVATE KEY", "BEGIN OPENSSH", `C:\Users`, "id_ed25519"} {
			if strings.Contains(text, secret) {
				t.Errorf("%q mentions %q", text, secret)
			}
		}
	}
}

func TestPresenceString(t *testing.T) {
	t.Parallel()

	tests := map[Presence]string{
		PresenceUnknown: "unknown",
		PresenceOnline:  "online",
		PresenceOffline: "offline",
		PresenceAbsent:  "absent",
		Presence(9):     "Presence(9)",
	}
	for presence, want := range tests {
		if got := presence.String(); got != want {
			t.Errorf("Presence(%d).String() = %q, want %q", int(presence), got, want)
		}
	}
}
