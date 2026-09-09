package main

import (
	"errors"
	"fmt"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
	"github.com/pinnnthreetriples/link-monitor/internal/ui"
)

// Every string in this block is read by the user and is therefore in Russian.
//
// The verb runs in a process with no console and no window of its own, so a
// notification is the only thing the user will ever see of it. That is why
// there is one for every outcome including success: a file transfer that says
// nothing is indistinguishable from a menu item that does nothing.
const (
	sendOKTitle   = "Файл отправлен"
	sendOKBodyFmt = "«%s» — на втором компьютере."

	sendUnreachableTitle   = "Второй компьютер недоступен"
	sendUnreachableBodyFmt = "«%s» отправить не удалось: второй компьютер не отвечает. " +
		"Проверь, включён ли он и работает ли на нём Tailscale, и попробуй ещё раз."

	sendFailedTitle   = "Файл не отправлен"
	sendFailedBodyFmt = "«%s» отправить не удалось. Открой Link Monitor: на вкладке «Связь» " +
		"написано, что со связью, а на вкладке «Файлы» — что стало с передачей."

	sendStartedTitle   = "Отправляю файл"
	sendStartedBodyFmt = "«%s». Это может занять время — сообщу, когда закончу."

	sendNoFileTitle   = "Файл не отправлен"
	sendNoFileBodyFmt = "«%s» — такого файла нет, или это папка. " +
		"Пункт «Отправить на ПК» отправляет один файл за раз."
)

// sendOutcome is what became of one transfer, as the user needs to hear it.
type sendOutcome int

const (
	// sendDone is the file on the other machine.
	sendDone sendOutcome = iota
	// sendUnreachable is the peer not answering — the commonest failure by
	// far, and the one the user can do something about.
	sendUnreachable
	// sendFailed is everything else: a Tailscale daemon that is not running, a
	// file that could not be read, an API that answered badly.
	sendFailed
)

// classify decides which of the three outcomes a transfer had.
//
// It is a function of two facts rather than one because the error alone does
// not say enough. Taildrop reports the same failure whether the peer is off or
// the local daemon is confused, and the API — which the hand-off path talks
// to — deliberately answers every send failure with one Russian sentence and no
// detail, so there is nothing there to read either. Asking Tailscale whether
// the peer answers a ping is what separates "it is switched off" from "this
// machine has a problem", and those two need different words.
//
// peerReachable is only consulted for a failure. A transfer that worked
// obviously reached the peer, and pinging afterwards would be one more thing to
// go wrong between the file arriving and the user being told about it.
func classify(sendErr error, peerReachable bool) sendOutcome {
	switch {
	case sendErr == nil:
		return sendDone
	case errors.Is(sendErr, tailscale.ErrPeerUnreachable),
		errors.Is(sendErr, tailscale.ErrPeerNotFound):
		// The adapter already established this, so the ping adds nothing.
		return sendUnreachable
	case !peerReachable:
		return sendUnreachable
	default:
		return sendFailed
	}
}

// noticeFor is the notification one outcome earns. name is the file's base
// name — never its full path, which would put the user's folder structure in
// the Action Centre, and never its contents.
func noticeFor(outcome sendOutcome, name string) ui.Notification {
	switch outcome {
	case sendDone:
		return ui.Notification{Title: sendOKTitle, Body: fmt.Sprintf(sendOKBodyFmt, name)}
	case sendUnreachable:
		return ui.Notification{
			Title: sendUnreachableTitle,
			Body:  fmt.Sprintf(sendUnreachableBodyFmt, name),
		}
	default:
		return ui.Notification{Title: sendFailedTitle, Body: fmt.Sprintf(sendFailedBodyFmt, name)}
	}
}

// startedNotice is what a large file gets before the transfer begins.
//
// Taildrop reports no progress of its own — see app.Transfer.Pct — so there is
// no percentage to show from here. What can be promised is that something is
// happening and that the answer is coming, which is the difference between a
// program that is working and a program that appears frozen.
func startedNotice(name string) ui.Notification {
	return ui.Notification{Title: sendStartedTitle, Body: fmt.Sprintf(sendStartedBodyFmt, name)}
}

// noFileNotice is what the user gets when the path is not a file this program
// can send: it has gone, or it is a folder.
func noFileNotice(name string) ui.Notification {
	return ui.Notification{Title: sendNoFileTitle, Body: fmt.Sprintf(sendNoFileBodyFmt, name)}
}
