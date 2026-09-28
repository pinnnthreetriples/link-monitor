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
var Sizes = []int{16, 20, 24, 32, 48, 64, 128, 256}

const (
	// minSize preserves the public drawing size range.
	minSize = 8
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

// Draw renders two linked screens on a state-coloured rounded tile. Geometry
// uses a 16 px grid so the smallest tray entry keeps open screen apertures.
func Draw(s core.State, size int) (*image.NRGBA, error) {
	if size < minSize || size > icoMaxDim {
		return nil, fmt.Errorf("trayicon: size %d px is outside %d..%d", size, minSize, icoMaxDim)
	}

	fill := Color(s)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	box := func(x, y, w, h, radius float64, c color.NRGBA) {
		paint(img, roundedBox{x: x, y: y, w: w, h: h, r: radius, size: size}, c)
	}
	box(1, 1, 14, 14, 3.5, shade(fill, ringShade))
	box(1.5, 1.5, 13, 13, 3, fill)
	ink := color.NRGBA{R: 0xf4, G: 0xf8, B: 0xfc, A: 0xff}
	// An elbow joins the lower edge of the first screen to the second.
	box(5.5, 6, 1.5, 5.75, 0.65, ink)
	box(5.5, 10.25, 3, 1.5, 0.65, ink)
	for _, origin := range [][2]float64{{3, 3}, {7, 9}} {
		box(origin[0], origin[1], 6, 4, 0.8, ink)
		box(origin[0]+1, origin[1]+1, 4, 2, 0.2, shade(fill, 0.30))
	}
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

// roundedBox is a signed-distance alpha mask. Coverage fades over one output
// pixel at every size, keeping transparent edges smooth without blurring the
// one-pixel screen frames at 16 px.
type roundedBox struct {
	x, y, w, h, r float64
	size          int
}

func (b roundedBox) ColorModel() color.Model { return color.AlphaModel }

func (b roundedBox) Bounds() image.Rectangle { return image.Rect(0, 0, b.size, b.size) }

func (b roundedBox) At(x, y int) color.Color {
	scale := float64(b.size) / 16
	qx := math.Abs((float64(x)+0.5)/scale-b.x-b.w/2) - b.w/2 + b.r
	qy := math.Abs((float64(y)+0.5)/scale-b.y-b.h/2) - b.h/2 + b.r
	dist := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - b.r
	switch cover := 0.5 - dist*scale; {
	case cover <= 0:
		return color.Alpha{A: 0}
	case cover >= 1:
		return color.Alpha{A: 0xff}
	default:
		return color.Alpha{A: uint8(math.Round(cover * 0xff))}
	}
}
