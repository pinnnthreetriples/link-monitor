package clipshare

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/png"
)

// Image limits bound encoded input and the decoded pixel allocation independently.
const (
	MaxImageBytes  = 32 << 20
	MaxImagePixels = 16 << 20
)

// ErrImage means that an image is invalid, unsupported, or above the size limit.
var ErrImage = errors.New("clipboard: invalid or unsupported image")

// DecodePNG validates dimensions before decoding any pixels. Errors never include input.
func DecodePNG(text []byte) (*image.NRGBA, error) {
	if len(text) == 0 || len(text) > MaxImageBytes {
		return nil, ErrImage
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(text))
	if err != nil || !ImageDimensions(cfg.Width, cfg.Height) {
		return nil, ErrImage
	}
	im, err := png.Decode(bytes.NewReader(text))
	if err != nil {
		return nil, ErrImage
	}
	if rgba, ok := im.(*image.NRGBA); ok {
		return rgba, nil
	}
	out := image.NewNRGBA(im.Bounds())
	draw.Draw(out, out.Bounds(), im, im.Bounds().Min, draw.Src)
	return out, nil
}

// ImageDimensions checks by division so hostile dimensions cannot overflow.
func ImageDimensions(w, h int) bool {
	return w > 0 && h > 0 && w <= MaxImagePixels && h <= MaxImagePixels/w
}

// EncodePNG uses one canonical encoding; locally read and received images hash identically.
func EncodePNG(im *image.NRGBA) ([]byte, error) {
	if !ImageDimensions(im.Bounds().Dx(), im.Bounds().Dy()) {
		return nil, ErrImage
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		return nil, ErrImage
	}
	if out.Len() > MaxImageBytes {
		Zero(out.Bytes())
		return nil, ErrImage
	}
	return out.Bytes(), nil
}

// NormalizePNG drops metadata and canonicalizes encoding, retaining pixels only in memory.
func NormalizePNG(text []byte) ([]byte, error) {
	im, err := DecodePNG(text)
	if err != nil {
		return nil, err
	}
	defer Zero(im.Pix)
	return EncodePNG(im)
}

// ImageFingerprint separates image bytes from the legacy text fingerprint domain.
func ImageFingerprint(text []byte) Print {
	marked := append([]byte("image/png\x00"), text...)
	defer Zero(marked)
	return Fingerprint(marked)
}
