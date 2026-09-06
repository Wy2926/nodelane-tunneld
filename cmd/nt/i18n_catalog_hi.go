package main

var catalogHI = map[messageID]string{
	msgChooseMode:           "मोड चुनें",
	msgAnonymousMode:        "अनाम टनल",
	msgAccountMode:          "खाते में लॉगिन",
	msgLoggedIn:             "लॉगिन सहेजा गया",
	msgLoggedOut:            "स्थानीय लॉगिन हटाया गया",
	msgLoginURL:             "लॉगिन URL",
	msgLoginCode:            "डिवाइस कोड",
	msgNoRoutes:             "कोई रूट नहीं",
	msgRoutesLabel:          "रूट",
	msgRouteIDLabel:         "रूट ID",
	msgRunIDLabel:           "रन ID",
	msgStateLabel:           "स्थिति",
	msgRouteSelectionFailed: "एक विशिष्ट सक्रिय रूट नहीं मिला।",
	msgLoginRequired:        "पहले nt login से लॉगिन करें।",
	msgOperationFailed:      "ऑपरेशन विफल: %s",
	msgRetryAfter:           "%d सेकंड बाद फिर कोशिश करें या मौजूदा रन रोकें।",
	msgRunStopping:          "टनल बंद हो रही है",
	msgRunStopped:           "टनल बंद हो गई",
	msgRunReconnecting:      "फिर कनेक्ट हो रहा है",
	msgStatusOK:             "ठीक", msgStatusWarning: "चेतावनी", msgStatusError: "त्रुटि",
	msgDirectCommandHint:       "अगली बार केवल nt चलाएँ—एक ही कमांड पर्याप्त है।",
	msgUsage:                   "उपयोग: nt [--lang LANGUAGE]\n  nt anonymous <http|tcp|udp> <host> <port>\n  nt login\n  nt logout\n  nt routes\n  nt start <route> <host> <port>\n  nt launch <code> <host> <port>",
	msgHelpDescription:         "बिना आर्ग्युमेंट nt अनाम उपयोग या खाते में लॉगिन का विकल्प देता है।",
	msgHelpLanguage:            "भाषा चुनने के लिए --lang भाषा दें; सूची के लिए ‘nt languages’ चलाएँ।",
	msgHelpLanguageEnvironment: "NT_LANG सिस्टम भाषा से पहले लागू होता है। LC_ALL, LC_MESSAGES और LANG भी समर्थित हैं।",
	msgSupportedLanguages:      "समर्थित भाषाएँ", msgMissingLanguage: "--lang के साथ भाषा कोड देना आवश्यक है",
	msgUnsupportedLanguage: "भाषा %q समर्थित नहीं है; समर्थित भाषाएँ: %s",
	msgInvalidPort:         "लोकल पोर्ट 1 से 65535 के बीच की संख्या होना चाहिए", msgInvalidLocalAddress: "लोकल पता बिना पोर्ट का होस्ट नाम या IP पता होना चाहिए", msgInvalidProtocol: "प्रोटोकॉल http, tcp या udp होना चाहिए",

	msgNoInteractiveInput: "संवादात्मक इनपुट उपलब्ध नहीं है; प्रोटोकॉल, लोकल पता और पोर्ट सीधे दें, जैसे: nt anonymous http localhost 3000",

	msgChooseProtocol:     "टनल प्रोटोकॉल चुनें:",
	msgProtocolNavigation: "↑/↓ से चुनें · Enter से पुष्टि करें",

	msgLocalAddressPrompt:      "लोकल पता [localhost]: ",
	msgLocalAddressDefaultHelp: "localhost के लिए खाली छोड़ें",
	msgPortPrompt:              "लोकल पोर्ट [1-65535]: ",
	msgPortRangeHelp:           "1 से 65535 तक मान दर्ज करें",

	msgConnectingEdge: "NodeLane एज नोड से कनेक्ट हो रहा है",

	msgTunnelConnected:   "टनल कनेक्ट हो गई",
	msgLocalAddressLabel: "लोकल पता", msgPublicAddressLabel: "सार्वजनिक पता", msgExpiresAtLabel: "समाप्ति समय",
}
