package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/winsvc"
	"github.com/pinnnthreetriples/link-monitor/internal/core/diagnose"
)

// Service names this program acts on. Both are the Windows service keys, not
// display names, and both are identical on the two machines.
const (
	tailscaleService = "Tailscale"
	sshdService      = "sshd"
)

// LocalServiceControl is the part of the Windows service adapter that changes
// something. *winsvc.Client implements it.
type LocalServiceControl interface {
	StartService(ctx context.Context, name string) error
}

// RemoteShell runs a PowerShell script on the peer. *sshx.Client implements it:
// the script travels as -EncodedCommand, so nothing here meets a quoting rule.
type RemoteShell interface {
	RunPowerShell(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error)
}

// FixOutcome is what came of carrying out one fix. Message is Russian and is
// shown to the user verbatim.
//
// NeedsAdmin means elevation of *this* process would have helped. A peer that
// refuses for lack of rights is a different problem — relaunching here would
// change nothing — so that case explains itself in Message and leaves NeedsAdmin
// false.
type FixOutcome struct {
	OK         bool
	Message    string
	NeedsAdmin bool
}

// Fixer carries out the repairs core/diagnose only suggests.
//
// Three of them it deliberately does not carry out. The AmneziaVPN kill switch
// and its split-tunnel setting belong to somebody else's VPN: reaching into
// another program's security configuration behind the user's back is not a fix,
// it is a surprise. Authorizing this machine's key on the other one is the same
// thing one step further — it needs elevation on the far side and it decides who
// may log in there, so a monitor that did it would be granting itself access.
// All three return instructions instead, and say plainly that this one is the
// user's to do.
type Fixer struct {
	local LocalServiceControl
	peer  RemoteShell
}

// NewFixer builds a fixer. Either dependency may be nil; the fixes that needed
// it then explain why they cannot run instead of panicking.
func NewFixer(local LocalServiceControl, peer RemoteShell) *Fixer {
	return &Fixer{local: local, peer: peer}
}

// adviceOnly is every fix this program refuses to carry out, mapped onto what
// it says instead.
//
// They are a table rather than a switch because being on it is the thing they
// have in common: each one is a decision that belongs to the person at the
// keyboard — somebody else's VPN, another machine's authorized-keys file, this
// machine's own credentials, a Windows component that installs a server, or the
// question of which machine a host key belongs to — and Apply does the same
// thing for all of them, which is to explain and stop. A new entry here is a
// deliberate refusal, and it costs one line; a new action costs a branch below.
var adviceOnly = map[diagnose.FixID]string{
	diagnose.FixDisableKillSwitch: msgKillSwitchManual,
	diagnose.FixSplitTunnelSSH:    msgSplitTunnelManual,
	diagnose.FixCheckPeerOnline:   msgPeerOnlineManual,
	diagnose.FixPeerNotInTailnet:  msgPeerNotInTailnetManual,
	diagnose.FixInstallSSHServer:  msgInstallSSHServerManual,
	diagnose.FixAuthorizeKey:      msgAuthorizeKeyManual,
	diagnose.FixUsableKey:         msgUsableKeyManual,
	diagnose.FixVerifyHostKey:     msgVerifyHostKeyManual,
	diagnose.FixConfirmHostKey:    msgConfirmHostKeyManual,
}

// Apply carries out one fix and reports what happened, in Russian. It returns
// no error: every outcome, success or not, is something the user must read, and
// a Go error string is not that.
func (f *Fixer) Apply(ctx context.Context, id diagnose.FixID) FixOutcome {
	if msg, advice := adviceOnly[id]; advice {
		return FixOutcome{Message: msg}
	}
	switch id {
	case diagnose.FixStartTailscale:
		return f.startLocal(ctx, tailscaleService, msgTailscaleStarted)
	case diagnose.FixStartLocalSSHD:
		return f.startLocal(ctx, sshdService, msgLocalSSHDStarted)
	case diagnose.FixStartSSHD:
		return f.startPeerSSHD(ctx)
	default:
		return FixOutcome{Message: msgUnknownFix}
	}
}

