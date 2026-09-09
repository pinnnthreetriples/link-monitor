// Package tailscale is the adapter over the local Tailscale daemon. It reports
// the tailnet status, round-trips peers through Tailscale itself, moves files
// with Taildrop, and exposes a local port to the tailnet.
//
// Everything impure sits behind two small interfaces declared here — [Daemon]
// for the LocalAPI and [Runner] for the tailscale CLI — so that no test needs a
// live daemon, the network, or the real tailscale.exe. [New] wires the real
// implementations; tests pass fakes with [WithDaemon] and [WithRunner].
//
// Status and ping go through the LocalAPI, whose JSON is a stable contract.
// Taildrop does too: the LocalAPI reports the exact node ID it pushed to and
// the exact bytes it received, which parsing "tailscale file get" cannot.
// Only "tailscale serve" has no LocalAPI equivalent worth the trouble, so that
// one shells out.
package tailscale

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// DefaultExePath is where the Windows installer puts the CLI.
const DefaultExePath = `C:\Program Files\Tailscale\tailscale.exe`

const (
	// defaultPingAttempts is how many disco pings to send before calling a peer
	// unreachable. The first one often loses the race against path discovery.
	defaultPingAttempts = 3
	defaultPingBackoff  = 300 * time.Millisecond
)

// Daemon is the slice of the Tailscale LocalAPI this adapter uses.
// *local.Client implements it; tests supply a fake.
type Daemon interface {
	Status(ctx context.Context) (*ipnstate.Status, error)
	Ping(ctx context.Context, ip netip.Addr, pingType tailcfg.PingType) (*ipnstate.PingResult, error)

	FileTargets(ctx context.Context) ([]apitype.FileTarget, error)
	PushFile(ctx context.Context, target tailcfg.StableNodeID, size int64, name string, r io.Reader) error
	WaitingFiles(ctx context.Context) ([]apitype.WaitingFile, error)
	GetWaitingFile(ctx context.Context, baseName string) (io.ReadCloser, int64, error)
	DeleteWaitingFile(ctx context.Context, baseName string) error
}

// The real LocalAPI client must keep satisfying the interface we depend on.
var _ Daemon = (*local.Client)(nil)

// CommandResult is the outcome of one CLI invocation.
type CommandResult struct {
	Stdout string
	Stderr string
	Code   int
}

// Runner executes the tailscale CLI. It returns a non-nil error only when the
// command could not be started or the context ended; a command that ran and
// failed reports its exit status in CommandResult.Code, for the caller to read.
type Runner interface {
	Run(ctx context.Context, args ...string) (CommandResult, error)
}

// Peer is one member of the tailnet.
type Peer struct {
	// Name is the MagicDNS base name, e.g. "win-sttm11d02rd".
	Name string
	// Host is the machine's own hostname, e.g. "WIN-STTM11D02RD".
	Host string
	// Addr is the Tailscale IPv4 address, e.g. "100.127.188.87".
	Addr string
	// Online is whether the node is connected to the control plane.
	Online bool
	// Self marks this machine's own node.
	Self bool
}

var (
	// ErrPeerUnreachable means the peer did not answer a ping sent through
	// Tailscale itself, so the tailnet path to it is down.
	ErrPeerUnreachable = errors.New("peer did not answer the Tailscale ping")

	// ErrPeerNotFound means no node in the tailnet goes by that name or address.
	ErrPeerNotFound = errors.New("no such peer in the tailnet")
)

// Client is the Tailscale adapter. The zero value is not usable; call [New].
type Client struct {
	daemon Daemon
	runner Runner

	pingAttempts int
	pingBackoff  time.Duration
	upTimeout    time.Duration
}

// The Client must keep fitting the three methods of core.Probe it answers for.
// The shape is spelled out rather than imported: the other methods belong to
// other adapters, so core.Probe itself cannot be asserted here.
var _ interface {
	TailscaleUp(ctx context.Context) (bool, string, error)
	PeerReachable(ctx context.Context, addr string) (time.Duration, error)
	PeerPresence(ctx context.Context, peer core.Machine) (core.Presence, error)
} = (*Client)(nil)

// Option configures a [Client]. Later options win over earlier ones.
type Option func(*Client)

// WithDaemon replaces the LocalAPI client, which is how tests inject a fake.
func WithDaemon(d Daemon) Option { return func(c *Client) { c.daemon = d } }

// WithRunner replaces the CLI runner, which is how tests inject a fake.
func WithRunner(r Runner) Option { return func(c *Client) { c.runner = r } }

// WithExePath points the CLI runner at another tailscale executable, for
// installs that are not in the default location.
func WithExePath(path string) Option {
	return func(c *Client) { c.runner = newExecRunner(path) }
}

// WithPingPolicy sets how many pings [Client.PeerReachable] sends before it
// gives up, and how long it waits between them. Values below one attempt or a
// negative backoff are ignored.
func WithPingPolicy(attempts int, backoff time.Duration) Option {
	return func(c *Client) {
		if attempts > 0 {
			c.pingAttempts = attempts
		}
		if backoff >= 0 {
			c.pingBackoff = backoff
		}
	}
}

// WithUpTimeout bounds how long [Client.Up] waits for the daemon to reach a
// running state. A value of zero or less is ignored.
func WithUpTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.upTimeout = d
		}
	}
}

// New builds a Client that talks to the local Tailscale daemon and, for serve
// and connecting, to the CLI at [DefaultExePath]. It does not contact anything
// until a method is called, so it never fails.
func New(opts ...Option) *Client {
	c := &Client{
		daemon:       &local.Client{},
		runner:       newExecRunner(DefaultExePath),
		pingAttempts: defaultPingAttempts,
		pingBackoff:  defaultPingBackoff,
		upTimeout:    defaultUpTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
