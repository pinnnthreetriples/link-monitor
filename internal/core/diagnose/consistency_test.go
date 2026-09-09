package diagnose

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// headlinesFor lists the headlines that may stand over one verdict.
//
// The rule is not a style preference, it is the contract between snapshot() and
// the user: snapshot() takes the worst row as the verdict, and the headline is
// the sentence printed above it. «Связь установлена» over a verdict of
// «unknown» is not an imprecision, it is a statement contradicted by the line
// underneath it — and it is what the engine really printed on the work PC.
//
// StateOK and StateUnknown each admit exactly one headline: either everything
// passed or something has no answer, and there is one honest sentence for each.
// A verdict of Warn or Fail admits several, because *what* went wrong is
// precisely what those headlines are for — but never summaryOK, and never
// summaryUnknown, because by then we do know.
var headlinesFor = map[core.State][]string{
	core.StateOK:      {summaryOK},
	core.StateUnknown: {summaryUnknown},
	core.StateWarn:    {summaryPartial},
	core.StateFail: {
		summaryDown, summaryBlocked, summaryPartial, summaryRefused,
		// A key of our own that cannot be read is a definite failure with a
		// name, like the refusal beside it, so it earns a headline of its own.
		summaryKeyUnusable,
		// A peer the tailnet has never heard of is not a broken link and does
		// not read like one, so it does not borrow «Связи нет».
		summaryNotInTailnet,
		// The two host-key conditions. Neither is a fault of the link, both are
		// definite, and they are two headlines because an unverified host and a
		// changed key are two different things to be told.
		summaryHostKeyChanged, summaryHostUnverified,
	},
}

// probeAnswer is one canned answer from one probe, carrying the name it will be
// reported under so a failure says which combination produced it.
//
// marker is a distinctive fragment of the error text this answer injects, and
// is set only for the answers that hand the engine something it cannot classify.
// TestEveryUnknownRowNamesItsCause looks for it in the note: that is how "the
// row carries the cause" is asserted rather than assumed.
type probeAnswer struct {
	name   string
	marker string
	apply  func(*fakeProbe)
}

// probeShape is a whole probe, assembled from one answer per method, together
// with the markers of every unclassifiable error it will produce.
type probeShape struct {
	name    string
	markers []string
	probe   fakeProbe
}

// refusedByPeer is the peer declining our key: the failure this package learned
// to name, in the shape the SSH adapter reports it.
func refusedByPeer() *core.AuthRefusedError {
	return &core.AuthRefusedError{Addr: workPC.Addr, Port: sshPort, User: workPC.User}
}

// lockedKey is this machine's own key refusing to load, in the shape the SSH
// adapter reports it — the work PC's real condition, an ed25519 key with a
// passphrase on it.
func lockedKey() *core.KeyUnusableError {
	return &core.KeyUnusableError{
		Dir:     `C:\Users\pnj\.ssh`,
		Problem: core.KeyPassphraseNeeded,
		Err:     errors.New("ssh: this private key is passphrase protected"),
	}
}

// portClosed is a connect the far end answered with a reset: it is up, and
// nothing is listening.
func portClosed(addr string) *core.PortClosedError {
	return &core.PortClosedError{Addr: addr, Port: sshPort}
}

