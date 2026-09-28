package window

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/ui/trayicon"
)

// buildICO packs one .ico holding a square of each given side, using the same
// encoder that produces the bytes this package is handed at runtime.
func buildICO(t *testing.T, sides ...int) []byte {
	t.Helper()

	images := make([]image.Image, 0, len(sides))
	for _, side := range sides {
		images = append(images, image.NewNRGBA(image.Rect(0, 0, side, side)))
	}

	ico, err := trayicon.EncodeICO(images...)
	if err != nil {
		t.Fatalf("EncodeICO(%v) = %v, want an icon", sides, err)
	}
	return ico
}

func TestIcoImagePicksTheSizeWindowsAskedFor(t *testing.T) {
	t.Parallel()

	// The sizes the tray icon really ships.
	ico := buildICO(t, trayicon.Sizes...)

	for _, tc := range []struct {
		name string
		want int
		side int
	}{
		{name: "an exact match is used as it is", side: 16, want: 16},
		{name: "the small icon on a 125% display", side: 20, want: 20},
		{name: "an odd size takes the next one up", side: 17, want: 20},
		{name: "the big icon", side: 32, want: 32},
		{name: "a large icon takes the next one up", side: 40, want: 48},
		{name: "the explorer-size icon", side: 256, want: 256},
		{name: "beyond the largest, the largest is used", side: 300, want: 256},
		{name: "a zero size takes the smallest", side: 0, want: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload, err := icoImage(ico, tc.side)
			if err != nil {
				t.Fatalf("icoImage(_, %d) = %v, want an image", tc.side, err)
			}
			if got := sideOfPayload(t, ico, payload); got != tc.want {
				t.Errorf("icoImage(_, %d) chose the %d px image, want %d px", tc.side, got, tc.want)
			}
		})
	}
}

// sideOfPayload finds which directory entry a returned payload belongs to and
// reports the side it describes.
func sideOfPayload(t *testing.T, ico, payload []byte) int {
	t.Helper()

	entries, err := icoEntries(ico)
	if err != nil {
		t.Fatalf("icoEntries() = %v, want a directory", err)
	}
	for _, e := range entries {
		if bytes.Equal(ico[e.offset:e.offset+e.size], payload) {
			return e.side
		}
	}
	t.Fatal("the payload returned does not match any directory entry")
	return 0
}

func TestIcoImageReturnsRealPngBytes(t *testing.T) {
	t.Parallel()

	// The bytes go straight to CreateIconFromResourceEx, so the slice has to
	// be the image and nothing else — no header, no neighbouring entry.
	ico, err := trayicon.Render(core.StateOK)
	if err != nil {
		t.Fatalf("Render() = %v, want the tray icon", err)
	}

	payload, err := icoImage(ico, 32)
	if err != nil {
		t.Fatalf("icoImage() = %v, want an image", err)
	}
	if !bytes.HasPrefix(payload, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("the chosen image does not start with a PNG signature: % x", payload[:8])
	}
}

func TestIcoEntriesRefusesBytesThatAreNotAnIcon(t *testing.T) {
	t.Parallel()

	good := buildICO(t, 16, 32)

	for _, tc := range []struct {
		name string
		ico  []byte
	}{
		{name: "nothing at all", ico: nil},
		{name: "shorter than a header", ico: good[:4]},
		{name: "a non-zero reserved field", ico: patch(good, 0, 1)},
		{name: "a cursor rather than an icon", ico: patch(good, 2, 2)},
		{name: "an icon with no images", ico: patch(good, 4, 0)},
		{name: "more images than there is directory", ico: patch(good, 4, 64)},
		{name: "truncated in the middle of the directory", ico: good[:icoHeaderSize+8]},
		{name: "an image whose bytes are cut off", ico: good[:len(good)-1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := icoEntries(tc.ico); !errors.Is(err, errBadICO) {
				t.Errorf("icoEntries() = %v, want an errBadICO", err)
			}
		})
	}
}

func TestIcoEntriesRefusesAnEntryPointingAtItself(t *testing.T) {
	t.Parallel()

	// An offset inside the directory would have Windows read the directory as
	// image data. It is the shape a hand-edited or truncated file takes.
	ico := buildICO(t, 16)
	binary.LittleEndian.PutUint32(ico[icoHeaderSize+12:], 2)

	if _, err := icoEntries(ico); !errors.Is(err, errBadICO) {
		t.Errorf("icoEntries() = %v, want an errBadICO", err)
	}
}

func TestIcoEntriesRefusesAnEmptyEntry(t *testing.T) {
	t.Parallel()

	ico := buildICO(t, 16)
	binary.LittleEndian.PutUint32(ico[icoHeaderSize+8:], 0)

	if _, err := icoEntries(ico); !errors.Is(err, errBadICO) {
		t.Errorf("icoEntries() = %v, want an errBadICO", err)
	}
}

func TestIcoEntriesReadsAFullSizeIconAsTwoHundredAndFiftySix(t *testing.T) {
	t.Parallel()

	// Width and height are single bytes, so the encoder writes zero for 256.
	ico := buildICO(t, icoMaxDim)

	entries, err := icoEntries(ico)
	if err != nil {
		t.Fatalf("icoEntries() = %v, want a directory", err)
	}
	if len(entries) != 1 || entries[0].side != icoMaxDim {
		t.Fatalf("entries = %+v, want one entry of %d px", entries, icoMaxDim)
	}
}

func TestBestICOEntryPrefersShrinkingToStretching(t *testing.T) {
	t.Parallel()

	entries := []icoEntry{
		{side: 16, offset: 100, size: 1},
		{side: 32, offset: 200, size: 1},
		{side: 48, offset: 300, size: 1},
	}

	for _, tc := range []struct {
		name string
		side int
		want int
	}{
		{name: "smaller than everything", side: 8, want: 16},
		{name: "exactly one of them", side: 32, want: 32},
		{name: "between two", side: 33, want: 48},
		{name: "larger than everything", side: 512, want: 48},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := bestICOEntry(entries, tc.side); got.side != tc.want {
				t.Errorf("bestICOEntry(_, %d) = %d px, want %d px", tc.side, got.side, tc.want)
			}
		})
	}
}

// patch returns a copy of ico with a uint16 written at offset, for building
// malformed headers without hand-assembling a whole file.
func patch(ico []byte, offset int, value uint16) []byte {
	out := append([]byte(nil), ico...)
	binary.LittleEndian.PutUint16(out[offset:], value)
	return out
}
