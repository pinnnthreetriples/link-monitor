package ui

import (
	"strings"
	"unicode/utf16"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// Every string in this file is read by the user and is therefore in Russian.
const (
	menuOpen  = "Открыть"
	menuCheck = "Проверить сейчас"
	menuQuit  = "Выход"

	// menuShell is the checkable item that puts «Отправить на ПК» into
	// Explorer's right-click menu. The label names what the item controls
	// rather than what clicking it will do, because a checkbox already says
	// that: ticked means the item is in the menu.
	menuShell = "Пункт «Отправить на ПК» в Проводнике"

	// menuClip is the checkable item that switches the shared clipboard. Named
	// for the thing rather than for the click, like menuShell, and worded
	// exactly as internal/app/httpapi/text.go words the same switch in the
	// window: «общий буфер обмена» is this feature's name in both places.
	menuClip = "Общий буфер обмена"

	// The three quick actions. Every label opens with «Открыть» so the block
	// reads as one list, and every one of them names the place it lands rather
	// than the tool it uses: the user is choosing a destination, not a
	// program. «ПК» is the second machine throughout this UI, as in
	// «Отправить на ПК».
	menuTerminal = "Открыть терминал на ПК"
	menuFolder   = "Открыть общую папку"
	menuDesktop  = "Открыть рабочий стол ПК"

	hintOpen  = "Открыть окно Link Monitor"
	hintCheck = "Проверить связь прямо сейчас"
	hintQuit  = "Завершить работу"
	hintShell = "Показывать «Отправить на ПК» в меню правой кнопки мыши"
	hintClip  = "Передавать скопированный текст между этим компьютером и ПК — только текст"

	hintTerminal = "Терминал с готовым входом на второй компьютер по SSH"
	hintFolder   = "Общая папка на этом компьютере — в Проводнике"
	hintDesktop  = "Удалённый рабочий стол второго компьютера"

	// The three answers a click on that item can get. Switching a menu item on
	// leaves nothing on screen to look at, so each outcome says so out loud —
	// and each says where to go next, because the point of the item is that the
	// user does not have to come back to this program to use it.
	shellOnTitle = "Пункт добавлен в меню Проводника"
	shellOnBody  = "Нажми на файл правой кнопкой и выбери «Отправить на ПК». " +
		"Ход передачи виден на вкладке «Файлы»."

	shellOffTitle = "Пункт убран из меню Проводника"
	shellOffBody  = "«Отправить на ПК» больше не появится в меню правой кнопки. " +
		"Файлы можно отправлять, перетащив их в окно программы."

	shellFailTitle = "Не удалось изменить меню Проводника"
	shellFailBody  = "Windows не дала записать настройку в реестр текущего пользователя. " +
		"Попробуй ещё раз; если не поможет — подробности в журнале программы."

	// The three answers a click on «Общий буфер обмена» can get.
	//
	// Every headline is the first clause of the sentence the window already
	// gets for the same state from internal/app/httpapi/text.go —
	// msgClipTurnedOn, msgClipTurnedOff and msgClipNoClipboard — and every body
	// continues with the rest of it, so the two switches never describe the
	// same state in different words. The bodies are duplicated rather than
	// imported: internal/ui does not depend on an HTTP transport to know how to
	// spell its own menu item, and the test below is what keeps the copies
	// honest.
	//
	// Not one of them can carry what was copied, and that is structural rather
	// than lucky: they are constants with no formatting verb anywhere in them,
	// so there is nowhere for content to be interpolated.
	clipOnTitle = "Общий буфер обмена включён"
	clipOnBody  = "Скопированный здесь текст будет появляться в буфере обмена второй машины. " +
		"Передаётся только текст, и нигде не сохраняется."

	clipOffTitle = "Общий буфер обмена выключен"
	clipOffBody  = "Программа больше не читает буфер обмена и ничего никуда не передаёт."

	clipFailTitle = "Общий буфер обмена недоступен"
	clipFailBody  = "Программа не может обратиться к буферу обмена этой машины. " +
		"Общий буфер работает только в сеансе, в котором ты работаешь за компьютером: " +
		"разблокируй экран и попробуй ещё раз."

	// What a quick action says when it could not start. Each names what did
	// not happen and what to try, because the click left nothing on screen to
	// look at — the whole point of the item was a window that did not appear.
	quickTermFailTitle = "Терминал не открылся"
	quickTermFailBody  = "Не удалось запустить терминал с входом на ПК. " +
		"Проверь связь пунктом «Проверить сейчас»; подробности — в журнале программы."

	// The folder was never configured. Not a failure, so the wording explains
	// rather than apologises, and it names the switch that turns it on.
	quickNoFolderTitle = "Общая папка не настроена"
	quickNoFolderBody  = "Папку задаёт ключ -sync-folder при запуске программы. " +
		"Пока он не задан, общая папка выключена и открывать нечего."

	quickFolderFailTitle = "Общая папка не открылась"
	quickFolderFailBody  = "Проводник не открыл общую папку. Возможно, её переименовали " +
		"или удалили; подробности — в журнале программы."

	// One sentence for both ways this can go wrong — the peer is not answering
	// on the remote-desktop port, or the client would not start — because from
	// the user's side they are the same event: the window did not appear.
	quickDesktopFailTitle = "Рабочий стол ПК не открылся"
	quickDesktopFailBody  = "Второй компьютер не отвечает на подключение к удалённому рабочему " +
		"столу, либо программа подключения не запустилась. Проверь связь пунктом " +
		"«Проверить сейчас»; подробности — в журнале программы."

	// startingSummary is what the tooltip says between the tray appearing and
	// the first probe finishing. Claiming anything else would be a guess.
	startingSummary = "Проверка связи…"

	// noDetail is the notification body when the snapshot offers none.
	noDetail = "Подробности — в окне Link Monitor."
)

// worseTitles are the notification headlines used when a snapshot arrives
// without a summary of its own.
var worseTitles = map[core.State]string{
	core.StateUnknown: "Состояние связи неизвестно",
	core.StateWarn:    "Связь работает с перебоями",
	core.StateFail:    "Связь потеряна",
}

// tooltipLimit is the size of the Windows NOTIFYICONDATA tooltip buffer, in
// UTF-16 code units, minus the terminating NUL. Cyrillic costs one unit per
// letter, so this is roughly 127 characters over two lines.
const tooltipLimit = 127

// tooltip is what the user sees when hovering the tray icon: the headline,
// and the detail underneath it when there is one.
func tooltip(st Status) string {
	summary := strings.TrimSpace(st.Summary)
	if summary == "" {
		summary = titleFor(st.Overall)
	}
	text := summary
	if detail := strings.TrimSpace(st.Detail); detail != "" {
		text = summary + "\n" + detail
	}
	return truncateUTF16(text, tooltipLimit)
}

// titleFor names a state in Russian, for the moments a snapshot does not.
func titleFor(s core.State) string {
	if t, ok := worseTitles[s]; ok {
		return t
	}
	return "Связь установлена"
}

// notificationFor turns a status into the toast the user will read. A snapshot
// normally supplies both lines; the fallbacks are there so a notification is
// never blank.
func notificationFor(st Status) Notification {
	title := strings.TrimSpace(st.Summary)
	if title == "" {
		title = titleFor(st.Overall)
	}
	body := strings.TrimSpace(st.Detail)
	if body == "" {
		body = noDetail
	}
	return Notification{Title: title, Body: body}
}

// ShellMenuNotice is what the user is told after Explorer's «Отправить на ПК»
// item was switched. installed is the state it ended up in, not the one it came
// from.
//
// It is exported, unlike the rest of the text in this file, because
// cmd/linkmon's -menu flag switches the same item from a command line and has
// to say the same words. One copy of a sentence the user reads is worth an
// exported function.
func ShellMenuNotice(installed bool) Notification {
	if installed {
		return Notification{Title: shellOnTitle, Body: shellOnBody}
	}
	return Notification{Title: shellOffTitle, Body: shellOffBody}
}

// ShellMenuFailure is what the user is told when the Explorer item could not be
// switched at all. Exported for the same reason as [ShellMenuNotice].
func ShellMenuFailure() Notification {
	return Notification{Title: shellFailTitle, Body: shellFailBody}
}

// clipNotice is what the user is told after the shared clipboard was switched.
// on is the state it ended up in, not the one it came from.
//
// Unexported, unlike [ShellMenuNotice]: nothing outside this package switches
// this feature from a command line, because rule 1 says nothing but a person
// may switch it on at all.
func clipNotice(on bool) Notification {
	if on {
		return Notification{Title: clipOnTitle, Body: clipOnBody}
	}
	return Notification{Title: clipOffTitle, Body: clipOffBody}
}

// clipFailure is what the user is told when sharing could not be switched on.
// There is deliberately no counterpart for switching off: that direction
// cannot fail.
func clipFailure() Notification {
	return Notification{Title: clipFailTitle, Body: clipFailBody}
}

// truncateUTF16 cuts a string to at most limit UTF-16 code units, on a rune
// boundary, appending an ellipsis when anything was dropped. Windows measures
// its tooltip buffer in UTF-16 units, not bytes or runes.
func truncateUTF16(s string, limit int) string {
	if len(utf16.Encode([]rune(s))) <= limit {
		return s
	}

	units, cut := 0, 0
	for i, r := range s {
		n := 1
		if r > 0xFFFF { // outside the BMP, so a surrogate pair
			n = 2
		}
		if units+n > limit-1 { // leave room for the ellipsis
			break
		}
		units += n
		cut = i + len(string(r))
	}
	return strings.TrimRight(s[:cut], " \n") + "…"
}
