package peerclip

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// DeliverImage sends one bounded PNG to the confirmed peer instance.
func (p *Peer) DeliverImage(ctx context.Context, image []byte) error {
	decoded, err := clipshare.DecodePNG(image)
	if err != nil {
		return err
	}
	clipshare.Zero(decoded.Pix)
	rec, err := p.Record(ctx)
	if err != nil {
		return err
	}
	port, err := rec.Port()
	if err != nil {
		return fmt.Errorf("reading %s's endpoint: %w", p.name, err)
	}
	tunnel, err := p.fwd.LocalForward(ctx, 0, loopback, port)
	if err != nil {
		return fmt.Errorf("forwarding %s's image port: %w", p.name, err)
	}
	defer func() { _ = tunnel.Close() }()
	addr, err := tunnelAddr(tunnel)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/api/clip/image", bytes.NewReader(image))
	if err != nil {
		return fmt.Errorf("building image request to %s: %w", p.name, err)
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("handing image to %s's instance: %w", p.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return p.readAnswer(resp)
}
