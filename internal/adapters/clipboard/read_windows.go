//go:build windows

package clipboard

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// Sequence is GetClipboardSequenceNumber: the counter Windows bumps whenever
// the clipboard's contents change. Reading it opens nothing and locks nobody
// out, which is why the loop above polls this and never the contents.
//
// Windows returns zero when the caller has no access to the clipboard at all,
// and this reports that as an error rather than as "nothing has changed": a
// silent zero would make the loop believe the clipboard was frozen forever.
func (c *Clipboard) Sequence() (uint32, error) {
	n, err := call(procGetClipboardSequenceNumber)
	if n == 0 {
		return 0, fmt.Errorf("reading the clipboard sequence number: %w", err)
	}
	if n > math.MaxUint32 {
		return 0, fmt.Errorf("the clipboard sequence number %d does not fit a DWORD", n)
	}
	// The sequence number is a DWORD; the bound above is this conversion's
	// whole safety.
	return uint32(n), nil //nolint:gosec // G115: bounded immediately above
}

// Look reports what the clipboard is offering, in the shape the decision in
// internal/core/clipshare needs.
//
// The order is the point. Whether there is text at all is asked first, so that
// a file or an image costs one syscall and no lock. The markers are asked
// second, and an item that has asked not to be recorded is reported without its
// size and without being read: nothing about it is copied into this process.
// Only an item that is text and is allowed to travel is read at all, and even
// then only up to maxBytes+1 code units — see [readText].
func (c *Clipboard) Look(maxBytes int) (clipshare.Snapshot, error) {
	if maxBytes <= 0 {
		maxBytes = clipshare.DefaultMaxBytes
	}
	ids, err := registerFormats()
	if err != nil {
		return clipshare.Snapshot{}, err
	}
	if err := openClipboard(); err != nil {
		return clipshare.Snapshot{}, err
	}
	defer closeClipboard()

	if !formatAvailable(cfUnicodeText) {
		return clipshare.Snapshot{}, nil
	}
	if !recordable(ids) {
		return clipshare.Snapshot{HasText: true}, nil
	}
	return readText(maxBytes)
}

// recordable is rule 3: does this item allow itself to be recorded?
//
// Every answer is fail-safe. The presence of ExcludeClipboardContentFromMonitor-
// Processing is a refusal whatever its payload says, and a flag format that is
// present but cannot be read — a block too small for a DWORD, a handle that
// will not lock — is a refusal too. The alternative would be to transmit an
// item whose own application asked us not to because we could not read its
// answer, which is the one outcome rule 3 exists to prevent.
func recordable(ids formatIDs) bool {
	if formatAvailable(ids.exclude) {
		return false
	}
	for _, id := range []uint32{ids.history, ids.cloud} {
		if !formatAvailable(id) {
			continue
		}
		allowed, err := readFlag(id)
		if err != nil || !allowed {
			return false
		}
	}
	return true
}

// readFlag reads one of the two DWORD marker formats. Zero means no.
func readFlag(id uint32) (bool, error) {
	h, err := clipboardData(id)
	if err != nil {
		return false, err
	}
	size, err := globalSize(h)
	if err != nil {
		return false, err
	}
	if size < dwordBytes {
		return false, fmt.Errorf("clipboard format %d carries %d bytes, not a DWORD", id, size)
	}
	addr, err := globalLock(h)
	if err != nil {
		return false, err
	}
	defer globalUnlock(h)

	var raw [dwordBytes]byte
	copyOutBytes(raw[:], addr)
	return binary.LittleEndian.Uint32(raw[:]) != 0, nil
}

// readText reads CF_UNICODETEXT, or measures it and refuses to.
//
// The cap is honoured without copying an oversized item out of the clipboard.
// One UTF-16 code unit encodes to at least one UTF-8 byte, so reading
// maxBytes+1 units is enough to know that an item is past the cap: if no
// terminator appears within them, the string is longer than the cap can be.
// Only when the terminator is found is the UTF-8 length exact, and only then
// can the item be reported with a size the window can show.
func readText(maxBytes int) (clipshare.Snapshot, error) {
	h, err := clipboardData(cfUnicodeText)
	if err != nil {
		return clipshare.Snapshot{}, err
	}
	size, err := globalSize(h)
	if err != nil {
		return clipshare.Snapshot{}, err
	}
	units := min(int(size/2), maxBytes+1)
	if units == 0 {
		// A block with no room even for a terminator. There is nothing on the
		// clipboard to share, and nothing to complain about either.
		return clipshare.Snapshot{HasText: true, Recordable: true}, nil
	}

	addr, err := globalLock(h)
	if err != nil {
		return clipshare.Snapshot{}, err
	}
	buf := make([]uint16, units)
	copyOut(buf, addr)
	globalUnlock(h)

	text, whole := toUTF8(buf)
	zero16(buf)
	if !whole || len(text) > maxBytes {
		return oversized(text, units), nil
	}
	return clipshare.Snapshot{HasText: true, Recordable: true, Bytes: len(text), Text: text}, nil
}

// oversized reports an item past the cap, having first destroyed the part of it
// that was read. Bytes is the exact UTF-8 length when the whole item was read
// and a lower bound — the number of code units looked at — when it was not; the
// window says «больше допустимого» rather than printing a number either way.
func oversized(text []byte, units int) clipshare.Snapshot {
	size := len(text)
	if units > size {
		size = units
	}
	clipshare.Zero(text)
	return clipshare.Snapshot{HasText: true, Recordable: true, Bytes: size}
}

// toUTF8 converts UTF-16 code units to UTF-8, stopping at the first NUL, and
// reports whether that terminator was found.
//
// unicode/utf16.Decode is not used because it allocates a []rune of the whole
// item — a second copy of the content that the caller cannot reach and so
// cannot zero. This converts unit by unit into one buffer the caller owns.
func toUTF8(units []uint16) (text []byte, whole bool) {
	end := len(units)
	for i, u := range units {
		if u == 0 {
			end, whole = i, true
			break
		}
	}
	out := make([]byte, 0, end+end/2)
	for i := 0; i < end; {
		r, size := decodeRune(units[i:end])
		out = utf8.AppendRune(out, r)
		i += size
	}
	return out, whole
}

// decodeRune reads one rune from the front of a UTF-16 sequence, joining a
// surrogate pair and reporting the replacement character for a half of one.
func decodeRune(units []uint16) (rune, int) {
	u := units[0]
	switch {
	case u < 0xD800 || u > 0xDFFF:
		return rune(u), 1
	case u <= 0xDBFF && len(units) > 1 && units[1] >= 0xDC00 && units[1] <= 0xDFFF:
		return utf16.DecodeRune(rune(u), rune(units[1])), 2
	default:
		return utf8.RuneError, 1
	}
}

// zero16 overwrites a UTF-16 buffer, for the same reason [clipshare.Zero]
// overwrites a UTF-8 one.
func zero16(b []uint16) {
	for i := range b {
		b[i] = 0
	}
}
