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
	// FixStartLocalSSHD starts the OpenSSH server on this machine, without
	// which nothing can ever connect to us. It assumes the service exists;
	// FixInstallSSHServer is the fix for when it does not, and the two are
	// separate because starting a service that is not installed cannot work.
	FixStartLocalSSHD FixID = "start_local_sshd"

	// FixInstallSSHServer is advice, not an action, and deliberately so.
	// Installing a Windows optional feature puts new software on this machine
	// and needs elevation — and the software in question is a server that
	// listens on the network. A monitor that installed one quietly would be a
	// monitor that opened a way in without being asked. We say what to do; we
	// do not do it.
	FixInstallSSHServer FixID = "install_ssh_server"

	// FixPeerNotInTailnet is advice, not an action: the machine this program
	// was told to watch is not a member of this tailnet, and only the person at
	// the keyboard can say whether the setting is wrong or the node is gone.
	FixPeerNotInTailnet FixID = "peer_not_in_tailnet"

	// FixVerifyHostKey is advice, and it is the one fix here that must never
	// become an action. The machine at that address offered a host key other
	// than the recorded one, and the only honest way through it is out of
	// band: read the fingerprint on the machine itself, over a channel that is
	// not this connection, and compare. There is deliberately no "trust it
	// anyway" button — that button is what an attacker in the middle needs
	// pressed — and this fix is never carried out automatically, for the same
	// reason the AmneziaVPN kill switch is not.
	FixVerifyHostKey FixID = "verify_host_key"

	// FixConfirmHostKey is the same discipline for the milder case: this host
	// has never been verified and trust-on-first-use is off. Nothing
	// contradicts anything, and the remedy is still to learn the fingerprint
	// from the host itself before recording it.
	FixConfirmHostKey FixID = "confirm_host_key"
	// FixCheckPeerOnline is advice, not an action: the peer has to come back
	// before anything else can be tested.
	FixCheckPeerOnline FixID = "check_peer_online"
	// FixAuthorizeKey is advice, not an action, and deliberately so. The public
	// key of this machine has to be added to the other machine's
	// authorized-keys file, which needs administrator rights over there and
	// decides who may log in — a security decision belonging to the person at
	// the keyboard, like the AmneziaVPN kill switch this program also refuses
	// to touch. We say what to do; we do not do it.
	FixAuthorizeKey FixID = "authorize_key"
	// FixUsableKey is advice, not an action, and for the same reason as
	// FixAuthorizeKey one step closer to home. This machine's private key
	// cannot be read unattended — it is passphrase protected, the passphrase
	// does not fit, or the file is not there — and every remedy means deciding
	// how this machine's credentials are stored: rewriting a key without its
	// passphrase weakens it, generating a new one changes what the peer has to
	// trust, and handing the passphrase to a background process is a choice
	// only its owner can make. A monitor that did any of that quietly would be
	// a monitor that rewrote your keys. We say what to do; we do not do it.
	FixUsableKey FixID = "usable_key"
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

// fixStartLocalSSHD starts a service that is there to be started. It used to
// promise an install as well — «при необходимости доустановлю» — which it never
// did and could not do: the app layer calls StartService and nothing else. The
// sentence now says what happens, and the missing component has its own fix.
func fixStartLocalSSHD() Fix {
	return Fix{
		ID:    FixStartLocalSSHD,
		Title: "Запустить SSH-сервер на этой машине",
		Explanation: "Запущу службу sshd на этой машине и поставлю её в автозапуск, чтобы она " +
			"поднималась вместе с системой. Пока служба стоит, подключиться к этой машине " +
			"нельзя ниоткуда.",
		NeedsAdmin: true,
	}
}

// fixInstallSSHServer is the remedy for the state this laptop was really in:
// no OpenSSH Server at all, while the row said the service was not running and
// sent the user to start something that did not exist.
//
// It is written as instructions because installing it is not this program's
// decision to make; see FixInstallSSHServer. NeedsAdmin stays false because
// that flag marks elevation of *this* process, which would not help — the
// elevation is wanted by the Windows installer the user runs.
func fixInstallSSHServer() Fix {
	return Fix{
		ID:    FixInstallSSHServer,
		Title: "Установить компонент «OpenSSH Server»",
		Explanation: "Это придётся сделать вручную: программы на эту машину Link Monitor не " +
			"устанавливает. Откройте «Параметры → Система → Дополнительные компоненты → " +
			"Добавить компонент», найдите «Сервер OpenSSH» и установите его — понадобятся права " +
			"администратора. Потом нажмите «Проверить»: службу sshd после установки программа " +
			"поднимет сама. Пока сервера нет, подключиться к этой машине нельзя ниоткуда.",
		NeedsAdmin: false,
	}
}

