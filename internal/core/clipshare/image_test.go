package clipshare

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func samplePNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	im.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	im.SetNRGBA(1, 0, color.NRGBA{B: 255, A: 128})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImageNormalizationPreservesPixelsAndRejectsInvalidData(t *testing.T) {
	raw := samplePNG(t)
	normal, err := NormalizePNG(raw)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(normal))
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(im.At(1, 0)); got != (color.NRGBA{B: 255, A: 128}) {
		t.Fatalf("pixel = %v", got)
	}
	again, err := NormalizePNG(normal)
	if err != nil || !bytes.Equal(normal, again) {
		t.Fatal("normalization is not stable")
	}
	large := append([]byte(nil), raw...)
	binary.BigEndian.PutUint32(large[16:20], 100000)
	binary.BigEndian.PutUint32(large[29:33], crc32.ChecksumIEEE(large[12:29]))
	for _, bad := range [][]byte{nil, []byte("not an image"), raw[:len(raw)-5], large} {
		if _, err := NormalizePNG(bad); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
}

func TestImagePolicyHonorsMarkersCapsAndEchoes(t *testing.T) {
	s := Snapshot{Format: "png", Text: samplePNG(t), Recordable: true}
	s.Bytes = len(s.Text)
	w, p := Decide(s, Memory{}, 1)
	if w != WhySend {
		t.Fatalf("image wrongly used text cap: %s", w)
	}
	if w, _ = Decide(s, Memory{}.AfterPlanting(p), 1); w != WhyEcho {
		t.Fatal(w)
	}
	s.Recordable = false
	if w, _ = Decide(s, Memory{}, 1); w != WhyMarked {
		t.Fatal(w)
	}
	s.Recordable, s.Bytes = true, MaxImageBytes+1
	if w, _ = Decide(s, Memory{}, 1); w != WhyTooBig {
		t.Fatal(w)
	}
	s.Format = "arbitrary"
	if w, _ = Decide(s, Memory{}, 1); w != WhyNotText {
		t.Fatal(w)
	}
}

func TestDecodePaletteImageAndRejectEmptyEncoding(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	im := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	im.SetColorIndex(0, 0, 1)
	var data bytes.Buffer
	if err := png.Encode(&data, im); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePNG(data.Bytes())
	if err != nil || decoded.NRGBAAt(0, 0) != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Fatalf("palette decode = %v, %v", decoded, err)
	}
	if _, err := EncodePNG(image.NewNRGBA(image.Rect(0, 0, 0, 1))); err == nil {
		t.Fatal("empty image was encoded")
	}
}
