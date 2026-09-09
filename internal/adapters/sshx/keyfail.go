package sshx

import (
	"crypto/x509"
	"errors"
	"io/fs"

	"golang.org/x/crypto/ssh"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// This file answers one question: why could this machine's own private key not
// be loaded? Getting it wrong costs an hour of somebody's evening — that is not
// a figure of speech, it is what happened on the work PC, whose key is
// passphrase protected (aes256-ctr, bcrypt KDF) while the program reported
// «результат неизвестен» five rows down.
//
// Unlike authfail.go, nothing here has to read a message. Every case is a type
// or a sentinel the library exports on purpose:
//
//   - *ssh.PassphraseMissingError — keys.go in golang.org/x/crypto/ssh v0.56.0
//     declares it exported and documents both ParsePrivateKey and
//     ParseRawPrivateKey as returning it for an encrypted key. It is matched
//     with errors.As, so a reworded message cannot break the detection and a
//     look-alike message cannot trigger it.
//   - x509.IncorrectPasswordError — the sentinel the OpenSSH and PEM paths both
//     return when a passphrase was supplied and the key would not decrypt with
//     it, matched with errors.Is.
//   - fs.ErrNotExist — no key file at all.
//
// Everything else is a file that is not a key we can read, which is one
// condition however many ways the parsers spell it.
//
// What deliberately does NOT appear anywhere below: key bytes, the passphrase,
// or the key's file name. The classification carries the key's *directory* and
// a value out of a four-member enum, which is all the diagnosis needs to name
// the cause and all a person needs to find the file.

// keyUnusable classifies a failure to load the private key in dir. It never
// returns nil: every caller has already failed by the time it is asked.
func keyUnusable(err error, dir string) *core.KeyUnusableError {
	// A failed file operation names the file it was given. Unwrapping the
	// *fs.PathError keeps the reason and drops the name, which is why the
	// classification is the only place that touches a read error: the rule
	// that the key's file must not be named lives here, once.
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return &core.KeyUnusableError{Dir: dir, Problem: keyProblem(err), Err: err}
}

// keyProblem maps the library's error onto the domain's four conditions.
func keyProblem(err error) core.KeyProblem {
	var locked *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &locked):
		return core.KeyPassphraseNeeded
	case errors.Is(err, x509.IncorrectPasswordError):
		return core.KeyPassphraseWrong
	case errors.Is(err, fs.ErrNotExist):
		return core.KeyFileMissing
	default:
		return core.KeyFileUnusable
	}
}
