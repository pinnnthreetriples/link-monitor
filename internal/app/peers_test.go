package app

import (
	"context"
	"errors"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
)

func TestPeerServiceListsTheTailnet(t *testing.T) {
	t.Parallel()

	src := &fakePeerLister{peers: []tailscale.Peer{
		{
			Name: "workspace-claude-pc", Host: "DESKTOP-L9DJSE9", Addr: "100.124.47.73",
			Online: true, Self: true,
		},
		{Name: "", Host: "WIN-STTM11D02RD", Addr: "100.127.188.87", Online: false},
		{Name: "", Host: "", Addr: "100.90.1.2", Online: true},
	}}

	got, err := NewPeerService(src).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d peers", len(got))
	}
	if got[0].Name != "workspace-claude-pc" || !got[0].Self || !got[0].Online {
		t.Errorf("self peer = %+v", got[0])
	}
	if got[1].Name != "WIN-STTM11D02RD" {
		t.Errorf("a peer with no MagicDNS name should fall back to its hostname: %+v", got[1])
	}
	if got[2].Name != "100.90.1.2" {
		t.Errorf("a nameless peer should fall back to its address: %+v", got[2])
	}
}

func TestPeerServiceReportsFailures(t *testing.T) {
	t.Parallel()

	failing := NewPeerService(&fakePeerLister{err: errBoom})
	if _, err := failing.List(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("List err = %v", err)
	}
	if _, err := NewPeerService(nil).List(context.Background()); !errors.Is(err, ErrNoTailnet) {
		t.Errorf("List without a source = %v", err)
	}
}