// answersByProbe is every answer each probe method can give, one slice per
// question. Together they describe every path through the engine — including
// the ones no hand-written scenario covers, which is the point.
//
// Two of the slices cover two probe methods each, and both times for the same
// reason: the second method is asked only when the first leaves it worth
// asking, so making them separate dimensions would multiply out combinations
// the engine cannot produce, and would say nothing more about it. The
// properties that make them unreachable — no ping after the tailnet says the
// peer is away, no "is it installed" while the service is running — are held by
// guards of their own in presence_test.go and text_test.go.
func answersByProbe() [][]probeAnswer {
	return [][]probeAnswer{
		{
			{name: "tailscale up", apply: func(p *fakeProbe) { p.up = true }},
			{name: "tailscale down", apply: func(p *fakeProbe) { p.up = false }},
			{
				name: "tailscale silent", marker: "ipn: connection refused",
				apply: func(p *fakeProbe) { p.upErr = errors.New("ipn: connection refused") },
			},
		},
		{
			// What the daemon says about the peer, and — when that leaves the
			// question open — what the tunnel says. A peer the daemon reports
			// as away has its ping armed with a deadline that never gets
			// consulted, so a run that consults it anyway fails loudly.
			{name: "tailnet says online · tunnel answers", apply: func(p *fakeProbe) {
				p.presence = core.PresenceOnline
			}},
			{name: "tailnet says online · tunnel times out", apply: func(p *fakeProbe) {
				p.presence = core.PresenceOnline
				p.latency, p.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}
			}},
			{
				name: "tailnet says online · tunnel probe broken", marker: "tailscaled: no route",
				apply: func(p *fakeProbe) {
					p.presence = core.PresenceOnline
					p.latency, p.peerErr = 0, errors.New("tailscaled: no route")
				},
			},
			{name: "tailnet says offline", apply: func(p *fakeProbe) {
				p.presence = core.PresenceOffline
				p.latency, p.peerErr = 0, context.DeadlineExceeded
			}},
			{name: "tailnet has never heard of it", apply: func(p *fakeProbe) {
				p.presence = core.PresenceAbsent
				p.latency, p.peerErr = 0, context.DeadlineExceeded
			}},
			{name: "daemon will not say · tunnel answers", apply: func(p *fakeProbe) {
				p.presence, p.presenceErr = core.PresenceUnknown, errors.New("ipn: no status")
			}},
			{name: "daemon will not say · tunnel times out", apply: func(p *fakeProbe) {
				p.presence, p.presenceErr = core.PresenceUnknown, errors.New("ipn: no status")
				p.latency, p.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}
			}},
			// The daemon answering and settling nothing is not the same as the
			// daemon failing, and it is a third thing again: the run has no word
			// from the control plane and no error to report either, so it asks
			// the network and claims nothing about the tailnet's view.
			{name: "daemon settles nothing · tunnel times out", apply: func(p *fakeProbe) {
				p.presence = core.PresenceUnknown
				p.latency, p.peerErr = 0, &core.TimeoutError{Addr: workPC.Addr}
			}},
		},
		{
			{name: "port answers", apply: func(*fakeProbe) {}},
			{name: "port blocked", apply: func(p *fakeProbe) {
				p.tcpErr = &core.BlockedError{Addr: workPC.Addr, Port: sshPort}
			}},
			{name: "port silent", apply: func(p *fakeProbe) {
				p.tcpErr = &core.TimeoutError{Addr: workPC.Addr, Port: sshPort}
			}},
			{name: "port refused", apply: func(p *fakeProbe) { p.tcpErr = portClosed(workPC.Addr) }},
			{
				name: "port probe broken", marker: "wsarecv: reset",
				apply: func(p *fakeProbe) { p.tcpErr = errors.New("wsarecv: reset") },
			},
		},
		{
			{name: "sshd runs", apply: func(p *fakeProbe) { p.serviceUp = true }},
			{name: "sshd stopped", apply: func(p *fakeProbe) { p.serviceUp = false }},
			{
				name: "sshd unanswerable", marker: "ssh: closed",
				apply: func(p *fakeProbe) { p.serviceErr = errors.New("ssh: closed") },
			},
			{name: "sshd refuses our key", apply: func(p *fakeProbe) { p.serviceErr = refusedByPeer() }},
			{name: "our key will not load", apply: func(p *fakeProbe) { p.serviceErr = lockedKey() }},
			{name: "the host key does not match", apply: func(p *fakeProbe) {
				p.serviceErr = changedHostKey()
			}},
			{name: "the host was never verified", apply: func(p *fakeProbe) {
				p.serviceErr = unverifiedHost()
			}},
		},
		{
			// Our own service manager. Whether the service exists is asked only
			// when it is not running, so the two answers travel together.
			{name: "our sshd runs", apply: func(p *fakeProbe) { p.localServiceUp = true }},
			{name: "our sshd is installed but stopped", apply: func(p *fakeProbe) {
				p.localServiceUp, p.localInstalled = false, true
			}},
			{name: "we have no ssh server at all", apply: func(p *fakeProbe) {
				p.localServiceUp, p.localInstalled = false, false
			}},
			{name: "our sshd is stopped and we cannot tell whether it exists", apply: func(p *fakeProbe) {
				p.localServiceUp = false
				p.localInstalledErr = errors.New("OpenSCManager: no access to the query")
			}},
			{
				name: "our sshd unanswerable", marker: "OpenSCManager: access denied",
				apply: func(p *fakeProbe) {
					p.localServiceErr = errors.New("OpenSCManager: access denied")
				},
			},
		},
		{
			{name: "dial-back arrives", apply: func(*fakeProbe) {}},
			{name: "dial-back blocked", apply: func(p *fakeProbe) {
				p.dialBackErr = &core.BlockedError{Addr: laptop.Addr, Port: sshPort}
			}},
			{name: "dial-back silent", apply: func(p *fakeProbe) {
				p.dialBackErr = &core.TimeoutError{Addr: laptop.Addr, Port: sshPort}
			}},
			{name: "dial-back refused by our port", apply: func(p *fakeProbe) {
				p.dialBackErr = portClosed(laptop.Addr)
			}},
			{name: "dial-back refused", apply: func(p *fakeProbe) { p.dialBackErr = refusedByPeer() }},
			{name: "dial-back has no key", apply: func(p *fakeProbe) { p.dialBackErr = lockedKey() }},
			{name: "dial-back meets a changed host key", apply: func(p *fakeProbe) {
				p.dialBackErr = changedHostKey()
			}},
			{name: "dial-back meets an unverified host", apply: func(p *fakeProbe) {
				p.dialBackErr = unverifiedHost()
			}},
			{
				name: "dial-back broken", marker: "ssh: channel closed",
				apply: func(p *fakeProbe) { p.dialBackErr = errors.New("ssh: channel closed") },
			},
		},
	}
}

