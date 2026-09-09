package sshx

import (
	"errors"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// This file answers one question: did the far side refuse our key, or did the
// network refuse the packet? Getting it wrong in either direction is a real
// defect - a network fault reported as a refused key sends the user editing an
// authorized-keys file for nothing, and a refused key reported as a network
// fault is the shrug this program exists to replace.
//
// golang.org/x/crypto/ssh gives us no type to match on. Its client
// authentication loop (client_auth.go, v0.56.0) ends by returning a plain
// fmt.Errorf, and the transport wraps that in "ssh: handshake failed: %w".
// Checked against this package's own in-process server, a key the server does
// not accept produces exactly:
//
//	ssh handshake with 127.0.0.1:54293: ssh: handshake failed:
//	ssh: unable to authenticate, attempted methods [none publickey],
//	no supported methods remain
//
// so the library's own sentence is the only thing there is to match. Two
// properties make that safe rather than a guess at a message:
//
//   - The sentence is written by Go, in English, in this process. It is not a
//     Windows message, so the peer's Russian display language cannot change it.
//     That is the same reason every other answer in this package is a number
//     rather than a word - but here the text never crosses the wire at all.
//   - It is matched as the prefix of one link of the error chain, never as a
//     substring of the whole message. Our own wrapper ("ssh handshake with
//     %s") and the library's ("ssh: handshake failed") are walked past; a dial
//     error, a read error, a host-key mismatch or a context deadline begins
//     with none of these sentences.
//
// What is deliberately NOT matched is a server-side disconnect: x/crypto/ssh
// renders that as "ssh: disconnect, reason 14: ..." through an unexported type,
// and reason 14 (SSH_DISCONNECT_NO_MORE_AUTH_METHODS_AVAILABLE) is the server's
// own account of why it hung up rather than the client's account of what it
// tried. Matching a reason code out of a formatted string would widen the match
// for a case Windows OpenSSH does not produce here, so it is left out.
var authRefusalPrefixes = [...]string{
	// The terminal state of the client's authentication loop: every method it
	// had was refused and the server offered nothing else it can do.
	"ssh: unable to authenticate",
	// The client's own guard against a server that keeps asking for more. It
	// can only fire after a run of refusals, so it means the same thing.
	"ssh: too many authentication attempts",
}

// isAuthRefused reports whether err is the SSH library saying our credentials
// were refused, as opposed to anything the network did.
func isAuthRefused(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := e.Error()
		for _, prefix := range authRefusalPrefixes {
			if strings.HasPrefix(msg, prefix) {
				return true
			}
		}
	}
	return false
}

// authRefused describes a refused handshake in the terms core understands. It
// names the destination and the account and nothing else: the key path, the key
// bytes and the passphrase never appear in it, and the library's own sentence is
// dropped because it has already been read for everything it can tell us.
func authRefused(cfg Config) *core.AuthRefusedError {
	return &core.AuthRefusedError{Addr: cfg.Addr, Port: cfg.Port, User: cfg.User}
}
