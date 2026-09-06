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
		d.set(core.CheckTailscale, core.StateFail, "Демон Tailscale не подключён к тайлнету")
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
func (d *diagnosis) tunnel(ctx context.Context, p core.Probe) bool {
	latency, err := p.PeerReachable(ctx, d.peer.Addr)
	if err == nil {
		d.latency = latency
		return true
	}
	wrapped := fmt.Errorf("round-tripping %s through the tunnel: %w", d.peer.Addr, err)

	var timedOut *core.TimeoutError
	if !errors.As(wrapped, &timedOut) {
		d.pending(unknownNote(wrapped), afterTailscale...)
		d.summary = summaryUnknown
		d.detail = "Дотянуться до второй машины не удалось — проверка не завершилась"
		return false
	}
	d.set(core.CheckSSHOut, core.StateFail, fmt.Sprintf("%s не отвечает в тоннеле", name(d.peer)))
	d.set(core.CheckSSHIn, core.StateFail, "Встречное подключение тоже не пройдёт: машина молчит")
	d.pending(noteNoAnswerFromPeer, core.CheckSSHD, core.CheckKillSwitch)
	d.summary = summaryDown
	d.detail = "Вторая машина не отвечает — похоже, она выключена или осталась без сети"
	d.fixes = append(d.fixes, fixCheckPeerOnline(d.peer))
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
	var timedOut *core.TimeoutError
	if errors.As(wrapped, &timedOut) {
		d.noAnswerOnPort(ctx, p)
		return
	}
	d.pending(unknownNote(wrapped), afterTailscale...)
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
	d.sshd(ctx, p, true)
	// Inbound speaks last: it is the direction we cannot infer, so when it is
	// broken its verdict owns the headline.
	d.inbound(ctx, p, true, "")
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

// noAnswerOnPort records a dial that left the machine and died of silence: the
// packet got out, so no local filter is involved, and the peer is at fault.
func (d *diagnosis) noAnswerOnPort(ctx context.Context, p core.Probe) {
	d.set(core.CheckKillSwitch, core.StateOK, "Пакеты уходят с машины — локальные фильтры ни при чём")
	d.set(core.CheckSSHOut, core.StateWarn,
		fmt.Sprintf("Порт %d не отвечает: пакет ушёл, ответа нет", sshPort))
	d.summary, d.detail = summaryPartial, detailNoSSHD
	d.inbound(ctx, p, false, "Не проверялось: нет рабочего сеанса, чтобы позвонить нам в ответ")
	d.sshd(ctx, p, false)
}

// sshd asks the peer about its OpenSSH service. portAnswered says whether the
// TCP dial got through, which is what makes a contradiction between the two
// visible instead of silently resolved in favour of one of them.
func (d *diagnosis) sshd(ctx context.Context, p core.Probe, portAnswered bool) {
	running, err := p.ServiceRunning(ctx, sshdService)
	if err != nil {
		wrapped := fmt.Errorf("asking %s about the %q service: %w", d.peer.Addr, sshdService, err)
		d.set(core.CheckSSHD, core.StateUnknown, unknownNote(wrapped))
		// The headline keeps whatever the transport step decided, but it must
		// not go on implying we know how the far side is doing.
		d.detail = "Состояние службы sshd выяснить не удалось"
		return
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
}
