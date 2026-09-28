package app

import (
	"context"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// ImageClipboard extends the text adapter without breaking text-only integrations.
type ImageClipboard interface{ PutImage(text []byte) error }

// ImageInbox sends a bounded PNG through the same peer authentication and tunnel.
type ImageInbox interface {
	DeliverImage(context.Context, []byte) error
}

// ReceiveImage validates and normalizes before changing the clipboard. access also
// bounds concurrent image decodes and serializes pause with clipboard writes.
func (c *Clip) ReceiveImage(text []byte) error {
	c.access.Lock()
	defer c.access.Unlock()
	if !c.Available() {
		return ErrClipUnavailable
	}
	if !c.On() {
		return ErrClipOff
	}
	if len(text) > clipshare.MaxImageBytes {
		c.note(clipshare.WhyTooBig, len(text))
		return ErrClipTooBig
	}
	normal, err := clipshare.NormalizePNG(text)
	if err != nil {
		return err
	}
	defer clipshare.Zero(normal)
	here, ok := c.deps.Here.(ImageClipboard)
	if !ok {
		return ErrClipUnavailable
	}
	if err := here.PutImage(normal); err != nil {
		c.fail(msgClipPutFailed)
		return ErrClipUnavailable
	}
	c.plant(clipshare.ImageFingerprint(normal))
	c.rebase()
	c.received(len(normal))
	return nil
}

func (c *Clip) deliverItem(ctx context.Context, got item) error {
	if got.snap.Format == "png" {
		peer, ok := c.deps.There.(ImageInbox)
		if !ok {
			return ErrClipUnavailable
		}
		return peer.DeliverImage(ctx, got.snap.Text)
	}
	return c.deps.There.Deliver(ctx, got.snap.Text)
}
