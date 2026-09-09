package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// icoEntry is a decoded ICONDIRENTRY, so the tests can talk about the header
// in the same words the format does.
type icoEntry struct {
	width, height       byte
	colours, reserved   byte
	planes, bitCount    uint16
	byteCount, offsetAt uint32
}

// parseICO reads the container back. It is deliberately a separate, literal
// implementation: a test that reuses the encoder's own arithmetic proves only
// that the encoder agrees with itself.
func parseICO(t *testing.T, b []byte) []icoEntry {
	t.Helper()

	if len(b) < 6 {
		t.Fatalf("icon is %d bytes, too short for an ICONDIR", len(b))
	}
	if got := binary.LittleEndian.Uint16(b[0:2]); got != 0 {
		t.Errorf("reserved field = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint16(b[2:4]); got != 1 {
		t.Errorf("type field = %d, want 1 (icon)", got)
	}
	count := int(binary.LittleEndian.Uint16(b[4:6]))
	if want := 6 + 16*count; len(b) < want {
		t.Fatalf("icon is %d bytes, too short for %d entries", len(b), count)
	}

	entries := make([]icoEntry, 0, count)
	for i := range count {
		e := b[6+16*i : 6+16*(i+1)]
		entries = append(entries, icoEntry{
			width:     e[0],
			height:    e[1],
			colours:   e[2],
			reserved:  e[3],
			planes:    binary.LittleEndian.Uint16(e[4:6]),
			bitCount:  binary.LittleEndian.Uint16(e[6:8]),
			byteCount: binary.LittleEndian.Uint32(e[8:12]),
			offsetAt:  binary.LittleEndian.Uint32(e[12:16]),
		})
	}
	return entries
}

func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// twoEntryICO is the container both header tests read, encoded once here so
// each of them asserts about one thing: the entry fields, or the offsets.
func twoEntryICO(t *testing.T) ([]byte, []icoEntry) {
	t.Helper()

	b, err := EncodeICO(
		solid(16, 16, color.NRGBA{R: 1, G: 2, B: 3, A: 255}),
		solid(32, 32, color.NRGBA{R: 4, G: 5, B: 6, A: 255}),
	)
	if err != nil {
		t.Fatalf("EncodeICO: %v", err)
	}
	entries := parseICO(t, b)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	return b, entries
}

func TestEncodeICOEntryFields(t *testing.T) {
	_, entries := twoEntryICO(t)

	wantDims := []byte{16, 32}
	for i, e := range entries {
		if e.width != wantDims[i] || e.height != wantDims[i] {
			t.Errorf("entry %d is %dx%d, want %dx%[4]d", i, e.width, e.height, wantDims[i])
		}
		if e.colours != 0 || e.reserved != 0 {
			t.Errorf("entry %d: colours=%d reserved=%d, want 0 and 0", i, e.colours, e.reserved)
		}
		if e.planes != 1 {
			t.Errorf("entry %d: planes = %d, want 1", i, e.planes)
		}
		if e.bitCount != 32 {
			t.Errorf("entry %d: bit count = %d, want 32", i, e.bitCount)
		}
	}
}

func TestEncodeICOOffsetsTileTheFile(t *testing.T) {
	b, entries := twoEntryICO(t)

	if got := entries[0].offsetAt; got != 6+16*2 {
		t.Errorf("first payload starts at %d, want %d", got, 6+16*2)
	}
	if got, want := entries[1].offsetAt, entries[0].offsetAt+entries[0].byteCount; got != want {
		t.Errorf("second payload starts at %d, want %d", got, want)
	}
	if got, want := int(entries[1].offsetAt+entries[1].byteCount), len(b); got != want {
		t.Errorf("payloads end at %d, file is %d bytes", got, want)
	}
}

func TestEncodeICOPayloadIsTheImageWeGaveIt(t *testing.T) {
	want := solid(24, 24, color.NRGBA{R: 0x3f, G: 0xbf, B: 0x7f, A: 0xff})

	b, err := EncodeICO(want)
	if err != nil {
		t.Fatalf("EncodeICO: %v", err)
	}

	e := parseICO(t, b)[0]
	payload := b[e.offsetAt : e.offsetAt+e.byteCount]

	if !bytes.HasPrefix(payload, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("payload does not start with the PNG signature: % x", payload[:8])
	}

	got, format, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("decoding the payload: %v", err)
	}
	if format != "png" {
		t.Errorf("payload format = %q, want png", format)
	}
	if got.Bounds() != want.Bounds() {
		t.Fatalf("payload bounds = %v, want %v", got.Bounds(), want.Bounds())
	}
	for y := range 24 {
		for x := range 24 {
			gr, gg, gb, ga := got.At(x, y).RGBA()
			wr, wg, wb, wa := want.At(x, y).RGBA()
			if gr != wr || gg != wg || gb != wb || ga != wa {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got.At(x, y), want.At(x, y))
			}
		}
	}
}

