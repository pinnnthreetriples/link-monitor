// Package core holds the domain model and the decisions made from it. It is
// pure: it never runs a process, opens a socket, or imports an adapter. What it
// needs from the outside world it takes as an interface declared here.
package core

import (
	"context"
	"fmt"
	"time"
)

// Machine is one end of the link.
type Machine struct {
	// WindowsName is the NetBIOS name, e.g. "WIN-STTM11D02RD".
	WindowsName string
	// TailnetName is the MagicDNS name, e.g. "win-sttm11d02rd".
	TailnetName string
	// Addr is the Tailscale IPv4 address, e.g. "100.127.188.87".
	Addr string
	// User is the account SSH logs in as.
	User string
}

// CheckID names one probe. The set is fixed: these are the five rows the
// dashboard shows, in display order.
type CheckID string

const (
	// CheckTailscale is the tunnel itself: the local daemon is connected to the
	// tailnet and the peer answers a Tailscale-level ping. Everything below it
	// depends on this one, which is why it is first.
	CheckTailscale CheckID = "tailscale"

	// CheckSSHOut is an ordinary TCP connection from this machine to the peer's
	// SSH port. It is the outbound direction, and it is the one that a local
	// packet filter can break while CheckTailscale still passes.
	CheckSSHOut CheckID = "ssh_out"

	// CheckSSHIn is the same connection the other way round, from the peer to
	// this machine. It is a separate row because it cannot be inferred from the
	// outbound one: on these two machines outbound SSH worked for hours while
	// inbound was impossible, this side having no SSH server installed at all.
	CheckSSHIn CheckID = "ssh_in"

	// CheckSSHD is the peer's OpenSSH Server service, asked about by name
	// rather than guessed at from the port. Holding the service and the port as
	// two rows is what makes a contradiction between them visible.
	CheckSSHD CheckID = "sshd"

	// CheckKillSwitch is a local packet filter blocking the tailnet — in
	// practice AmneziaVPN's kill switch, whose WFP rule denies 100.64.0.0/10.
	// It is the one cause that makes the link look dead from here while the
	// peer is perfectly healthy, so it gets a row of its own.
	CheckKillSwitch CheckID = "kill_switch"
)

// State is the outcome of a check, worst-last so that comparison orders by
// severity: StateOK < StateUnknown < StateWarn < StateFail.
type State int

const (
	// StateOK is a check that passed, with nothing left to explain.
	StateOK State = iota

	// StateUnknown is a check that was never answered: the probe that would
	// have answered it was not wired, or the context ended first. It sorts
	// below StateWarn on purpose — "we did not ask" is not evidence of a fault,
	// and must not outrank one we did find.
	StateUnknown

	// StateWarn is a check that found something real but not fatal to it, or
	// found two facts that disagree: the port answers while the peer's service
	// reports itself stopped, the service runs while the port is silent, an
	// inbound connection that went unanswered rather than being refused. The
	// link may still be usable; something about it wants attention.
	StateWarn

	// StateFail is a check that failed outright, and the reason the overall
	// state is a failure.
	StateFail
)

