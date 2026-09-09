//go:build windows

package clipboard

import (
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// Put replaces the clipboard's contents with text, as CF_UNICODETEXT.
//
// Only text is written, and no marker is written with it. Marking a received
// item "do not record" would be this program deciding, on the user's behalf,
// that something they are about to paste must vanish from their own clipboard
// history — a decision belonging to whoever copied it, on the machine where
// they copied it, and one this end has no way to make correctly.
//
// The bytes reach the clipboard through a block this function allocates and
// hands over: on success the operating system owns it, and on failure it is
// released here. The UTF-16 buffer in between is zeroed either way.
func (c *Clipboard) Put(text []byte) error {
	units := toUTF16(text)
	defer zero16(units)

	if err := openClipboard(); err != nil {
		return err
	}
	defer closeClipboard()

	// EmptyClipboard is required before SetClipboardData and is what discards
	// whatever the previous owner had put there, including any marker it had
	// set: a stale "do not record" left behind would then be read back as this
	// item's answer on the next poll.
	if ok, err := call(procEmptyClipboard); ok == 0 {
		return fmt.Errorf("emptying the clipboard: %w", err)
	}
	return handOver(units)
}

// handOver allocates the block Windows will own, fills it and gives it away.
func handOver(units []uint16) error {
	block, err := globalAlloc(uintptr(len(units)) * 2)
	if err != nil {
		return err
	}
	addr, err := globalLock(block)
	if err != nil {
		globalFree(block)
		return err
	}
	copyInto(addr, units)
	globalUnlock(block)

	if ok, callErr := call(procSetClipboardData, cfUnicodeText, block); ok == 0 {
		// The clipboard did not take it, so the block is still ours; leaving it
		// allocated would leak the content as well as the memory.
		globalFree(block)
		return fmt.Errorf("putting text on the clipboard: %w", callErr)
	}
	// From here the block belongs to the operating system, which frees it the
	// next time the clipboard is emptied. Freeing it here would be a
	// double free of memory another process may already be reading.
	return nil
}

// toUTF16 encodes UTF-8 bytes as a NUL-terminated UTF-16 sequence.
//
// It is written out rather than left to utf16.Encode([]rune(string(text)))
// because that spelling makes two more copies of the content — a string and a
// []rune — that this package cannot reach and therefore cannot zero. Invalid
// UTF-8 becomes the replacement character rather than an error: the only
// caller is the receiving endpoint, whose input has already been through a JSON
// decoder, and refusing an item at this point would lose it silently.
func toUTF16(text []byte) []uint16 {
	out := make([]uint16, 0, len(text)+1)
	for len(text) > 0 {
		r, size := utf8.DecodeRune(text)
		out = utf16.AppendRune(out, r)
		text = text[size:]
	}
	return append(out, 0)
}