// everyProbeShape is the cross product of answersByProbe: every combination of
// answers the six probes can hand the engine. It is built by folding rather
// than by nesting six loops, so adding a seventh answer to any probe costs one
// line and no new indentation.
func everyProbeShape() []probeShape {
	shapes := []probeShape{{name: "probe", probe: healthy()}}
	for _, answers := range answersByProbe() {
		next := make([]probeShape, 0, len(shapes)*len(answers))
		for _, shape := range shapes {
			for _, answer := range answers {
				probe := shape.probe
				answer.apply(&probe)
				markers := shape.markers
				if answer.marker != "" {
					markers = append(slices.Clone(markers), answer.marker)
				}
				next = append(next, probeShape{
					name:    shape.name + " · " + answer.name,
					markers: markers,
					probe:   probe,
				})
			}
		}
		shapes = next
	}
	return shapes
}

// TestHeadlineMatchesTheVerdict is the regression guard for the defect this
// package shipped with, and the more valuable of the two: a branch that recorded
// a row or a detail and left the headline alone.
//
// The one that was found sat in dialBack — an unclassifiable dial-back error set
// d.detail and never touched d.summary, so the work PC printed
//
//	ИТОГ    : unknown
//	заголовок: Связь установлена
//
// with the headline flatly contradicting the verdict below it. Two more branches
// had the same omission (sshd's and inbound's unanswerable-probe cases), which is
// why this test does not check the branch that was reported: it runs every
// combination of answers the six probes can give and holds the claim over all of
// them, so the next branch written with the same omission fails here rather than
// on somebody's screen.
func TestHeadlineMatchesTheVerdict(t *testing.T) {
	t.Parallel()

	shapes := everyProbeShape()
	if len(shapes) < 1000 {
		t.Fatalf("only %d probe shapes; the cross product is not being built", len(shapes))
	}
	for _, shape := range shapes {
		probe := shape.probe
		snap, _ := Run(context.Background(), &probe, laptop, workPC)

		assertHeadlineMatches(t, shape.name, snap)
		if snap.Detail == "" {
			t.Errorf("%s: no detail under the headline", shape.name)
		}
	}
	assertHeadlineMatches(t, "context already done", cancelledRun())
}

