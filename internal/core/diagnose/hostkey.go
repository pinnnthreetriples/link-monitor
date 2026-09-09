package diagnose

import (
	"fmt"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// This file holds the one finding in this program that is not a fault of the
// link, and it is kept apart from the others for that reason.
//
// The packet filter, the closed port, the refused key and the key we cannot
// read are all answers to "why does the connection not work". A host key that
// does not match is an answer to a question nobody asks until it matters: is
// the machine answering on that address the machine we mean? Every layer below
// may be in perfect health and the answer still be no.
//
// So it gets a verdict of its own, it shares a family with nothing, and it is
// never repaired. There is no «доверять всё равно» button and there will not
// be: that button is the one an attacker in the middle needs pressed, and this
// program already refuses to touch the AmneziaVPN kill switch and the peer's
// authorized-keys file on the same principle — a security decision belongs to
// the person at the keyboard, who is the only one who can check it out of band.
//
// The milder relative is kept apart from it too. A host absent from known_hosts
// with trust-on-first-use off is *unknown*; a host whose key changed is a
// *mismatch*. Reading them as one sentence would be the exact dishonesty this
// engine is built against, so they are two conditions with two verdicts.

// hostKeyChanged records a host that offered a key other than the recorded one.
//
// The outbound row fails because that direction really does not work — we
// refuse to speak to it — but the row and the headline are about identity, not
// about the network, and neither blames the peer for anything. What they say is
// what is true: the key does not match, so which machine answered is not
// established, and only the machine itself can settle it.
func (d *diagnosis) hostKeyChanged(m *core.HostKeyMismatchError) {
	d.set(core.CheckSSHOut, core.StateFail, fmt.Sprintf(
		"Ключ хоста %s не совпал с записанным — подключение прервано", name(d.peer)))
	d.summary = summaryHostKeyChanged
	d.detail = "Машина по этому адресу предъявила не тот ключ хоста, что записан у нас: " +
		"возможно, это не та машина. Так бывает после переустановки системы или " +
		"замены машины — но убедиться в этом можно только на самой машине."
	d.fixes = append(d.fixes, fixVerifyHostKey(d.peer, m.Fingerprint))
}

// hostUnverified records a host we have never seen, in a configuration that
// forbids accepting one on sight.
//
// Nothing is wrong and nothing is proven, and the sentence says both. It is
// deliberately not the mismatch's sentence: an unverified host has contradicted
// nothing, and telling the user their machine may have been replaced when it
// has merely never been checked would be a scare with no evidence behind it.
func (d *diagnosis) hostUnverified(u *core.HostKeyUnknownError) {
	d.set(core.CheckSSHOut, core.StateFail, fmt.Sprintf(
		"Ключ хоста %s ещё не подтверждён — подключение не состоялось", name(d.peer)))
	d.summary = summaryHostUnverified
	d.detail = "Это не сбой связи и не смена ключа: подходящего ключа этой машины " +
		"у нас в known_hosts нет, а принимать непроверенный ключ программе запрещено."
	d.fixes = append(d.fixes, fixConfirmHostKey(d.peer, u.Fingerprint))
}
