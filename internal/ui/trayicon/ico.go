// Package trayicon draws the tray icon at runtime and packs it into the .ico
// container Windows expects. Nothing is shipped as an asset: the four states
// differ only by colour, so four .ico files would be four things to keep in
// sync with the design.
package trayicon

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
)

const (
	// icoDirSize is the ICONDIR header: reserved, type, image count.
	icoDirSize = 6
	// icoEntrySize is one ICONDIRENTRY.
	icoEntrySize = 16
	// icoTypeIcon is the ICONDIR type for an icon; 2 would be a cursor.
	icoTypeIcon = 1
	// icoBitCount is the bits per pixel we advertise. The payload is RGBA.
	icoBitCount = 32
	// icoMaxDim is the largest dimension the container can describe: width and
	// height are single bytes, and 0 there is read as 256.
	icoMaxDim = 256
	// icoMaxImages is the ceiling implied by the uint16 image count.
	icoMaxImages = 0xFFFF
	// icoMaxOffset is the last byte an ICONDIRENTRY can name: both the payload
	// offset and the payload size are uint32 fields. The image count and the
	// per-side limit together do not imply it — 0xFFFF payloads of a 256 px
	// image run to tens of gibibytes — so it is checked rather than assumed.
	icoMaxOffset = uint64(math.MaxUint32)
)

var errNoImages = errors.New("an .ico needs at least one image")

// EncodeICO packs images into one .ico. Each image becomes a directory entry
// whose payload is a PNG — the form Windows has understood since Vista, and
// the reason this encoder is twenty-two bytes of header per image rather than
// a bitmap writer with a separate AND mask.
//
// Order matters only as a tie-break: Windows picks the entry nearest the size
// it wants, so pass every size you are willing to have chosen.
func EncodeICO(images ...image.Image) ([]byte, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("trayicon: encoding an icon: %w", errNoImages)
	}
	if len(images) > icoMaxImages {
		return nil, fmt.Errorf("trayicon: %d images, the container holds %d", len(images), icoMaxImages)
	}

	header := icoDirSize + icoEntrySize*len(images)
	payloads, spots, err := encodePayloads(images, uint64(header), icoMaxOffset)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, header+payloadBytes(payloads))
	out = binary.LittleEndian.AppendUint16(out, 0) // reserved, always zero
	out = binary.LittleEndian.AppendUint16(out, icoTypeIcon)
	// Bounded by the icoMaxImages check above, which is that field's own width.
	out = binary.LittleEndian.AppendUint16(out, uint16(len(images))) //nolint:gosec // G115: checked

	for i, img := range images {
		b := img.Bounds()
		out = append(out,
			dimByte(b.Dx()),
			dimByte(b.Dy()),
			0, // colours in the palette; 0 means "no colour table"
			0, // reserved
		)
		out = binary.LittleEndian.AppendUint16(out, 1) // colour planes
		out = binary.LittleEndian.AppendUint16(out, icoBitCount)
		out = binary.LittleEndian.AppendUint32(out, spots[i].size)
		out = binary.LittleEndian.AppendUint32(out, spots[i].offset)
	}

	for _, p := range payloads {
		out = append(out, p...)
	}
	return out, nil
}

// spot is where one payload sits in the file and how long it is, in the widths
// ICONDIRENTRY spells those two fields.
type spot struct {
	offset, size uint32
}

// encodePayloads turns every image into the PNG bytes its directory entry will
// point at, and works out where each of them lands. It refuses sizes the
// container cannot describe: a side outside 1..256, and — because that limit
// alone does not bound the total — an icon whose bytes run past the last
// offset the directory can name.
//
// first is where the payloads begin, and ceiling is the last addressable byte.
// The latter is a parameter rather than the constant so that the refusal can
// be tested without building four gibibytes of icon.
func encodePayloads(images []image.Image, first, ceiling uint64) ([][]byte, []spot, error) {
	payloads := make([][]byte, len(images))
	spots := make([]spot, len(images))
	at := first
	for i, img := range images {
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		if w < 1 || h < 1 || w > icoMaxDim || h > icoMaxDim {
			return nil, nil, fmt.Errorf("trayicon: image %d is %dx%d, an .ico holds 1..%d per side",
				i, w, h, icoMaxDim)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, nil, fmt.Errorf("trayicon: encoding image %d as PNG: %w", i, err)
		}
		payloads[i] = buf.Bytes()

		end := at + uint64(len(payloads[i]))
		if end > ceiling {
			return nil, nil, fmt.Errorf("trayicon: image %d ends at byte %d, an .ico names up to %d",
				i, end, ceiling)
		}
		// Both are at most end, which the check above holds at or under ceiling,
		// and ceiling is at most math.MaxUint32.
		spots[i] = spot{offset: uint32(at), size: uint32(len(payloads[i]))} //nolint:gosec // G115
		at = end
	}
	return payloads, spots, nil
}

// payloadBytes is the total size of the encoded images, for one allocation.
func payloadBytes(payloads [][]byte) int {
	n := 0
	for _, p := range payloads {
		n += len(p)
	}
	return n
}

// dimByte encodes one side length the way ICONDIRENTRY wants it: a single
// byte, where 0 stands for 256.
func dimByte(n int) byte {
	if n >= icoMaxDim {
		return 0
	}
	// encodePayloads has already refused anything outside 1..256, and 256 is
	// the branch above, so n is 1..255 here. TestDimByte pins both ends.
	return byte(n) //nolint:gosec // G115: 1..255 by the caller's validation
}
