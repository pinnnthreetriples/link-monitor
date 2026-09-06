package diagnose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// TestRunStopsOnCancelledContext checks that a context already done keeps the
// engine from probing at all, and that the snapshot says so instead of
// inventing a verdict.
func TestRunStopsOnCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	probe := healthy()
	snap, fixes := Run(ctx, &probe, laptop, workPC)

	assertInvariants(t, snap)
	if len(probe.calls) != 0 {
		t.Errorf("probe was called %v; a cancelled context must stop us first", probe.calls)
	}
	if snap.Overall != core.StateUnknown {
		t.Errorf("Overall = %v, want %v", snap.Overall, core.StateUnknown)
	}
	if len(fixes) != 0 {
		t.Errorf("got fixes %v, want none: nothing was diagnosed", fixIDs(fixes))
	}
	for _, c := range snap.Checks {
		if !strings.Contains(c.Note, "отменена") {
			t.Errorf("%s note = %q, want it to say the run was cancelled", c.ID, c.Note)
		}
	}
}

// TestRunReportsDeadline maps a deadline that expired mid-probe onto a note the
// user can act on, rather than a generic failure.
func TestRunReportsDeadline(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.upErr = fmt.Errorf("tailscaled did not answer: %w", context.DeadlineExceeded)

	snap, _ := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	if got := snap.Checks[0]; got.State != core.StateUnknown ||
		!strings.Contains(got.Note, "время ожидания") {
		t.Errorf("tailscale row = %v / %q, want unknown with a timeout note", got.State, got.Note)
	}
}

// TestBlockedLinkBlamesThisMachine guards the distinction the whole program
// exists for: the daemon reached the peer, so a refused socket is a local
// filter and the peer must not be accused of anything.
func TestBlockedLinkBlamesThisMachine(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.tcpErr = fmt.Errorf("dial tcp: %w", &core.BlockedError{Addr: workPC.Addr, Port: sshPort})

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)

	killSwitch := snap.Checks[4]
	if killSwitch.ID != core.CheckKillSwitch || killSwitch.State != core.StateFail {
		t.Fatalf("kill switch row = %v / %v, want fail", killSwitch.ID, killSwitch.State)
	}
	if !strings.Contains(killSwitch.Note, "WSAEACCES") {
		t.Errorf("kill switch note = %q, want the Windows error named", killSwitch.Note)
	}
	if !strings.Contains(killSwitch.Note, workPC.Addr) {
		t.Errorf("kill switch note = %q, want the refused address named", killSwitch.Note)
	}
	if snap.Checks[3].State == core.StateFail {
		t.Errorf("sshd row = fail; the peer was never asked and must not be blamed")
	}
	if snap.Latency == 0 {
		t.Error("latency should survive: the daemon did reach the peer")
	}
	for _, f := range fixes {
		if !f.NeedsAdmin {
			t.Errorf("fix %q should be marked as needing elevation", f.ID)
		}
	}
}

// TestBlockedErrorIsMatchedByType makes sure the engine identifies the failure
// by type, not by reading the message: the same text without the type must not
// produce the kill-switch verdict.
func TestBlockedErrorIsMatchedByType(t *testing.T) {
	t.Parallel()

	blocked := &core.BlockedError{Addr: workPC.Addr, Port: sshPort}

	probe := healthy()
	probe.tcpErr = errors.New(blocked.Error()) // same words, wrong type

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	if snap.Summary == summaryBlocked {
		t.Error("a look-alike error text must not be diagnosed as the kill switch")
	}
	if len(fixes) != 0 {
		t.Errorf("got fixes %v, want none on an unclassifiable error", fixIDs(fixes))
	}
}

// TestInboundIsNeverInferred is the regression guard for the defect this
// package shipped with: the inbound row was set to OK because the *outbound*
// socket opened. Nothing but a dial-back — or a stopped server of our own —
// settles that direction, so wherever the peer never dialled back, the row must
// not read OK.
func TestInboundIsNeverInferred(t *testing.T) {
	t.Parallel()

	probes := map[string]func(fakeProbe) fakeProbe{
		"peer port is silent": func(p fakeProbe) fakeProbe {
			p.tcpErr = &core.TimeoutError{Addr: workPC.Addr, Port: sshPort}
			return p
		},
		"a local filter blocks us": func(p fakeProbe) fakeProbe {
			p.tcpErr = &core.BlockedError{Addr: workPC.Addr, Port: sshPort}
			return p
		},
		"the dial-back fails": func(p fakeProbe) fakeProbe {
			p.dialBackErr = &core.TimeoutError{Addr: laptop.Addr, Port: sshPort}
			return p
		},
	}
	for name, shape := range probes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			probe := shape(healthy())
			snap, _ := Run(context.Background(), &probe, laptop, workPC)

			inbound := snap.Checks[2]
			if inbound.ID != core.CheckSSHIn {
				t.Fatalf("Checks[2] is %q, want the inbound row", inbound.ID)
			}
			if inbound.State == core.StateOK {
				t.Errorf("inbound = ok on note %q, but the peer never dialled back", inbound.Note)
			}
		})
	}
}

