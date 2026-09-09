package foldersync

import (
	"path"
	"strconv"
	"strings"
	"time"
)

// The two Russian fragments a conflict copy's name is built from. They reach a
// screen and a file listing, so they are in the language the user reads — and
// they are the only user-visible strings this package owns.
const (
	// conflictFrom introduces the machine the copy came from, e.g.
	// "заметки (с win-sttm11d02rd, 21-04).txt".
	conflictFrom = " (с "
	// unknownPeer stands in when nothing named the peer.
	unknownPeer = "другой машины"
)

// conflictStamp is the time in a conflict copy's name: hour and minute, with a
// hyphen because a colon cannot appear in a Windows file name.
const conflictStamp = "15-04"

// maxPeerLabel caps how much of the peer's name goes into a file name, so a
// long tailnet name cannot push a deep path past what Windows will open.
const maxPeerLabel = 40

// badInName are the characters Windows refuses in a file name. The peer's name
// arrives from configuration rather than from the peer itself, but it ends up
// in a path either way, so it is cleaned rather than trusted.
const badInName = `\/:*?"<>|`

// conflictName is the name the peer's copy of rel lands under, beside the
// local file rather than on top of it:
//
//	заметки.txt  ->  заметки (с win-sttm11d02rd, 21-04).txt
//
// taken is every name already present on the destination side, case-folded,
// plus the names earlier conflicts in the same plan have claimed. A candidate
// that is taken gains a counter, so two conflicts in the same minute cannot
// collide and neither can ever land on an existing file.
func conflictName(rel, peer string, now time.Time, taken map[string]bool) string {
	dir := path.Dir(rel)
	base := path.Base(rel)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	middle := conflictFrom + peerLabel(peer) + ", " + now.Format(conflictStamp)

	// The loop terminates because taken is finite and every candidate differs.
	candidate := join(dir, stem+middle+")"+ext)
	for n := 2; taken[fold(candidate)]; n++ {
		candidate = join(dir, stem+middle+", "+strconv.Itoa(n)+")"+ext)
	}
	return candidate
}

// join puts a name back under its directory. path.Dir answers "." for a name
// at the root, and "./name" is a different string from "name" — which would
// key as a different file.
func join(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// peerLabel reduces the peer's name to something that can sit in a file name.
func peerLabel(peer string) string {
	cleaned := strings.Map(func(r rune) rune {
		if isControl(r) || strings.ContainsRune(badInName, r) {
			return -1
		}
		return r
	}, peer)
	cleaned = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(cleaned), "."))
	if cleaned == "" {
		return unknownPeer
	}
	if runes := []rune(cleaned); len(runes) > maxPeerLabel {
		return strings.TrimSpace(string(runes[:maxPeerLabel]))
	}
	return cleaned
}
