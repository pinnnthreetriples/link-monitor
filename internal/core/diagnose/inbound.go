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
			d.unsureAbout("Состояние своего SSH-сервера выяснить не удалось")
		}
	case !running:
		detail := d.localServerDown(ctx, p)
		if outboundOK {
			d.summary = summaryPartial
			d.detail = detail
		}
	case !outboundOK:
		d.set(core.CheckSSHIn, core.StateUnknown, blockedNote)
	default:
		d.dialBack(ctx, p)
	}
}

// localServerDown fills the inbound row for an SSH server of our own that is
// not serving, and tells the two reasons for that apart.
//
// «Not installed» and «stopped» are different problems with different remedies
// — one wants a Windows optional feature added, the other wants a service
// started — and the service manager answers "not running" to both, because a
// service that does not exist is certainly not running. This laptop was in the
// first state for hours while the row read as the second, which sends the user
// to services.msc to start something that is not there.
//
// It returns the sentence for the headline rather than writing it, because
// whether this row owns the headline is the caller's to decide: a peer that is
// switched off owns it instead.
func (d *diagnosis) localServerDown(ctx context.Context, p core.Probe) string {
	installed, err := p.LocalServiceInstalled(ctx, sshdService)
	switch {
	case err == nil && !installed:
		d.set(core.CheckSSHIn, core.StateFail, noteLocalServerAbsent)
		d.fixes = append(d.fixes, fixInstallSSHServer())
		return detailNoLocalServer
	case err == nil:
		d.set(core.CheckSSHIn, core.StateFail, noteLocalServerStopped)
		d.fixes = append(d.fixes, fixStartLocalSSHD())
		return detailLocalServerStopped
	}
	// It is not running — that much is already established and settles the
	// direction. Which of the two remedies applies is what went unanswered, so
	// the row says the weaker true thing and offers the smaller action; the
	// error is not quoted, because this row is a proven failure and not an
	// unanswered question.
	d.set(core.CheckSSHIn, core.StateFail, noteLocalServerDown)
	d.fixes = append(d.fixes, fixStartLocalSSHD())
	return detailLocalServerDown
}

// dialBack asks the peer to open a connection back to us — the only direct
// evidence about the inbound direction this program can gather.
//
// It proves a network path and a listening socket, and it must not be written up
// as proving a login. It used to say «направление проверено», which claimed one:
// on these two machines that row was green while the work PC could provably not
// log in, its key missing from this laptop's administrators_authorized_keys. The
// wording below says only what a TCP connect establishes.
//
// Making it prove a login was considered and rejected, three ways:
//
//   - The peer would have to sign with the peer's own private key. This program
//     must not read another machine's key material, and does not know where it
//     is; that rule is not negotiable for a probe's convenience.
//   - A direct-tcpip channel over the session we already hold does put a
//     genuine connection on the wire from the peer's own stack, and the client
//     handshake could then run in this process over it. But the credential
//     would be ours, and our key is not necessarily in our own
//     authorized_keys — on these machines it is not, it authorises us on the
//     work PC. A refusal would then mean "we cannot log into ourselves" while
//     the row said "the peer cannot log in", which is a new lie of exactly the
//     kind this comment exists to record the removal of.
//   - Driving ssh.exe on the peer is what this adapter exists not to do, and
//     the peer's ssh-agent answering "agent refused operation" before the
//     server ever declined the key is a live example of why.
//
// So the check stays a TCP connect and the row stops overstating it. When the
// user needs the login proven, the honest answer is that they must try it.
func (d *diagnosis) dialBack(ctx context.Context, p core.Probe) {
	err := p.PeerCanReachUs(ctx, d.local.Addr, sshPort)
	if err == nil {
		d.set(core.CheckSSHIn, core.StateOK, fmt.Sprintf(
			"%s открыл соединение к нашему порту %d — путь до нас есть; сам вход не проверялся",
			name(d.peer), sshPort))
		return
	}
	wrapped := fmt.Errorf("asking %s to dial back to %s:%d: %w", d.peer.Addr, d.local.Addr, sshPort, err)

	if failure := sessionCause(wrapped); failure != nil {
		// There was no session to ask for a call back: the peer declined our
		// key, or our own key would not load. Either way that is a definite
		// answer about the outbound direction and no answer at all about this
		// one, so it is reported where it belongs and this row says plainly
		// that it went unchecked.
		d.set(core.CheckSSHIn, core.StateUnknown, d.blame(failure))
		return
	}
	var closed *core.PortClosedError
	if errors.As(wrapped, &closed) {
		// Our own stack refused the peer's connect. inbound() has already
		// established that our sshd service is running, so the two facts
		// disagree — the service is up and nothing is listening where the peer
		// knocked — and saying so is more use than «нет ответа».
		d.set(core.CheckSSHIn, core.StateWarn, fmt.Sprintf(
			"Обратное подключение отклонено: на порту %d этой машины никто не слушает", sshPort))
		d.summary = summaryPartial
		d.detail = "Отсюда связь есть, а обратно нет: служба sshd запущена, но подключение к " +
			"нашему порту отклоняется — проверьте, на каком адресе она слушает"
		return
	}
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
	// Neither typed error, so the probe itself did not finish. This row goes
	// Unknown and drags the verdict to unknown with it, so the headline has to
	// come down too — this branch used to set only the detail, leaving «Связь
	// установлена» standing over a verdict of «unknown».
	d.set(core.CheckSSHIn, core.StateUnknown, unknownNote(wrapped))
	d.unsureAbout("Обратное подключение проверить не удалось — судить о нём нельзя")
}
