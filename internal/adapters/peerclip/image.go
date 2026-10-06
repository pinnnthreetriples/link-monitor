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
		return fmt.Errorf("validating image for %s: %w", p.name, err)
	}
	clipshare.Zero(decoded.Pix)
	return p.via(ctx, func(addr string) error { return p.postImage(ctx, addr, image) })
}

// postImage sends the PNG through the tunnel to the peer's instance.
func (p *Peer) postImage(ctx context.Context, addr string, image []byte) error {
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