// String reports the state as a stable lowercase token, for logs and tests.
func (s State) String() string {
	switch s {
	case StateOK:
		return "ok"
	case StateUnknown:
		return "unknown"
	case StateWarn:
		return "warn"
	case StateFail:
		return "fail"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Check is one row on the dashboard. Label and Note are shown to the user and
// are therefore written in Russian.
type Check struct {
	ID    CheckID
	State State
	Label string
	Note  string
}

// Snapshot is everything the UI needs to draw one moment of the link.
type Snapshot struct {
	Taken   time.Time
	Checks  []Check
	Overall State
	// Summary is the headline, e.g. "Связь установлена".
	Summary string
	// Detail is the sentence under it, naming the cause when there is one.
	Detail string
	// Latency is the round-trip to the peer; zero when unreachable.
	Latency time.Duration
	// Peer is what this run established about the peer machine itself, joined
	// from the two facts that can disagree about it — see PeerState. The
	// machine card is drawn from this and from nothing else, so that the card
	// and the headline cannot claim different things about whether the peer
	// answers.
	Peer PeerState
}

// Presence is what the local Tailscale daemon already knows about a node,
// without a single packet being sent to it. The control plane tells the daemon
// which nodes are in the tailnet and which of them are connected, so this is
// the cheapest and most authoritative answer this program can get about a peer
// — and the three cases below lead the user to three different places.
type Presence int

const (
	// PresenceUnknown is the daemon having no opinion: it answered, and what it
	// said does not settle the question. It is the zero value on purpose, so an
	// adapter that cannot tell falls back to asking the network rather than to
	// a claim nobody made.
	PresenceUnknown Presence = iota

	// PresenceOnline is a node the tailnet knows and the control plane sees as
	// connected. It is not proof of a working path — that is what a ping is
	// for — only that there is a machine at the other end to have a path to.
	PresenceOnline

	// PresenceOffline is a node the tailnet knows and the control plane does
	// not see: switched off, asleep, or without a network. It is the commonest
	// of the three failure modes this program exists to distinguish, and the
	// daemon knows it instantly, so waiting out a ping deadline to rediscover
	// it is time spent proving what was already answered.
	PresenceOffline

	// PresenceAbsent is a node the tailnet does not know at all. That is a
	// different and more serious thing than being switched off: nothing was
	// removed from the network, the machine we were told to watch is not part
	// of this tailnet — a wrong name in the settings, a node deleted in the
	// admin console, or a machine logged into somebody else's tailnet.
	PresenceAbsent
)

// String reports the presence as a stable lowercase token, for logs and tests.
func (p Presence) String() string {
	switch p {
	case PresenceUnknown:
		return "unknown"
	case PresenceOnline:
		return "online"
	case PresenceOffline:
		return "offline"
	case PresenceAbsent:
		return "absent"
	default:
		return fmt.Sprintf("Presence(%d)", int(p))
	}
}

// Probe is what core needs to know about the outside world. Adapters implement
// it; tests supply a fake. No method may block past the context.
type Probe interface {
	// TailscaleUp reports whether the local Tailscale daemon is connected, plus
	// a short human note such as "v1.102.3 · 3 узла".
	TailscaleUp(ctx context.Context) (up bool, note string, err error)

	// PeerPresence asks the local Tailscale daemon what it already knows about
	// the peer: whether that node is in the tailnet at all and, if it is,
	// whether the control plane currently sees it as connected.
	//
	// It sends nothing to the peer, so it answers at once and it answers even
	// when the peer is unreachable — which is the point. A machine that is
	// switched off is one of the three failures this program exists to name,
	// and it used to be diagnosed by waiting out a ping deadline and then
	// reporting the deadline, while the daemon had known the answer from the
	// start.
	//
	// The peer is identified the way a person identifies it — by name or by
	// address — because either may be all the configuration carries, and both
	// have to agree with the machine card the UI draws from the same daemon.
	PeerPresence(ctx context.Context, peer Machine) (Presence, error)

	// PeerReachable round-trips the peer through Tailscale itself. It succeeds
	// even when a local packet filter blocks ordinary sockets, which is exactly
	// what makes it useful for telling those two failures apart.
	PeerReachable(ctx context.Context, addr string) (time.Duration, error)

	// TCPReachable opens a real TCP connection, the way an application would.
	// It returns *BlockedError when a local filter refuses the connect, and
	// *TimeoutError when the packet left but nothing answered.
	TCPReachable(ctx context.Context, addr string, port int) error

	// ServiceRunning reports whether a Windows service is running on the peer.
	// Asking needs a logged-in SSH session, so it returns *AuthRefusedError
	// when the peer would not accept our key — which answers a different
	// question from the one that was asked, and a more definite one.
	ServiceRunning(ctx context.Context, name string) (bool, error)

	// LocalServiceRunning reports whether a Windows service is running on THIS
	// machine. Inbound SSH cannot work while our own sshd is stopped, and that
	// is knowable without asking anyone — so a stopped local service is proof,
	// not a guess.
	//
	// A service that is not installed is not running, so it answers false here
	// too. Ask LocalServiceInstalled when the difference matters, which for
	// OpenSSH Server it always does.
	LocalServiceRunning(ctx context.Context, name string) (bool, error)

	// LocalServiceInstalled reports whether a Windows service exists on THIS
	// machine at all, running or not.
	//
	// It exists because "no OpenSSH Server on this machine" and "the OpenSSH
	// Server is stopped" are different problems with different remedies — one
	// wants a Windows optional feature installed, the other wants a service
	// started — and the row used to report both as the second one. That is the
	// case this laptop really lived through: outbound SSH worked for hours
	// while nothing could ever connect to it, because the server was not there
	// to be started.
	LocalServiceInstalled(ctx context.Context, name string) (bool, error)

	// PeerCanReachUs asks the peer to open a TCP connection back to this
	// machine. It is the only direct evidence about the inbound direction that
	// this program can gather; everything else is inference.
	//
	// Read the name literally: it proves that a packet from the peer reaches
	// our port and that something is listening on it. It does NOT prove that a
	// login would succeed, and the row it feeds must not claim otherwise. A
	// login would have to be signed by the peer's own private key, which this
	// program has no business reading; see the comment on diagnose.dialBack for
	// the alternatives that were considered and why each was worse than saying
	// less.
	//
	// It needs a working outbound session, so it is unavailable exactly when
	// the link is already known to be broken, and it returns *AuthRefusedError
	// when the peer refuses our key.
	PeerCanReachUs(ctx context.Context, localAddr string, port int) error
}

// BlockedError means a local packet filter refused the connection before it
// left the machine — Windows reports this as WSAEACCES (10013). On these
// machines the cause has been AmneziaVPN's kill switch, whose WFP filter
// "Block Internet" denies the whole Tailscale range 100.64.0.0/10.
type BlockedError struct {
	Addr string
	Port int
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("connection to %s:%d refused by a local packet filter (WSAEACCES)", e.Addr, e.Port)
}

// TimeoutError means the connection attempt left the machine and went
// unanswered — typically nothing is listening on that port.
type TimeoutError struct {
	Addr string
	Port int
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("connection to %s:%d timed out", e.Addr, e.Port)
}

// AuthRefusedError means the far machine answered, spoke SSH, and then refused
// the login: it did not accept the key this machine offered. On these two
// machines the cause has been the public key missing from the other side's
// administrators_authorized_keys, which OpenSSH reports as
// "Permission denied (publickey)".
//
// It is the third member of the family BlockedError and TimeoutError belong to,
// and the most specific of the three. Those two are conditions of the network —
// something refused the packet, or nothing answered it. This one is a decision
// the far side made about this machine, which means it has a name and a remedy,
// and must never be filed as StateUnknown.
//
// It carries nothing but what identifies the attempt. No key bytes, no key
// path, no passphrase: this value is formatted into wrapped errors and compared
// in tests, and a credential must not be reachable from either.
type AuthRefusedError struct {
	Addr string
	Port int
	// User is the account the login was attempted as — "user" on the work PC,
	// "pnj" on the laptop. It is an account name, not a credential.
	User string
}

func (e *AuthRefusedError) Error() string {
	return fmt.Sprintf("%s:%d refused the ssh login as %q: the offered key was not accepted",
		e.Addr, e.Port, e.User)
}