// cancelledRun is the one path the cross product cannot reach: a context that
// ended before the first probe, which giveUp answers without asking anything.
func cancelledRun() core.Snapshot {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	probe := healthy()
	snap, _ := Run(ctx, &probe, laptop, workPC)
	return snap
}

// assertHeadlineMatches holds one snapshot to the headlinesFor contract.
func assertHeadlineMatches(t *testing.T, name string, snap core.Snapshot) {
	t.Helper()

	allowed, known := headlinesFor[snap.Overall]
	if !known {
		t.Errorf("%s: verdict %v has no agreed headline", name, snap.Overall)
		return
	}
	if !slices.Contains(allowed, snap.Summary) {
		t.Errorf("%s: verdict %v under the headline %q, want one of %q",
			name, snap.Overall, snap.Summary, allowed)
	}
}

// TestEveryProbeShapeHoldsTheInvariants runs the same cross product through the
// checks every snapshot must satisfy — all five rows present, in order, each
// labelled and noted, and the verdict equal to the worst of them. The table in
// diagnose_test.go asserts them scenario by scenario; this asserts them for the
// combinations nobody wrote a scenario for.
func TestEveryProbeShapeHoldsTheInvariants(t *testing.T) {
	t.Parallel()

	for _, shape := range everyProbeShape() {
		probe := shape.probe
		snap, fixes := Run(context.Background(), &probe, laptop, workPC)

		assertInvariants(t, snap)
		for _, f := range fixes {
			if f.ID == "" || f.Title == "" || f.Explanation == "" {
				t.Fatalf("%s: fix %+v is not presentable", shape.name, f)
			}
		}
		if t.Failed() {
			t.Fatalf("first failing shape: %s", shape.name)
		}
	}
}

// selfExplainingNotes are the notes an Unknown row may carry that name their
// own cause without quoting an error: each one says which precondition was not
// met, so the reader knows why nothing was asked. Every other Unknown row has
// to carry the error's own words — see TestEveryUnknownRowNamesItsCause.
//
// noteNotChecked is deliberately absent. It is the note a row starts life with,
// and a finished run that still shows it is a row the engine forgot.
func selfExplainingNotes() []string {
	return []string{
		noteWaitsForTailscale,
		noteNoTailscaleAnswer,
		noteNoAnswerFromPeer,
		noteBlockedSocket,
		noteNoSession,
		noteKeyRefused,
		noteNoKeySession,
		noteNoTunnelAnswer,
		noteNoPortAnswer,
		// The peer being away, as the tailnet itself reported it. These two
		// replaced «истекло время ожидания» on a machine that was switched off.
		notePeerOffline,
		notePeerAbsent,
		// A host key that stopped the session. Both name their own cause, and
		// neither claims anything about the peer's service — when the key does
		// not match, whose machine answered is the open question.
		noteHostKeyChanged,
		noteHostUnverified,
		unknownNote(context.DeadlineExceeded),
		unknownNote(context.Canceled),
	}
}