// TestNoLocalServerIsProof: a stopped SSH server of our own settles the inbound
// direction without asking anyone, and must be reported even when the outbound
// half of the link is perfectly healthy.
func TestNoLocalServerIsProof(t *testing.T) {
	t.Parallel()

	probe := healthy()
	probe.localServiceUp = false

	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	if got := snap.Checks[2]; got.State != core.StateFail {
		t.Errorf("inbound = %v (%q), want fail", got.State, got.Note)
	}
	if snap.Summary == summaryOK {
		t.Errorf("Summary = %q; the link is not established in both directions", snap.Summary)
	}
	if got := fixIDs(fixes); len(got) != 1 || got[0] != FixStartLocalSSHD {
		t.Errorf("fixes = %v, want just %v", got, FixStartLocalSSHD)
	}
	for _, call := range probe.calls {
		if strings.HasPrefix(call, "PeerCanReachUs") {
			t.Error("no point dialling back into a machine with no server listening")
		}
	}
}

func TestLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id   core.CheckID
		want string
	}{
		{core.CheckTailscale, "Tailscale"},
		{core.CheckSSHOut, "SSH: workspace-claude-pc → win-sttm11d02rd"},
		{core.CheckSSHIn, "SSH: win-sttm11d02rd → workspace-claude-pc"},
		{core.CheckSSHD, "Служба sshd на win-sttm11d02rd"},
		{core.CheckKillSwitch, "Блокировка трафика на этой машине"},
		{core.CheckID("что-то новое"), "Неизвестная проверка"},
	}
	for _, tt := range tests {
		if got := label(tt.id, laptop, workPC); got != tt.want {
			t.Errorf("label(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		machine core.Machine
		want    string
	}{
		{"tailnet name wins", workPC, "win-sttm11d02rd"},
		{"windows name next", core.Machine{WindowsName: "WIN-STTM11D02RD"}, "WIN-STTM11D02RD"},
		{"then the address", core.Machine{Addr: "100.127.188.87"}, "100.127.188.87"},
		{"and a fallback", core.Machine{}, "вторая машина"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := name(tt.machine); got != tt.want {
				t.Errorf("name() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDaemonNote(t *testing.T) {
	t.Parallel()

	if got := daemonNote("  v1.102.3 · 3 узла  "); got != "v1.102.3 · 3 узла" {
		t.Errorf("daemonNote() = %q, want the probe's own note, trimmed", got)
	}
	if got := daemonNote("   "); got != "Демон подключён к тайлнету" {
		t.Errorf("daemonNote() = %q, want the fallback sentence", got)
	}
}

func TestUnknownNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"deadline", fmt.Errorf("probe: %w", context.DeadlineExceeded), "время ожидания"},
		{"cancelled", fmt.Errorf("probe: %w", context.Canceled), "отменена"},
		{"anything else", errors.New("boom"), "не удалось выполнить"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := unknownNote(tt.err); !strings.Contains(got, tt.want) {
				t.Errorf("unknownNote() = %q, want it to mention %q", got, tt.want)
			}
		})
	}
}

// TestFixesAreSpelledOut keeps every suggestion presentable: a Russian title
// and an explanation that says what will happen.
func TestFixesAreSpelledOut(t *testing.T) {
	t.Parallel()

	fixes := []Fix{
		fixStartTailscale(),
		fixDisableKillSwitch(),
		fixSplitTunnelSSH(),
		fixStartSSHD(workPC),
		fixStartLocalSSHD(),
		fixCheckPeerOnline(workPC),
	}
	seen := make(map[FixID]bool, len(fixes))
	for _, f := range fixes {
		if f.ID == "" {
			t.Error("a fix without an ID cannot be executed")
		}
		if seen[f.ID] {
			t.Errorf("fix ID %q is used twice", f.ID)
		}
		seen[f.ID] = true
		if len([]rune(f.Title)) < 5 || len([]rune(f.Explanation)) < 20 {
			t.Errorf("fix %q reads too thin: %q / %q", f.ID, f.Title, f.Explanation)
		}
	}
	if !strings.Contains(fixDisableKillSwitch().Explanation, "100.64.0.0/10") {
		t.Error("the kill switch fix should name the range it unblocks")
	}
}

// TestSnapshotIsStamped is a small guard on the clock and the row count, kept
// apart from the table so the table stays about diagnosis.
func TestSnapshotIsStamped(t *testing.T) {
	t.Parallel()

	before := time.Now()
	probe := healthy()
	snap, _ := Run(context.Background(), &probe, laptop, workPC)

	if snap.Taken.Before(before) {
		t.Errorf("Taken = %v, want at or after %v", snap.Taken, before)
	}
}
