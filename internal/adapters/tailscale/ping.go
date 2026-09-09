package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"tailscale.com/tailcfg"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// PeerReachable round-trips the peer through Tailscale itself with a disco
// ping, which travels inside the tunnel rather than over an ordinary socket.
// It therefore keeps answering when a local packet filter blocks plain TCP to
// the tailnet — telling those two failures apart is the point of it.
//
// addr may be a Tailscale IP or a peer name; a name costs one status lookup.
// A peer that does not answer yields an error wrapping both
// [ErrPeerUnreachable] and *core.TimeoutError; see [silentPeer] for why both.
func (c *Client) PeerReachable(ctx context.Context, addr string) (time.Duration, error) {
	latency, err := c.pingUntilAnswered(ctx, addr)
	if err != nil {
		return 0, silentPeer(addr, err)
	}
	return latency, nil
}

// silentPeer marks a peer that never answered as the domain's own
// *core.TimeoutError, keeping [ErrPeerUnreachable] in the chain.
//
// Both, because both are load-bearing and they are matched by different people.
// core/diagnose asks errors.As for *core.TimeoutError: that is the branch that
// says «Вторая машина не отвечает — похоже, она выключена», marks the two SSH
// rows and offers the "check the peer is on" advice. Nothing in this program
// produced that type, so the branch was dead code and a machine that was simply
// switched off was reported as «Дотянуться до второй машины не удалось —
// проверка не завершилась»: a knowable cause, filed as an unknown. Callers
// inside this package, and this package's tests, still match
// ErrPeerUnreachable with errors.Is.
//
// The port is zero on purpose: a disco ping has no port, and core.TimeoutError
// is the shape core/diagnose reads for "nothing answered", not a socket record.
func silentPeer(addr string, err error) error {
	if !errors.Is(err, ErrPeerUnreachable) {
		return err
	}
	return fmt.Errorf("%w: %w", &core.TimeoutError{Addr: addr}, err)
}

// pingUntilAnswered is PeerReachable's retry loop: only a silent peer is worth
// asking twice.
func (c *Client) pingUntilAnswered(ctx context.Context, addr string) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("tailscale ping %s: %w", addr, err)
	}

	ip, err := c.resolveAddr(ctx, addr)
	if err != nil {
		return 0, err
	}

	attempts := c.pingAttempts
	if attempts < 1 {
		attempts = 1
	}

	var last error
	for i := range attempts {
		if i > 0 {
			if err := sleepCtx(ctx, c.pingBackoff); err != nil {
				return 0, fmt.Errorf("tailscale ping %s: %w", addr, err)
			}
		}
		d, err := c.pingOnce(ctx, ip)
		if err == nil {
			return d, nil
		}
		last = err
		// Only a silent peer is worth another try: a broken daemon or a dead
		// context will not get better by asking again.
		if !errors.Is(err, ErrPeerUnreachable) || ctx.Err() != nil {
			return 0, err
		}
	}
	return 0, last
}

// pingOnce sends a single disco ping and reads the round-trip out of it.
func (c *Client) pingOnce(ctx context.Context, ip netip.Addr) (time.Duration, error) {
	res, err := c.daemon.Ping(ctx, ip, tailcfg.PingDisco)
	if err != nil {
		return 0, fmt.Errorf("tailscale ping %s: %w", ip, err)
	}
	switch {
	case res == nil:
		return 0, fmt.Errorf("tailscale ping %s: empty result: %w", ip, ErrPeerUnreachable)
	case res.Err != "":
		return 0, fmt.Errorf("tailscale ping %s: %s: %w", ip, res.Err, ErrPeerUnreachable)
	case res.IsLocalIP:
		// Pinging ourselves always works and takes no measurable time.
		return 0, nil
	case res.LatencySeconds <= 0:
		return 0, fmt.Errorf("tailscale ping %s: no latency reported: %w", ip, ErrPeerUnreachable)
	}
	return time.Duration(res.LatencySeconds * float64(time.Second)), nil
}

// resolveAddr accepts an IP as it stands and looks a name up in the tailnet.
func (c *Client) resolveAddr(ctx context.Context, addr string) (netip.Addr, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return netip.Addr{}, fmt.Errorf("tailscale ping: empty address: %w", ErrPeerNotFound)
	}
	if ip, err := netip.ParseAddr(trimmed); err == nil {
		return ip, nil
	}

	peers, err := c.Peers(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	want := strings.ToLower(strings.TrimSuffix(trimmed, "."))
	for _, p := range peers {
		if p.Name != want && !strings.EqualFold(p.Host, want) {
			continue
		}
		ip, parseErr := netip.ParseAddr(p.Addr)
		if parseErr != nil {
			return netip.Addr{}, fmt.Errorf("peer %q has no usable address: %w", trimmed, parseErr)
		}
		return ip, nil
	}
	return netip.Addr{}, fmt.Errorf("resolving %q: %w", trimmed, ErrPeerNotFound)
}

// sleepCtx waits, but gives up the moment the context does.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