// TestEveryUnknownRowNamesItsCause is the guard for the general defect, and the
// valuable one: unknownNote used to throw the error away.
//
// Every unanswerable row got the same content-free sentence — «Проверку не
// удалось выполнить — результат неизвестен» — so the program told the user
// nothing, and told whoever was debugging it nothing either. That is what made
// a passphrase-protected key invisible and cost an hour: the answer was in the
// error chain the whole time and the note dropped it on the floor.
//
// The rule this asserts is therefore not about wording. For every combination
// of answers the six probes can give, an Unknown row must either name the
// precondition that stopped it (one of selfExplainingNotes) or carry the words
// of the error that actually failed. A note that does neither names no cause,
// and this test fails on it.
func TestEveryUnknownRowNamesItsCause(t *testing.T) {
	t.Parallel()

	allowed := selfExplainingNotes()
	for _, shape := range everyProbeShape() {
		probe := shape.probe
		snap, _ := Run(context.Background(), &probe, laptop, workPC)

		for _, row := range snap.Checks {
			if row.State != core.StateUnknown || namesACause(row.Note, allowed, shape.markers) {
				continue
			}
			t.Fatalf("%s:\n  %s is unknown with the note %q\n"+
				"  which names no cause: it neither says which precondition stopped it "+
				"nor carries what the error said (%q)",
				shape.name, row.ID, row.Note, shape.markers)
		}
	}
}

// namesACause reports whether one note tells the reader anything about why the
// row has no answer.
func namesACause(note string, allowed, markers []string) bool {
	if slices.Contains(allowed, note) {
		return true
	}
	for _, marker := range markers {
		if strings.Contains(note, marker) {
			return true
		}
	}
	return false
}

// TestKeyThatCannotBeReadIsNeverUnknown is the regression guard for the defect
// this change was written for.
//
// The work PC's private key is passphrase protected. x/crypto answers
// *ssh.PassphraseMissingError — an exported type, saying exactly what is wrong
// — and the engine reported «Проверку не удалось выполнить — результат
// неизвестен» about every SSH row while the verdict read «unknown» and no fix
// was offered. A key we cannot read is the opposite of having no answer: it is
// a fact about this machine, and it has a remedy.
//
// So wherever it arrives, the run must name it: the outbound row fails and says
// the key is the reason, the headline is the key's own, one fix is offered as
// advice, and no row is left holding the generic note.
func TestKeyThatCannotBeReadIsNeverUnknown(t *testing.T) {
	t.Parallel()

	// The generic note, obtained the way the engine obtains it, so this test
	// keeps working if its wording is ever improved.
	generic := unknownNote(errors.New("anything unclassifiable"))

	shapes := map[string]func(fakeProbe) fakeProbe{
		"the key fails when sshd is asked about": func(p fakeProbe) fakeProbe {
			p.serviceErr = lockedKey()
			return p
		},
		"the key fails when the dial-back is asked for": func(p fakeProbe) fakeProbe {
			p.dialBackErr = lockedKey()
			return p
		},
		"the port is silent and the key will not load either": func(p fakeProbe) fakeProbe {
			p.tcpErr = &core.TimeoutError{Addr: workPC.Addr, Port: sshPort}
			p.serviceErr = lockedKey()
			return p
		},
		"the port is refused and the key will not load either": func(p fakeProbe) fakeProbe {
			p.tcpErr = portClosed(workPC.Addr)
			p.serviceErr = lockedKey()
			return p
		},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := shape(healthy())
			snap, fixes := Run(context.Background(), &probe, laptop, workPC)

			assertInvariants(t, snap)
			assertKeyProblemIsNamed(t, snap, generic)
			assertKeyProblemOffersAdvice(t, fixes)
			assertNamesNoCredential(t, snap, fixes)
		})
	}
}

