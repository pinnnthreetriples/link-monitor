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
	CheckTailscale  CheckID = "tailscale"
	CheckSSHOut     CheckID = "ssh_out"     // this machine -> peer
	CheckSSHIn      CheckID = "ssh_in"      // peer -> this machine
	CheckSSHD       CheckID = "sshd"        // the peer's SSH service
	CheckKillSwitch CheckID = "kill_switch" // a local packet filter blocking the tailnet
)

// State is the outcome of a check, worst-last so that comparison orders by
// severity: StateOK < StateUnknown < StateWarn < StateFail.
type State int

const (
	StateOK State = iota
	StateUnknown
	StateWarn
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
}

// Probe is what core needs to know about the outside world. Adapters implement
// it; tests supply a fake. No method may block past the context.
type Probe interface {
	// TailscaleUp reports whether the local Tailscale daemon is connected, plus
	// a short human note such as "v1.102.3 · 3 узла".
	TailscaleUp(ctx context.Context) (up bool, note string, err error)

	// PeerReachable round-trips the peer through Tailscale itself. It succeeds
	// even when a local packet filter blocks ordinary sockets, which is exactly
	// what makes it useful for telling those two failures apart.
	PeerReachable(ctx context.Context, addr string) (time.Duration, error)

	// TCPReachable opens a real TCP connection, the way an application would.
	// It returns *BlockedError when a local filter refuses the connect, and
	// *TimeoutError when the packet left but nothing answered.
	TCPReachable(ctx context.Context, addr string, port int) error

	// ServiceRunning reports whether a Windows service is running on the peer.
	ServiceRunning(ctx context.Context, name string) (bool, error)

	// LocalServiceRunning reports whether a Windows service is running on THIS
	// machine. Inbound SSH cannot work while our own sshd is stopped, and that
	// is knowable without asking anyone — so a stopped local service is proof,
	// not a guess.
	LocalServiceRunning(ctx context.Context, name string) (bool, error)

	// PeerCanReachUs asks the peer to open a TCP connection back to this
	// machine. It is the only direct evidence that the inbound direction works;
	// everything else is inference. It needs a working outbound session, so it
	// is unavailable exactly when the link is already known to be broken.
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
