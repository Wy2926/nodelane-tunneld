package main

var catalogES = map[messageID]string{
	msgChooseMode:           "Elegir modo",
	msgAnonymousMode:        "Túnel anónimo",
	msgAccountMode:          "Iniciar sesión",
	msgLoggedIn:             "Sesión guardada",
	msgLoggedOut:            "Sesión local eliminada",
	msgLoginURL:             "URL de acceso",
	msgLoginCode:            "Código de dispositivo",
	msgNoRoutes:             "No hay rutas",
	msgRoutesLabel:          "Rutas",
	msgRouteIDLabel:         "ID de ruta",
	msgRunIDLabel:           "ID de ejecución",
	msgStateLabel:           "Estado",
	msgRouteSelectionFailed: "No se encontró una ruta activa única.",
	msgLoginRequired:        "Inicia sesión primero con nt login.",
	msgOperationFailed:      "Error de operación: %s",
	msgRetryAfter:           "Reintenta en %d segundos o detén una ejecución existente.",
	msgRunStopping:          "Deteniendo túnel",
	msgRunStopped:           "Túnel detenido",
	msgRunReconnecting:      "Reconectando",
	msgStatusOK:             "OK", msgStatusWarning: "AVISO", msgStatusError: "ERROR",
	msgDirectCommandHint:       "La próxima vez, ejecuta nt; un solo comando es suficiente.",
	msgUsage:                   "Uso: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "Sin argumentos, nt permite elegir entre uso anónimo e inicio de sesión.",
	msgHelpLanguage:            "Usa --lang IDIOMA para elegir el idioma; ejecuta «nt languages» para ver la lista.",
	msgHelpLanguageEnvironment: "NT_LANG prevalece sobre el idioma del sistema. También se admiten LC_ALL, LC_MESSAGES y LANG.",
	msgSupportedLanguages:      "Idiomas disponibles", msgMissingLanguage: "--lang requiere un código de idioma",
	msgUnsupportedLanguage: "idioma no compatible %q; idiomas disponibles: %s",
	msgInvalidPort:         "el puerto local debe ser un número entre 1 y 65535", msgInvalidLocalAddress: "la dirección local debe ser un nombre de host o una IP sin puerto", msgInvalidProtocol: "el protocolo debe ser http, tcp o udp",

	msgNoInteractiveInput: "la entrada interactiva no está disponible; indica el protocolo, la dirección local y el puerto, por ejemplo: nt anonymous http localhost 3000",

	msgChooseProtocol:     "Elige un protocolo de túnel:",
	msgProtocolNavigation: "↑/↓ para elegir · Intro para confirmar",

	msgLocalAddressPrompt:      "Dirección local [localhost]: ",
	msgLocalAddressDefaultHelp: "Déjalo vacío para usar localhost",
	msgPortPrompt:              "Puerto local [1-65535]: ",
	msgPortRangeHelp:           "Introduce un valor entre 1 y 65535",

	msgConnectingEdge: "Conectando a un nodo perimetral de NodeLane",

	msgTunnelConnected:   "Túnel conectado",
	msgLocalAddressLabel: "Dirección local", msgPublicAddressLabel: "Dirección pública", msgExpiresAtLabel: "Caduca el",
}