// assertKeyProblemIsNamed holds the rows and the headline to the cause.
func assertKeyProblemIsNamed(t *testing.T, snap core.Snapshot, generic string) {
	t.Helper()

	outbound := snap.Checks[1]
	if outbound.ID != core.CheckSSHOut {
		t.Fatalf("Checks[1] is %q, want the outbound row", outbound.ID)
	}
	if outbound.State != core.StateFail {
		t.Errorf("outbound = %v (%q), want fail: there is no key to log in with",
			outbound.State, outbound.Note)
	}
	for _, want := range []string{"ключ", "парол"} {
		if !strings.Contains(outbound.Note, want) {
			t.Errorf("outbound note = %q, want it to mention %q", outbound.Note, want)
		}
	}
	if snap.Summary != summaryKeyUnusable {
		t.Errorf("Summary = %q, want %q", snap.Summary, summaryKeyUnusable)
	}
	if !strings.Contains(snap.Detail, "ключ") {
		t.Errorf("Detail = %q, want the key named as the cause", snap.Detail)
	}
	// The peer is innocent here: nothing was ever sent to it.
	if strings.Contains(snap.Detail, "не приняла") || strings.Contains(snap.Detail, "отклоняет") {
		t.Errorf("Detail = %q; the peer refused nothing, our own key never loaded", snap.Detail)
	}
	assertNoRowShrugs(t, snap, generic)
}

// assertNoRowShrugs: the cause is named once, and every row that could not be
// answered because of it points at it rather than shrugging.
func assertNoRowShrugs(t *testing.T, snap core.Snapshot, generic string) {
	t.Helper()

	for _, c := range snap.Checks {
		if c.Note == generic {
			t.Errorf("%s note = %q; a key we cannot read is not an unknown", c.ID, c.Note)
		}
		if c.State == core.StateUnknown && !strings.Contains(c.Note, "ключ") {
			t.Errorf("%s is unknown with the note %q, which does not point at the key", c.ID, c.Note)
		}
	}
}

// assertKeyProblemOffersAdvice holds the remedy to being advice. Rewriting a
// key without its passphrase, generating a new one, or storing the passphrase
// where a background process can read it are all decisions about how this
// machine's credentials are kept — the user's, never the monitor's.
func assertKeyProblemOffersAdvice(t *testing.T, fixes []Fix) {
	t.Helper()

	idx := slices.IndexFunc(fixes, func(f Fix) bool { return f.ID == FixUsableKey })
	if idx < 0 {
		t.Fatalf("fixes = %v, want %q among them", fixIDs(fixes), FixUsableKey)
	}
	if len(fixes) != len(slices.Compact(slices.Clone(fixIDs(fixes)))) {
		t.Errorf("fixes = %v, want each offered once", fixIDs(fixes))
	}
	fix := fixes[idx]
	if !strings.Contains(fix.Explanation, "вручную") {
		t.Errorf("fix explanation = %q, want it to say plainly that this one is the user's to do",
			fix.Explanation)
	}
	if fix.NeedsAdmin {
		t.Error("nothing in this remedy needs elevation of this process")
	}
	// The passphrase field exists; the flag deliberately does not, and the
	// reason has to reach the person who would otherwise ask for one.
	for _, want := range []string{"Config.KeyPassphrase", "командная строка"} {
		if !strings.Contains(fix.Explanation, want) {
			t.Errorf("fix explanation = %q, want it to mention %q", fix.Explanation, want)
		}
	}
}

// assertNamesNoCredential is the absolute rule, applied to the new diagnosis:
// naming the condition is the point, naming a key or a passphrase never is.
func assertNamesNoCredential(t *testing.T, snap core.Snapshot, fixes []Fix) {
	t.Helper()

	shown := []string{snap.Summary, snap.Detail}
	for _, c := range snap.Checks {
		shown = append(shown, c.Note)
	}
	for _, f := range fixes {
		shown = append(shown, f.Title, f.Explanation)
	}
	text := strings.Join(shown, "\n")

	for _, secret := range []string{"id_ed25519", "id_rsa", "PRIVATE KEY", "BEGIN OPENSSH", `C:\Users`} {
		if strings.Contains(text, secret) {
			t.Errorf("the diagnosis mentions %q; key files and key material stay out of it", secret)
		}
	}
}

