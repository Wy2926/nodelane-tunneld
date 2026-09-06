package main

import (
	"fmt"
	"os"
	"strings"
)

type messageID string

const (
	msgStatusOK                messageID = "status.ok"
	msgStatusWarning           messageID = "status.warning"
	msgStatusError             messageID = "status.error"
	msgDirectCommandHint       messageID = "brand.direct_command_hint"
	msgUsage                   messageID = "help.usage"
	msgHelpDescription         messageID = "help.description"
	msgHelpLanguage            messageID = "help.language"
	msgHelpLanguageEnvironment messageID = "help.language_environment"
	msgSupportedLanguages      messageID = "help.supported_languages"
	msgMissingLanguage         messageID = "error.missing_language"
	msgUnsupportedLanguage     messageID = "error.unsupported_language"
	msgInvalidPort             messageID = "error.invalid_port"
	msgInvalidLocalAddress     messageID = "error.invalid_local_address"
	msgInvalidProtocol         messageID = "error.invalid_protocol"

	msgNoInteractiveInput messageID = "error.no_interactive_input"

	msgChooseProtocol     messageID = "prompt.choose_protocol"
	msgProtocolNavigation messageID = "prompt.protocol_navigation"

	msgLocalAddressPrompt      messageID = "prompt.local_address"
	msgLocalAddressDefaultHelp messageID = "prompt.local_address_default"

	msgPortPrompt    messageID = "prompt.port"
	msgPortRangeHelp messageID = "prompt.port_range"

	msgConnectingEdge messageID = "tunnel.connecting_edge"

	msgTunnelConnected messageID = "tunnel.connected"

	msgLocalAddressLabel  messageID = "detail.local_address"
	msgPublicAddressLabel messageID = "detail.public_address"
	msgExpiresAtLabel     messageID = "detail.expires_at"
)

var supportedLocales = []string{
	"zh-CN", "zh-TW", "en", "es", "fr", "de", "ja", "ko", "pt-BR", "ru", "ar", "hi",
}

type localizer struct {
	locale  string
	catalog map[messageID]string
}

func newLocalizer(locale string) localizer {
	normalized, ok := normalizeLocale(locale)
	if !ok {
		normalized = "en"
	}
	return localizer{locale: normalized, catalog: messageCatalogs[normalized]}
}

func (l localizer) text(id messageID, values ...any) string {
	template := l.catalog[id]
	if template == "" {
		template = messageCatalogs["en"][id]
	}
	if len(values) == 0 {
		return template
	}
	return fmt.Sprintf(template, values...)
}

func supportedLocaleList() string {
	return strings.Join(supportedLocales, ", ")
}

// parseLanguageOptions removes the global language option and selects a locale.
// An explicit option wins over NT_LANG, POSIX locale variables, and the OS locale.
func parseLanguageOptions(args []string, lookupEnv func(string) (string, bool)) ([]string, localizer, error) {
	detected := detectLocale(lookupEnv)
	fallback := newLocalizer(detected)
	remaining := make([]string, 0, len(args))
	requested := ""
	languageOptionSet := false

	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--lang" || argument == "--language" || argument == "-l":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return nil, fallback, fmt.Errorf("%s", fallback.text(msgMissingLanguage))
			}
			index++
			requested = args[index]
			languageOptionSet = true
		case strings.HasPrefix(argument, "--lang="):
			requested = strings.TrimPrefix(argument, "--lang=")
			languageOptionSet = true
		case strings.HasPrefix(argument, "--language="):
			requested = strings.TrimPrefix(argument, "--language=")
			languageOptionSet = true
		default:
			remaining = append(remaining, argument)
		}
	}

	if languageOptionSet && strings.TrimSpace(requested) == "" {
		return nil, fallback, fmt.Errorf("%s", fallback.text(msgMissingLanguage))
	}
	if requested == "" || strings.EqualFold(strings.TrimSpace(requested), "auto") {
		return remaining, fallback, nil
	}
	locale, ok := normalizeLocale(requested)
	if !ok {
		return nil, fallback, fmt.Errorf("%s", fallback.text(msgUnsupportedLanguage, requested, supportedLocaleList()))
	}
	return remaining, newLocalizer(locale), nil
}

func detectLocale(lookupEnv func(string) (string, bool)) string {
	for _, name := range []string{"NT_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if value, ok := lookupEnv(name); ok && strings.TrimSpace(value) != "" {
			if locale, supported := normalizeLocale(value); supported {
				return locale
			}
		}
	}
	if locale, supported := normalizeLocale(systemLocale()); supported {
		return locale
	}
	return "en"
}

func normalizeLocale(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		value = value[:dot]
	}
	if modifier := strings.IndexByte(value, '@'); modifier >= 0 {
		value = value[:modifier]
	}
	value = strings.ReplaceAll(value, "_", "-")
	parts := strings.Split(strings.ToLower(value), "-")
	if len(parts) == 0 {
		return "", false
	}
	switch parts[0] {
	case "zh":
		for _, part := range parts[1:] {
			switch part {
			case "tw", "hk", "mo", "hant":
				return "zh-TW", true
			}
		}
		return "zh-CN", true
	case "pt":
		return "pt-BR", true
	case "en", "es", "fr", "de", "ja", "ko", "ru", "ar", "hi":
		return parts[0], true
	default:
		return "", false
	}
}

func environmentLookup(name string) (string, bool) {
	return os.LookupEnv(name)
}
