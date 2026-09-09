package window

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The .ico container, as much of it as picking one image out needs. The layout
// is the same one internal/ui/trayicon writes, which is where these bytes come
// from.
const (
	// icoHeaderSize is the ICONDIR: reserved, type, image count.
	icoHeaderSize = 6
	// icoEntrySize is one ICONDIRENTRY.
	icoEntrySize = 16
	// icoTypeIcon is the ICONDIR type for an icon; 2 would be a cursor.
	icoTypeIcon = 1
	// icoMaxDim is the side a zero in the directory stands for: width and
	// height are single bytes, so 256 cannot be written literally.
	icoMaxDim = 256
)

// errBadICO reports bytes that are not a usable .ico.
var errBadICO = errors.New("malformed .ico")

// icoEntry is one image in the container: how big it is and where its bytes
// are.
type icoEntry struct {
	side   int
	offset int
	size   int
}

// icoImage returns the bytes of the image inside ico that best fits a square
// of side pixels, ready to hand to CreateIconFromResourceEx.
//
// Picking here rather than letting Windows pick is what keeps the icon out of
// the build: the alternative is a resource section in the exe, which needs a
// .syso and a build step, for a picture we already have in memory.
func icoImage(ico []byte, side int) ([]byte, error) {
	entries, err := icoEntries(ico)
	if err != nil {
		return nil, err
	}

	best := bestICOEntry(entries, side)
	return ico[best.offset : best.offset+best.size], nil
}

// icoEntries reads the directory. Every offset it returns has been checked
// against the length of the input, because the bytes are about to be handed to
// a syscall that will read exactly as far as it is told to.
func icoEntries(ico []byte) ([]icoEntry, error) {
	if len(ico) < icoHeaderSize {
		return nil, fmt.Errorf("%w: %d bytes, too short for a header", errBadICO, len(ico))
	}
	if reserved := binary.LittleEndian.Uint16(ico); reserved != 0 {
		return nil, fmt.Errorf("%w: reserved field is %d, want 0", errBadICO, reserved)
	}
	if kind := binary.LittleEndian.Uint16(ico[2:]); kind != icoTypeIcon {
		return nil, fmt.Errorf("%w: type %d, want %d", errBadICO, kind, icoTypeIcon)
	}

	count := int(binary.LittleEndian.Uint16(ico[4:]))
	if count == 0 {
		return nil, fmt.Errorf("%w: it holds no images", errBadICO)
	}
	directory := icoHeaderSize + icoEntrySize*count
	if len(ico) < directory {
		return nil, fmt.Errorf("%w: %d images need %d bytes of directory, the file is %d",
			errBadICO, count, directory, len(ico))
	}

	entries := make([]icoEntry, 0, count)
	for i := range count {
		entry, err := readICOEntry(ico[icoHeaderSize+icoEntrySize*i:], len(ico), directory)
		if err != nil {
			return nil, fmt.Errorf("%w: image %d: %w", errBadICO, i, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// readICOEntry decodes one ICONDIRENTRY and checks that it points inside the
// file, past the directory it is part of.
func readICOEntry(raw []byte, total, directory int) (icoEntry, error) {
	entry := icoEntry{
		side:   dimension(raw[0]),
		size:   int(binary.LittleEndian.Uint32(raw[8:])),
		offset: int(binary.LittleEndian.Uint32(raw[12:])),
	}
	switch {
	case entry.size <= 0:
		return icoEntry{}, errors.New("it is empty")
	case entry.offset < directory:
		return icoEntry{}, fmt.Errorf("its bytes start at %d, inside the directory", entry.offset)
	case entry.offset+entry.size > total:
		return icoEntry{}, fmt.Errorf("its bytes run to %d, past the end at %d",
			entry.offset+entry.size, total)
	default:
		return entry, nil
	}
}

// dimension reads one side length, where a zero means 256.
func dimension(b byte) int {
	if b == 0 {
		return icoMaxDim
	}
	return int(b)
}

// bestICOEntry picks the image to use for a square of side pixels: the
// smallest one at least that big, or the largest there is when every image is
// smaller. Shrinking a picture looks like the picture; enlarging one looks
// like a mistake.
//
// The slice must not be empty — [icoEntries] refuses a container with no
// images, which is the only way to get one.
func bestICOEntry(entries []icoEntry, side int) icoEntry {
	best := entries[0]
	for _, e := range entries[1:] {
		if betterICOEntry(e, best, side) {
			best = e
		}
	}
	return best
}

// betterICOEntry reports whether candidate suits side better than current.
func betterICOEntry(candidate, current icoEntry, side int) bool {
	switch {
	case candidate.side >= side && current.side >= side:
		return candidate.side < current.side // both fit: take the tighter one
	case candidate.side >= side:
		return true // it fits and the incumbent does not
	case current.side >= side:
		return false
	default:
		return candidate.side > current.side // neither fits: take the largest
	}
}