// TestAuthRefusalIsNeverUnknown is the regression guard for the second defect:
// a refused key reported as «Проверку не удалось выполнить — результат
// неизвестен», the sentence this engine uses when it has no answer at all.
//
// A refusal is the opposite of having no answer. The peer answered, spoke SSH
// and declined the key — that is definite, nameable and fixable, which is the
// whole reason this program exists rather than a ping. So wherever the refusal
// arrives, the run must name it: the outbound row fails and says why, the
// headline is the refusal's own, a fix is offered, and no row is left holding
// the generic note.
func TestAuthRefusalIsNeverUnknown(t *testing.T) {
	t.Parallel()

	// The generic note, obtained the way the engine obtains it, so this test
	// keeps working if its wording is ever improved.
	generic := unknownNote(errors.New("anything unclassifiable"))

	shapes := map[string]func(fakeProbe) fakeProbe{
		"the peer refuses the session we ask sshd through": func(p fakeProbe) fakeProbe {
			p.serviceErr = refusedByPeer()
			return p
		},
		"the peer refuses the session the dial-back needs": func(p fakeProbe) fakeProbe {
			p.dialBackErr = refusedByPeer()
			return p
		},
		"the port is silent and the key is refused too": func(p fakeProbe) fakeProbe {
			p.tcpErr = &core.TimeoutError{Addr: workPC.Addr, Port: sshPort}
			p.serviceErr = refusedByPeer()
			return p
		},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := shape(healthy())
			snap, fixes := Run(context.Background(), &probe, laptop, workPC)

			assertInvariants(t, snap)
			assertRefusalIsNamed(t, snap, generic)
			assertRefusalOffersAdvice(t, fixes)
		})
	}
}

// assertRefusalIsNamed holds the rows and the headline to the refusal.
func assertRefusalIsNamed(t *testing.T, snap core.Snapshot, generic string) {
	t.Helper()

	outbound := snap.Checks[1]
	if outbound.ID != core.CheckSSHOut {
		t.Fatalf("Checks[1] is %q, want the outbound row", outbound.ID)
	}
	if outbound.State != core.StateFail {
		t.Errorf("outbound = %v (%q), want fail: the peer refused the login", outbound.State, outbound.Note)
	}
	if !strings.Contains(outbound.Note, "ключ") {
		t.Errorf("outbound note = %q, want the key named as the cause", outbound.Note)
	}
	if snap.Summary != summaryRefused {
		t.Errorf("Summary = %q, want %q", snap.Summary, summaryRefused)
	}
	if !strings.Contains(snap.Detail, "ключ") {
		t.Errorf("Detail = %q, want the key named as the cause", snap.Detail)
	}
	for _, c := range snap.Checks {
		if c.Note == generic {
			t.Errorf("%s note = %q; a refused key is not an unknown", c.ID, c.Note)
		}
		if c.State == core.StateUnknown && !strings.Contains(c.Note, "ключ") {
			t.Errorf("%s is unknown with the note %q, which does not say the key was refused",
				c.ID, c.Note)
		}
	}
}

// assertRefusalOffersAdvice holds the remedy to being a remedy the user carries
// out. Writing into the other machine's authorized-keys file needs elevation
// there and decides who may log in; this program must say so and stop.
func assertRefusalOffersAdvice(t *testing.T, fixes []Fix) {
	t.Helper()

	idx := slices.IndexFunc(fixes, func(f Fix) bool { return f.ID == FixAuthorizeKey })
	if idx < 0 {
		t.Fatalf("fixes = %v, want %q among them", fixIDs(fixes), FixAuthorizeKey)
	}
	if len(fixes) != len(slices.Compact(slices.Clone(fixIDs(fixes)))) {
		t.Errorf("fixes = %v, want each offered once", fixIDs(fixes))
	}
	fix := fixes[idx]
	if !strings.Contains(fix.Explanation, "вручную") {
		t.Errorf("fix explanation = %q, want it to say plainly that this one is the user's to do",
			fix.Explanation)
	}
	if fix.NeedsAdmin {
		t.Error("NeedsAdmin marks elevation of THIS process; the rights wanted are the peer's")
	}
	if !strings.Contains(fix.Explanation, "authorized_keys") {
		t.Errorf("fix explanation = %q, want the file to be named", fix.Explanation)
	}
}
