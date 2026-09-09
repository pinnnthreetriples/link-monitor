package app

// Everything in this file is read by the user, so all of it is Russian — the
// same division of labour internal/app/fixtext.go and foldersynctext.go keep.
//
// Not one of these sentences can carry what was on the clipboard, and that is
// deliberate rather than lucky: they are constants, with no formatting verb
// between them, so there is nowhere for content to be interpolated. The one
// state that is *not* a failure — the program not running on the second
// machine — has no sentence here at all, because it is a flag on the status
// and internal/app/httpapi words it.
const (
	msgClipNoClipboard = "Не удалось прочитать буфер обмена этой машины. " +
		"Общий буфер работает только в сеансе, в котором вы работаете за компьютером."
	msgClipReadFailed = "Не удалось прочитать буфер обмена: его удерживает другая программа. " +
		"Попробуем при следующем изменении."
	msgClipPutFailed  = "Не удалось положить полученное в буфер обмена этой машины."
	msgClipSendFailed = "Не удалось передать содержимое на вторую машину: " +
		"нет рабочего SSH-сеанса или программа там не отвечает."
)
