package trayicon

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// The approved palette. These four colours are the whole point of the tray
// icon: green means the link is up, red means it is down, and the user should
// never have to open the window to learn which.
var palette = map[core.State]color.NRGBA{
	core.StateOK:      {R: 0x3f, G: 0xbf, B: 0x7f, A: 0xff}, // #3fbf7f
	core.StateWarn:    {R: 0xd9, G: 0xa1, B: 0x3a, A: 0xff}, // #d9a13a
	core.StateFail:    {R: 0xe0, G: 0x5c, B: 0x5c, A: 0xff}, // #e05c5c
	core.StateUnknown: {R: 0x64, G: 0x6c, B: 0x7c, A: 0xff}, // #646c7c
}

// Sizes are the square sizes packed into every icon. Windows asks for the
// 32x32 entry when it loads the file and then scales it down to the tray's
// 16x16 (or a DPI multiple of it), so shipping only 16 would look soft.
var Sizes = []int{16, 20, 24, 32, 48}

const (
	// minSize is the smallest icon the disc still reads at.
	minSize = 8
	// insetRatio is the transparent margin around the disc, as a fraction of
	// the icon's side. Tray icons sit shoulder to shoulder; a little air keeps
	// this one from touching its neighbours.
	insetRatio = 0.09
	// ringRatio is the width of the darker outline, as a fraction of the side.
	// It is what keeps the disc visible against both a light and a dark
	// taskbar, which is the one thing a flat colour cannot do on its own.
	ringRatio = 0.10
	// ringShade multiplies the fill to get the outline colour.
	ringShade = 0.55
)

// Color reports the palette colour for a state. An unrecognised state is grey,
// the same as StateUnknown: an icon that lies about the link is worse than one
// that admits it does not know.
func Color(s core.State) color.NRGBA {
	if c, ok := palette[s]; ok {
		return c
	}
	return palette[core.StateUnknown]
}

// Draw renders one state as a square, anti-aliased disc of the given side in
// pixels: a filled circle in the state's colour inside a darker ring.
func Draw(s core.State, size int) (*image.NRGBA, error) {
	if size < minSize || size > icoMaxDim {
		return nil, fmt.Errorf("trayicon: size %d px is outside %d..%d", size, minSize, icoMaxDim)
	}

	fill := Color(s)
	side := float64(size)
	centre := side / 2
	outer := centre - side*insetRatio
	inner := outer - side*ringRatio

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	paint(img, disc{cx: centre, cy: centre, r: outer, size: size}, shade(fill, ringShade))
	paint(img, disc{cx: centre, cy: centre, r: inner, size: size}, fill)
	return img, nil
}

// paint composites one flat colour through a mask.
func paint(dst *image.NRGBA, mask image.Image, c color.NRGBA) {
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// cache holds the encoded bytes per state. Rendering is cheap, but the tray
// asks for the same four icons for the life of the process.
var cache sync.Map // core.State -> []byte

// Render returns the .ico bytes for a state, ready to hand to systray. The
// returned slice is shared between callers and must not be modified.
func Render(s core.State) ([]byte, error) {
	if b, ok := cache.Load(s); ok {
		return b.([]byte), nil //nolint:forcetypeassert // only Render writes this map
	}

	images := make([]image.Image, 0, len(Sizes))
	for _, size := range Sizes {
		img, err := Draw(s, size)
		if err != nil {
			return nil, fmt.Errorf("trayicon: drawing %s at %d px: %w", s, size, err)
		}
		images = append(images, img)
	}

	b, err := EncodeICO(images...)
	if err != nil {
		return nil, fmt.Errorf("trayicon: packing the %s icon: %w", s, err)
	}
	cache.Store(s, b)
	return b, nil
}

// shade multiplies a colour towards black, leaving alpha alone.
func shade(c color.NRGBA, f float64) color.NRGBA {
	scale := func(v uint8) uint8 { return uint8(math.Round(float64(v) * f)) }
	return color.NRGBA{R: scale(c.R), G: scale(c.G), B: scale(c.B), A: c.A}
}

// disc is an alpha mask for a filled circle. The edge fades over one pixel,
// which is all the anti-aliasing a 16 px dot needs and costs no dependency.
type disc struct {
	cx, cy, r float64
	size      int
}

func (d disc) ColorModel() color.Model { return color.AlphaModel }

func (d disc) Bounds() image.Rectangle { return image.Rect(0, 0, d.size, d.size) }

func (d disc) At(x, y int) color.Color {
	dist := math.Hypot(float64(x)+0.5-d.cx, float64(y)+0.5-d.cy)
	switch cover := d.r + 0.5 - dist; {
	case cover <= 0:
		return color.Alpha{A: 0}
	case cover >= 1:
		return color.Alpha{A: 0xff}
	default:
		return color.Alpha{A: uint8(math.Round(cover * 0xff))}
	}
}
