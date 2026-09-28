package httpapi

// Everything in this file reaches a screen, so all of it is Russian. A Go error
// string never does: it is wrapped, logged by the caller if it wants, and
// replaced here by a sentence a person can act on.
const (
	msgNotCheckedYet       = "Проверка ещё не выполнялась"
	msgNotCheckedYetDetail = "Первая проверка ещё не завершилась — подождите несколько секунд."

	msgBadOrigin        = "Запрос отклонён: Link Monitor принимает обращения только со своего окна."
	msgNotFound         = "Такого адреса в API нет."
	msgMethodNotAllowed = "Этот адрес не отвечает на такой запрос."
	msgBadJSON          = "Не удалось разобрать запрос: ожидался корректный JSON."
	msgBodyTooBig       = "Запрос слишком большой."

	msgPollerDown  = "Служба проверки не запущена — состояние связи сейчас неизвестно."
	msgCheckFailed = "Проверка не завершилась: истекло время ожидания."

	msgPeersFailed = "Не удалось получить список машин в тайлнете."

	msgNeedPath      = "Укажите файл, который нужно отправить."
	msgNeedPeer      = "Укажите, на какую машину отправить файл."
	msgSendFailed    = "Не удалось отправить файл. Проверьте, что Tailscale запущен на обеих машинах."
	msgSendOK        = "Файл отправлен."
	msgReceiveFailed = "Не удалось забрать файлы из входящих Taildrop."
	msgReceiveEmpty  = "Во входящих Taildrop ничего нет."
	msgReceiveOKFmt  = "Принято файлов: %d."

	// The shared folder. Rule 6 in the design's own voice: what it did, what
	// it left alone, and the one guarantee that surprises people.
	msgSyncOff = "Общая папка не выбрана. Укажите её ключом -sync-folder при запуске — " +
		"без папки синхронизация не работает и ничего не трогает."
	msgSyncNotYet    = "Сверка ещё не выполнялась."
	msgSyncStarted   = "Сверка запущена."
	msgSyncSame      = "Папки совпадают."
	msgSyncNoDeletes = "Удаление не передаётся: файл, удалённый на одной машине, " +
		"останется на второй — и может вернуться при следующей сверке."
	msgSyncMovedFmt     = "Перенесено файлов: %d."
	msgSyncSkippedFmt   = "Ничего не перенесено, пропущено файлов: %d."
	msgSyncConflictsFmt = "Расхождений: %d. Файл изменён на обеих машинах — " +
		"копия со второй машины сохранена рядом, ваш файл не тронут."

	msgSkipTooBig     = "больше допустимого размера — не переносим"
	msgSkipBusy       = "файл сейчас изменяется — вернёмся к нему при следующей сверке"
	msgSkipUnresolved = "расхождение не устранено: на машинах разное содержимое"
	msgSkipUnsafeName = "имя отклонено: оно уводит за пределы общей папки"
	msgSkipUnknown    = "пропущен — причина не указана"

	// The shared clipboard. Rule 1 in the design's own voice — off is the
	// default and off means nothing happens — rule 3's refusal said out loud so
	// the user can see it working, and the state this feature has that no other
	// one does: it needs the program running on both machines.
	msgClipOff = "Общий буфер обмена выключен. Программа не читает буфер обмена " +
		"и ничего никуда не передаёт."
	msgClipOnNothingYet = "Общий буфер обмена включён. Скопируйте текст или скриншот — он появится " +
		"в буфере обмена второй машины."
	msgClipTurnedOn = "Общий буфер обмена включён. Текст и скриншоты будут " +
		"появляться в буфере обмена второй машины."
	msgClipTurnedOff      = "Общий буфер обмена выключен."
	msgClipCountsFmt      = "Передано: %d, получено: %d."
	msgClipPeerMissingFmt = "На машине «%s» программа не запущена — передавать некуда. " +
		"Общий буфер обмена работает, только когда Link Monitor запущен на обеих машинах."
	msgClipNoClipboard = "Общий буфер обмена недоступен: программа не может обратиться " +
		"к буферу обмена этой машины."
	msgClipReceived      = "Принято в буфер обмена."
	msgClipIsOff         = "Общий буфер обмена здесь выключен — содержимое не принято."
	msgClipArrivedTooBig = "Содержимое больше допустимого размера — не принято."
	msgClipPutFailed     = "Не удалось положить принятое в буфер обмена этой машины."
	msgClipNote          = "Передаются текст и скриншоты PNG; файлы отправляйте перетаскиванием. " +
		"Содержимое нигде не сохраняется и не попадает в журнал: видно только, что и какого " +
		"размера было передано. Всё идёт по SSH внутри Tailscale и в открытом виде в сеть не выходит."

	msgClipEventSent     = "передано на вторую машину"
	msgClipEventReceived = "получено со второй машины"
	msgClipEventTooBig   = "не передано: больше допустимого размера"
	msgClipEventMarked   = "не передано: программа-источник запретила запись буфера обмена"
	msgClipEventFailed   = "передать не удалось"
	msgClipEventUnknown  = "событие без описания"

	msgNeedRemoteHost  = "Укажите адрес машины, к порту которой нужно пробросить соединение."
	msgBadPort         = "Порт должен быть числом от 1 до 65535."
	msgBadLocalPort    = "Локальный порт должен быть числом от 0 до 65535; 0 — выбрать свободный."
	msgForwardFailed   = "Не удалось открыть проброс порта: нет рабочего SSH-сеанса со второй машиной."
	msgForwardOKFmt    = "Проброс открыт: %s → %s:%d."
	msgNeedForwardID   = "Укажите, какой проброс закрыть."
	msgForwardNotFound = "Такого проброса нет — возможно, он уже закрыт."
	msgForwardStopped  = "Проброс закрыт."
	msgForwardStopBad  = "Проброс закрыт, но слушающий сокет закрылся с ошибкой."

	msgServeFailed = "Не удалось опубликовать порт в тайлнете. Проверьте, что Tailscale запущен."
	msgServeOKFmt  = "Порт %d опубликован в тайлнете."

	msgBadLinkAction   = "Неизвестное действие: укажите «up», чтобы подключить, или «down», чтобы отключить."
	msgLinkUnavailable = "Управление подключением недоступно: Tailscale не подключён к программе."
)