// fixPeerNotInTailnet is the remedy for a peer this tailnet has never heard of.
// It is separate from fixCheckPeerOnline because waking a machine will not add
// it to a tailnet it is not in, and telling the user to try that would waste
// their time on the more alarming of the two conditions.
func fixPeerNotInTailnet(peer core.Machine) Fix {
	return Fix{
		ID:    FixPeerNotInTailnet,
		Title: fmt.Sprintf("Разобраться, почему %s нет в tailnet", name(peer)),
		Explanation: fmt.Sprintf("Тайлнет не знает узла с таким именем или адресом, так что "+
			"будить машину бессмысленно. Проверьте три вещи: то ли имя и адрес указаны для "+
			"второй машины (сейчас это «%s» и %s), запущен ли и выполнен ли вход в Tailscale "+
			"на ней, и не удалён ли её узел в панели администратора тайлнета. "+
			"Затем нажмите «Проверить».", name(peer), addrOrDash(peer)),
		NeedsAdmin: false,
	}
}

// fixVerifyHostKey is the remedy for a changed host key, and it is instructions
// on purpose and for good. See FixVerifyHostKey: accepting a host key is a
// decision about which machine is trusted, and it can only be checked away from
// the connection in question.
//
// The fingerprint offered is printed because it is the value the user has to
// compare, and a public key's fingerprint is not a secret — it is the thing
// people read to each other over the phone for exactly this purpose.
func fixVerifyHostKey(peer core.Machine, fingerprint string) Fix {
	return Fix{
		ID:    FixVerifyHostKey,
		Title: "Сверить отпечаток ключа хоста",
		Explanation: fmt.Sprintf("Это придётся сделать вручную, и автоматически такое не "+
			"решается: подтвердить ключ — значит решить, той ли машине мы доверяем. "+
			"Машина по адресу %s предъявила ключ с отпечатком %s. Сверьте его на самой "+
			"второй машине, не через это подключение — за её клавиатурой или по другому уже "+
			"доверенному каналу; там отпечаток покажет команда "+
			"ssh-keygen -lf C:\\ProgramData\\ssh\\ssh_host_ed25519_key.pub. Совпало — "+
			"удалите старую строку про этот адрес из своего файла known_hosts и нажмите "+
			"«Проверить». Не совпало — не подключайтесь и выясните, что отвечает по этому "+
			"адресу.", addrOrDash(peer), fingerprintOrDash(fingerprint)),
		NeedsAdmin: false,
	}
}

// fixConfirmHostKey is the remedy for a host we have never verified. It differs
// from fixVerifyHostKey in the one way that matters: there is no stale line to
// remove, because there was never a line — so the advice is to record the key
// once the fingerprint has been checked, not to delete anything.
func fixConfirmHostKey(peer core.Machine, fingerprint string) Fix {
	return Fix{
		ID:    FixConfirmHostKey,
		Title: "Подтвердить ключ хоста",
		Explanation: fmt.Sprintf("Это придётся сделать вручную: принимать незнакомый ключ "+
			"хоста за пользователя программа не станет. Машина по адресу %s предъявила ключ "+
			"с отпечатком %s. Сверьте его на самой второй машине, не через это подключение "+
			"(там его покажет ssh-keygen -lf C:\\ProgramData\\ssh\\ssh_host_ed25519_key.pub), "+
			"и, если совпало, добавьте ключ этого хоста в свой файл known_hosts — например "+
			"командой ssh-keyscan для этого адреса, сверив её вывод с отпечатком. "+
			"Затем нажмите «Проверить».", addrOrDash(peer), fingerprintOrDash(fingerprint)),
		NeedsAdmin: false,
	}
}

// addrOrDash names the machine's address, falling back to whatever else
// identifies it: a fix that says «машина по адресу » with nothing after it is
// worse than one that names the machine.
func addrOrDash(peer core.Machine) string {
	if peer.Addr != "" {
		return peer.Addr
	}
	return name(peer)
}

