// Package clipshare is the shared clipboard's decision and nothing else: given
// one look at the clipboard and what the loop remembers, may this item travel
// to the other machine?
//
// It is pure, and deliberately so. Four of the five rules the shared clipboard
// is built on are decisions rather than syscalls — text only, a size cap,
// Windows' own "do not record this" markers, and the item that must not bounce
// back — and a decision that lives in a polling loop next to a Win32 call is a
// decision nobody can test. Everything impure is above: reading and writing the
// clipboard in internal/adapters/clipboard, reaching the peer in
// internal/adapters/peerclip, the loop in internal/app.
//
// # The echo problem
//
// Setting the clipboard from a received item bumps the sequence number exactly
// as a human copy does, so the next poll finds a change and, without this
// package, would send it straight back — and the machine at the other end would
// do the same, forever. What breaks the loop is that the item we planted is
// known: the loop records its fingerprint, and an item whose fingerprint
// matches it is [WhyEcho] rather than something new. See [Memory.AfterPlanting]
// and [Decide].
//
// # What is never here
//
// Nothing in this package writes anything down, and nothing that leaves it
// carries content: a [Print] is a hash held in memory for the length of one
// comparison, [Why] is one of seven tokens, and both are the whole of what the
// caller learns. The content itself only ever arrives as an argument.
package clipshare

import "crypto/sha256"

// DefaultMaxBytes is the size cap when the caller names none: 64 KiB of UTF-8.
//
// It is a cap on ordinary text — a page of prose is a few kilobytes, a long SQL
// statement or a stack trace tens of them — chosen so that the item that gets
// skipped is the one nobody meant to share: a spreadsheet range, a document
// pasted whole, the output of a build. Rule 2 asks for a cap and asks for what
// it stops to be counted where the user can see it, not for a cap so generous
// that nothing ever meets it.
const DefaultMaxBytes = 64 << 10

// Print identifies one clipboard item without keeping it.
//
// It is a SHA-256 of the item's bytes, and it exists so that the loop can
// answer "is this the thing I just planted?" and "is this the thing I already
// sent?" while holding nothing anybody could read. It is a value in memory for
// the life of the loop and never anything else: rule 4 forbids the content, a
// prefix of it, and a length-plus-hash of it from reaching a log or a file, and
// that includes this.
type Print [sha256.Size]byte

// Fingerprint is the print of one item's bytes. Two identical items have the
// same print, which is the whole of what [Decide] asks of it.
func Fingerprint(text []byte) Print { return sha256.Sum256(text) }

// Zero overwrites b, so content does not sit in a reusable buffer longer than
// it must. Like the same courtesy in internal/adapters/sshx, it is a courtesy
// and not a guarantee: the runtime may already have copied the bytes when a
// slice grew, and a string made from them cannot be overwritten at all.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Snapshot is one look at the clipboard, as the reader in
// internal/adapters/clipboard reports it.
//
// Text is separate from Bytes on purpose. An item past the cap is measured and
// never copied out — the size comes from the clipboard's own allocation, so
// nothing anybody meant to keep private is read into this process merely to be
// rejected — and in that case Bytes is the real size and Text is nil.
type Snapshot struct {
	// HasText says the clipboard offered CF_UNICODETEXT at all. False is a
	// file, an image, an HTML-only or RTF-only item, or an empty clipboard.
	HasText bool
	// Bytes is the item's size in UTF-8 bytes, whether or not Text was copied.
	Bytes int
	// Text is the content, copied out only when it is text within the cap.
	Text []byte
	// Recordable is false when Windows' own markers say this item must not be
	// recorded — see internal/adapters/clipboard for the two formats and what
	// each of them means. False is the fail-safe answer: a marker that is
	// present and cannot be read counts as a refusal.
	Recordable bool
}

// Why is what [Decide] concluded. Exactly one of these means "send it".
type Why string

const (
	// WhySend is the only verdict that transmits.
	WhySend Why = "send"
	// WhyNotText is an item with no text in it: a file, an image, an
	// HTML-only or RTF-only flavour. Rule 2 carries text and nothing else.
	WhyNotText Why = "not_text"
	// WhyEmpty is an empty clipboard, or a size with no content behind it.
	WhyEmpty Why = "empty"
	// WhyMarked is rule 3: the item asked not to be recorded, and is not.
	WhyMarked Why = "marked"
	// WhyTooBig is rule 2's cap.
	WhyTooBig Why = "too_big"
	// WhyEcho is the item we planted ourselves, coming back round.
	WhyEcho Why = "echo"
	// WhySame is the item we last sent, offered again.
	WhySame Why = "same"
)

// Sends reports whether this verdict transmits. It exists so that no caller has
// to spell the comparison and get it the wrong way round.
func (w Why) Sends() bool { return w == WhySend }

// Memory is what the loop remembers between clipboard changes: the print of the
// last item it sent, and the print of the last item it planted here after
// receiving it. Both are needed, and for different reasons — see [Decide].
//
// The zero value is a loop that has neither sent nor received anything, which
// is the state the feature starts in every time it is switched on.
type Memory struct {
	sent        Print
	planted     Print
	haveSent    bool
	havePlanted bool
}

// AfterSending is the memory to keep once an item has gone to the peer. It
// returns a new value rather than mutating: this package is pure, and the loop
// owns the one copy that matters.
func (m Memory) AfterSending(p Print) Memory {
	m.sent, m.haveSent = p, true
	return m
}

// AfterPlanting is the memory to keep once a received item has been written to
// this machine's clipboard. It is the whole of the echo defence.
func (m Memory) AfterPlanting(p Print) Memory {
	m.planted, m.havePlanted = p, true
	return m
}

// Decide answers whether one clipboard item may travel, and returns its print
// so the caller can remember it without hashing twice.
//
// The order of the checks is itself a decision. The two that protect the user
// come first: an item with no text is not this feature's business, and an item
// Windows was asked not to record is refused before its content is looked at,
// so that [WhyMarked] is what the user is shown rather than a size or a
// duplicate. Only then do the cap and the two fingerprint comparisons run.
//
// maxBytes of zero or less means [DefaultMaxBytes]; a caller cannot switch the
// cap off by passing nothing.
func Decide(s Snapshot, mem Memory, maxBytes int) (Why, Print) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	switch {
	case !s.HasText:
		return WhyNotText, Print{}
	case !s.Recordable:
		return WhyMarked, Print{}
	case s.Bytes > maxBytes:
		return WhyTooBig, Print{}
	case len(s.Text) == 0:
		// Either the clipboard held an empty string, or the reader measured an
		// item and handed over no content. There is nothing to send either way,
		// and the second case must not be mistaken for one worth reporting.
		return WhyEmpty, Print{}
	}

	p := Fingerprint(s.Text)
	switch {
	case mem.havePlanted && p == mem.planted:
		// We wrote this here a moment ago after receiving it. Sending it back
		// is how the two machines would talk to each other forever.
		return WhyEcho, p
	case mem.haveSent && p == mem.sent:
		// The peer already has it. Sending it again would cost a round trip
		// and plant an echo on the far side for nothing.
		return WhySame, p
	default:
		return WhySend, p
	}
}
