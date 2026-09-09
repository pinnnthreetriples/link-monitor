package tailscale

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// phone is the third node in this tailnet, and the one that found the defect
// this file exists for: an iPhone the control plane reports as offline.
const (
	phoneName = "iphone181"
	phoneAddr = "100.64.6.3"
)

// tailnetWithPhone is the real tailnet: this laptop, the work PC online, and
// the phone offline.
func tailnetWithPhone(t *testing.T) *ipnstate.Status {
	t.Helper()

	st := runningStatus(t)
	st.Peer[nodeKey(t)] = &ipnstate.PeerStatus{
		ID:           "nPHONE",
		DNSName:      phoneName + tailnetSuffix,
		HostName:     "iPhone181",
		TailscaleIPs: []netip.Addr{netip.MustParseAddr(phoneAddr)},
		Online:       false,
	}
	return st
}

// TestPeerPresence is the observation this adapter was extended for: the daemon
// knows the phone is offline, instantly, and it knows the difference between a
// node that is offline and a node it has never heard of.
func TestPeerPresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		peer core.Machine
		want core.Presence
	}{
		{
			name: "the work PC is online",
			peer: core.Machine{TailnetName: workName, Addr: workAddr},
			want: core.PresenceOnline,
		},
		{
			name: "the phone is offline and the tailnet says so",
			peer: core.Machine{TailnetName: phoneName, Addr: phoneAddr},
			want: core.PresenceOffline,
		},
		{
			name: "matched by address alone",
			peer: core.Machine{Addr: phoneAddr},
			want: core.PresenceOffline,
		},
		{
			name: "matched by MagicDNS name alone, case-insensitively",
			peer: core.Machine{TailnetName: "IPhone181"},
			want: core.PresenceOffline,
		},
		{
			name: "matched by the machine's own Windows name",
			peer: core.Machine{WindowsName: workHost},
			want: core.PresenceOnline,
		},
		{
			name: "a Windows name that is really the MagicDNS name still matches",
			peer: core.Machine{WindowsName: workName},
			want: core.PresenceOnline,
		},
		{
			name: "this machine's own node counts as online while the backend runs",
			peer: core.Machine{TailnetName: laptopName},
			want: core.PresenceOnline,
		},
		{
			// The bug this was caught by, live: the flags set the MagicDNS name
			// and the address, and WindowsName keeps the default of a different
			// machine. The stale field must not be an identity of its own, or
			// the run diagnoses the work PC while claiming to watch a phone.
			name: "a stale Windows name does not outvote the MagicDNS name",
			peer: core.Machine{TailnetName: phoneName, Addr: phoneAddr, WindowsName: workHost},
			want: core.PresenceOffline,
		},
		{
			name: "a stale Windows name does not resurrect a machine that is not here",
			peer: core.Machine{TailnetName: "no-such-machine", Addr: "100.99.99.99", WindowsName: workHost},
			want: core.PresenceAbsent,
		},
		{
			name: "a machine nobody in this tailnet is",
			peer: core.Machine{TailnetName: "someone-elses-pc", Addr: "100.99.99.99"},
			want: core.PresenceAbsent,
		},
		{
			// The rule that keeps a Machine with nothing filled in from
			// matching the first node that also has nothing filled in.
			name: "an empty machine matches nothing",
			peer: core.Machine{},
			want: core.PresenceAbsent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestClient(&fakeDaemon{status: tailnetWithPhone(t)}, &fakeRunner{})
			got, err := c.PeerPresence(context.Background(), tt.peer)
			if err != nil {
				t.Fatalf("PeerPresence() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("PeerPresence(%+v) = %v, want %v", tt.peer, got, tt.want)
			}
		})
	}
}

// TestPeerPresenceWithNoDaemon: a daemon that cannot be asked yields no
// opinion, which is the zero value, and the error. Answering "absent" there
// would turn a broken socket into an accusation about the user's tailnet.
func TestPeerPresenceWithNoDaemon(t *testing.T) {
	t.Parallel()

	c := newTestClient(&fakeDaemon{statusErr: errBoom}, &fakeRunner{})
	got, err := c.PeerPresence(context.Background(), core.Machine{TailnetName: workName})

	if !errors.Is(err, errBoom) {
		t.Errorf("PeerPresence() error = %v, want it to carry %v", err, errBoom)
	}
	if got != core.PresenceUnknown {
		t.Errorf("PeerPresence() = %v, want %v", got, core.PresenceUnknown)
	}
}

// TestPeerPresenceOfAStoppedDaemonsOwnNode: Peers reports the local node's
// presence from the backend state, so a stopped daemon does not claim its own
// machine is connected.
func TestPeerPresenceOfAStoppedDaemonsOwnNode(t *testing.T) {
	t.Parallel()

	st := &ipnstate.Status{
		Version:      longVersion,
		BackendState: "Stopped",
		Self: &ipnstate.PeerStatus{
			DNSName:      laptopName + tailnetSuffix,
			HostName:     laptopHost,
			TailscaleIPs: []netip.Addr{netip.MustParseAddr(laptopAddr)},
		},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{},
	}

	c := newTestClient(&fakeDaemon{status: st}, &fakeRunner{})
	got, err := c.PeerPresence(context.Background(), core.Machine{TailnetName: laptopName})
	if err != nil {
		t.Fatalf("PeerPresence() error = %v", err)
	}
	if got != core.PresenceOffline {
		t.Errorf("PeerPresence() = %v, want %v", got, core.PresenceOffline)
	}
}

// TestPeerPresenceSendsNothingToThePeer is the property the whole change rests
// on: the answer comes out of one local status read, with no ping behind it.
// A presence check that pinged would be the deadline this replaced.
func TestPeerPresenceSendsNothingToThePeer(t *testing.T) {
	t.Parallel()

	d := &fakeDaemon{status: tailnetWithPhone(t)}
	c := newTestClient(d, &fakeRunner{})

	if _, err := c.PeerPresence(context.Background(), core.Machine{Addr: phoneAddr}); err != nil {
		t.Fatalf("PeerPresence() error = %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pingN != 0 {
		t.Errorf("the daemon was pinged %d times; presence must send nothing", d.pingN)
	}
	if d.statusN != 1 {
		t.Errorf("status was read %d times, want exactly one local read", d.statusN)
	}
}
