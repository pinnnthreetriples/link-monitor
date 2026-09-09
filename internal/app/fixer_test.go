package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/winsvc"
	"github.com/pinnnthreetriples/link-monitor/internal/core/diagnose"
)

func TestFixerStartsLocalServices(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		id      diagnose.FixID
		service string
		want    string
	}{
		{"tailscale", diagnose.FixStartTailscale, tailscaleService, msgTailscaleStarted},
		{"local sshd", diagnose.FixStartLocalSSHD, sshdService, msgLocalSSHDStarted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeServiceControl{}
			out := NewFixer(svc, nil).Apply(context.Background(), tc.id)
			if !out.OK || out.Message != tc.want || out.NeedsAdmin {
				t.Fatalf("Apply = %+v", out)
			}
			if names := svc.names(); len(names) != 1 || names[0] != tc.service {
				t.Errorf("started %v, want [%s]", names, tc.service)
			}
		})
	}
}

func TestFixerReportsWhenAdminRightsAreTheObstacle(t *testing.T) {
	t.Parallel()

	svc := &fakeServiceControl{err: fmt.Errorf("starting %q: %w", "Tailscale", winsvc.ErrAccessDenied)}
	out := NewFixer(svc, nil).Apply(context.Background(), diagnose.FixStartTailscale)

	if out.OK || !out.NeedsAdmin {
		t.Fatalf("Apply = %+v, want a refusal that asks for elevation", out)
	}
	if !strings.Contains(out.Message, "администратора") {
		t.Errorf("message does not mention elevation: %q", out.Message)
	}
}

func TestFixerTranslatesTheOtherServiceFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not installed", winsvc.ErrNotInstalled, fmt.Sprintf(msgNotInstalledFmt, sshdService)},
		{"unsupported", winsvc.ErrUnsupported, msgUnsupported},
		{"cancelled", context.Canceled, msgCancelled},
		{"timed out", context.DeadlineExceeded, msgTimedOut},
		{"anything else", errBoom, fmt.Sprintf(msgStartFailedFmt, sshdService)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeServiceControl{err: fmt.Errorf("starting the service: %w", tc.err)}
			out := NewFixer(svc, nil).Apply(context.Background(), diagnose.FixStartLocalSSHD)
			if out.OK || out.NeedsAdmin || out.Message != tc.want {
				t.Errorf("Apply = %+v, want message %q", out, tc.want)
			}
		})
	}
}

func TestFixerStartsSSHDOnThePeer(t *testing.T) {
	t.Parallel()

	shell := &fakeShell{stdout: "какой-то мусор\nLINKMON-FIX 0\n"}
	out := NewFixer(nil, shell).Apply(context.Background(), diagnose.FixStartSSHD)

	if !out.OK || out.Message != msgPeerSSHDStarted {
		t.Fatalf("Apply = %+v", out)
	}
	if !strings.Contains(shell.script, "Start-Service") ||
		!strings.Contains(shell.script, "'sshd'") {
		t.Errorf("the script does not start sshd:\n%s", shell.script)
	}
	if !strings.Contains(shell.script, "Set-Service") {
		t.Error("the script does not put the service into autostart")
	}
}

func TestFixerTranslatesPeerOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		shell *fakeShell
		want  string
	}{
		{"denied", &fakeShell{stdout: "LINKMON-FIX 2\n"}, msgPeerDenied},
		{"not installed", &fakeShell{stdout: "LINKMON-FIX 3\n"}, msgPeerNotInstalled},
		{"failed", &fakeShell{stdout: "LINKMON-FIX 1\n"}, msgPeerStartFailed},
		{"no verdict", &fakeShell{stdout: "тишина\n"}, msgPeerNoAnswer},
		{"bad number", &fakeShell{stdout: "LINKMON-FIX семь\n"}, msgPeerNoAnswer},
		{"session down", &fakeShell{err: errBoom}, msgPeerUnreachable},
		{"cancelled", &fakeShell{err: context.Canceled}, msgCancelled},
		{"timed out", &fakeShell{err: context.DeadlineExceeded}, msgTimedOut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out := NewFixer(nil, tc.shell).Apply(context.Background(), diagnose.FixStartSSHD)
			if out.OK || out.Message != tc.want {
				t.Errorf("Apply = %+v, want message %q", out, tc.want)
			}
			if out.NeedsAdmin {
				t.Error("a peer-side refusal must not ask this process to elevate")
			}
		})
	}
}

