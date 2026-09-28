//go:build windows

package clipboard

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

func TestClipboardTestCleanupRestoresScreenshot(t *testing.T) {
	requireClipboard(t)
	keepClipboard(t)
	im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	im.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	png, err := clipshare.EncodePNG(im)
	if err != nil {
		t.Fatal(err)
	}
	c := New()
	if err := c.PutImage(png); err != nil {
		t.Fatal(err)
	}
	t.Run("clipboard test", func(t *testing.T) {
		keepClipboard(t)
		if err := c.Put([]byte("temporary test text")); err != nil {
			t.Fatal(err)
		}
	})
	got, err := c.Look(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "png" || len(got.Text) == 0 {
		t.Fatalf("cleanup left format %q, want the screenshot", got.Format)
	}
}

func TestDIBRoundTripPreservesScreenshotPixels(t *testing.T) {
	im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	im.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	im.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	im.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	im.SetNRGBA(1, 1, color.NRGBA{R: 128, G: 64, A: 255})
	dib := encodeDIB(im)
	got, err := decodeDIB(dib)
	if err != nil {
		t.Fatal(err)
	}
	for y := range 2 {
		for x := range 2 {
			if got.NRGBAAt(x, y) != im.NRGBAAt(x, y) {
				t.Fatalf("pixel %d,%d = %v, want %v", x, y, got.NRGBAAt(x, y), im.NRGBAAt(x, y))
			}
		}
	}
}

func TestDIBRejectsOversizedDimensionsBeforeAllocating(t *testing.T) {
	dib := make([]byte, 40)
	binary.LittleEndian.PutUint32(dib[0:], 40)
	binary.LittleEndian.PutUint32(dib[4:], 50000)
	binary.LittleEndian.PutUint32(dib[8:], 50000)
	binary.LittleEndian.PutUint16(dib[12:], 1)
	binary.LittleEndian.PutUint16(dib[14:], 32)
	if _, err := decodeDIB(dib); err == nil {
		t.Fatal("huge DIB accepted")
	}
}