// fingerprintOrDash keeps the sentence readable when the adapter had no
// fingerprint to give. It should not happen; a fix with a gap in it where the
// value the user must compare should be would be worse than saying so.
func fingerprintOrDash(fingerprint string) string {
	if fingerprint != "" {
		return fingerprint
	}
	return "(отпечаток не сообщён)"
}

// fixAuthorizeKey is the remedy for a refused key, written as instructions
// because it must stay instructions. Carrying it out means writing into the
// other machine's authorized-keys file, which needs elevation on that side and
// changes who may log in there; a monitor that did it quietly would be a
// monitor that granted itself access. NeedsAdmin stays false because it is the
// far side's rights that are wanted, not this process's — the sentence says so.
func fixAuthorizeKey(local, peer core.Machine) Fix {
	return Fix{
		ID:    FixAuthorizeKey,
		Title: fmt.Sprintf("Разрешить ключ этой машины на %s", name(peer)),
		Explanation: fmt.Sprintf(
			"Это придётся сделать вручную: в чужой файл ключей Link Monitor не пишет. "+
				"На %s добавьте открытый ключ %s в конец файла разрешённых ключей — "+
				"для учётной записи администратора это C:\\ProgramData\\ssh\\"+
				"administrators_authorized_keys, для обычной — .ssh\\authorized_keys "+
				"в её профиле, — затем перезапустите там службу sshd и нажмите «Проверить». "+
				"Нужны права администратора на той машине; мы заходим туда под «%s».",
			name(peer), name(local), peer.User),
		NeedsAdmin: false,
	}
}

// fixUsableKey is the remedy for a key this program cannot read. There are two
// of them, because a locked key and a missing one send the user to different
// places, and both are written as instructions on purpose — see FixUsableKey.
//
// The passphrase branch names the field that exists (Config.KeyPassphrase) and
// says why there is no flag for it, because "why can't I just pass it on the
// command line" is the first thing anyone asks: a command line is readable by
// every other process on the machine, so a flag would turn a protected key into
// an exposed one. That sentence is the fix's, not a comment's, so the user
// reads the reason too.
func fixUsableKey(problem core.KeyProblem) Fix {
	if problem.NeedsPassphrase() {
		return Fix{
			ID:    FixUsableKey,
			Title: "Дать программе ключ, который читается без пароля",
			Explanation: "Это придётся сделать вручную: ключи Link Monitor не перевыпускает и " +
				"пароли к ним не подбирает. Нужен ключ, который программа откроет сама, без " +
				"вопросов: либо снимите с ключа пароль (ssh-keygen -p для файла ключа в папке " +
				".ssh вашего профиля — это ослабляет защиту ключа, и решать вам), либо " +
				"сделайте для Link Monitor отдельный ключ без пароля и разрешите его на " +
				"второй машине. Пароль к ключу программа умеет принимать (Config.KeyPassphrase), " +
				"но флага командной строки для него нет намеренно: командная строка процесса " +
				"видна в системе другим программам, и пароль из неё утёк бы в список процессов.",
			NeedsAdmin: false,
		}
	}
	return Fix{
		ID:    FixUsableKey,
		Title: "Указать программе рабочий ключ SSH",
		Explanation: "Это придётся сделать вручную: ключи Link Monitor не создаёт и не подменяет. " +
			"Проверьте параметр -key: он должен указывать на файл закрытого ключа SSH — обычно " +
			"это папка .ssh вашего профиля. Если ключа там нет, создайте его командой " +
			"ssh-keygen -t ed25519 без пароля и разрешите соответствующий открытый ключ на " +
			"второй машине, затем нажмите «Проверить».",
		NeedsAdmin: false,
	}
}

func fixCheckPeerOnline(peer core.Machine) Fix {
	return Fix{
		ID:    FixCheckPeerOnline,
		Title: fmt.Sprintf("Проверить, включена ли %s", name(peer)),
		// The sentence used to begin «Тоннель поднят, но машина не отвечает»,
		// which was written for the only way this advice could once be reached:
		// a ping that went unanswered. Now that the tailnet is asked first, the
		// commonest way here is the daemon saying outright that the node is not
		// in the network, and «не отвечает» would understate an answer we have.
		Explanation: "Тоннель поднят, а машины в сети нет: скорее всего она выключена, " +
			"спит или осталась без сети. Разбудите её и запустите проверку заново.",
		NeedsAdmin: false,
	}
}
