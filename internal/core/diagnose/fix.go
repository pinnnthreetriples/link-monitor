package diagnose

import (
	"fmt"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// FixID names one repair. The app layer switches on it to decide what to run;
// core only decides which repairs are worth offering.
type FixID string

const (
	// FixStartTailscale brings the local Tailscale daemon back up.
	FixStartTailscale FixID = "start_tailscale"
	// FixDisableKillSwitch turns off the VPN kill switch whose WFP filter
	// denies the whole Tailscale range.
	FixDisableKillSwitch FixID = "disable_kill_switch"
	// FixSplitTunnelSSH keeps the kill switch on but lets the SSH client past it.
	FixSplitTunnelSSH FixID = "split_tunnel_ssh"
	// FixStartSSHD starts the OpenSSH server on the peer.
	FixStartSSHD FixID = "start_sshd"
	// FixStartLocalSSHD installs or starts the OpenSSH server on this machine,
	// without which nothing can ever connect to us.
	FixStartLocalSSHD FixID = "start_local_sshd"
	// FixCheckPeerOnline is advice, not an action: the peer has to come back
	// before anything else can be tested.
	FixCheckPeerOnline FixID = "check_peer_online"
)

// Fix is one repair worth suggesting. Title and Explanation are shown to the
// user and are therefore written in Russian. Nothing here executes: a Fix is a
// value, and the app layer decides whether and how to carry it out.
type Fix struct {
	ID          FixID
	Title       string
	Explanation string
	// NeedsAdmin reports whether carrying the fix out on *this* machine
	// requires elevation, so the UI can mark it with a shield beforehand.
	NeedsAdmin bool
}

func fixStartTailscale() Fix {
	return Fix{
		ID:    FixStartTailscale,
		Title: "Запустить Tailscale",
		Explanation: "Подниму службу Tailscale и дождусь входа в тайлнет. " +
			"Пока демон лежит, проверять остальное бессмысленно.",
		NeedsAdmin: true,
	}
}

func fixDisableKillSwitch() Fix {
	return Fix{
		ID:    FixDisableKillSwitch,
		Title: "Выключить kill switch AmneziaVPN",
		Explanation: "Сниму запрет «Block Internet»: именно он закрывает весь диапазон " +
			"Tailscale 100.64.0.0/10 для обычных программ. Демон Tailscale ходит мимо " +
			"этого фильтра, поэтому связь и выглядит живой, пока не откроешь сокет.",
		NeedsAdmin: true,
	}
}

func fixSplitTunnelSSH() Fix {
	return Fix{
		ID:    FixSplitTunnelSSH,
		Title: "Пустить SSH мимо VPN",
		Explanation: "Добавлю SSH-клиент в раздельное туннелирование AmneziaVPN, " +
			"чтобы его трафик не попадал под запрет. Kill switch при этом останется включённым.",
		NeedsAdmin: true,
	}
}

func fixStartSSHD(peer core.Machine) Fix {
	return Fix{
		ID:    FixStartSSHD,
		Title: fmt.Sprintf("Запустить sshd на %s", name(peer)),
		Explanation: "Включу службу OpenSSH Server на той машине и поставлю её в автозапуск, " +
			"чтобы она поднималась вместе с системой. Учётной записи на той стороне " +
			"нужны права администратора.",
		NeedsAdmin: false,
	}
}

func fixStartLocalSSHD() Fix {
	return Fix{
		ID:    FixStartLocalSSHD,
		Title: "Поднять SSH-сервер на этой машине",
		Explanation: "Проверю, установлен ли компонент «OpenSSH Server», при необходимости " +
			"доустановлю его, запущу службу sshd и поставлю её в автозапуск. Пока сервера нет, " +
			"подключиться к этой машине нельзя ниоткуда.",
		NeedsAdmin: true,
	}
}

func fixCheckPeerOnline(peer core.Machine) Fix {
	return Fix{
		ID:    FixCheckPeerOnline,
		Title: fmt.Sprintf("Проверить, включена ли %s", name(peer)),
		Explanation: "Тоннель поднят, но машина не отвечает: скорее всего она выключена, " +
			"спит или осталась без сети. Разбудите её и запустите проверку заново.",
		NeedsAdmin: false,
	}
}
