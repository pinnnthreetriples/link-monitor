package foldersync

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// DefaultMaxBytes is the size cap when Limits.MaxBytes is unset: 100 MiB.
//
// It is a chosen ceiling, not a technical one. A shared folder is for
// documents and code; a pass that spends ten minutes carrying one disk image
// is a pass that is not keeping anything in step, and the file it is carrying
// is one the user would rather have sent by hand.
const DefaultMaxBytes = 100 << 20

// TempSuffix is the extension a half-written file carries while it is being
// transferred. It is in the built-in exclude list, so a temporary file left
// behind by a broken link is never itself synced.
const TempSuffix = ".lmtmp"

// ErrUnsafeName means a relative path could reach outside the shared folder.
var ErrUnsafeName = errors.New("foldersync: unsafe relative path")

// builtinExclude is what nobody wants synced. Each pattern is matched against
// every component of a relative path, so ".git" and "node_modules" prune a
// whole subtree rather than one entry.
var builtinExclude = []string{
	".git", "node_modules", "~$*", "*.tmp", "desktop.ini", "thumbs.db",
	"*" + TempSuffix,
}

// fold is the key a path is remembered by. Both machines are Windows, where
// file names are case-insensitive, so folding is what makes "Notes.txt" here
// and "notes.txt" there one file rather than two — see the package comment for
// why keying them apart would be dangerous rather than merely untidy.
func fold(rel string) string { return strings.ToLower(rel) }

// SafeRel reduces a relative path to one that cannot leave the shared folder,
// refusing anything that reaches outside it.
//
// A name arriving from the peer is untrusted input, and this is deliberately
// the same reasoning internal/adapters/tailscale's safeBase applies to a
// Taildrop inbox entry rather than a second rule invented here: every
// component must be a plain file name — no separator of either kind, no volume
// letter, no "." or "..". Two Windows conditions are added because a path, not
// a single base name, is being checked: a backslash anywhere is a separator on
// this platform whatever the sender meant by it, and a component ending in a
// space or a dot names a *different* file once Windows has stripped them.
func SafeRel(rel string) (string, error) {
	clean := strings.TrimSpace(rel)
	if clean == "" {
		return "", fmt.Errorf("path %q: %w", rel, ErrUnsafeName)
	}
	if strings.ContainsRune(clean, '\\') {
		return "", fmt.Errorf("path %q: %w", rel, ErrUnsafeName)
	}
	for _, part := range strings.Split(clean, "/") {
		if err := safeComponent(part); err != nil {
			return "", fmt.Errorf("path %q: %w", rel, err)
		}
	}
	return clean, nil
}

// safeComponent checks one component of a relative path.
//
// safeBase's "the name must be its own filepath.Base" test has no counterpart
// here and needs none: splitting on '/' and refusing '\' outright is what
// achieves the same thing for a path, and a component that has survived both
// is already its own base name.
func safeComponent(part string) error {
	switch {
	case part == "" || part == "." || part == "..":
		return ErrUnsafeName
	case strings.ContainsRune(part, ':'):
		// A volume letter, and an NTFS alternate data stream with it.
		return ErrUnsafeName
	case part != strings.TrimRight(part, " ."):
		// Windows strips a trailing space or dot, so "notes.txt." addresses
		// "notes.txt" — a different file from the one the name says.
		return ErrUnsafeName
	case strings.IndexFunc(part, isControl) >= 0:
		return ErrUnsafeName
	default:
		return nil
	}
}

// isControl reports whether r is a control character, which no file name on
// either machine may contain.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// Excluded reports whether a relative path is one nobody wants synced. Every
// component is tested, so a pattern naming a directory prunes its whole
// subtree; a pattern containing '/' is tested against the whole path instead.
func Excluded(rel string, lim Limits) bool {
	lower := fold(rel)
	parts := strings.Split(lower, "/")
	for _, pattern := range builtinExclude {
		if matches(pattern, lower, parts) {
			return true
		}
	}
	for _, pattern := range lim.Exclude {
		if matches(fold(strings.TrimSpace(pattern)), lower, parts) {
			return true
		}
	}
	return false
}

// matches applies one already-folded pattern. A pattern with a separator in it
// is about the whole path; anything else is about a single component.
//
// path.Match's error means the pattern itself is malformed — an unclosed
// character class, nothing else. It is answered with "no match" rather than
// propagated on purpose: the patterns come from the user's own exclude list,
// a malformed one can never match any name, and refusing to sync the folder
// at all because one line of that list has a stray bracket in it would be a
// far worse answer than ignoring the line.
func matches(pattern, lower string, parts []string) bool {
	if pattern == "" {
		return false
	}
	if strings.ContainsRune(pattern, '/') {
		ok, err := path.Match(pattern, lower)
		return err == nil && ok
	}
	for _, part := range parts {
		if ok, err := path.Match(pattern, part); err == nil && ok {
			return true
		}
	}
	return false
}

// maxBytes is the cap in force.
func maxBytes(l Limits) int64 {
	if l.MaxBytes <= 0 {
		return DefaultMaxBytes
	}
	return l.MaxBytes
}
