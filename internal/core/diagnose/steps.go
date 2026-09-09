package diagnose

import (
	"context"
	"errors"
	"fmt"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// tailscale fills the first row and reports whether the rest is worth probing.
func (d *diagnosis) tailscale(ctx context.Context, p core.Probe) bool {
	up, note, err := p.TailscaleUp(ctx)
	if err != nil {
		wrapped := fmt.Errorf("asking the local Tailscale daemon for its state: %w", err)
		d.set(core.CheckTailscale, core.StateUnknown, unknownNote(wrapped))
		d.pending(noteNoTailscaleAnswer, afterTailscale...)
		d.summary, d.detail = summaryUnknown, "Состояние Tailscale выяснить не удалось"
		return false
	}
	if !up {
		// The probe's own note says *how* it is not connected — stopped, logged
		// out, waiting for device approval, held by another Windows user — and
		// those lead the user to different places. It used to be dropped on the
		// floor here in favour of one sentence for all of them.
		d.set(core.CheckTailscale, core.StateFail, daemonDownNote(note))
		d.pending(noteWaitsForTailscale, afterTailscale...)
		d.summary, d.detail = summaryDown, detailNoTS
		d.fixes = append(d.fixes, fixStartTailscale())
		return false
	}
	d.set(core.CheckTailscale, core.StateOK, daemonNote(note))
	return true
}

// tunnel round-trips the peer through Tailscale itself. An answer here is what
// later lets us blame a local filter instead of the peer.
//
// It is also the step that settles the machine card, because it is the only
// party that actually asks the peer anything: an answer here is the one piece
// of evidence that licenses telling the user the machine is there, and silence
// here outranks the control plane's «connected» flag. See peer.go.
func (d *diagnosis) tunnel(ctx context.Context, p core.Probe) bool {
	latency, err := p.PeerReachable(ctx, d.peer.Addr)
	if err == nil {
		d.latency = latency
		d.peerAnswered()
		return true
	}
	wrapped := fmt.Errorf("round-tripping %s through the tunnel: %w", d.peer.Addr, err)

	var timedOut *core.TimeoutError
	if !errors.As(wrapped, &timedOut) {
		// One failure, not four. The row about reaching the peer carries what
		// the system said; the three that depend on it say they were not
		// checked, so the reader sees one cause instead of the same foreign
		// sentence repeated down the dashboard.
		//
		// The peer's state stays unknown here, and this is the one no-answer
		// branch where the local dial is not made either. Nothing failed to
		// answer: our own ping probe did not work, so there is no finding about
		// the peer to report and no sentence for the machine card to borrow.
		// The row that owns the failure is this one, and it carries the words
		// the system used.
		d.set(core.CheckSSHOut, core.StateUnknown, unknownNote(wrapped))
		d.pending(noteNoTunnelAnswer, core.CheckSSHIn, core.CheckSSHD, core.CheckKillSwitch)
		d.summary = summaryUnknown
		d.detail = "Дотянуться до второй машины не удалось — проверка не завершилась"
		return false
	}
	d.peerWentSilent()
	d.set(core.CheckSSHOut, core.StateFail, fmt.Sprintf("%s не отвечает в тоннеле", name(d.peer)))
	d.set(core.CheckSSHIn, core.StateFail, "Встречное подключение тоже не пройдёт: машина молчит")
	d.pending(noteNoAnswerFromPeer, core.CheckSSHD)
	d.summary = summaryDown
	d.detail = "Вторая машина не отвечает — похоже, она выключена или осталась без сети"
	d.fixes = append(d.fixes, fixCheckPeerOnline(d.peer))

	// The peer's own sshd needs a login to a machine that is not answering, so
	// that row stays unchecked. The kill switch does not: whether a filter on
	// this machine refuses a socket is answered by this machine, before a packet
	// leaves it, and an unreachable peer is no reason to stop saying what can
	// still be known here.
	d.localFilter(ctx, p)
	return false
}

// transport dials port 22 the way an application would. Whether that dial is
// refused locally or merely goes unanswered is the whole point of this program.
func (d *diagnosis) transport(ctx context.Context, p core.Probe) {
	err := p.TCPReachable(ctx, d.peer.Addr, sshPort)
	if err == nil {
		d.linkIsUp(ctx, p)
		return
	}
	wrapped := fmt.Errorf("dialing %s:%d: %w", d.peer.Addr, sshPort, err)

	var blocked *core.BlockedError
	if errors.As(wrapped, &blocked) {
		d.blockedLocally(ctx, p, blocked)
		return
	}
	// A reset is asked about before a timeout because it says more: the peer's
	// stack answered, so the machine is up and nothing is listening on 22.
	// Until *core.PortClosedError existed this — the commonest SSH failure
	// there is — matched nothing and fell through to «результат неизвестен».
	var closed *core.PortClosedError
	if errors.As(wrapped, &closed) {
		d.peerPortDown(ctx, p, fmt.Sprintf(
			"Порт %d закрыт: %s ответил отказом — на той стороне никто не слушает",
			sshPort, name(d.peer)))
		return
	}
	var timedOut *core.TimeoutError
	if errors.As(wrapped, &timedOut) {
		d.peerPortDown(ctx, p, fmt.Sprintf("Порт %d не отвечает: пакет ушёл, ответа нет", sshPort))
		return
	}
	// As in tunnel(): the row the dial belongs to carries the system's words,
	// and the rows that only waited on it say so.
	d.set(core.CheckSSHOut, core.StateUnknown, unknownNote(wrapped))
	d.pending(noteNoPortAnswer, core.CheckSSHIn, core.CheckSSHD, core.CheckKillSwitch)
	d.summary = summaryUnknown
	d.detail = "Проверка порта не завершилась — связь не подтверждена"
}

// linkIsUp records a socket that opened: nothing local is filtering and the peer
// answers. The other direction proves nothing about itself here — inbound is
// established separately, on its own evidence.
func (d *diagnosis) linkIsUp(ctx context.Context, p core.Probe) {
	d.set(core.CheckKillSwitch, core.StateOK, "Локальные фильтры трафику не мешают")
	d.set(core.CheckSSHOut, core.StateOK, fmt.Sprintf("Порт %d отвечает", sshPort))
	d.summary, d.detail = summaryOK, detailOK

	// An open port is not a working login, and asking the peer about its own
	// service is the first thing here that needs one. When no session can be
	// had — the peer refused our key, or our own key would not load — the
	// outbound row's «Порт 22 отвечает» is an overstatement and blame replaces
	// it with the cause.
	if failure := d.sshd(ctx, p, true); failure != nil {
		d.inbound(ctx, p, false, d.blame(failure))
		return
	}
	// Inbound speaks last: it is the direction we cannot infer, so when it is
	// broken its verdict owns the headline.
	d.inbound(ctx, p, true, "")
}

// keyRefused is the second finding this program exists for, after the packet
// filter. The tunnel is up, the port answers, the peer speaks SSH — and then it
// declines the key this machine offered. That is not a check we failed to run:
// it is definite, it has a name, and it has a remedy, so it is reported as a
// failure of the outbound direction rather than as an unanswered probe.
//
// It owns the headline because nothing else found on such a run outranks it:
// every layer below works, and the only thing wrong is a decision the far side
// made about this machine. The remedy is offered as advice and never carried
// out — writing into another machine's authorized-keys file needs elevation
// over there and is the user's security decision, exactly like the AmneziaVPN
// kill switch this program refuses to touch.
func (d *diagnosis) keyRefused(refused *core.AuthRefusedError) {
	d.set(core.CheckSSHOut, core.StateFail, fmt.Sprintf(
		"%s не принял наш ключ: вход под «%s» отклонён", name(d.peer), refused.User))
	d.summary = summaryRefused
	d.detail = fmt.Sprintf(
		"%s отклоняет вход по ключу: сеть и порт в порядке, ключа нет в списке разрешённых",
		name(d.peer))
	d.fixes = append(d.fixes, fixAuthorizeKey(d.local, d.peer))
}

// blockedLocally is the finding this program exists for. The daemon reaches the
// peer, yet an ordinary socket is refused before it ever leaves the machine
// (WSAEACCES 10013). That is a local packet filter — on these machines,
// AmneziaVPN's kill switch, whose «Block Internet» rule denies 100.64.0.0/10 —
// and the peer is innocent. Say so plainly, and do not blame the far side.
func (d *diagnosis) blockedLocally(ctx context.Context, p core.Probe, blocked *core.BlockedError) {
	d.set(core.CheckKillSwitch, core.StateFail, fmt.Sprintf(
		"Локальный фильтр отказал сокету к %s:%d (WSAEACCES 10013)", blocked.Addr, blocked.Port))
	d.set(core.CheckSSHOut, core.StateFail,
		"Соединение не выходит с этой машины: его режет локальный фильтр, а не рабочий ПК")
	d.pending(noteBlockedSocket, core.CheckSSHD)
	d.summary = summaryBlocked
	d.detail = "Kill switch AmneziaVPN режет диапазон Tailscale 100.64.0.0/10: " +
		"демон до машины дотягивается, а обычные программы — нет"
	d.fixes = append(d.fixes, fixDisableKillSwitch(), fixSplitTunnelSSH())
	// Our own SSH server is still knowable: the filter does not stand between
	// us and the local service manager.
	d.inbound(ctx, p, false, noteBlockedSocket)
}

// peerPortDown records a dial that left this machine and got nowhere: the
// packet got out, so no local filter is involved, and the peer's port is at
// fault. outboundNote says which way — silence, or an outright refusal — which
// is the one thing the two callers disagree about.
func (d *diagnosis) peerPortDown(ctx context.Context, p core.Probe, outboundNote string) {
	d.set(core.CheckKillSwitch, core.StateOK, "Пакеты уходят с машины — локальные фильтры ни при чём")
	d.set(core.CheckSSHOut, core.StateWarn, outboundNote)
	d.summary, d.detail = summaryPartial, detailNoSSHD

	// sshd goes first here, as it does in linkIsUp, so that a failed session is
	// named before the inbound row has to explain why it was not checked.
	if failure := d.sshd(ctx, p, false); failure != nil {
		d.inbound(ctx, p, false, d.blame(failure))
		return
	}
	d.inbound(ctx, p, false, noteNoSession)
}

// sshd asks the peer about its OpenSSH service. portAnswered says whether the
// TCP dial got through, which is what makes a contradiction between the two
// visible instead of silently resolved in favour of one of them.
//
// Asking needs a login, so this is also where a failure to get a session
// surfaces first. It is returned rather than handled: the failure says little
// about the service and everything about the outbound direction, and the caller
// owns that row.
func (d *diagnosis) sshd(ctx context.Context, p core.Probe, portAnswered bool) *sessionFailure {
	running, err := p.ServiceRunning(ctx, sshdService)
	if err != nil {
		wrapped := fmt.Errorf("asking %s about the %q service: %w", d.peer.Addr, sshdService, err)
		if failure := sessionCause(wrapped); failure != nil {
			state, note := failure.sshdRow()
			d.set(core.CheckSSHD, state, note)
			return failure
		}
		d.set(core.CheckSSHD, core.StateUnknown, unknownNote(wrapped))
		// The headline keeps whatever the transport step decided, unless that
		// was «Связь установлена» — it must not go on claiming an established
		// link while a row it depends on has no answer.
		d.unsureAbout("Состояние службы sshd выяснить не удалось")
		return nil
	}
	switch {
	case running && portAnswered:
		d.set(core.CheckSSHD, core.StateOK, "Служба sshd запущена и слушает порт")
	case running:
		d.set(core.CheckSSHD, core.StateWarn,
			"Служба запущена, но порт молчит — похоже на брандмауэр на той стороне")
		d.detail = "Служба sshd работает, а подключение не проходит — проверьте брандмауэр рабочего ПК"
	case portAnswered:
		d.set(core.CheckSSHD, core.StateWarn, "Служба числится остановленной, хотя порт 22 отвечает")
		d.summary = summaryPartial
		d.detail = "Порт 22 отвечает, но служба sshd числится остановленной"
	default:
		d.set(core.CheckSSHD, core.StateWarn, "Служба sshd на той машине не запущена")
		d.fixes = append(d.fixes, fixStartSSHD(d.peer))
	}
	return nil
}
