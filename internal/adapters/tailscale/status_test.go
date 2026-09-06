package tailscale

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestTailscaleUp(t *testing.T) {
	t.Parallel()

	manyPeers := func(n int) map[key.NodePublic]*ipnstate.PeerStatus {
		peers := make(map[key.NodePublic]*ipnstate.PeerStatus, n)
		for range n {
			peers[nodeKey(t)] = &ipnstate.PeerStatus{DNSName: "n" + tailnetSuffix}
		}
		return peers
	}

	tests := []struct {
		name     string
		status   *ipnstate.Status
		err      error
		wantUp   bool
		wantNote string
		wantErr  bool
	}{
		{
			name:     "running with the work PC online",
			status:   runningStatus(t),
			wantUp:   true,
			wantNote: "v1.102.3 · 2 узла",
		},
		{
			name: "single node keeps the singular",
			status: &ipnstate.Status{
				Version: longVersion, BackendState: "Running",
				Self: &ipnstate.PeerStatus{DNSName: laptopName + tailnetSuffix},
			},
			wantUp:   true,
			wantNote: "v1.102.3 · 1 узел",
		},
		{
			name: "five nodes take the plural genitive",
			status: &ipnstate.Status{
				Version: longVersion, BackendState: "Running",
				Self: &ipnstate.PeerStatus{DNSName: laptopName + tailnetSuffix},
				Peer: manyPeers(4),
			},
			wantUp:   true,
			wantNote: "v1.102.3 · 5 узлов",
		},
		{
			name:     "stopped daemon is not an error",
			status:   &ipnstate.Status{Version: longVersion, BackendState: "Stopped"},
			wantUp:   false,
			wantNote: "v1.102.3 · остановлен",
		},
		{
			name:     "logged out",
			status:   &ipnstate.Status{Version: longVersion, BackendState: "NeedsLogin"},
			wantUp:   false,
			wantNote: "v1.102.3 · требуется вход",
		},
		{
			name:     "waiting for machine approval",
			status:   &ipnstate.Status{Version: longVersion, BackendState: "NeedsMachineAuth"},
			wantUp:   false,
			wantNote: "v1.102.3 · ожидает подтверждения устройства",
		},
		{
			name:     "starting up",
			status:   &ipnstate.Status{Version: longVersion, BackendState: "Starting"},
			wantUp:   false,
			wantNote: "v1.102.3 · запускается",
		},
		{
			name:     "a state we do not know, and no version",
			status:   &ipnstate.Status{BackendState: "Bewildered"},
			wantUp:   false,
			wantNote: "состояние неизвестно",
		},
		{
			name:    "daemon down",
			err:     errBoom,
			wantErr: true,
		},
		{
			name:    "daemon answers with nothing",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(&fakeDaemon{status: tc.status, statusErr: tc.err}, &fakeRunner{})

			up, note, err := c.TailscaleUp(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("TailscaleUp() error = nil, want one")
				}
				if up || note != "" {
					t.Errorf("TailscaleUp() = (%v, %q), want (false, \"\") on error", up, note)
				}
				return
			}
			if err != nil {
				t.Fatalf("TailscaleUp() error = %v", err)
			}
			if up != tc.wantUp {
				t.Errorf("up = %v, want %v", up, tc.wantUp)
			}
			if note != tc.wantNote {
				t.Errorf("note = %q, want %q", note, tc.wantNote)
			}
		})
	}
}

func TestTailscaleUpWrapsTheDaemonError(t *testing.T) {
	t.Parallel()
	c := newTestClient(&fakeDaemon{statusErr: errBoom}, &fakeRunner{})

	_, _, err := c.TailscaleUp(context.Background())
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want it to wrap %v", err, errBoom)
	}
}

func TestTailscaleUpHonoursContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newTestClient(&fakeDaemon{status: runningStatus(t)}, &fakeRunner{})
	if _, _, err := c.TailscaleUp(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestPeers(t *testing.T) {
	t.Parallel()

	offline := runningStatus(t)
	for _, ps := range offline.Peer {
		ps.Online = false
	}

	noMagicDNS := &ipnstate.Status{
		BackendState: "Running",
		Self:         &ipnstate.PeerStatus{HostName: laptopHost},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			nodeKey(t): {
				HostName:     workHost,
				TailscaleIPs: []netip.Addr{netip.MustParseAddr("fd7a::1"), netip.MustParseAddr(workAddr)},
				Online:       true,
			},
		},
	}

	tests := []struct {
		name    string
		status  *ipnstate.Status
		err     error
		want    []Peer
		wantErr bool
	}{
		{
			name:   "self first, then the peer",
			status: runningStatus(t),
			want: []Peer{
				{Name: laptopName, Host: laptopHost, Addr: laptopAddr, Online: true, Self: true},
				{Name: workName, Host: workHost, Addr: workAddr, Online: true},
			},
		},
		{
			name:   "an offline peer is still a member",
			status: offline,
			want: []Peer{
				{Name: laptopName, Host: laptopHost, Addr: laptopAddr, Online: true, Self: true},
				{Name: workName, Host: workHost, Addr: workAddr},
			},
		},
		{
			name:   "self is offline when the backend is stopped",
			status: &ipnstate.Status{BackendState: "Stopped", Self: runningStatus(t).Self},
			want: []Peer{
				{Name: laptopName, Host: laptopHost, Addr: laptopAddr, Self: true},
			},
		},
		{
			name:   "without MagicDNS the hostname stands in, IPv4 is preferred",
			status: noMagicDNS,
			want: []Peer{
				{Name: "desktop-l9djse9", Host: laptopHost, Online: true, Self: true},
				{Name: "win-sttm11d02rd", Host: workHost, Addr: workAddr, Online: true},
			},
		},
		{
			name:   "an empty tailnet is empty, not an error",
			status: &ipnstate.Status{BackendState: "Running"},
			want:   []Peer{},
		},
		{
			name:    "daemon down",
			err:     errBoom,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(&fakeDaemon{status: tc.status, statusErr: tc.err}, &fakeRunner{})

			got, err := c.Peers(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Peers() error = nil, want one")
				}
				return
			}
			if err != nil {
				t.Fatalf("Peers() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Peers() = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("peer %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestPeersSkipsNilEntries(t *testing.T) {
	t.Parallel()
	st := runningStatus(t)
	st.Peer[nodeKey(t)] = nil

	c := newTestClient(&fakeDaemon{status: st}, &fakeRunner{})
	got, err := c.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Peers() returned %d peers, want 2: %+v", len(got), got)
	}
}

func TestNodeWordRU(t *testing.T) {
	t.Parallel()
	tests := map[int]string{
		0: "узлов", 1: "узел", 2: "узла", 4: "узла", 5: "узлов",
		11: "узлов", 12: "узлов", 14: "узлов", 21: "узел", 22: "узла",
		25: "узлов", 101: "узел", 111: "узлов", -1: "узел",
	}
	for n, want := range tests {
		if got := nodeWordRU(n); got != want {
			t.Errorf("nodeWordRU(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestShortVersion(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		longVersion: "v1.102.3",
		"1.102.3":   "v1.102.3",
		"v1.102.3":  "v1.102.3",
		"  ":        "",
		"-weird":    "v-weird",
	}
	for in, want := range tests {
		if got := shortVersion(in); got != want {
			t.Errorf("shortVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinNote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version, state, want string
	}{
		{"v1.102.3", "2 узла", "v1.102.3 · 2 узла"},
		{"", "2 узла", "2 узла"},
		{"v1.102.3", "", "v1.102.3"},
		{"", "", ""},
	}
	for _, tc := range tests {
		if got := joinNote(tc.version, tc.state); got != tc.want {
			t.Errorf("joinNote(%q, %q) = %q, want %q", tc.version, tc.state, got, tc.want)
		}
	}
}

func TestBackendNoteRU(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Stopped":          "остановлен",
		"NeedsLogin":       "требуется вход",
		"NeedsMachineAuth": "ожидает подтверждения устройства",
		"Starting":         "запускается",
		"NoState":          "состояние неизвестно",
		"":                 "состояние неизвестно",
		"Puzzled":          "состояние неизвестно",
	}
	for state, want := range tests {
		if got := backendNoteRU(state); got != want {
			t.Errorf("backendNoteRU(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestPreferredAddr(t *testing.T) {
	t.Parallel()
	v4 := netip.MustParseAddr(workAddr)
	v6 := netip.MustParseAddr("fd7a:115c:a1e0::1")

	tests := []struct {
		name  string
		addrs []netip.Addr
		want  string
	}{
		{name: "IPv4 wins", addrs: []netip.Addr{v6, v4}, want: workAddr},
		{name: "IPv6 will do", addrs: []netip.Addr{v6}, want: v6.String()},
		{name: "nothing at all", want: ""},
	}
	for _, tc := range tests {
		if got := preferredAddr(tc.addrs); got != tc.want {
			t.Errorf("%s: preferredAddr() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