// These are the fixes this program refuses to automate: reaching into somebody
// else's kill switch is a surprise, not a repair, and writing our key into the
// other machine's authorized-keys file would be this program granting itself
// access to a machine — a decision that is the user's to make, with elevation
// on that side that we do not have and should not want.
//
// FixUsableKey is the same rule one step closer to home. When this machine's
// own key cannot be read — passphrase protected, most often — the remedies are
// to strip the passphrase, to generate another key, or to hand the passphrase
// to a background process, and every one of them is a decision about how this
// machine's credentials are stored. A monitor that quietly rewrote a user's key
// would be a monitor nobody should run.
func TestFixerHandsTheVPNFixesBackToTheUser(t *testing.T) {
	t.Parallel()

	cases := []struct {
		id   diagnose.FixID
		want string
	}{
		{diagnose.FixDisableKillSwitch, msgKillSwitchManual},
		{diagnose.FixSplitTunnelSSH, msgSplitTunnelManual},
		{diagnose.FixCheckPeerOnline, msgPeerOnlineManual},
		{diagnose.FixPeerNotInTailnet, msgPeerNotInTailnetManual},
		{diagnose.FixInstallSSHServer, msgInstallSSHServerManual},
		{diagnose.FixAuthorizeKey, msgAuthorizeKeyManual},
		{diagnose.FixUsableKey, msgUsableKeyManual},
		{diagnose.FixVerifyHostKey, msgVerifyHostKeyManual},
		{diagnose.FixConfirmHostKey, msgConfirmHostKeyManual},
	}
	svc := &fakeServiceControl{}
	shell := &fakeShell{}
	f := NewFixer(svc, shell)

	for _, tc := range cases {
		out := f.Apply(context.Background(), tc.id)
		if out.OK {
			t.Errorf("%s reported success — nothing was actually done", tc.id)
		}
		if out.Message != tc.want || !strings.Contains(out.Message, "вручную") {
			t.Errorf("%s message = %q", tc.id, out.Message)
		}
	}
	if len(svc.names()) != 0 || shell.script != "" {
		t.Error("a manual fix touched a service or the peer")
	}
	assertHostKeyAdviceStaysAdvice(t, f)
	// The key advice must not turn into "we did it for you" by any route: no
	// keygen, no rewriting, and no passphrase on a command line.
	keyAdvice := f.Apply(context.Background(), diagnose.FixUsableKey).Message
	for _, forbidden := range []string{"создал", "перевыпустил", "снял пароль", "-key-passphrase"} {
		if strings.Contains(keyAdvice, forbidden) {
			t.Errorf("the key advice claims to have done something (%q): %q", forbidden, keyAdvice)
		}
	}
}

// assertHostKeyAdviceStaysAdvice holds the two host-key remedies to being
// remedies the user carries out.
//
// The one that matters most is the absence: there is no offer to accept the new
// key, and there will not be one. That button is what an attacker in the middle
// needs pressed, and the only honest check happens on the other machine, over a
// channel that is not this connection.
func assertHostKeyAdviceStaysAdvice(t *testing.T, f *Fixer) {
	t.Helper()

	advice := f.Apply(context.Background(), diagnose.FixVerifyHostKey).Message
	for _, claimed := range []string{"принял", "удалил", "добавил", "подтвердил"} {
		if strings.Contains(advice, claimed) {
			t.Errorf("the host-key advice claims to have %q: %q", claimed, advice)
		}
	}
	for _, want := range []string{"не будет", "не через это подключение", "known_hosts"} {
		if !strings.Contains(advice, want) {
			t.Errorf("the host-key advice should mention %q: %q", want, advice)
		}
	}
	// And it must not read like the milder condition: an unverified host has
	// contradicted nothing, and there is no stale line there to remove.
	if confirm := f.Apply(context.Background(), diagnose.FixConfirmHostKey).Message; confirm == advice {
		t.Error("a changed host key and an unverified one must not give the same instructions")
	}
}

func TestFixerWithoutDependenciesExplainsItself(t *testing.T) {
	t.Parallel()

	f := NewFixer(nil, nil)
	if out := f.Apply(context.Background(), diagnose.FixStartTailscale); out.Message != msgNoServiceControl {
		t.Errorf("no service control: %+v", out)
	}
	if out := f.Apply(context.Background(), diagnose.FixStartSSHD); out.Message != msgNoPeerSession {
		t.Errorf("no peer session: %+v", out)
	}
}

func TestFixerRejectsAnUnknownFix(t *testing.T) {
	t.Parallel()

	out := NewFixer(&fakeServiceControl{}, &fakeShell{}).Apply(context.Background(), "нет такого")
	if out.OK || out.Message != msgUnknownFix {
		t.Errorf("Apply = %+v", out)
	}
}

func TestPeerFixStatusTakesTheLastVerdict(t *testing.T) {
	t.Parallel()

	got, ok := peerFixStatus("LINKMON-FIX 1\nLINKMON-FIX 0\n")
	if !ok || got != peerFixStarted {
		t.Errorf("peerFixStatus = (%d, %v)", got, ok)
	}
	if _, ok := peerFixStatus("LINKMON-FIX\n"); ok {
		t.Error("a token line with no value was accepted")
	}
}

func TestQuotePowerShellLiteralRefusesRubbish(t *testing.T) {
	t.Parallel()

	if _, err := quotePowerShellLiteral(""); err == nil {
		t.Error("an empty literal was accepted")
	}
	if _, err := quotePowerShellLiteral("ss\x00hd"); err == nil {
		t.Error("a literal with a NUL was accepted")
	}
	got, err := quotePowerShellLiteral("it's")
	if err != nil || got != "'it''s'" {
		t.Errorf("quotePowerShellLiteral = (%q, %v)", got, err)
	}
	if _, err := peerStartServiceScript("bad\x01name"); err == nil {
		t.Error("a service name with a control character built a script")
	}
}

func TestFixerSurvivesAScriptItCannotBuild(t *testing.T) {
	t.Parallel()

	// The only way in is a service name this package would never pass, so the
	// guard is exercised through the helper the fixer uses.
	if _, err := peerStartServiceScript(""); err == nil {
		t.Fatal("an empty service name built a script")
	}
}
