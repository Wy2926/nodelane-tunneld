package main

var catalogPtBR = map[messageID]string{
	msgChooseMode:           "Escolher modo",
	msgAnonymousMode:        "Túnel anônimo",
	msgAccountMode:          "Entrar na conta",
	msgLoggedIn:             "Login salvo",
	msgLoggedOut:            "Login local removido",
	msgLoginURL:             "URL de login",
	msgLoginCode:            "Código do dispositivo",
	msgNoRoutes:             "Nenhuma rota",
	msgRoutesLabel:          "Rotas",
	msgRouteIDLabel:         "ID da rota",
	msgRunIDLabel:           "ID da execução",
	msgStateLabel:           "Estado",
	msgRouteSelectionFailed: "Nenhuma rota ativa única foi encontrada.",
	msgLoginRequired:        "Entre primeiro com nt login.",
	msgOperationFailed:      "Falha na operação: %s",
	msgRetryAfter:           "Tente novamente em %d segundos ou pare uma execução existente.",
	msgRunStopping:          "Parando túnel",
	msgRunStopped:           "Túnel parado",
	msgRunReconnecting:      "Reconectando",
	msgStatusOK:             "OK", msgStatusWarning: "AVISO", msgStatusError: "ERRO",
	msgDirectCommandHint:       "Na próxima vez, execute nt; um único comando é suficiente.",
	msgUsage:                   "Uso: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "Sem argumentos, o nt oferece uso anônimo ou login na conta.",
	msgHelpLanguage:            "Use --lang IDIOMA para escolher o idioma; execute “nt languages” para ver a lista.",
	msgHelpLanguageEnvironment: "NT_LANG substitui o idioma do sistema. LC_ALL, LC_MESSAGES e LANG também são aceitos.",
	msgSupportedLanguages:      "Idiomas disponíveis", msgMissingLanguage: "--lang requer um código de idioma",
	msgUnsupportedLanguage: "idioma não compatível %q; idiomas disponíveis: %s",
	msgInvalidPort:         "a porta local deve ser um número entre 1 e 65535", msgInvalidLocalAddress: "o endereço local deve ser um nome de host ou IP sem porta", msgInvalidProtocol: "o protocolo deve ser http, tcp ou udp",

	msgNoInteractiveInput: "a entrada interativa não está disponível; informe o protocolo, o endereço local e a porta, por exemplo: nt anonymous http localhost 3000",

	msgChooseProtocol:     "Escolha um protocolo de túnel:",
	msgProtocolNavigation: "↑/↓ para escolher · Enter para confirmar",

	msgLocalAddressPrompt:      "Endereço local [localhost]: ",
	msgLocalAddressDefaultHelp: "Deixe em branco para usar localhost",
	msgPortPrompt:              "Porta local [1-65535]: ",
	msgPortRangeHelp:           "Digite um valor de 1 a 65535",

	msgConnectingEdge: "Conectando a um nó de borda NodeLane",

	msgTunnelConnected:   "Túnel conectado",
	msgLocalAddressLabel: "Endereço local", msgPublicAddressLabel: "Endereço público", msgExpiresAtLabel: "Expira em",
}
