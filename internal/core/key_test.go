package core

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// TestKeyUnusableErrorNamesNoCredential holds the one absolute rule over the
// new type. Its text is formatted into wrapped errors and may be shown to a
// person debugging, so it may name the directory the key lives in — that is
// what internal/adapters/sshx already allows itself — and it may never name the
// key file, the key bytes or the passphrase.
func TestKeyUnusableErrorNamesNoCredential(t *testing.T) {
	t.Parallel()

	err := &KeyUnusableError{
		Dir:     `C:\Users\pnj\.ssh`,
		Problem: KeyPassphraseNeeded,
		Err:     errors.New("ssh: this private key is passphrase protected"),
	}
	text := err.Error()

	if !strings.Contains(text, `C:\Users\pnj\.ssh`) {
		t.Errorf("Error() = %q, want the key's directory named", text)
	}
	// The file name, any key material, and the passphrase itself. Naming the
	// *condition* ("passphrase protected") is the point of the type; naming the
	// secret, or the file holding it, never is.
	for _, secret := range []string{"id_ed25519", "id_rsa", "BEGIN OPENSSH", "hunter2"} {
		if strings.Contains(text, secret) {
			t.Errorf("Error() = %q, must not mention %q", text, secret)
		}
	}
}

// TestKeyUnusableErrorKeepsTheLibraryError checks that wrapping still works:
// the adapter's callers match fs.ErrNotExist through this type, and a test in
// internal/adapters/sshx depends on it.
func TestKeyUnusableErrorKeepsTheLibraryError(t *testing.T) {
	t.Parallel()

	err := &KeyUnusableError{Dir: `C:\Users\pnj\.ssh`, Problem: KeyFileMissing, Err: fs.ErrNotExist}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false, want the library error to stay reachable")
	}

	var target *KeyUnusableError
	if !errors.As(err, &target) || target.Problem != KeyFileMissing {
		t.Errorf("errors.As did not recover the problem from %v", err)
	}
	if bare := (&KeyUnusableError{Dir: "d", Problem: KeyFileUnusable}); errors.Unwrap(bare) != nil {
		t.Errorf("Unwrap() = %v, want nil when there is no library error", errors.Unwrap(bare))
	}
}

func TestKeyProblem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		problem    KeyProblem
		says       string
		passphrase bool
	}{
		{KeyPassphraseNeeded, "no passphrase was supplied", true},
		{KeyPassphraseWrong, "does not decrypt", true},
		{KeyFileMissing, "no key file", false},
		{KeyFileUnusable, "not a private key", false},
		{KeyProblem(99), "KeyProblem(99)", false},
	}
	for _, tt := range tests {
		if got := tt.problem.String(); !strings.Contains(got, tt.says) {
			t.Errorf("KeyProblem(%d).String() = %q, want it to mention %q", tt.problem, got, tt.says)
		}
		if got := tt.problem.NeedsPassphrase(); got != tt.passphrase {
			t.Errorf("KeyProblem(%d).NeedsPassphrase() = %v, want %v", tt.problem, got, tt.passphrase)
		}
	}
}

// TestTypedErrorsSayWhatHappened keeps every member of the family readable: an
// error a person may see while debugging has to name the condition and the
// destination, and none of them may go bare.
func TestTypedErrorsSayWhatHappened(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want []string
	}{
		{"blocked", &BlockedError{Addr: "100.127.188.87", Port: 22}, []string{"100.127.188.87:22", "WSAEACCES"}},
		{"timeout", &TimeoutError{Addr: "100.127.188.87", Port: 22}, []string{"100.127.188.87:22", "timed out"}},
		{
			"refused",
			&AuthRefusedError{Addr: "100.127.188.87", Port: 22, User: "user"},
			[]string{"100.127.188.87:22", "user"},
		},
		{
			"port closed",
			&PortClosedError{Addr: "100.124.47.73", Port: 22},
			[]string{"100.124.47.73:22", "refused"},
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

func TestStateString(t *testing.T) {
	t.Parallel()

	tests := map[State]string{
		StateOK:      "ok",
		StateUnknown: "unknown",
		StateWarn:    "warn",
		StateFail:    "fail",
		State(9):     "State(9)",
	}
	for state, want := range tests {
		if got := state.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", int(state), got, want)
		}
	}
}
