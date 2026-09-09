package diagnose

import (
	"errors"
	"strings"
	"unicode"
)

// This file holds the answer to one design problem, and the answer is a
// compromise, so the reasoning is written down rather than implied.
//
// The problem: an unanswerable check has to say what went wrong. The row is
// user-visible Russian; the error underneath it is written by libraries in
// English (and on these two machines, by Windows in Russian); and a probe error
// is the last place a credential should be allowed to surface. Those three pull
// against each other and no option satisfies all of them:
//
//   - Print the error. Honest and useless: «ssh: handshake failed: ...» is not
//     a sentence the person at the keyboard can act on, and it opens the door
//     to whatever a library decided to put in its message.
//   - Print nothing but Russian. That is what the engine did, and it is the
//     defect: one content-free sentence for every failure, which cost an hour
//     of live debugging with the cause sitting in the error chain.
//   - Translate the errors. There is no honest way: the set is open (every
//     library and every Windows message table), and a mistranslated cause is
//     worse than a quoted one, because it looks authoritative.
//
// So: a Russian sentence the user can act on, plus the system's own words
// quoted after it and clearly subordinate — «Проверку не удалось выполнить.
// Система сообщила: «...»». The Russian carries the meaning; the quotation
// carries the evidence, marked as somebody else's words.
//
// What is quoted is deliberately narrow, and the narrowing is the safety
// argument:
//
//  1. Only the *root* of the error chain. Our own wrappers are dropped, which
//     is what keeps the key's directory, the known_hosts path and every other
//     detail this program adds out of the quotation. What is left is the
//     library's or the operating system's own sentence about the one thing that
//     failed — the shortest and most specific text there is.
//  2. Whitespace is collapsed. A localised multi-line message or a stray CLIXML
//     blob cannot smear the row.
//  3. Any token carrying a URL is dropped, and so is a run of text longer than
//     maxTokenRunes with nothing to break it. Between them those two cover the
//     shapes a secret takes: the Tailscale login URL is a one-time credential
//     this program already keeps out of its error text (see
//     adapters/tailscale.LoginRequiredError, which holds the URL in a field for
//     exactly this reason), and key material, auth keys and long file paths are
//     unbroken runs far longer than any word in a diagnostic sentence.
//  4. The whole thing is capped at maxDetailRunes, so no error can turn a row
//     into a paragraph.
//
// Anything left after that is a short, quoted, foreign sentence. When nothing
// is left, the note falls back to noteUnknownBare, which is then the truth.

const (
	// maxDetailRunes bounds the quoted text. Two lines in the window at most:
	// enough for any library sentence, too little for a payload.
	maxDetailRunes = 120

	// maxTokenRunes is the longest unbroken run of non-space characters that
	// may be quoted. Diagnostic prose has no words this long; keys, tokens,
	// hashes and long paths are nothing but.
	maxTokenRunes = 44

	// elision replaces what is dropped, so the reader can see that something
	// was there rather than wondering about a gap.
	elision = "…"
)

// systemDetail renders the system's own account of err, safe to show. It
// returns "" when nothing quotable survives.
func systemDetail(err error) string {
	if err == nil {
		return ""
	}
	text := strings.Map(spaceOrRune, rootCause(err).Error())

	kept := make([]string, 0, 8)
	for _, token := range strings.Fields(text) {
		switch {
		case looksLikeURL(token):
			kept = append(kept, elision)
		case len([]rune(token)) > maxTokenRunes:
			kept = append(kept, elision)
		default:
			kept = append(kept, token)
		}
	}
	return truncate(strings.TrimSpace(strings.Join(kept, " ")))
}

// rootCause walks to the innermost error, dropping every layer of context this
// program added on the way up. A multi-error (fmt.Errorf with two %w) does not
// unwrap to a single error, so the walk stops there and its own text is used.
func rootCause(err error) error {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			return err
		}
		err = inner
	}
}

// spaceOrRune folds every kind of whitespace to one plain space and drops the
// quotation marks the note itself uses, so a quoted message cannot appear to
// close the quotation early.
func spaceOrRune(r rune) rune {
	switch {
	case unicode.IsSpace(r):
		return ' '
	case r == '«' || r == '»':
		return ' '
	case unicode.IsControl(r):
		return ' '
	default:
		return r
	}
}

// looksLikeURL reports whether a token carries a link. A login URL is a
// credential; nothing that looks like one is ever quoted.
func looksLikeURL(token string) bool {
	lower := strings.ToLower(token)
	return strings.Contains(lower, "://") || strings.Contains(lower, "login.tailscale.com")
}

// truncate caps the quoted text at maxDetailRunes.
func truncate(s string) string {
	runes := []rune(s)
	if len(runes) <= maxDetailRunes {
		return s
	}
	return strings.TrimSpace(string(runes[:maxDetailRunes])) + elision
}
