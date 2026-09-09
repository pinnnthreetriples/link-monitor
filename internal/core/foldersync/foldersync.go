// Package foldersync decides what one pass of the shared folder must do.
//
// It is pure. It is handed two listings — this machine's and the peer's — and
// what was last synced, and it answers with copies to make, conflict copies to
// write beside a file, and files to leave alone. It opens nothing and reads
// nothing, and cannot be made to. That is the whole point: the rules that keep
// this feature from destroying the user's work are decidable from data, so they
// can be tested exhaustively rather than hoped about.
//
// The rules it carries, in the words they were agreed in:
//
//   - Never delete anything. No value in this package can ask for a file to be
//     removed, on either machine — [Way] has two members and both of them
//     copy. A file present on one side and missing on the other is copied to
//     the side that lacks it, which is also why a file the user deleted can
//     come back, and why the UI says so out loud.
//   - Never overwrite a change with another change. A file that differs on
//     both sides, with both differing from what was last synced, is a
//     conflict: the peer's copy is written beside the local one under a name
//     saying where it came from and when, and the local file is not touched.
//   - Content, not clocks. A copy is decided only after both sides' content
//     hashes have been compared. Clock skew between two machines is real and
//     an mtime is not evidence.
//   - Bounded. A file over the size cap is skipped and named. The exclude list
//     is applied to every path component, so .git and node_modules are pruned
//     whole.
//   - With no history, a difference is a conflict. A missing or unreadable
//     state file therefore costs the user a conflict copy, never a file.
//
// A pass calls [Decide] twice: once on the cheap listing, which answers with
// [Plan.Hash] — the files whose content must be weighed before they can be
// ruled on — and once more with those hashes filled in, which answers with the
// actions. [Commit] then turns the plan and what actually happened into the
// baseline to store.
//
// Paths are relative to each folder's root and use '/' separators. They are
// keyed case-insensitively, because both machines are Windows and "Notes.txt"
// and "notes.txt" are one file there: keying them apart would let a case-only
// rename copy the older content over the newer one.
package foldersync

import "time"

// Entry is one file as a scan found it.
type Entry struct {
	// Path is the file's path relative to its own side's root.
	Path string
	// Size and MTime are what the scan saw. They are evidence that a file has
	// *not* changed; they are never evidence that two files are the same.
	Size  int64
	MTime time.Time
	// Hash is the content hash, hex — empty when the content has not been
	// read. Decide asks for the ones it needs through [Plan.Hash].
	Hash string
}

// State is what one side of one file looked like when it was last agreed.
type State struct {
	Size  int64
	MTime time.Time
	Hash  string
}

// Record is what was last agreed about one file.
//
// Local and Remote differ only after a conflict: the two machines then hold
// different content on purpose, and remembering both is what stops the next
// pass either reporting the same conflict for ever or, far worse, deciding
// that one side "changed" and overwriting the other with it.
type Record struct {
	Local  State
	Remote State
}

// Baseline is what was last synced, keyed by the case-folded relative path. A
// nil or empty Baseline is not an error — it means there is no history, and
// with no history every difference is a conflict rather than a copy.
type Baseline map[string]Record

// Limits bound one pass.
type Limits struct {
	// MaxBytes is the size cap; a file over it is skipped and named in the UI.
	// Zero means DefaultMaxBytes.
	MaxBytes int64
	// Exclude are the user's own patterns, on top of the built-in list. Each is
	// matched against every component of a relative path, and against the whole
	// path when the pattern contains a '/'.
	Exclude []string
}

// Input is everything one decision stands on.
type Input struct {
	// Local and Remote are the two listings. Order does not matter.
	Local  []Entry
	Remote []Entry
	// Baseline is what was last synced. Nil means no history.
	Baseline Baseline
	Limits   Limits
	// Unreadable are paths this pass could not read — a file being written,
	// most likely. Rule 4: skipped this round and retried next, with no error
	// and no fix card.
	Unreadable []string
	// PeerName names the peer inside a conflict copy's file name, e.g.
	// "win-sttm11d02rd". Empty falls back to a generic Russian label.
	PeerName string
	// Now stamps a conflict copy's name.
	Now time.Time
}

// Way is the direction bytes travel. There are two, and both of them copy:
// this type is the reason a deletion cannot be expressed at all.
type Way string

const (
	// ToPeer copies this machine's file to the peer.
	ToPeer Way = "to_peer"
	// ToUs copies the peer's file to this machine.
	ToUs Way = "to_us"
)

// Action is one copy a pass must perform.
type Action struct {
	// Key is the case-folded path both machines remember this file by. It is
	// what [Commit] matches a result against, and it is never a name to open.
	Key string
	// Way is the direction the bytes travel.
	Way Way
	// Path is the file to read, relative to the source side's root.
	Path string
	// Dest is the file to write, relative to the destination side's root. It
	// is the destination's own name for an ordinary copy — which may differ
	// from Path only in case — and a name that did not exist before when
	// Conflict is set.
	Dest string
	// Size is the source file's length as the scan saw it.
	Size int64
	// Conflict marks a copy made because both machines changed the file. The
	// destination's own file is then left exactly as it is.
	Conflict bool
}

// Why says why a file was left alone this pass.
type Why string

const (
	// WhyTooBig is a file over the size cap. It is named in the UI: rule 5
	// forbids ignoring one silently.
	WhyTooBig Why = "too_big"
	// WhyUnsafeName is a name from the peer that reaches outside the folder.
	WhyUnsafeName Why = "unsafe_name"
	// WhyBusy is a file that could not be read this round — its size or mtime
	// moved while we looked, or it would not open. Normal, not an error.
	WhyBusy Why = "busy"
	// WhyUnresolved is a conflict nobody has resolved yet: the two machines
	// still hold different content and neither side has changed since. It is
	// reported every pass so a divergence does not quietly become permanent.
	WhyUnresolved Why = "unresolved"
)

// Skip is one file left alone, and why.
type Skip struct {
	Path string
	Why  Why
	Size int64
}

// Need is one file both sides must hash before Decide can rule on it. The two
// paths differ only when the machines disagree about the name's case.
type Need struct {
	Local  string
	Remote string
}

// Plan is what one pass must do.
type Plan struct {
	// Hash names the files whose content hash both sides must supply. While it
	// is non-empty the plan is provisional: hash these, put the hashes into the
	// entries, and call Decide again.
	Hash []Need
	// Actions are the copies to make, in path order.
	Actions []Action
	// Skips are the files left alone, in path order.
	Skips []Skip
	// Next is the baseline for everything this plan does not act on, plus the
	// two-sided record a conflict leaves behind. [Commit] folds the actions'
	// results into it.
	Next Baseline
	// Fallback holds, for each path an action touches, the record that must
	// stand instead if that action fails — the previous one, so the next pass
	// tries again rather than believing a copy that never happened.
	Fallback Baseline
}
