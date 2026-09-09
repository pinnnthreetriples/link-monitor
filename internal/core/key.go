package core

import "fmt"

// KeyProblem says which way this machine's own SSH key could not be used. The
// four values are the four things that can go wrong before a single packet is
// sent: there is no key file, the file is not a key we can read, the key is
// encrypted and nobody gave us the passphrase, or the passphrase we were given
// does not fit.
//
// They are split rather than collapsed because they lead the user to different
// actions — a locked key wants a passphrase or an unlocked key, a missing one
// wants a path or a keygen — and collapsing them is exactly the shrug this
// program exists to replace.
type KeyProblem int

const (
	// KeyPassphraseNeeded is the condition found on the work PC: the private
	// key is encrypted (aes256-ctr with a bcrypt KDF, as ssh-keygen writes it)
	// and no passphrase was configured, so the library refuses to parse it.
	// This is the one that cost a live debugging session, reported as
	// «результат неизвестен» while the cause was sitting in the error.
	KeyPassphraseNeeded KeyProblem = iota

	// KeyPassphraseWrong means a passphrase was configured and the key would
	// not decrypt with it. The remedy differs from the one above: the key is
	// fine and the secret is wrong.
	KeyPassphraseWrong

	// KeyFileMissing means there is no file where the key was expected.
	KeyFileMissing

	// KeyFileUnusable means the file is there and is not a private key this
	// program can read — the wrong file, a public key, a truncated one, or a
	// format the library does not support.
	KeyFileUnusable
)

// String reports the problem in English, for the error text and for logs. It
// describes the *condition*, never the key: no bytes, no passphrase, no file
// name. The Russian a user reads is composed in core/diagnose.
func (p KeyProblem) String() string {
	switch p {
	case KeyPassphraseNeeded:
		return "the key is passphrase protected and no passphrase was supplied"
	case KeyPassphraseWrong:
		return "the supplied passphrase does not decrypt the key"
	case KeyFileMissing:
		return "there is no key file"
	case KeyFileUnusable:
		return "the file is not a private key this program can read"
	default:
		return fmt.Sprintf("KeyProblem(%d)", int(p))
	}
}

// NeedsPassphrase reports whether the problem is about the passphrase rather
// than about the file. The two lead to different advice, and this is the one
// place that mapping is written down.
func (p KeyProblem) NeedsPassphrase() bool {
	return p == KeyPassphraseNeeded || p == KeyPassphraseWrong
}

// KeyUnusableError means this machine's own private key could not be loaded, so
// no SSH login could even be attempted. It is the fourth member of the family
// BlockedError, TimeoutError and AuthRefusedError belong to, and the only one
// whose cause is entirely on this side: nothing was asked of the peer, no
// packet was sent, and the peer is innocent.
//
// Like AuthRefusedError it must never be filed as StateUnknown. The library
// tells us exactly what is wrong — *ssh.PassphraseMissingError is an exported
// type — and a row that answers «результат неизвестен» to a question the
// library already answered is the defect this type exists to close.
//
// It carries nothing but what identifies the condition. Dir is the key's
// *directory* and never the file, and there are no key bytes and no passphrase
// anywhere in it or in its text: this value is formatted into wrapped errors,
// and a credential must not be reachable from one. Err keeps the library's own
// error so a person debugging can still see it; it is never rendered into the
// user's Russian.
type KeyUnusableError struct {
	// Dir is the directory the key file lives in, e.g. `C:\Users\pnj\.ssh`.
	// The file name stays out: naming it would name a credential's location
	// more precisely than this program is allowed to.
	Dir string
	// Problem says which way the key was unusable.
	Problem KeyProblem
	// Err is the underlying library error, kept for context so that
	// errors.Is(err, fs.ErrNotExist) and the like keep working. It may be nil.
	Err error
}

func (e *KeyUnusableError) Error() string {
	return fmt.Sprintf("the private key in %s cannot be used: %s", e.Dir, e.Problem)
}

// Unwrap exposes the library's error for errors.Is and errors.As, without
// putting its English text into anything the user reads.
func (e *KeyUnusableError) Unwrap() error { return e.Err }

// PortClosedError means the connection attempt reached the far end and was
// actively refused — the TCP stack there answered with a reset (Windows reports
// it as WSAECONNREFUSED, 10061). Nothing is listening on that port.
//
// It is deliberately not a TimeoutError: a refusal is *stronger* evidence than
// silence. Silence can be a firewall dropping packets; a reset means the
// machine is up, its stack answered, and the service is simply not there. Those
// two lead the user to different places, so they are two types and two
// sentences, and neither of them is «результат неизвестен» — which is what this
// condition used to be reported as, on the commonest SSH failure there is.
type PortClosedError struct {
	Addr string
	Port int
}

func (e *PortClosedError) Error() string {
	return fmt.Sprintf("connection to %s:%d was refused: nothing is listening there", e.Addr, e.Port)
}
