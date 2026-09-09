package diagnose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// TestTheWorkPCWithALockedKey is the run that was reported, reproduced.
//
// On the work PC everything below the login worked: Tailscale up, the peer
// answering, port 22 open. The private key there is passphrase protected, so
// every check that needed a session failed at once and the program printed
//
//	ИТОГ     : unknown
//	заголовок: Состояние неизвестно
//	пояснение: Обратное подключение проверить не удалось — судить о нём нельзя
//	починок: 0
//
// naming nothing, offering nothing, with *ssh.PassphraseMissingError sitting in
// the error chain the whole time. This is what it says now.
func TestTheWorkPCWithALockedKey(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.serviceErr = fmt.Errorf("opening a session: %w", lockedKey())

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	assertStates(t, snap, []core.State{
		core.StateOK,      // the tunnel is genuinely up
		core.StateFail,    // and this is the direction that does not work
		core.StateUnknown, // one cause, and these two are its symptoms
		core.StateUnknown,
		core.StateOK, // no local filter is involved at all
	})
	if snap.Overall != core.StateFail || snap.Summary != summaryKeyUnusable {
		t.Errorf("verdict = %v / %q, want fail under %q", snap.Overall, snap.Summary, summaryKeyUnusable)
	}
	if got := fixIDs(fixes); len(got) != 1 || got[0] != FixUsableKey {
		t.Fatalf("fixes = %v, want just %v", got, FixUsableKey)
	}

	// The cause is spelled out once, on the row that owns it, and the two rows
	// that merely depended on it point at it instead of repeating it.
	if !strings.Contains(snap.Checks[1].Note, "защищён паролем") {
		t.Errorf("outbound note = %q, want the passphrase named", snap.Checks[1].Note)
	}
	for _, i := range []int{2, 3} {
		if snap.Checks[i].Note != noteNoKeySession {
			t.Errorf("%s note = %q, want the shared pointer note %q",
				snap.Checks[i].ID, snap.Checks[i].Note, noteNoKeySession)
		}
	}
	if snap.Latency == 0 {
		t.Error("latency should survive: the tunnel did round-trip the peer")
	}
}

// TestAClosedPortIsNotAnUnknown covers the commonest SSH failure there is:
// sshd stopped on a machine that is otherwise perfectly healthy. Windows
// answers such a connect with a reset (WSAECONNREFUSED), which matched no
// classification at all until *core.PortClosedError existed — so the row said
// «Проверка порта не завершилась» about a port that had told us plainly there
// was nothing behind it.
func TestAClosedPortIsNotAnUnknown(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.tcpErr = fmt.Errorf("dialing: %w", portClosed(workPC.Addr))
	probe.serviceUp = false

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	outbound := snap.Checks[1]
	if outbound.State != core.StateWarn || !strings.Contains(outbound.Note, "отказом") {
		t.Errorf("outbound = %v (%q), want a warning naming the refusal", outbound.State, outbound.Note)
	}
	if snap.Checks[4].State != core.StateOK {
		t.Error("the kill-switch row must stay ok: the packet did leave this machine")
	}
	if snap.Summary != summaryPartial {
		t.Errorf("Summary = %q, want %q", snap.Summary, summaryPartial)
	}
	// A refused port with the service reported stopped is one coherent story,
	// and it comes with the one fix that helps.
	if got := fixIDs(fixes); len(got) != 1 || got[0] != FixStartSSHD {
		t.Errorf("fixes = %v, want just %v", got, FixStartSSHD)
	}
}

// TestOurOwnPortRefusingTheDialBack is the same condition in the other
// direction, where it is a contradiction worth naming: our sshd service reports
// itself running, and the peer's connect to it is refused.
func TestOurOwnPortRefusingTheDialBack(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.dialBackErr = portClosed(laptop.Addr)

	snap, _ := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	inbound := snap.Checks[2]
	if inbound.State != core.StateWarn || !strings.Contains(inbound.Note, "отклонено") {
		t.Errorf("inbound = %v (%q), want a warning naming the refusal", inbound.State, inbound.Note)
	}
	if !strings.Contains(snap.Detail, "запущена") {
		t.Errorf("Detail = %q, want it to name the contradiction with the running service", snap.Detail)
	}
}

// TestUnknownNoteQuotesTheSystem is the positive half of the general fix: an
// error the engine cannot classify still reaches the user as its own words,
// Russian sentence first, quotation second and subordinate.
func TestUnknownNoteQuotesTheSystem(t *testing.T) {
	t.Parallel()

	root := errors.New("ssh: unexpected packet in response to channel open")
	note := unknownNote(fmt.Errorf("asking 100.127.188.87 about the %q service: %w", "sshd", root))

	if !strings.HasPrefix(note, "Проверку не удалось выполнить") {
		t.Errorf("note = %q, want the Russian sentence first", note)
	}
	if !strings.Contains(note, root.Error()) {
		t.Errorf("note = %q, want the system's own words in it", note)
	}
	// Our own layers of context are dropped: they are prose we added, they are
	// in English, and the row's own label already says which check this was.
	if strings.Contains(note, "asking") {
		t.Errorf("note = %q, want only the root cause quoted", note)
	}
	if note == noteUnknownBare {
		t.Error("the note fell back to the content-free sentence with a cause available")
	}
}

