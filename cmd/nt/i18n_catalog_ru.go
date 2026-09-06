package main

var catalogRU = map[messageID]string{
	msgChooseMode:           "Выберите режим",
	msgAnonymousMode:        "Анонимный туннель",
	msgAccountMode:          "Войти в аккаунт",
	msgLoggedIn:             "Вход сохранён",
	msgLoggedOut:            "Локальный вход удалён",
	msgLoginURL:             "Адрес входа",
	msgLoginCode:            "Код устройства",
	msgNoRoutes:             "Маршрутов нет",
	msgRoutesLabel:          "Маршруты",
	msgRouteIDLabel:         "ID маршрута",
	msgRunIDLabel:           "ID запуска",
	msgStateLabel:           "Состояние",
	msgRouteSelectionFailed: "Не найден единственный активный маршрут.",
	msgLoginRequired:        "Сначала войдите с помощью nt login.",
	msgOperationFailed:      "Ошибка операции: %s",
	msgRetryAfter:           "Повторите через %d секунд или остановите текущий запуск.",
	msgRunStopping:          "Остановка туннеля",
	msgRunStopped:           "Туннель остановлен",
	msgRunReconnecting:      "Повторное подключение",
	msgStatusOK:             "ГОТОВО", msgStatusWarning: "ВНИМАНИЕ", msgStatusError: "ОШИБКА",
	msgDirectCommandHint:       "В следующий раз просто запустите nt — достаточно одной команды.",
	msgUsage:                   "Использование: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "Без аргументов nt предлагает анонимный туннель или вход в аккаунт.",
	msgHelpLanguage:            "Выберите язык через --lang ЯЗЫК; «nt languages» выводит список.",
	msgHelpLanguageEnvironment: "NT_LANG имеет приоритет над языком системы. Также поддерживаются LC_ALL, LC_MESSAGES и LANG.",
	msgSupportedLanguages:      "Поддерживаемые языки", msgMissingLanguage: "для --lang требуется код языка",
	msgUnsupportedLanguage: "язык %q не поддерживается; поддерживаемые языки: %s",
	msgInvalidPort:         "локальный порт должен быть числом от 1 до 65535", msgInvalidLocalAddress: "локальный адрес должен быть именем хоста или IP-адресом без порта", msgInvalidProtocol: "протокол должен быть http, tcp или udp",

	msgNoInteractiveInput: "интерактивный ввод недоступен; укажите протокол, локальный адрес и порт явно, например: nt anonymous http localhost 3000",

	msgChooseProtocol:     "Выберите протокол туннеля:",
	msgProtocolNavigation: "↑/↓ — выбор · Enter — подтвердить",

	msgLocalAddressPrompt:      "Локальный адрес [localhost]: ",
	msgLocalAddressDefaultHelp: "Оставьте пустым, чтобы использовать localhost",
	msgPortPrompt:              "Локальный порт [1-65535]: ",
	msgPortRangeHelp:           "Введите значение от 1 до 65535",

	msgConnectingEdge: "Подключение к граничному узлу NodeLane",

	msgTunnelConnected:   "Туннель подключён",
	msgLocalAddressLabel: "Локальный адрес", msgPublicAddressLabel: "Публичный адрес", msgExpiresAtLabel: "Истекает",
}
