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
	header, err := parseDIBHeader(raw)
	if err != nil {
		return nil, err
	}
	stride := ((header.width*header.bits + 31) / 32) * 4
	if stride > (len(raw)-header.offset)/header.height {
		return nil, errDIB
	}
	im := image.NewNRGBA(image.Rect(0, 0, header.width, header.height))
	fillDIBPixels(im, raw[header.offset:], stride, header)
	return im, nil
}

type dibHeader struct {
	offset, width, height, signedHeight, bits int
}

func parseDIBHeader(raw []byte) (dibHeader, error) {
	if len(raw) < 40 {
		return dibHeader{}, errDIB
	}
	hdr := int(binary.LittleEndian.Uint32(raw))
	if hdr < 40 || hdr > len(raw) {
		return dibHeader{}, errDIB
	}
	w := int(int32(binary.LittleEndian.Uint32(raw[4:])))       //nolint:gosec // G115: signed DIB width.
	signedH := int(int32(binary.LittleEndian.Uint32(raw[8:]))) //nolint:gosec // G115: signed DIB height.
	h := signedH
	if h < 0 {
		h = -h
	}
	bits := int(binary.LittleEndian.Uint16(raw[14:]))
	compression := binary.LittleEndian.Uint32(raw[16:])
	planes := binary.LittleEndian.Uint16(raw[12:])
	if !supportedDIB(w, h, planes, bits, compression) {
		return dibHeader{}, errDIB
	}
	if compression == 3 {
		if bits != 32 || !validDIBMasks(raw) {
			return dibHeader{}, errDIB
		}
		if hdr == 40 {
			hdr += 12
		}
	}
	return dibHeader{offset: hdr, width: w, height: h, signedHeight: signedH, bits: bits}, nil
}

func supportedDIB(w, h int, planes uint16, bits int, compression uint32) bool {
	return clipshare.ImageDimensions(w, h) && planes == 1 &&
		(bits == 24 || bits == 32) && (compression == 0 || compression == 3)
}

func validDIBMasks(raw []byte) bool {
	if len(raw) < 52 {
		return false
	}
	masks := raw[40:52]
	return binary.LittleEndian.Uint32(masks) == 0x00ff0000 &&
		binary.LittleEndian.Uint32(masks[4:]) == 0x0000ff00 &&
		binary.LittleEndian.Uint32(masks[8:]) == 0x000000ff
}

func fillDIBPixels(im *image.NRGBA, pixels []byte, stride int, header dibHeader) {
	for y := range header.height {
		rowY := y
		if header.signedHeight > 0 {
			rowY = header.height - 1 - y
		}
		row := pixels[rowY*stride:]
		for x := range header.width {
			i := x * (header.bits / 8)
			im.SetNRGBA(x, y, color.NRGBA{R: row[i+2], G: row[i+1], B: row[i], A: 255})
		}
	}
}

// encodeDIB produces a bottom-up 32-bit BI_RGB block pasteable by Windows apps.
func encodeDIB(im *image.NRGBA) []byte {
	w, h := im.Bounds().Dx(), im.Bounds().Dy()
	raw := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(raw, 40)
	binary.LittleEndian.PutUint32(raw[4:], uint32(w)) //nolint:gosec // G115: validated PNG width.
	binary.LittleEndian.PutUint32(raw[8:], uint32(h)) //nolint:gosec // G115: validated PNG height.
	binary.LittleEndian.PutUint16(raw[12:], 1)
	binary.LittleEndian.PutUint16(raw[14:], 32)
	binary.LittleEndian.PutUint32(raw[20:], uint32(w*h*4)) //nolint:gosec // G115: at most 16 Mi pixels.
	for y := range h {
		for x := range w {
			c := im.NRGBAAt(x, y)
			i := 40 + ((h-1-y)*w+x)*4
			raw[i], raw[i+1], raw[i+2], raw[i+3] = c.B, c.G, c.R, 0
		}
	}
	return raw
}