func TestEncodeICOEncodesEveryImageOnce(t *testing.T) {
	// Two identical images must still produce two entries: Windows chooses by
	// declared size, so a deduplicating encoder would silently drop a size.
	img := solid(16, 16, color.NRGBA{A: 255})

	b, err := EncodeICO(img, img)
	if err != nil {
		t.Fatalf("EncodeICO: %v", err)
	}
	entries := parseICO(t, b)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].offsetAt == entries[1].offsetAt {
		t.Error("both entries point at the same payload")
	}
}

func TestEncodeICORejectsNothing(t *testing.T) {
	if _, err := EncodeICO(); err == nil {
		t.Fatal("encoding zero images succeeded, want an error")
	}
}

func TestEncodeICORejectsUnrepresentableSizes(t *testing.T) {
	tests := map[string]image.Image{
		"empty":    image.NewNRGBA(image.Rect(0, 0, 0, 0)),
		"too wide": image.NewNRGBA(image.Rect(0, 0, 257, 16)),
		"too tall": image.NewNRGBA(image.Rect(0, 0, 16, 300)),
	}
	for name, img := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := EncodeICO(img); err == nil {
				t.Fatalf("encoding a %v image succeeded, want an error", img.Bounds())
			}
		})
	}
}

// The real ceiling is math.MaxUint32, which no test can reach by encoding
// images; the parameter exists so the refusal itself is exercised. Without it
// the offset written into the directory would silently wrap.
func TestEncodePayloadsRefusesWhatTheOffsetFieldCannotName(t *testing.T) {
	img := solid(16, 16, color.NRGBA{A: 255})

	// A single 16 px PNG is a few hundred bytes, so 40 is past the ceiling and
	// 1<<20 is nowhere near it.
	if _, _, err := encodePayloads([]image.Image{img}, 22, 40); err == nil {
		t.Error("a payload past the last addressable byte was accepted")
	}

	payloads, spots, err := encodePayloads([]image.Image{img, img}, 22, 1<<20)
	if err != nil {
		t.Fatalf("encodePayloads under a generous ceiling: %v", err)
	}
	if spots[0].offset != 22 || spots[0].size != uint32(len(payloads[0])) {
		t.Errorf("first spot = %+v, want offset 22 and size %d", spots[0], len(payloads[0]))
	}
	if want := 22 + uint32(len(payloads[0])); spots[1].offset != want {
		t.Errorf("second payload starts at %d, want %d", spots[1].offset, want)
	}
}

func TestEncodeICOAcceptsTheFullSizeBytePlusRoundTrip(t *testing.T) {
	// 256 is the one size the byte cannot hold, and the format spells it 0.
	b, err := EncodeICO(image.NewNRGBA(image.Rect(0, 0, 256, 256)))
	if err != nil {
		t.Fatalf("EncodeICO: %v", err)
	}
	e := parseICO(t, b)[0]
	if e.width != 0 || e.height != 0 {
		t.Errorf("256 px encoded as %dx%d, want 0x0", e.width, e.height)
	}

	cfg, err := png.DecodeConfig(bytes.NewReader(b[e.offsetAt : e.offsetAt+e.byteCount]))
	if err != nil {
		t.Fatalf("decoding the payload header: %v", err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Errorf("payload is %dx%d, want 256x256", cfg.Width, cfg.Height)
	}
}

func TestDimByte(t *testing.T) {
	tests := map[int]byte{1: 1, 16: 16, 48: 48, 255: 255, 256: 0}
	for in, want := range tests {
		if got := dimByte(in); got != want {
			t.Errorf("dimByte(%d) = %d, want %d", in, got, want)
		}
	}
}
