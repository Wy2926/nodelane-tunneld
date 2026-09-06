package main

var catalogFR = map[messageID]string{
	msgChooseMode:           "Choisir un mode",
	msgAnonymousMode:        "Tunnel anonyme",
	msgAccountMode:          "Se connecter",
	msgLoggedIn:             "Connexion enregistrée",
	msgLoggedOut:            "Connexion locale supprimée",
	msgLoginURL:             "URL de connexion",
	msgLoginCode:            "Code de l'appareil",
	msgNoRoutes:             "Aucune route",
	msgRoutesLabel:          "Routes",
	msgRouteIDLabel:         "ID de route",
	msgRunIDLabel:           "ID d'exécution",
	msgStateLabel:           "État",
	msgRouteSelectionFailed: "Aucune route active unique ne correspond.",
	msgLoginRequired:        "Connectez-vous d'abord avec nt login.",
	msgOperationFailed:      "Échec de l'opération : %s",
	msgRetryAfter:           "Réessayez dans %d secondes ou arrêtez une exécution existante.",
	msgRunStopping:          "Arrêt du tunnel",
	msgRunStopped:           "Tunnel arrêté",
	msgRunReconnecting:      "Reconnexion",
	msgStatusOK:             "OK", msgStatusWarning: "AVERT.", msgStatusError: "ERREUR",
	msgDirectCommandHint:       "La prochaine fois, exécutez nt ; une seule commande suffit.",
	msgUsage:                   "Utilisation: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "Sans argument, nt propose un tunnel anonyme ou la connexion au compte.",
	msgHelpLanguage:            "Utilisez --lang LANGUE pour choisir la langue ; lancez « nt languages » pour afficher la liste.",
	msgHelpLanguageEnvironment: "NT_LANG remplace la langue du système. LC_ALL, LC_MESSAGES et LANG sont aussi pris en charge.",
	msgSupportedLanguages:      "Langues disponibles", msgMissingLanguage: "--lang nécessite un code de langue",
	msgUnsupportedLanguage: "langue non prise en charge %q ; langues disponibles : %s",
	msgInvalidPort:         "le port local doit être un nombre compris entre 1 et 65535", msgInvalidLocalAddress: "l’adresse locale doit être un nom d’hôte ou une adresse IP sans port", msgInvalidProtocol: "le protocole doit être http, tcp ou udp",

	msgNoInteractiveInput: "la saisie interactive est indisponible ; indiquez le protocole, l’adresse locale et le port, par exemple : nt anonymous http localhost 3000",

	msgChooseProtocol:     "Choisissez un protocole de tunnel :",
	msgProtocolNavigation: "↑/↓ pour choisir · Entrée pour confirmer",

	msgLocalAddressPrompt:      "Adresse locale [localhost] : ",
	msgLocalAddressDefaultHelp: "Laissez vide pour utiliser localhost",
	msgPortPrompt:              "Port local [1-65535] : ",
	msgPortRangeHelp:           "Saisissez une valeur de 1 à 65535",

	msgConnectingEdge: "Connexion à un nœud périphérique NodeLane",

	msgTunnelConnected:   "Tunnel connecté",
	msgLocalAddressLabel: "Adresse locale", msgPublicAddressLabel: "Adresse publique", msgExpiresAtLabel: "Expiration",
}
