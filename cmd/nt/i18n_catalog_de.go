package main

var catalogDE = map[messageID]string{
	msgChooseMode:           "Modus wählen",
	msgAnonymousMode:        "Anonymer Tunnel",
	msgAccountMode:          "Am Konto anmelden",
	msgLoggedIn:             "Anmeldung gespeichert",
	msgLoggedOut:            "Lokale Anmeldung gelöscht",
	msgLoginURL:             "Anmelde-URL",
	msgLoginCode:            "Gerätecode",
	msgNoRoutes:             "Keine Routen",
	msgRoutesLabel:          "Routen",
	msgRouteIDLabel:         "Routen-ID",
	msgRunIDLabel:           "Lauf-ID",
	msgStateLabel:           "Status",
	msgRouteSelectionFailed: "Keine eindeutige aktive Route gefunden.",
	msgLoginRequired:        "Zuerst mit nt login anmelden.",
	msgOperationFailed:      "Vorgang fehlgeschlagen: %s",
	msgRetryAfter:           "In %d Sekunden erneut versuchen oder einen laufenden Tunnel stoppen.",
	msgRunStopping:          "Tunnel wird gestoppt",
	msgRunStopped:           "Tunnel gestoppt",
	msgRunReconnecting:      "Verbindung wird wiederhergestellt",
	msgStatusOK:             "OK", msgStatusWarning: "WARNUNG", msgStatusError: "FEHLER",
	msgDirectCommandHint:       "Beim nächsten Mal genügt der Befehl nt.",
	msgUsage:                   "Aufruf: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "Ohne Argumente bietet nt einen anonymen Tunnel oder die Kontoanmeldung an.",
	msgHelpLanguage:            "Mit --lang SPRACHE die Anzeigesprache wählen; „nt languages“ zeigt die Liste.",
	msgHelpLanguageEnvironment: "NT_LANG überschreibt die Systemsprache. LC_ALL, LC_MESSAGES und LANG werden ebenfalls unterstützt.",
	msgSupportedLanguages:      "Unterstützte Sprachen", msgMissingLanguage: "--lang benötigt einen Sprachcode",
	msgUnsupportedLanguage: "nicht unterstützte Sprache %q; unterstützte Sprachen: %s",
	msgInvalidPort:         "der lokale Port muss eine Zahl zwischen 1 und 65535 sein", msgInvalidLocalAddress: "die lokale Adresse muss ein Hostname oder eine IP-Adresse ohne Port sein", msgInvalidProtocol: "das Protokoll muss http, tcp oder udp sein",

	msgNoInteractiveInput: "keine interaktive Eingabe verfügbar; Protokoll, lokale Adresse und Port direkt angeben, zum Beispiel: nt anonymous http localhost 3000",

	msgChooseProtocol:     "Tunnelprotokoll auswählen:",
	msgProtocolNavigation: "↑/↓ auswählen · Enter bestätigen",

	msgLocalAddressPrompt:      "Lokale Adresse [localhost]: ",
	msgLocalAddressDefaultHelp: "Leer lassen für localhost",
	msgPortPrompt:              "Lokaler Port [1-65535]: ",
	msgPortRangeHelp:           "Wert von 1 bis 65535 eingeben",

	msgConnectingEdge: "Verbindung mit einem NodeLane-Edge-Knoten wird hergestellt",

	msgTunnelConnected:   "Tunnel verbunden",
	msgLocalAddressLabel: "Lokale Adresse", msgPublicAddressLabel: "Öffentliche Adresse", msgExpiresAtLabel: "Läuft ab",
}
