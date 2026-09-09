package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
)

// ErrNoTailnet means the tailnet side was never wired up.
var ErrNoTailnet = errors.New("app: the tailnet is not available")

// PeerLister is the part of the Tailscale adapter that enumerates the tailnet.
// *tailscale.Client implements it.
type PeerLister interface {
	Peers(ctx context.Context) ([]tailscale.Peer, error)
}

// Peer is one member of the tailnet as the UI shows it. It is a type of this
// package rather than the adapter's so that the UI never has to import an
// adapter to read a list.
type Peer struct {
	Name   string
	Addr   string
	Online bool
	Self   bool
}

// PeerService lists the tailnet.
type PeerService struct {
	src PeerLister
}

// NewPeerService builds the service. src may be nil, in which case List says so
// rather than panicking.
func NewPeerService(src PeerLister) *PeerService { return &PeerService{src: src} }

// List reports the tailnet members, this machine first — the order the adapter
// already guarantees, kept rather than re-sorted.
func (s *PeerService) List(ctx context.Context) ([]Peer, error) {
	if s.src == nil {
		return nil, fmt.Errorf("listing the tailnet: %w", ErrNoTailnet)
	}
	found, err := s.src.Peers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the tailnet: %w", err)
	}

	out := make([]Peer, 0, len(found))
	for _, p := range found {
		out = append(out, Peer{Name: peerName(p), Addr: p.Addr, Online: p.Online, Self: p.Self})
	}
	return out, nil
}

// peerName is the friendliest name a node has: the MagicDNS one when there is
// one, the machine's own hostname otherwise, and the address as a last resort.
func peerName(p tailscale.Peer) string {
	switch {
	case p.Name != "":
		return p.Name
	case p.Host != "":
		return p.Host
	default:
		return p.Addr
	}
}
