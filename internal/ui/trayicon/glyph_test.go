package trayicon

import (
	"bytes"
	"fmt"
	"image"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

func TestDrawShowsTwoScreensAndTheirLink(t *testing.T) {
	for _, size := range []int{16, 20, 24, 32, 48} {
		for _, state := range []core.State{core.StateOK, core.StateWarn, core.StateFail, core.StateUnknown} {
			t.Run(fmt.Sprintf("%s/%d", state, size), func(t *testing.T) {
				img, err := Draw(state, size)
				if err != nil {
					t.Fatal(err)
				}
				assertScreens(t, img, size)
				assertLink(t, img, size)
				assertStateCoverage(t, img, state, size)
			})
		}
	}
}

func assertScreens(t *testing.T, img *image.NRGBA, size int) {
	t.Helper()
	// Each screen needs a bright frame and a dark aperture at tray size too.
	for _, region := range []image.Rectangle{image.Rect(3, 3, 9, 7), image.Rect(7, 9, 13, 13)} {
		bright, dark := screenPixels(img, region, size)
		if bright < size*size/128 || dark < size*size/128 {
			t.Errorf("screen %v lacks readable frame/aperture: bright=%d dark=%d", region, bright, dark)
		}
	}
}

func screenPixels(img *image.NRGBA, region image.Rectangle, size int) (bright, dark int) {
	for y := region.Min.Y * size / 16; y < region.Max.Y*size/16; y++ {
		for x := region.Min.X * size / 16; x < region.Max.X*size/16; x++ {
			c := img.NRGBAAt(x, y)
			if c.A == 255 && c.R > 220 && c.G > 220 && c.B > 220 {
				bright++
			}
			if c.A == 255 && c.R < 90 && c.G < 90 && c.B < 90 {
				dark++
			}
		}
	}
	return bright, dark
}

func assertLink(t *testing.T, img *image.NRGBA, size int) {
	t.Helper()
	link := img.NRGBAAt(6*size/16, 8*size/16)
	if link.R < 180 || link.G < 180 || link.B < 180 || link.A != 255 {
		t.Errorf("missing visible connection between screens: %v", link)
	}
}

func assertStateCoverage(t *testing.T, img *image.NRGBA, state core.State, size int) {
	t.Helper()
	visibleState := 0
	for y := range size {
		for x := range size {
			if img.NRGBAAt(x, y) == Color(state) {
				visibleState++
			}
		}
	}
	if visibleState < size*size/5 {
		t.Errorf("state colour covers only %d of %d pixels", visibleState, size*size)
	}
}

func TestDrawAcceptsBoundarySizesAndFallsBackToUnknown(t *testing.T) {
	for _, size := range []int{8, 256} {
		unknown, err := Draw(core.StateUnknown, size)
		if err != nil {
			t.Fatal(err)
		}
		fallback, err := Draw(core.State(42), size)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(unknown.Pix, fallback.Pix) {
			t.Errorf("unrecognised state differs from unknown at %d px", size)
		}
	}
}
