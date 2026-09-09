package diagnose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// Headlines and notes shared by more than one branch. Everything the user reads
// is Russian; the technical detail stays inside the wrapped error.
const (
	summaryOK          = "Связь установлена"
	summaryDown        = "Связи нет"
	summaryBlocked     = "Трафик блокируется"
	summaryPartial     = "Частичная связь"
	summaryRefused     = "Ключ не принят"
	summaryKeyUnusable = "Свой ключ недоступен"
	summaryUnknown     = "Состояние неизвестно"

	// summaryNotInTailnet is not a broken link and does not read like one: the
	// machine this program was told to watch is not a member of this tailnet.
	// The wording follows the machine card, which has always said «Нет в
	// tailnet» for the same condition.
	summaryNotInTailnet = "Машины нет в tailnet"

	// summaryHostKeyChanged is its own verdict on purpose, and it sits in no
	// family with a key or a port problem. Everything about the link may be
	// working; what is in doubt is which machine is at the other end of it.
	summaryHostKeyChanged = "Ключ хоста изменился"

	// summaryHostUnverified is the milder relative of the one above, and the
	// difference between them is the whole reason there are two: an unverified
	// host is unknown, a changed key is a contradiction.
	summaryHostUnverified = "Хост не подтверждён"

	detailOK      = "Обе машины видят друг друга"
	detailNoTS    = "Tailscale отключён"
	detailNoSSHD  = "Рабочий ПК не принимает подключения"
	detailUnknown = "Проверки не удалось выполнить — о связи судить нельзя"

	// The three sentences for an SSH server of our own that is not serving.
	// They differ because the remedies differ, and the middle one exists for
	// the case where the service manager would not say which applies.
	detailNoLocalServer      = "Отсюда подключиться можно, а к нам — нет: SSH-сервера на этой машине нет вовсе"
	detailLocalServerDown    = "Отсюда подключиться можно, а к нам — нет: не запущен свой SSH-сервер"
	detailLocalServerStopped = "Отсюда подключиться можно, а к нам — нет: " +
		"свой SSH-сервер установлен, но не запущен"

	noteNotChecked        = "Проверка ещё не выполнялась"
	noteWaitsForTailscale = "Не проверялось: сначала нужно поднять Tailscale"
	noteNoTailscaleAnswer = "Не проверялось: неизвестно, работает ли Tailscale"
	noteNoAnswerFromPeer  = "Не проверялось: вторая машина не отвечает"
	noteBlockedSocket     = "Не проверялось: локальный фильтр не даёт открыть сокет"
	noteNoSession         = "Не проверялось: нет рабочего сеанса, чтобы позвонить нам в ответ"
	noteKeyRefused        = "Не проверялось: вторая машина не приняла наш ключ, попросить её некому"
	noteNoKeySession      = "Не проверялось: SSH-сеанс открыть было нечем — проблема со своим ключом"
	noteNoTunnelAnswer    = "Не проверялось: до второй машины не дотянулись"
	noteNoPortAnswer      = "Не проверялось: проверка порта не завершилась"

	// The two notes for a peer the tailnet itself says is not there. They are
	// the sentences that replaced «истекло время ожидания» on a machine that
	// was simply switched off.
	notePeerOffline = "Не проверялось: вторая машина не в сети"
	notePeerAbsent  = "Не проверялось: такого узла в tailnet нет"

	// The two notes for a host key that stopped the session before a login was
	// attempted. Neither says anything about the peer's service: when the key
	// does not match, we do not know whose machine answered, and saying «служба
	// отвечает» would be attributing it to the peer.
	noteHostKeyChanged = "Не проверялось: ключ хоста не совпал — чья это машина, неизвестно"
	noteHostUnverified = "Не проверялось: ключ этого хоста ещё не подтверждён"

	// The three notes for our own SSH server, matching the three details above.
	noteLocalServerDown    = "SSH-сервер на этой машине не запущен — подключиться к нам не сможет никто"
	noteLocalServerStopped = "SSH-сервер на этой машине установлен, но не запущен — " +
		"подключиться к нам сейчас нельзя"
	noteLocalServerAbsent = "SSH-сервера на этой машине нет: компонент «OpenSSH Server» " +
		"не установлен, подключиться к нам нельзя ниоткуда"

	// noteNoLocalRefusal is what the kill-switch row says when the socket was
	// not refused locally while the peer was known to be off. It claims exactly
	// that and no more: silence from a machine that is switched off is no
	// evidence that packets leave this one.
	noteNoLocalRefusal = "Локальный фильтр сокет не отклонил — kill switch здесь трафик не режет"

	// noteUnknownFmt is what an unanswerable check says now: a Russian sentence
	// naming the kind of failure, with the system's own words quoted after it
	// and clearly subordinate to it. See unknownNote.
	noteUnknownFmt = "Проверку не удалось выполнить. Система сообщила: «%s»"
	// noteUnknownBare is the last resort, for an error with nothing quotable
	// left in it after scrubbing. It is the sentence every unknown row used to
	// get, and it is kept for exactly the case where it is the truth.
	noteUnknownBare = "Проверку не удалось выполнить — результат неизвестен"
)

