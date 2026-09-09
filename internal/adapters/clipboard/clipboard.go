// Package clipboard reads and writes this Windows session's clipboard, and
// answers the one question the shared clipboard cannot be built without: has
// this item asked not to be recorded?
//
// # Why the clipboard cannot be reached over SSH
//
// A clipboard belongs to an interactive Windows session. Everything this
// program runs on the peer over SSH lands in the session OpenSSH gives it,
// which is not the user's, and OpenClipboard there answers about a clipboard
// nobody can see. That is why the shared clipboard needs the program running on
// both machines and cannot work the way the shared folder does — see
// internal/adapters/peerclip for how the peer's own instance is reached.
//
// # Detecting a change without reading the clipboard
//
// [Clipboard.Sequence] is GetClipboardSequenceNumber: a counter Windows bumps
// every time the clipboard's contents change, readable without opening the
// clipboard, without a window, and without disturbing anybody. Polling it is
// how the loop above notices a copy. Polling the *contents* instead would mean
// opening the clipboard several times a second — locking out the application
// the user is actually copying from, and reading every password that passes
// through it whether or not anything was going to be shared.
//
// # The two markers, and the third
//
// Windows documents three registered clipboard formats an application uses to
// say what may be done with what it has just copied (Clipboard Formats, "Cloud
// Clipboard and Clipboard History Formats"). Password managers set them:
//
//   - ExcludeClipboardContentFromMonitorProcessing — any data placed on the
//     clipboard in this format prevents all formats from being recorded in the
//     clipboard history or synchronised to the user's other devices. The
//     payload means nothing; its presence is the whole message.
//   - CanIncludeInClipboardHistory — a serialised DWORD. Zero forbids the
//     clipboard history; one explicitly asks for it.
//   - CanUploadToCloudClipboard — a serialised DWORD, the same way round, about
//     synchronising the item to the user's other devices.
//
// This program checks all three, and treats a refusal from any of them as a
// refusal. Rule 3 names the first two; the third is here because carrying an
// item to the machine on the other side of the room is precisely
// "synchronised to the user's other devices", and an application that has
// asked for that not to happen has asked this feature not to happen either.
// Reading it and ignoring it would be the odd choice, not this one.
//
// Every check is fail-safe. A marker that is present and cannot be read — a
// block too small to hold a DWORD, a handle Windows will not lock — counts as
// a refusal, because the alternative is to transmit an item whose own
// application asked us not to.
//
// # What this package never does
//
// It does not log, and it has nothing to write to: no file, no error message
// carrying content, no length and no hash of one. An item past the caller's cap
// is measured and never copied out of the clipboard at all. Everything it does
// copy out is handed to the caller, which zeroes it — see [clipshare.Zero].
package clipboard

import "errors"

// The three registered format names, spelled exactly as Windows knows them.
// RegisterClipboardFormatW returns the same id to every application that asks
// for the same name, which is what lets a password manager and this program
// talk about the same format without sharing any code.
const (
	formatExclude = "ExcludeClipboardContentFromMonitorProcessing"
	formatHistory = "CanIncludeInClipboardHistory"
	formatCloud   = "CanUploadToCloudClipboard"
)

var (
	// ErrUnsupported is what every call answers away from Windows. There is no
	// substitute clipboard to offer: the feature is a Windows one.
	ErrUnsupported = errors.New("clipboard: only Windows has the clipboard this package reads")

	// ErrBusy means the clipboard could not be opened because another
	// application holds it. It is an ordinary, transient condition — the loop
	// tries again on the next tick rather than treating it as a fault.
	ErrBusy = errors.New("clipboard: another application is holding the clipboard")
)

// Clipboard is this session's clipboard. It holds no state: every call opens
// the clipboard, does one thing and closes it, because a clipboard held open is
// a clipboard nobody else can use.
//
// It is safe for concurrent use only in the sense Windows is: two goroutines
// calling at once will take turns, and one of them may get [ErrBusy]. The loop
// in internal/app has exactly one goroutine touching it, which is the reason
// there is nothing to guard here.
type Clipboard struct{}

// New returns the real clipboard.
func New() *Clipboard { return &Clipboard{} }
