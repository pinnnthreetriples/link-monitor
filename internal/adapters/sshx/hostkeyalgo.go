package sshx

import (
	"errors"
	"net"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// This file answers one question before the handshake starts: which host key
// types should we ask this host for?
//
// Getting it wrong is what produced the worst defect this program has had. A
// Windows OpenSSH install generates three host keys — ed25519, ecdsa and rsa —
// and offers all three, while known_hosts usually holds exactly one entry per
// host, the type whichever client wrote it happened to negotiate. Ask for
// nothing in particular and x/crypto/ssh sends its whole default preference
// list, which puts ecdsa-sha2-nistp256 ahead of ssh-ed25519; the server then
// answers with a key of a type nobody ever pinned, knownhosts reports a
// KeyError, and a reader who takes any KeyError with entries in it for a
// changed key tells the user their machine may have been replaced. That is
// exactly what the work PC said about a healthy laptop.
//
// So we do what OpenSSH's own client does in order_hostkeyalgs: put the key
// types known_hosts has for this host at the front of the list we offer, and
// keep the rest behind them. The healthy case then negotiates the pinned type
// and verifies; a server that really has gained a new key type still
// negotiates, and hostKeyVerdictFor gets to say the honest thing about it
// rather than being handed a fait accompli by the algorithm ordering.
//
// One case is beyond reach here and is written down rather than papered over: a
// @cert-authority line is indistinguishable from a plain entry in
// knownhosts.KnownKey, which exposes no marker, so a host trusted only through
// a CA still looks to us like a host pinned to the CA's key type. Neither
// machine this program was built for uses host certificates, and the behaviour
// is no worse than before this file existed.

// certSuffix marks the certificate form of a host key algorithm name, as in
// "ssh-ed25519-cert-v01@openssh.com".
const certSuffix = "-cert-v01@openssh.com"

// hostKeyAlgorithms returns the value for ssh.ClientConfig.HostKeyAlgorithms
// given what known_hosts holds for the host: the algorithms authenticating a
// pinned key type first, then every other algorithm the library implements.
//
// It returns nil when known_hosts holds nothing for the host, and nil is the
// point rather than an oversight: an empty HostKeyAlgorithms leaves the
// library's default preference in place, which is the only sane thing to offer
// a host we have never seen. Requiring a pin in order to negotiate at all would
// break trust-on-first-use, whose whole job is the connection before the first
// pin exists.
func hostKeyAlgorithms(pinned []knownhosts.KnownKey) []string {
	if len(pinned) == 0 {
		return nil
	}
	types := make([]string, 0, len(pinned))
	for _, k := range pinned {
		types = append(types, hostKeyType(k.Key.Type()))
	}

	var preferred, rest []string
	for _, algo := range offerableHostKeyAlgos() {
		// Certificate algorithms stay behind the plain ones even for a pinned
		// type: a plain key is the thing a plain known_hosts entry can be
		// compared with, and a server holding both should give us that.
		if !strings.HasSuffix(algo, certSuffix) && slices.Contains(types, hostKeyType(algo)) {
			preferred = append(preferred, algo)
			continue
		}
		rest = append(rest, algo)
	}
	return append(preferred, rest...)
}

// offerableHostKeyAlgos is every host key algorithm this build of
// x/crypto/ssh implements, in the library's own preference order with the
// algorithms it considers sound first. It is derived from the library's two
// published lists instead of being written out here so that an algorithm a
// later x/crypto adds is offered as soon as a known_hosts entry pins one, and
// so that nothing we offer is an algorithm the library cannot verify.
func offerableHostKeyAlgos() []string {
	return append(ssh.SupportedAlgorithms().HostKeys, ssh.InsecureAlgorithms().HostKeys...)
}

// hostKeyType reports which known_hosts key type the host key algorithm algo
// authenticates. Three groups of names collapse onto one type:
//
//   - a certificate algorithm is signed by a CA key of the plain type, which is
//     what a @cert-authority line holds;
//   - the RSA SHA-2 signature algorithms "rsa-sha2-256" and "rsa-sha2-512"
//     both authenticate one "ssh-rsa" key — OpenSSH never writes the SHA-2
//     names into known_hosts;
//   - everything else already is its own key type.
//
// Comparing types rather than algorithm names is what keeps a host pinned as
// "ssh-rsa" from looking unpinned the moment the handshake settles on
// rsa-sha2-512.
func hostKeyType(algo string) string {
	algo = strings.TrimSuffix(algo, certSuffix)
	switch algo {
	case ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512:
		return ssh.KeyAlgoRSA
	default:
		return algo
	}
}

// pinnedHostKeys returns the entries known_hosts holds for hostPort.
//
// knownhosts exports no accessor for that, so we ask the only way it answers:
// offer it a key no file can hold and read the KnownKey entries out of the
// KeyError that comes back. Asking the library is worth more than re-parsing
// the file here — hashed host names, wildcard patterns and negations are then
// matched by the same code that will run the real check a moment later, on the
// same snapshot of the same file, and hostPort is the very address Dial passes
// the handshake, which is the address knownhosts gives preference to.
//
// Anything other than a KeyError means the file holds nothing that matches, so
// there is nothing to pin and the caller falls back to the library's defaults.
func pinnedHostKeys(verify ssh.HostKeyCallback, hostPort string) []knownhosts.KnownKey {
	var keyErr *knownhosts.KeyError
	if errors.As(verify(hostPort, probeAddr(hostPort), probeKey{}), &keyErr) {
		return keyErr.Want
	}
	return nil
}

// probeKeyType names the probe key. It is not an algorithm any SSH
// implementation knows, which is the property that matters.
const probeKeyType = "link-monitor-known-hosts-probe"

// probeKey is the key pinnedHostKeys offers. It is not a real public key and
// never leaves this process: its only job is to equal no line in known_hosts,
// so that the check fails and reports everything it holds for the host.
// knownhosts compares keys by their marshalled bytes, and these bytes are not
// even valid SSH wire format — a length-prefixed type name cannot begin with
// this text — so no parsed key can collide with it, including a @revoked one.
type probeKey struct{}

func (probeKey) Type() string { return probeKeyType }

func (probeKey) Marshal() []byte { return []byte(probeKeyType) }

// Verify is never called: nothing signs anything with this key, and saying so
// is more honest than returning nil.
func (probeKey) Verify([]byte, *ssh.Signature) error {
	return errors.New("sshx: the known_hosts probe key verifies nothing")
}

// The probe has to satisfy both interfaces the check is given, or it would not
// run at all and every host would silently look unpinned.
var (
	_ ssh.PublicKey = probeKey{}
	_ net.Addr      = probeAddr("")
)

// probeAddr is the remote address pinnedHostKeys hands the check. knownhosts
// needs one it can split into host and port even though it gives preference to
// the address it is passed alongside, so this carries the same "host:port".
type probeAddr string

func (probeAddr) Network() string { return "tcp" }

func (a probeAddr) String() string { return string(a) }
