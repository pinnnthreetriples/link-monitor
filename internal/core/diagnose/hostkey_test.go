package diagnose

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// theOfferedKey is the fingerprint the far machine presented. A public key's
// fingerprint is not a secret — it is the value the user is told to compare —
// so unlike every other credential in this program it is meant to be on screen.
const theOfferedKey = "SHA256:9qO1kQxUKZBk3H4tR2u6VXbmYcJ7d8fLpNa0sEgWiTo"

// changedHostKey is the peer offering a key other than the recorded one, in the
// shape the SSH adapter reports it.
func changedHostKey() *core.HostKeyMismatchError {
	return &core.HostKeyMismatchError{
		Addr: workPC.Addr, Port: sshPort, Fingerprint: theOfferedKey,
	}
}

// unverifiedHost is a host known_hosts has never seen, with trust-on-first-use
// off. It is the milder condition, and it must never read like the one above.
func unverifiedHost() *core.HostKeyUnknownError {
	return &core.HostKeyUnknownError{
		Addr: workPC.Addr, Port: sshPort, Fingerprint: theOfferedKey,
	}
}

// TestRunWithAHostKeyProblem covers both conditions wherever they can arrive.
func TestRunWithAHostKeyProblem(t *testing.T) {
	t.Parallel()

	tests := []scenario{
		{
			name: "the host key changed and the sshd question hit it first",
			probe: func() fakeProbe {
				p := healthy()
				p.serviceErr = changedHostKey()
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateUnknown, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   summaryHostKeyChanged,
			detailHas: []string{"не тот ключ хоста", "не та машина"},
			fixes:     []FixID{FixVerifyHostKey},
			latency:   12 * time.Millisecond,
		},
		{
			name: "the host key changed and the dial-back hit it first",
			probe: func() fakeProbe {
				p := healthy()
				p.dialBackErr = changedHostKey()
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   summaryHostKeyChanged,
			detailHas: []string{"не тот ключ хоста"},
			fixes:     []FixID{FixVerifyHostKey},
			latency:   12 * time.Millisecond,
		},
		{
			name: "the host was never verified and trust-on-first-use is off",
			probe: func() fakeProbe {
				p := healthy()
				p.serviceErr = unverifiedHost()
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateUnknown, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   summaryHostUnverified,
			detailHas: []string{"не сбой связи и не смена ключа", "known_hosts"},
			fixes:     []FixID{FixConfirmHostKey},
			latency:   12 * time.Millisecond,
		},
		{
			name: "the host was never verified and the dial-back hit it first",
			probe: func() fakeProbe {
				p := healthy()
				p.dialBackErr = unverifiedHost()
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   summaryHostUnverified,
			detailHas: []string{"не сбой связи"},
			fixes:     []FixID{FixConfirmHostKey},
			latency:   12 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runScenario(t, tt)
		})
	}
}

// TestAChangedHostKeyIsItsOwnVerdict is the guard for the decision behind this
// wording, and there are four parts to it.
//
// It is not a link fault: the tunnel is up, the port answers, and nothing about
// the network is wrong — so the verdict may not be one of the ones that blame
// the network, the port or a key, and the sentence may not imply that the link
// broke.
//
// It does not credit the peer with the answer: something spoke SSH on that
// address, but whose machine it is is exactly what is in doubt, so the row
// about the peer's own service may not claim the service answered.
//
// It is never automated, and there is no «доверять всё равно»: the only honest
// check is out of band, on the other machine, over a channel that is not this
// one. Every path here is advice.
//
// And it is not the unverified host: an unknown key and a changed key are two
// conditions with two verdicts, two notes and two remedies.
func TestAChangedHostKeyIsItsOwnVerdict(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.serviceErr = changedHostKey()
	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	assertNotALinkFault(t, snap)
	assertDoesNotCreditThePeer(t, snap)
	assertHostKeyAdviceIsOnlyAdvice(t, fixes)
	assertNotTheUnverifiedHost(t, snap, fixes)
}

// assertNotALinkFault: the verdict is the host key's own, and it sits in no
// family with the network, the port or a credential.
func assertNotALinkFault(t *testing.T, snap core.Snapshot) {
	t.Helper()

	if snap.Summary != summaryHostKeyChanged {
		t.Errorf("Summary = %q, want %q", snap.Summary, summaryHostKeyChanged)
	}
	for _, wrong := range []string{
		summaryRefused, summaryKeyUnusable, summaryBlocked, summaryDown,
		summaryPartial, summaryUnknown, summaryOK,
	} {
		if snap.Summary == wrong {
			t.Errorf("Summary = %q; a changed host key is not that kind of problem", wrong)
		}
	}
	// It must not read as a broken link, and it must not accuse anybody either.
	for _, melodrama := range []string{"взлом", "атак", "подмен", "злоумышл", "перехват"} {
		if strings.Contains(snap.Detail, melodrama) {
			t.Errorf("Detail = %q; it must say what is known without accusing anyone", snap.Detail)
		}
	}
	if !strings.Contains(snap.Detail, "не та машина") {
		t.Errorf("Detail = %q, want it to say plainly what the mismatch may mean", snap.Detail)
	}
	if snap.Overall != core.StateFail {
		t.Errorf("verdict = %v, want fail: we refused to speak to it", snap.Overall)
	}
}

// assertDoesNotCreditThePeer: a refusal earns the peer's service an «ok» row,
// because only an SSH server gets as far as declining a key. A host key
// mismatch earns it nothing, because we do not know whose server that was.
func assertDoesNotCreditThePeer(t *testing.T, snap core.Snapshot) {
	t.Helper()

	sshd := snap.Checks[3]
	if sshd.ID != core.CheckSSHD {
		t.Fatalf("Checks[3] is %q, want the peer's service row", sshd.ID)
	}
	if sshd.State == core.StateOK {
		t.Errorf("the peer's service = ok (%q), but whose machine answered is the question",
			sshd.Note)
	}
	if sshd.Note != noteHostKeyChanged {
		t.Errorf("the peer's service note = %q, want it to point at the mismatch", sshd.Note)
	}
}

// assertHostKeyAdviceIsOnlyAdvice: no fix offered here may be one this program
// carries out, and none of them may offer to accept the key.
func assertHostKeyAdviceIsOnlyAdvice(t *testing.T, fixes []Fix) {
	t.Helper()

	if len(fixes) != 1 || fixes[0].ID != FixVerifyHostKey {
		t.Fatalf("fixes = %v, want just %v", fixIDs(fixes), FixVerifyHostKey)
	}
	fix := fixes[0]
	if !strings.Contains(fix.Explanation, "вручную") {
		t.Errorf("fix explanation = %q, want it to say plainly that this one is the user's to do",
			fix.Explanation)
	}
	if fix.NeedsAdmin {
		t.Error("nothing in this remedy is an action of this process, let alone an elevated one")
	}
	for _, want := range []string{theOfferedKey, "не через это подключение", "known_hosts"} {
		if !strings.Contains(fix.Explanation, want) {
			t.Errorf("fix explanation = %q, want it to mention %q", fix.Explanation, want)
		}
	}
	// The one sentence that must never appear: an offer to trust it anyway.
	for _, forbidden := range []string{"доверять всё равно", "принять новый ключ автоматически"} {
		if strings.Contains(fix.Explanation, forbidden) {
			t.Errorf("fix explanation = %q offers %q", fix.Explanation, forbidden)
		}
	}
}

// assertNotTheUnverifiedHost: the two conditions differ in every sentence and
// in the remedy. Conflating them is the dishonesty this pair exists to avoid.
func assertNotTheUnverifiedHost(t *testing.T, snap core.Snapshot, fixes []Fix) {
	t.Helper()

	other := healthy()
	other.serviceErr = unverifiedHost()
	unverified, otherFixes := Run(context.Background(), &other, laptop, workPC)

	if snap.Summary == unverified.Summary {
		t.Errorf("both host-key conditions read %q; one is unknown, the other is a mismatch",
			snap.Summary)
	}
	if snap.Detail == unverified.Detail {
		t.Errorf("both host-key conditions explain themselves as %q", snap.Detail)
	}
	if snap.Checks[1].Note == unverified.Checks[1].Note {
		t.Errorf("both host-key conditions say %q on the outbound row", snap.Checks[1].Note)
	}
	if fixes[0].ID == otherFixes[0].ID {
		t.Errorf("both host-key conditions offer %v; one deletes a stale line, "+
			"the other records a first one", fixes[0].ID)
	}
	// An unverified host has contradicted nothing, so its sentence must not
	// suggest the machine may have been swapped.
	if strings.Contains(unverified.Detail, "не та машина") {
		t.Errorf("the unverified-host detail = %q; nothing here contradicts anything",
			unverified.Detail)
	}
}

// TestHostKeyFixesStayReadableWithoutTheirValues: the two fixes are built from
// what the adapter reported, and an adapter that reported neither an address
// nor a fingerprint must not leave a sentence with a hole in it.
func TestHostKeyFixesStayReadableWithoutTheirValues(t *testing.T) {
	t.Parallel()

	bare := core.Machine{TailnetName: "win-sttm11d02rd"}
	for _, fix := range []Fix{fixVerifyHostKey(bare, ""), fixConfirmHostKey(bare, "")} {
		if strings.Contains(fix.Explanation, "адресу  ") || strings.Contains(fix.Explanation, "отпечатком .") {
			t.Errorf("fix %q has a gap where a value should be: %q", fix.ID, fix.Explanation)
		}
		if !strings.Contains(fix.Explanation, "win-sttm11d02rd") {
			t.Errorf("fix %q should fall back to naming the machine: %q", fix.ID, fix.Explanation)
		}
		if !strings.Contains(fix.Explanation, "отпечаток не сообщён") {
			t.Errorf("fix %q should say the fingerprint is missing rather than print nothing: %q",
				fix.ID, fix.Explanation)
		}
	}
}
