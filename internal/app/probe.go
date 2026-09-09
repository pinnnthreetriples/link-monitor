// Package app is the orchestration layer: it drives the pure diagnosis in
// core/diagnose with the impure adapters, keeps the results where the UI can
// see them, and carries out the actions the user asks for.
//
// Nothing here talks to the outside world directly. Every dependency arrives as
// a small interface declared in this package — which is also why no test in it
// needs a tailnet, an SSH server, a Windows service or the network.
//
// Layering: app imports core and adapters, never ui.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// ErrProbeUnavailable means the part of the composite probe that answers this
// question was never wired up — typically the SSH client, which does not exist
// while there is no session to the peer. Callers see it wrapped, and
// diagnose.Run turns it into an Unknown row rather than a verdict.
var ErrProbeUnavailable = errors.New("app: this probe is not available")

// TailnetProbe is the slice of the Tailscale adapter the composite needs.
// *tailscale.Client implements it.
type TailnetProbe interface {
	TailscaleUp(ctx context.Context) (up bool, note string, err error)
	PeerReachable(ctx context.Context, addr string) (time.Duration, error)
	PeerPresence(ctx context.Context, peer core.Machine) (core.Presence, error)
}

// RemoteProbe is the slice of the SSH adapter the composite needs.
// *sshx.Client implements it.
type RemoteProbe interface {
	TCPReachable(ctx context.Context, addr string, port int) error
	ServiceRunning(ctx context.Context, name string) (bool, error)
	PeerCanReachUs(ctx context.Context, localAddr string, port int) error
}

// LocalServiceProbe is the slice of the Windows service adapter the composite
// needs. *winsvc.Client implements it.
type LocalServiceProbe interface {
	LocalServiceRunning(ctx context.Context, name string) (bool, error)
	LocalServiceInstalled(ctx context.Context, name string) (bool, error)
}

// Probe is the one core.Probe the diagnosis asks, assembled from the three
// adapters that each answer part of it. It exists because core.Probe is a
// single interface on purpose — the diagnosis should not know that "is the
// daemon up" and "is the peer's sshd running" arrive by different roads.
//
// A missing part is not a panic: the method reports ErrProbeUnavailable and the
// row it feeds goes Unknown, which is the honest answer when nobody was asked.
type Probe struct {
	tailnet TailnetProbe
	remote  RemoteProbe
	local   LocalServiceProbe
}

// The composite must keep satisfying the interface the diagnosis takes.
var _ core.Probe = (*Probe)(nil)

// NewProbe assembles the composite. Any of the three may be nil; the methods it
// would have answered then fail with ErrProbeUnavailable instead.
func NewProbe(tailnet TailnetProbe, remote RemoteProbe, local LocalServiceProbe) *Probe {
	return &Probe{tailnet: tailnet, remote: remote, local: local}
}

// TailscaleUp asks the Tailscale daemon for its state.
func (p *Probe) TailscaleUp(ctx context.Context) (bool, string, error) {
	if p.tailnet == nil {
		return false, "", fmt.Errorf("asking the Tailscale daemon: %w", ErrProbeUnavailable)
	}
	return p.tailnet.TailscaleUp(ctx)
}

// PeerReachable round-trips the peer through Tailscale itself.
func (p *Probe) PeerReachable(ctx context.Context, addr string) (time.Duration, error) {
	if p.tailnet == nil {
		return 0, fmt.Errorf("pinging %s through the tunnel: %w", addr, ErrProbeUnavailable)
	}
	return p.tailnet.PeerReachable(ctx, addr)
}

// PeerPresence asks the Tailscale daemon what it already knows about the peer.
func (p *Probe) PeerPresence(ctx context.Context, peer core.Machine) (core.Presence, error) {
	if p.tailnet == nil {
		return core.PresenceUnknown, fmt.Errorf(
			"asking the Tailscale daemon about %s: %w", peer.TailnetName, ErrProbeUnavailable)
	}
	return p.tailnet.PeerPresence(ctx, peer)
}

// TCPReachable opens an ordinary socket, the way an application would.
func (p *Probe) TCPReachable(ctx context.Context, addr string, port int) error {
	if p.remote == nil {
		return fmt.Errorf("dialing %s:%d: %w", addr, port, ErrProbeUnavailable)
	}
	return p.remote.TCPReachable(ctx, addr, port)
}

// ServiceRunning asks the peer about one of its Windows services.
func (p *Probe) ServiceRunning(ctx context.Context, name string) (bool, error) {
	if p.remote == nil {
		return false, fmt.Errorf("asking the peer about the %q service: %w", name, ErrProbeUnavailable)
	}
	return p.remote.ServiceRunning(ctx, name)
}

// LocalServiceRunning asks this machine's service manager.
func (p *Probe) LocalServiceRunning(ctx context.Context, name string) (bool, error) {
	if p.local == nil {
		return false, fmt.Errorf("asking this machine about the %q service: %w", name, ErrProbeUnavailable)
	}
	return p.local.LocalServiceRunning(ctx, name)
}

// LocalServiceInstalled asks this machine's service manager whether the
// service exists at all — which is a different question from whether it runs,
// and the one that separates "install the OpenSSH Server" from "start it".
func (p *Probe) LocalServiceInstalled(ctx context.Context, name string) (bool, error) {
	if p.local == nil {
		return false, fmt.Errorf(
			"asking this machine whether the %q service is installed: %w", name, ErrProbeUnavailable)
	}
	return p.local.LocalServiceInstalled(ctx, name)
}

// PeerCanReachUs asks the peer to dial back to this machine.
func (p *Probe) PeerCanReachUs(ctx context.Context, localAddr string, port int) error {
	if p.remote == nil {
		return fmt.Errorf("asking the peer to dial back to %s:%d: %w", localAddr, port, ErrProbeUnavailable)
	}
	return p.remote.PeerCanReachUs(ctx, localAddr, port)
}
