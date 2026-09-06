package main

var catalogEnglish = map[messageID]string{
	msgChooseMode:              "Choose a mode",
	msgAnonymousMode:           "Anonymous tunnel",
	msgAccountMode:             "Log in to account",
	msgLoggedIn:                "Account login saved",
	msgLoggedOut:               "Local account login cleared",
	msgLoginURL:                "Sign-in URL",
	msgLoginCode:               "Device code",
	msgNoRoutes:                "No routes",
	msgRoutesLabel:             "Routes",
	msgRouteIDLabel:            "Route ID",
	msgRunIDLabel:              "Run ID",
	msgStateLabel:              "State",
	msgRouteSelectionFailed:    "No unique active route matches this name.",
	msgLoginRequired:           "Log in first with nt login.",
	msgOperationFailed:         "Operation failed: %s",
	msgRetryAfter:              "Retry in %d seconds, or stop an existing run.",
	msgRunStopping:             "Stopping tunnel",
	msgRunStopped:              "Tunnel stopped",
	msgRunReconnecting:         "Reconnecting",
	msgStatusOK:                "OK",
	msgStatusWarning:           "WARN",
	msgStatusError:             "ERROR",
	msgDirectCommandHint:       "Next time, run nt — one command is all you need.",
	msgUsage:                   "Usage: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "Without arguments, nt offers anonymous use or account login.",
	msgHelpLanguage:            "Use --lang LANGUAGE to select the display language; use 'nt languages' to list them.",
	msgHelpLanguageEnvironment: "NT_LANG overrides the system locale. LC_ALL, LC_MESSAGES, and LANG are also supported.",
	msgSupportedLanguages:      "Supported languages",
	msgMissingLanguage:         "--lang requires a language code",
	msgUnsupportedLanguage:     "unsupported language %q; supported languages: %s",
	msgInvalidPort:             "the local port must be a number from 1 to 65535",
	msgInvalidLocalAddress:     "the local address must be a hostname or IP address without a port",
	msgInvalidProtocol:         "the protocol must be http, tcp, or udp",

	msgNoInteractiveInput: "interactive input is unavailable; pass the protocol, local address, and port directly, for example: nt anonymous http localhost 3000",

	msgChooseProtocol:     "Choose a tunnel protocol:",
	msgProtocolNavigation: "↑/↓ select · Enter confirm",

	msgLocalAddressPrompt:      "Local address [localhost]: ",
	msgLocalAddressDefaultHelp: "Leave empty to use localhost",

	msgPortPrompt:    "Local port [1-65535]: ",
	msgPortRangeHelp: "Enter a value from 1 to 65535",

	msgConnectingEdge: "Connecting to a NodeLane edge node",

	msgTunnelConnected: "Tunnel connected",

	msgLocalAddressLabel:  "Local address",
	msgPublicAddressLabel: "Public address",
	msgExpiresAtLabel:     "Expires at",
}
