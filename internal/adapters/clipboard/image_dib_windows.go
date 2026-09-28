//go:build windows

package clipboard

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

var errDIB = errors.New("clipboard: unsupported or damaged bitmap")

// decodeDIB accepts the uncompressed 24/32-bit DIB formats used by Windows screenshots.
// Header, dimensions and row length are checked before allocating pixel memory.
func decodeDIB(raw []byte) (*image.NRGBA, error) {
	if len(raw) < 40 {
		return nil, errDIB
	}
	hdr := int(binary.LittleEndian.Uint32(raw))
	if hdr < 40 || hdr > len(raw) {
		return nil, errDIB
	}
	w, signedH := int(int32(binary.LittleEndian.Uint32(raw[4:]))), int(int32(binary.LittleEndian.Uint32(raw[8:])))
	h := signedH
	if h < 0 {
		h = -h
	}
	bits := int(binary.LittleEndian.Uint16(raw[14:]))
	compression := binary.LittleEndian.Uint32(raw[16:])
	if !clipshare.ImageDimensions(w, h) || binary.LittleEndian.Uint16(raw[12:]) != 1 ||
		(bits != 24 && bits != 32) || (compression != 0 && compression != 3) {
		return nil, errDIB
	}
	if compression == 3 {
		if bits != 32 || len(raw) < 52 || (hdr == 40 && len(raw) < 52) {
			return nil, errDIB
		}
		masks := raw[40:52]
		if binary.LittleEndian.Uint32(masks) != 0x00ff0000 ||
			binary.LittleEndian.Uint32(masks[4:]) != 0x0000ff00 ||
			binary.LittleEndian.Uint32(masks[8:]) != 0x000000ff {
			return nil, errDIB
		}
		if hdr == 40 {
			hdr += 12
		}
	}
	stride := ((w*bits + 31) / 32) * 4
	if stride > (len(raw)-hdr)/h {
		return nil, errDIB
	}
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		rowY := y
		if signedH > 0 {
			rowY = h - 1 - y
		}
		row := raw[hdr+rowY*stride:]
		for x := range w {
			i := x * (bits / 8)
			im.SetNRGBA(x, y, color.NRGBA{R: row[i+2], G: row[i+1], B: row[i], A: 255})
		}
	}
	return im, nil
}

// encodeDIB produces a bottom-up 32-bit BI_RGB block pasteable by Windows apps.
func encodeDIB(im *image.NRGBA) []byte {
	w, h := im.Bounds().Dx(), im.Bounds().Dy()
	raw := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(raw, 40)
	binary.LittleEndian.PutUint32(raw[4:], uint32(w))
	binary.LittleEndian.PutUint32(raw[8:], uint32(h))
	binary.LittleEndian.PutUint16(raw[12:], 1)
	binary.LittleEndian.PutUint16(raw[14:], 32)
	binary.LittleEndian.PutUint32(raw[20:], uint32(w*h*4))
	for y := range h {
		for x := range w {
			c := im.NRGBAAt(x, y)
			i := 40 + ((h-1-y)*w+x)*4
			raw[i], raw[i+1], raw[i+2], raw[i+3] = c.B, c.G, c.R, 0
		}
	}
	return raw
}
