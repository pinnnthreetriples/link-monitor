package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

type imageClipboard struct{ *fakeClipboard }

func (f *imageClipboard) PutImage(text []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.snap = clipshare.Snapshot{
		Format: "png", Recordable: true, Bytes: len(text),
		Text: append([]byte(nil), text...),
	}
	return f.putErr
}

type imageInbox struct {
	target *Clip
	sent   int
}

func (f *imageInbox) Name() string                          { return "test peer" }
func (f *imageInbox) Deliver(context.Context, []byte) error { return nil }
func (f *imageInbox) DeliverImage(_ context.Context, text []byte) error {
	f.sent++
	return f.target.ReceiveImage(text)
}

func testImage(t *testing.T) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	im.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImagesTravelBothWaysWithoutEcho(t *testing.T) {
	a, b := &imageClipboard{newFakeClipboard()}, &imageClipboard{newFakeClipboard()}
	ab, ba := &imageInbox{}, &imageInbox{}
	left := NewClip(ClipConfig{}, ClipDeps{Here: a, There: ab})
	right := NewClip(ClipConfig{}, ClipDeps{Here: b, There: ba})
	ab.target, ba.target = right, left
	for _, c := range []*Clip{left, right} {
		if err := c.TurnOn(); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.PutImage(testImage(t)); err != nil {
		t.Fatal(err)
	}
	left.tick(context.Background())
	b.bump()
	right.tick(context.Background())
	if ab.sent != 1 || ba.sent != 0 || right.Status().Counts.Received != 1 {
		t.Fatal("image was lost or echoed")
	}
	// A different valid image in the other direction must still travel.
	im := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	im.SetNRGBA(0, 0, color.NRGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	if err := b.PutImage(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	right.tick(context.Background())
	a.bump()
	left.tick(context.Background())
	if ba.sent != 1 || ab.sent != 1 {
		t.Fatal("reverse image was lost or echoed")
	}
	left.TurnOff()
	if err := left.ReceiveImage(testImage(t)); err != ErrClipOff {
		t.Fatal(err)
	}
}

func TestReceiveInvalidImageNeverWritesClipboard(t *testing.T) {
	c, here, _ := clipUnderTest(0)
	if err := c.TurnOn(); err != nil {
		t.Fatal(err)
	}
	if err := c.ReceiveImage([]byte("invalid")); err == nil {
		t.Fatal("invalid image accepted")
	}
	if len(here.delivered()) != 0 {
		t.Fatal("invalid image replaced clipboard")
	}
}

func TestReadFailureRetriesSameSequence(t *testing.T) {
	c, here, there := clipUnderTest(0)
	if err := c.TurnOn(); err != nil {
		t.Fatal(err)
	}
	here.copyText("retry")
	here.breakLook(errBroken)
	c.tick(context.Background())
	here.breakLook(nil)
	c.tick(context.Background())
	if len(there.received()) != 1 {
		t.Fatal("temporary read failure lost the copy")
	}
}