// keyProblemNote is the outbound row's sentence when this machine's own key
// could not be loaded. It names the cause once, on the row that owns it; the
// rows that merely depended on it get noteNoKeySession and do not repeat it.
//
// None of these mentions the key's file, its bytes or its passphrase. Naming
// the condition is what makes the row actionable; naming the credential would
// be the one rule this program does not break.
func keyProblemNote(p core.KeyProblem) string {
	return "Войти по SSH нечем: " + keyProblemClause(p)
}

// keyProblemDetail is the headline's second line for the same failure. It says
// where the fault is *not*, because on the work PC everything below the login
// worked and the temptation to blame the peer or the network is exactly what
// made the original report useless.
func keyProblemDetail(p core.KeyProblem) string {
	return "Дело не в сети и не во второй машине — " + keyProblemClause(p)
}

// keyProblemClause is the one clause both of the above are built from, so the
// row and the headline can never disagree about what happened.
func keyProblemClause(p core.KeyProblem) string {
	switch p {
	case core.KeyPassphraseNeeded:
		return "закрытый ключ на этой машине защищён паролем, а пароля у программы нет"
	case core.KeyPassphraseWrong:
		return "пароль к закрытому ключу на этой машине не подошёл"
	case core.KeyFileMissing:
		return "файла закрытого ключа на этой машине нет"
	case core.KeyFileUnusable:
		return "файл закрытого ключа на этой машине не читается как ключ"
	default:
		return "закрытый ключ этой машины использовать не удалось"
	}
}

// blockedSocketNote is the kill-switch row's sentence when a local filter
// refused the socket. It is one function because two steps reach the same
// finding — the ordinary transport dial, and the short dial that still asks the
// question when the peer is off — and the row must read the same either way.
func blockedSocketNote(b *core.BlockedError) string {
	return fmt.Sprintf("Локальный фильтр отказал сокету к %s:%d (WSAEACCES 10013)", b.Addr, b.Port)
}

// label is the Russian caption of one row. The peer is named where it helps:
// with two machines in the tailnet, «SSH: ноутбук → рабочий ПК» beats a generic
// caption that leaves the direction to guesswork.
func label(id core.CheckID, local, peer core.Machine) string {
	switch id {
	case core.CheckTailscale:
		return "Tailscale"
	case core.CheckSSHOut:
		return fmt.Sprintf("SSH: %s → %s", name(local), name(peer))
	case core.CheckSSHIn:
		return fmt.Sprintf("SSH: %s → %s", name(peer), name(local))
	case core.CheckSSHD:
		return fmt.Sprintf("Служба sshd на %s", name(peer))
	case core.CheckKillSwitch:
		return "Блокировка трафика на этой машине"
	default:
		return "Неизвестная проверка"
	}
}

// name is the friendliest identifier a machine has.
func name(m core.Machine) string {
	switch {
	case m.TailnetName != "":
		return m.TailnetName
	case m.WindowsName != "":
		return m.WindowsName
	case m.Addr != "":
		return m.Addr
	default:
		return "вторая машина"
	}
}

// unknownNote turns a failed probe into a Russian note that says what actually
// went wrong.
//
// It used to return one content-free sentence for every unanswerable check —
// «Проверку не удалось выполнить — результат неизвестен» — which is how a
// passphrase-protected key stayed invisible for an hour with the answer sitting
// in the error chain the whole time. So the error is still never dumped, but it
// is no longer thrown away: a known kind of failure gets its own sentence, and
// anything else gets a Russian sentence with the system's own words quoted
// after it, scrubbed by systemDetail and clearly subordinate to it.
//
// Russian first is not decoration. The row is read by whoever is at the
// keyboard, and the sentence they can act on has to be in their language; the
// library's English (or, on these machines, Windows' Russian) is evidence
// underneath it, in quotation marks, for whoever is debugging.
func unknownNote(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "Проверку не удалось завершить: истекло время ожидания"
	case errors.Is(err, context.Canceled):
		return "Проверка была отменена и не завершилась"
	}
	if detail := systemDetail(err); detail != "" {
		return fmt.Sprintf(noteUnknownFmt, detail)
	}
	return noteUnknownBare
}

// daemonNote prefers the note the probe supplies («v1.102.3 · 3 узла») and
// falls back to a sentence of our own when it comes back empty.
func daemonNote(note string) string {
	if trimmed := strings.TrimSpace(note); trimmed != "" {
		return trimmed
	}
	return "Демон подключён к тайлнету"
}

// daemonDownNote reports a daemon that is not in the tailnet, keeping whatever
// it said about itself — «v1.102.3 · требуется вход», «остановлен», «занят
// другим пользователем Windows».
//
// Those are four different problems with four different remedies, and this row
// used to answer all of them with the same sentence: the adapter took the
// trouble to translate the daemon's state and the engine threw it away.
func daemonDownNote(note string) string {
	if trimmed := strings.TrimSpace(note); trimmed != "" {
		return "Демон Tailscale не подключён к тайлнету: " + trimmed
	}
	return "Демон Tailscale не подключён к тайлнету"
}
