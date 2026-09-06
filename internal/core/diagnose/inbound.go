package diagnose

import (
	"context"
	"errors"
	"fmt"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// inbound decides the «peer → this machine» row from evidence only.
//
// It is never settled by inference from the outbound direction: on these two
// machines outbound worked for hours while inbound was impossible, because this
// laptop had no OpenSSH *Server* installed at all. Saying «тоннель открыт в обе
// стороны» in that state is worse than admitting the direction was not checked.
//
// The order is proof first: a stopped local service closes the question without
// asking anyone. Only then do we make the peer dial back, which needs a working
// outbound session — outboundOK says whether we have one, and blockedNote
// explains, in the user's words, why we do not.
func (d *diagnosis) inbound(ctx context.Context, p core.Probe, outboundOK bool, blockedNote string) {
	running, err := p.LocalServiceRunning(ctx, sshdService)
	switch {
	case err != nil:
		wrapped := fmt.Errorf("asking this machine about the %q service: %w", sshdService, err)
		d.set(core.CheckSSHIn, core.StateUnknown, unknownNote(wrapped))
		if outboundOK {
			d.detail = "Состояние своего SSH-сервера выяснить не удалось"
		}
	case !running:
		d.set(core.CheckSSHIn, core.StateFail,
			"SSH-сервер на этой машине не запущен — подключиться к нам не сможет никто")
		d.fixes = append(d.fixes, fixStartLocalSSHD())
		if outboundOK {
			d.summary = summaryPartial
			d.detail = "Отсюда подключиться можно, а к нам — нет: не запущен свой SSH-сервер"
		}
	case !outboundOK:
		d.set(core.CheckSSHIn, core.StateUnknown, blockedNote)
	default:
		d.dialBack(ctx, p)
	}
}

// dialBack asks the peer to open a connection back to us — the only direct
// evidence that the inbound direction works.
func (d *diagnosis) dialBack(ctx context.Context, p core.Probe) {
	err := p.PeerCanReachUs(ctx, d.local.Addr, sshPort)
	if err == nil {
		d.set(core.CheckSSHIn, core.StateOK,
			fmt.Sprintf("%s дозвонился до нас в ответ — направление проверено", name(d.peer)))
		return
	}
	wrapped := fmt.Errorf("asking %s to dial back to %s:%d: %w", d.peer.Addr, d.local.Addr, sshPort, err)

	var blocked *core.BlockedError
	if errors.As(wrapped, &blocked) {
		d.set(core.CheckSSHIn, core.StateFail, fmt.Sprintf(
			"Обратное подключение к %s:%d отбил пакетный фильтр (WSAEACCES 10013)", blocked.Addr, blocked.Port))
		d.summary = summaryPartial
		d.detail = "Отсюда связь есть, а обратно нет: подключение к нам режет пакетный фильтр"
		return
	}
	var timedOut *core.TimeoutError
	if errors.As(wrapped, &timedOut) {
		d.set(core.CheckSSHIn, core.StateWarn,
			fmt.Sprintf("Обратное подключение ушло в никуда: порт %d на этой машине не ответил", sshPort))
		d.summary = summaryPartial
		d.detail = "Отсюда связь есть, а к нам не достучаться — проверьте брандмауэр этой машины"
		return
	}
	d.set(core.CheckSSHIn, core.StateUnknown, unknownNote(wrapped))
	d.detail = "Обратное подключение проверить не удалось"
}
