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
	summaryOK      = "Связь установлена"
	summaryDown    = "Связи нет"
	summaryBlocked = "Трафик блокируется"
	summaryPartial = "Частичная связь"
	summaryUnknown = "Состояние неизвестно"

	detailOK      = "Обе машины видят друг друга"
	detailNoTS    = "Tailscale отключён"
	detailNoSSHD  = "Рабочий ПК не принимает подключения"
	detailUnknown = "Проверки не удалось выполнить — о связи судить нельзя"

	noteNotChecked        = "Проверка ещё не выполнялась"
	noteWaitsForTailscale = "Не проверялось: сначала нужно поднять Tailscale"
	noteNoTailscaleAnswer = "Не проверялось: неизвестно, работает ли Tailscale"
	noteNoAnswerFromPeer  = "Не проверялось: вторая машина не отвечает"
	noteBlockedSocket     = "Не проверялось: локальный фильтр не даёт открыть сокет"
)

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

// unknownNote turns a failed probe into a Russian note. The error is examined,
// never printed: user-visible strings stay Russian, and the technical text
// stays inside the error we wrapped.
func unknownNote(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "Проверку не удалось завершить: истекло время ожидания"
	case errors.Is(err, context.Canceled):
		return "Проверка была отменена и не завершилась"
	default:
		return "Проверку не удалось выполнить — результат неизвестен"
	}
}

// daemonNote prefers the note the probe supplies («v1.102.3 · 3 узла») and
// falls back to a sentence of our own when it comes back empty.
func daemonNote(note string) string {
	if trimmed := strings.TrimSpace(note); trimmed != "" {
		return trimmed
	}
	return "Демон подключён к тайлнету"
}