// TestSystemDetailNeverLeaks holds the quotation to the one rule that outranks
// helpfulness. It is the reason the quoted text is scrubbed at all rather than
// pasted: what a library puts in an error is not this program's decision, so
// the shapes a credential takes are removed before anything reaches a window.
func TestSystemDetailNeverLeaks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want []string
		deny []string
	}{
		{
			name: "a login URL is dropped, and it is a one-time credential",
			err: errors.New("to authenticate, visit " +
				"https://login.tailscale.com/a/0123456789abcdef"),
			want: []string{"authenticate", elision},
			deny: []string{"login.tailscale.com", "0123456789abcdef", "https"},
		},
		{
			name: "an auth key is dropped for being one long unbroken run",
			err:  errors.New("bad key tskey-auth-k123CNTRL-abcdefghijklmnopqrstuvwxyz0123456789"),
			deny: []string{"tskey", "abcdefghijklmnopqrstuvwxyz"},
			want: []string{"bad key", elision},
		},
		{
			name: "newlines and a stray blob cannot smear the row",
			err:  errors.New("first line\r\nsecond\tline"),
			want: []string{"first line second line"},
			deny: []string{"\n", "\r", "\t"},
		},
		{
			name: "the note's own quotation marks are taken out",
			err:  errors.New(`service «sshd» not found`),
			want: []string{"service sshd not found"},
			deny: []string{"«", "»"},
		},
		{
			name: "control characters cannot reach the window",
			err:  errors.New("service\x07sshd\x00broken"),
			want: []string{"service sshd broken"},
			deny: []string{"\x07", "\x00"},
		},
		{
			name: "a long message is capped",
			err:  errors.New(strings.Repeat("подробность ", 40)),
			want: []string{elision},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := systemDetail(tt.err)
			if len([]rune(got)) > maxDetailRunes+len([]rune(elision)) {
				t.Errorf("systemDetail() = %q, longer than the cap", got)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("systemDetail() = %q, want it to contain %q", got, want)
				}
			}
			for _, deny := range tt.deny {
				if strings.Contains(got, deny) {
					t.Errorf("systemDetail() = %q, must not contain %q", got, deny)
				}
			}
		})
	}
}

// TestSystemDetailFallsBackToSilence: when there is nothing quotable, the note
// says only what it knows, which is the sentence every unknown row used to get.
func TestSystemDetailFallsBackToSilence(t *testing.T) {
	t.Parallel()

	if got := systemDetail(nil); got != "" {
		t.Errorf("systemDetail(nil) = %q, want empty", got)
	}
	if got := systemDetail(errors.New("   ")); got != "" {
		t.Errorf("systemDetail(blank) = %q, want empty", got)
	}
	if got := unknownNote(errors.New("")); got != noteUnknownBare {
		t.Errorf("unknownNote(empty) = %q, want %q", got, noteUnknownBare)
	}
}

// TestRootCauseStopsAtAMultiError: fmt.Errorf with two %w does not unwrap to
// one error, so the walk stops there and uses its own text. That is the shape
// the Tailscale adapter produces for a silent peer, and this proves the walk
// does not lose it.
func TestRootCauseStopsAtAMultiError(t *testing.T) {
	t.Parallel()

	joined := fmt.Errorf("%w: %w", &core.TimeoutError{Addr: workPC.Addr}, errors.New("peer did not answer"))
	got := systemDetail(fmt.Errorf("round-tripping through the tunnel: %w", joined))

	for _, want := range []string{"timed out", "peer did not answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("systemDetail() = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "round-tripping") {
		t.Errorf("systemDetail() = %q, want our own wrapper dropped", got)
	}
}

// TestSilentPeerStillReadsAsATimeout guards the seam the Tailscale adapter
// fills: a peer that never answered the tunnel ping arrives as
// *core.TimeoutError, and that is the branch which names a switched-off machine
// instead of shrugging at it. Nothing produced that type for the whole life of
// the engine, so the branch was unreachable in the field.
func TestSilentPeerStillReadsAsATimeout(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.latency = 0
	probe.peerErr = fmt.Errorf("%w: %w",
		&core.TimeoutError{Addr: workPC.Addr},
		errors.New("tailscale ping 100.127.188.87: peer did not answer the Tailscale ping"))

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	if snap.Summary != summaryDown {
		t.Errorf("Summary = %q, want %q", snap.Summary, summaryDown)
	}
	if !strings.Contains(snap.Detail, "выключена") {
		t.Errorf("Detail = %q, want the switched-off machine named", snap.Detail)
	}
	if got := fixIDs(fixes); len(got) != 1 || got[0] != FixCheckPeerOnline {
		t.Errorf("fixes = %v, want just %v", got, FixCheckPeerOnline)
	}
	if snap.Latency != 0 {
		t.Errorf("Latency = %v, want zero: nothing was measured", snap.Latency)
	}
}