// startLocal starts a service on this machine and translates the outcome.
func (f *Fixer) startLocal(ctx context.Context, name, done string) FixOutcome {
	if f.local == nil {
		return FixOutcome{Message: msgNoServiceControl}
	}
	err := f.local.StartService(ctx, name)
	switch {
	case err == nil:
		return FixOutcome{OK: true, Message: done}
	case errors.Is(err, winsvc.ErrAccessDenied):
		return FixOutcome{Message: fmt.Sprintf(msgNeedsAdminFmt, name), NeedsAdmin: true}
	case errors.Is(err, winsvc.ErrNotInstalled):
		return FixOutcome{Message: fmt.Sprintf(msgNotInstalledFmt, name)}
	case errors.Is(err, winsvc.ErrUnsupported):
		return FixOutcome{Message: msgUnsupported}
	case errors.Is(err, context.Canceled):
		return FixOutcome{Message: msgCancelled}
	case errors.Is(err, context.DeadlineExceeded):
		return FixOutcome{Message: msgTimedOut}
	default:
		return FixOutcome{Message: fmt.Sprintf(msgStartFailedFmt, name)}
	}
}

// Statuses the peer script prints. They are numbers rather than words because
// the peer runs a Russian Windows and every message it would otherwise echo is
// translated.
const (
	peerFixStarted      = 0
	peerFixFailed       = 1
	peerFixDenied       = 2
	peerFixNotInstalled = 3
)

// peerFixToken prefixes the one line the peer script prints.
const peerFixToken = "LINKMON-FIX"

// startPeerSSHD starts the OpenSSH server on the peer over the SSH session we
// still have — which is the case that matters: outbound works, inbound does not.
func (f *Fixer) startPeerSSHD(ctx context.Context) FixOutcome {
	if f.peer == nil {
		return FixOutcome{Message: msgNoPeerSession}
	}
	script, err := peerStartServiceScript(sshdService)
	if err != nil {
		return FixOutcome{Message: msgUnknownFix}
	}

	// The peer's verdict wins over its exit code and its stderr: a verdict on the
	// stream is an answer however the shell felt about the run, and when no
	// verdict arrives the outcome is "unknown" whatever the exit code said.
	stdout, _, _, err := f.peer.RunPowerShell(ctx, script)
	switch {
	case errors.Is(err, context.Canceled):
		return FixOutcome{Message: msgCancelled}
	case errors.Is(err, context.DeadlineExceeded):
		return FixOutcome{Message: msgTimedOut}
	case err != nil:
		return FixOutcome{Message: msgPeerUnreachable}
	}

	status, ok := peerFixStatus(stdout)
	if !ok {
		return FixOutcome{Message: msgPeerNoAnswer}
	}
	switch status {
	case peerFixStarted:
		return FixOutcome{OK: true, Message: msgPeerSSHDStarted}
	case peerFixDenied:
		return FixOutcome{Message: msgPeerDenied}
	case peerFixNotInstalled:
		return FixOutcome{Message: msgPeerNotInstalled}
	default:
		return FixOutcome{Message: msgPeerStartFailed}
	}
}

// peerFixStatus reads the token line out of the peer's stdout. The whole stream
// is not trusted: PowerShell started from cmd.exe can prepend a CLIXML blob on a
// cold run, so the line is looked for rather than the output parsed.
func peerFixStatus(stdout string) (int, bool) {
	status, found := 0, false
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || fields[0] != peerFixToken {
			continue
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		// The last verdict wins: nothing after it can be a later answer.
		status, found = n, true
	}
	return status, found
}

// peerStartServiceScript builds the PowerShell the peer runs. It sets the
// service to start with Windows too, because a service that has to be started
// by hand after every reboot is not a fix.
func peerStartServiceScript(name string) (string, error) {
	quoted, err := quotePowerShellLiteral(name)
	if err != nil {
		return "", err
	}
	say := func(code int) string {
		return "Write-Output '" + peerFixToken + " " + strconv.Itoa(code) + "'"
	}
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$n = " + quoted,
		"if ($null -eq (Get-Service -Name $n -ErrorAction SilentlyContinue)) {",
		"  " + say(peerFixNotInstalled) + "; exit 0",
		"}",
		"try { Set-Service -Name $n -StartupType Automatic } catch { }",
		"try {",
		"  Start-Service -Name $n",
		"  " + say(peerFixStarted),
		"} catch {",
		"  $inner = $_.Exception.InnerException",
		"  if ($inner -is [System.ComponentModel.Win32Exception] -and $inner.NativeErrorCode -eq 5) {",
		"    " + say(peerFixDenied),
		"  } else {",
		"    " + say(peerFixFailed),
		"  }",
		"}",
	}, "\n"), nil
}

// quotePowerShellLiteral renders s as a PowerShell single-quoted string, in
// which the only special character is the quote itself, doubled. Control
// characters are refused rather than escaped: no Windows service is named with
// one, so their presence means the caller passed something it should not have.
func quotePowerShellLiteral(s string) (string, error) {
	if s == "" {
		return "", errors.New("app: empty PowerShell literal")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("app: refusing a PowerShell literal with a control character (%U)", r)
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}
