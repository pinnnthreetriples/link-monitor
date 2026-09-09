package diagnose

import (
	"errors"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// A probe that needs an SSH session can fail to get one for two reasons this
// program can name, and both of them are definite: the peer declined the key we
// offered, or our own key could not be loaded in the first place. Neither is an
// unanswered question — one is a decision the far side made about this machine,
// the other is a fact about this machine — so neither may be filed as
// StateUnknown, and each has a remedy the user can carry out.
//
// The distinction between them matters more than it looks. A refusal proves
// there is an SSH server over there listening and speaking; a key that never
// loaded proves nothing at all about the peer, because nothing was ever sent.
// sshdRow is where that difference is written down.

// sessionFailure is one nameable reason no SSH session could be had. Exactly
// one of its fields is set, which is what lets every branch below be reached by
// a test rather than guarded by an unreachable default.
//
// Two of the four are about credentials and two are about identity, and the
// second pair does not belong in the same family as the first — see
// core/hostkey.go. They travel together here only because they all arrive the
// same way: as the error of a probe that needed a session and did not get one.
type sessionFailure struct {
	refused    *core.AuthRefusedError
	unusable   *core.KeyUnusableError
	mismatch   *core.HostKeyMismatchError
	unverified *core.HostKeyUnknownError
}

// sessionCause classifies a probe error, returning nil when the failure was
// something else — a broken connection, a dead context, anything the engine
// genuinely cannot name. Matching is by type, never by message text.
func sessionCause(err error) *sessionFailure {
	var refused *core.AuthRefusedError
	if errors.As(err, &refused) {
		return &sessionFailure{refused: refused}
	}
	var mismatch *core.HostKeyMismatchError
	if errors.As(err, &mismatch) {
		return &sessionFailure{mismatch: mismatch}
	}
	var unverified *core.HostKeyUnknownError
	if errors.As(err, &unverified) {
		return &sessionFailure{unverified: unverified}
	}
	var unusable *core.KeyUnusableError
	if errors.As(err, &unusable) {
		return &sessionFailure{unusable: unusable}
	}
	return nil
}

// sshdRow reports what the failure says about the *peer's* service.
//
// A refusal says a great deal, and better than the question we asked would have
// got: only an SSH server reaches the point of declining a key. A key of our
// own that never loaded says nothing whatever — the question was never put — so
// that row goes Unknown and points at the cause instead of inventing evidence
// about a machine we did not manage to speak to.
// A host key that does not match is the one case where something answered on
// port 22 and spoke SSH, and we still may not credit the peer's service with
// it: whose machine replied is precisely what is in doubt. So that row goes
// Unknown too, and points at the mismatch instead of at a service.
func (f *sessionFailure) sshdRow() (core.State, string) {
	switch {
	case f.refused != nil:
		return core.StateOK, "Служба отвечает по SSH, но наш ключ отклоняет"
	case f.mismatch != nil:
		return core.StateUnknown, noteHostKeyChanged
	case f.unverified != nil:
		return core.StateUnknown, noteHostUnverified
	}
	return core.StateUnknown, noteNoKeySession
}

// blame records the cause on the row that owns it and returns the note for the
// rows that only depended on it.
//
// That split is the point. One unusable key breaks the login, the question
// about the peer's service and the dial-back all at once — one cause, three
// symptoms. Writing the whole explanation into all three rows would read as
// three independent findings and bury the one thing the user has to fix, so the
// cause is named once, on the outbound row, and the others say plainly that
// they were not checked and why.
func (d *diagnosis) blame(f *sessionFailure) string {
	switch {
	case f.refused != nil:
		d.keyRefused(f.refused)
		return noteKeyRefused
	case f.mismatch != nil:
		d.hostKeyChanged(f.mismatch)
		return noteHostKeyChanged
	case f.unverified != nil:
		d.hostUnverified(f.unverified)
		return noteHostUnverified
	}
	d.keyUnusable(f.unusable)
	return noteNoKeySession
}

// keyUnusable is the third finding this program can name, after the packet
// filter and the refused key — and the first whose cause is entirely on this
// side of the tunnel.
//
// It is what the work PC really had: an ed25519 key protected by a passphrase
// (aes256-ctr, bcrypt KDF), which x/crypto refuses to parse without one. Every
// SSH-dependent check therefore failed at the same moment and the engine
// reported all of them as «результат неизвестен», which is how an hour went
// into finding by hand what the error chain had already said.
//
// The outbound row carries the verdict because that is the direction that does
// not work, and the headline is the key's own: nothing else found on such a run
// outranks it — the tunnel is up, the port may well answer, and the only thing
// wrong is a credential on this machine.
//
// The remedy is advice and is never carried out. Generating a key, rewriting
// one without its passphrase, or putting a passphrase where this program could
// read it are all decisions about how this machine's credentials are stored,
// which belong to the person at the keyboard — exactly like the AmneziaVPN kill
// switch and the peer's authorized-keys file this program also refuses to
// touch.
func (d *diagnosis) keyUnusable(k *core.KeyUnusableError) {
	d.set(core.CheckSSHOut, core.StateFail, keyProblemNote(k.Problem))
	d.summary = summaryKeyUnusable
	d.detail = keyProblemDetail(k.Problem)
	d.fixes = append(d.fixes, fixUsableKey(k.Problem))
}
