package core

import "fmt"

// This file holds the two things that can go wrong with a host key, and they
// are held apart on purpose.
//
// Every other member of this family — BlockedError, TimeoutError,
// PortClosedError, AuthRefusedError, KeyUnusableError — is a fault of the
// *link*: something refused a packet, nothing answered one, or a login was
// declined. Neither of the two below is. They are answers to a different
// question, the one nobody thinks to ask until it matters: is the machine
// answering on that address the machine we think it is?
//
// So they never share a verdict with a key or a port problem, and they never
// share one with each other:
//
//   - HostKeyUnknownError: we have never seen this host's key and we were told
//     not to accept one on trust. Nothing is wrong; nothing is verified either.
//   - HostKeyMismatchError: we have seen this host's key, and the key offered
//     now is a different one. That is not an unverified host, it is a
//     contradiction, and it deserves to be read as one.
//
// Conflating those two would be the exact dishonesty this program is built
// against — «неизвестно» and «не совпадает» are not the same sentence — which
// is why they are two types rather than one with a flag.
//
// Both carry the fingerprint of the key that was actually offered. A public
// key's fingerprint is not a credential: it is the one value the user has to
// compare against what the far machine says about itself, and comparing it is
// the only honest way out of either condition. Neither type carries the path of
// known_hosts, the private key, or anything else this program is not allowed to
// put on a screen.

// HostKeyMismatchError means the host at Addr offered a key that is not the one
// known_hosts records for it. The connection was refused and no login was
// attempted.
//
// It is never repaired automatically, and this program offers no way to accept
// the new key with one click. Deciding that a machine is still the machine you
// trust is the user's decision — the same rule that keeps the AmneziaVPN kill
// switch and the peer's authorized-keys file out of the repair path.
type HostKeyMismatchError struct {
	// Addr and Port identify the destination that answered.
	Addr string
	Port int
	// Fingerprint is the SHA256 fingerprint of the key that was offered, in
	// OpenSSH's own form ("SHA256:..."). It is what the user compares with the
	// fingerprint read on the other machine itself.
	Fingerprint string
	// Err is the library's own verification error, kept for context. It may be
	// nil, and it is never rendered into anything the user reads.
	Err error
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("%s:%d offered a host key that does not match the recorded one (%s)",
		e.Addr, e.Port, e.Fingerprint)
}

// Unwrap exposes the library's error for errors.Is and errors.As.
func (e *HostKeyMismatchError) Unwrap() error { return e.Err }

// HostKeyUnknownError means known_hosts has no entry for the host at Addr and
// trust-on-first-use is off, so the connection was refused rather than the key
// accepted on sight.
//
// This is a configuration state, not a fault of the link: nothing is broken and
// nothing is proven. The remedy is to learn the host's fingerprint from the
// host itself and record it.
type HostKeyUnknownError struct {
	// Addr and Port identify the destination that answered.
	Addr string
	Port int
	// Fingerprint is the SHA256 fingerprint of the key that was offered, in
	// OpenSSH's own form ("SHA256:...").
	Fingerprint string
	// Err is the library's own verification error, kept for context. It may be
	// nil, and it is never rendered into anything the user reads.
	Err error
}

func (e *HostKeyUnknownError) Error() string {
	return fmt.Sprintf("%s:%d is not in known_hosts and trust-on-first-use is off (%s)",
		e.Addr, e.Port, e.Fingerprint)
}

// Unwrap exposes the library's error for errors.Is and errors.As.
func (e *HostKeyUnknownError) Unwrap() error { return e.Err }
