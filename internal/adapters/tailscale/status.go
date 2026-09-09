package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

// errEmptyStatus is what an answering daemon that says nothing amounts to.
var errEmptyStatus = errors.New("the daemon returned an empty status")

// TailscaleUp reports whether the local daemon is connected to the tailnet,
// with a short note for the user such as "v1.102.3 · 3 узла". When the daemon
// answers but is not running, up is false and the note says why; only a daemon
// that cannot be reached at all is an error.
func (c *Client) TailscaleUp(ctx context.Context) (bool, string, error) {
	st, err := c.status(ctx)
	if err != nil {
		return false, "", err
	}

	version := shortVersion(st.Version)
	if st.BackendState != ipn.Running.String() {
		return false, joinNote(version, backendNoteRU(st.BackendState)), nil
	}

	nodes := len(st.Peer)
	if st.Self != nil {
		nodes++
	}
	return true, joinNote(version, fmt.Sprintf("%d %s", nodes, nodeWordRU(nodes))), nil
}

// Peers lists the members of the tailnet, this machine first and the rest by
// name, so the order does not jump between calls.
func (c *Client) Peers(ctx context.Context) ([]Peer, error) {
	st, err := c.status(ctx)
	if err != nil {
		return nil, err
	}

	peers := make([]Peer, 0, len(st.Peer)+1)
	if st.Self != nil {
		self := toPeer(st.Self)
		self.Self = true
		// The daemon does not fill Online for the local node; being connected to
		// the control plane is exactly what a running backend means.
		self.Online = st.BackendState == ipn.Running.String()
		peers = append(peers, self)
	}
	for _, ps := range st.Peer {
		if ps == nil {
			continue
		}
		peers = append(peers, toPeer(ps))
	}

	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].Self != peers[j].Self {
			return peers[i].Self
		}
		return peers[i].Name < peers[j].Name
	})
	return peers, nil
}

// status fetches the daemon status and turns "no answer" into one error shape.
func (c *Client) status(ctx context.Context) (*ipnstate.Status, error) {
	st, err := c.daemon.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}
	if st == nil {
		return nil, fmt.Errorf("tailscale status: %w", errEmptyStatus)
	}
	return st, nil
}

func toPeer(ps *ipnstate.PeerStatus) Peer {
	return Peer{
		Name:   baseName(ps.DNSName, ps.HostName),
		Host:   ps.HostName,
		Addr:   preferredAddr(ps.TailscaleIPs),
		Online: ps.Online,
	}
}

// baseName is the MagicDNS label, e.g. "win-sttm11d02rd" out of
// "win-sttm11d02rd.tail1a2b.ts.net.". It falls back to the hostname for nodes
// without MagicDNS.
func baseName(dnsName, hostName string) string {
	name := strings.TrimSuffix(dnsName, ".")
	if name == "" {
		return strings.ToLower(hostName)
	}
	if i := strings.Index(name, "."); i > 0 {
		name = name[:i]
	}
	return strings.ToLower(name)
}

// preferredAddr picks the IPv4 address, the one users recognise, and settles
// for whatever is there when the node has none.
func preferredAddr(addrs []netip.Addr) string {
	for _, a := range addrs {
		if a.Is4() {
			return a.String()
		}
	}
	if len(addrs) > 0 {
		return addrs[0].String()
	}
	return ""
}

// shortVersion turns the daemon's long version "1.102.3-t01234abcd-g..." into
// the "v1.102.3" a user recognises.
func shortVersion(long string) string {
	v := strings.TrimSpace(long)
	if v == "" {
		return ""
	}
	if i := strings.Index(v, "-"); i > 0 {
		v = v[:i]
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// joinNote glues the version onto the state note, dropping either if missing.
func joinNote(version, state string) string {
	switch {
	case version == "":
		return state
	case state == "":
		return version
	default:
		return version + " · " + state
	}
}

// backendNoteRU explains a backend state that is not Running, for the user.
//
// InUseOtherUser is here because it is a real condition with a real remedy —
// another Windows account's session owns the daemon, and the person at the
// keyboard has to switch to it or log that one out — and it used to fall into
// the default and be reported as «состояние неизвестно», which is a shrug at a
// state the daemon named precisely.
func backendNoteRU(state string) string {
	switch state {
	case ipn.Stopped.String():
		return "остановлен"
	case ipn.NeedsLogin.String():
		return "требуется вход"
	case ipn.NeedsMachineAuth.String():
		return "ожидает подтверждения устройства"
	case ipn.Starting.String():
		return "запускается"
	case ipn.InUseOtherUser.String():
		return "занят другим пользователем Windows"
	case ipn.NoState.String(), "":
		return "состояние неизвестно"
	default:
		return "состояние неизвестно"
	}
}

// nodeWordRU agrees "узел" with the count: 1 узел, 2 узла, 5 узлов.
func nodeWordRU(n int) string {
	if n < 0 {
		n = -n
	}
	tens := n % 100
	if tens >= 11 && tens <= 14 {
		return "узлов"
	}
	switch n % 10 {
	case 1:
		return "узел"
	case 2, 3, 4:
		return "узла"
	default:
		return "узлов"
	}
}
