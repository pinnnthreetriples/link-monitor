package app

// Everything in this file is read by the user, so all of it is Russian. The
// technical detail stays in the wrapped Go error and in the log, which never
// reach a screen — the same division of labour internal/app/fixtext.go keeps.
const (
	msgSyncNoPeer = "Не удалось открыть общую папку на второй машине: " +
		"нет рабочего SSH-сеанса или папки там нет."
	msgSyncScanHere   = "Не удалось прочитать общую папку на этой машине."
	msgSyncScanThere  = "Не удалось прочитать общую папку на второй машине."
	msgSyncCopyFailed = "Часть файлов передать не удалось — папки пока совпадают не полностью. " +
		"Попробуем снова при следующей сверке."
	msgSyncStateFailed = "Сверка прошла, но состояние синхронизации не сохранилось. " +
		"В следующий раз расхождения будут сохранены как конфликтные копии."
	msgSyncTimedOut = "Сверка не уложилась во время ожидания и была прервана. " +
		"Ничего не потеряно: незавершённые копии удалены."
)
