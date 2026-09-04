package main

import (
	"fmt"
	"os"
	"strings"
)

type messageID string

const (
	msgStatusOK                   messageID = "status.ok"
	msgStatusWarning              messageID = "status.warning"
	msgStatusError                messageID = "status.error"
	msgUsage                      messageID = "help.usage"
	msgHelpDescription            messageID = "help.description"
	msgHelpLanguage               messageID = "help.language"
	msgHelpLanguageEnvironment    messageID = "help.language_environment"
	msgSupportedLanguages         messageID = "help.supported_languages"
	msgMissingLanguage            messageID = "error.missing_language"
	msgUnsupportedLanguage        messageID = "error.unsupported_language"
	msgInvalidPort                messageID = "error.invalid_port"
	msgInvalidLocalAddress        messageID = "error.invalid_local_address"
	msgInvalidProtocol            messageID = "error.invalid_protocol"
	msgNoInteractiveArguments     messageID = "error.no_interactive_arguments"
	msgNoInteractiveInput         messageID = "error.no_interactive_input"
	msgProtocolReadFailed         messageID = "error.protocol_read_failed"
	msgLocalAddressReadFailed     messageID = "error.local_address_read_failed"
	msgPortReadFailed             messageID = "error.port_read_failed"
	msgChooseProtocol             messageID = "prompt.choose_protocol"
	msgHTTPDescription            messageID = "prompt.http_description"
	msgTCPDescription             messageID = "prompt.tcp_description"
	msgUDPDescription             messageID = "prompt.udp_description"
	msgProtocolPrompt             messageID = "prompt.protocol"
	msgInvalidProtocolChoice      messageID = "prompt.invalid_protocol"
	msgLocalAddressPrompt         messageID = "prompt.local_address"
	msgInvalidLocalAddressChoice  messageID = "prompt.invalid_local_address"
	msgPortPrompt                 messageID = "prompt.port"
	msgInvalidPortChoice          messageID = "prompt.invalid_port"
	msgCheckLocalService          messageID = "tunnel.check_local_service"
	msgLocalServiceUnavailable    messageID = "tunnel.local_service_unavailable"
	msgLocateCredentialsFailed    messageID = "tunnel.locate_credentials_failed"
	msgRegisteringDevice          messageID = "tunnel.registering_device"
	msgRegisterDeviceFailed       messageID = "tunnel.register_device_failed"
	msgSaveCredentialsFailed      messageID = "tunnel.save_credentials_failed"
	msgLoadCredentialsFailed      messageID = "tunnel.load_credentials_failed"
	msgRequestingTunnel           messageID = "tunnel.requesting"
	msgCreateTunnelFailed         messageID = "tunnel.create_failed"
	msgConnectingEdge             messageID = "tunnel.connecting_edge"
	msgConnectTimeout             messageID = "tunnel.connect_timeout"
	msgFRPStoppedUnexpectedly     messageID = "tunnel.frp_stopped_unexpectedly"
	msgFRPStopped                 messageID = "tunnel.frp_stopped"
	msgTunnelConnected            messageID = "tunnel.connected"
	msgClientLabel                messageID = "detail.client"
	msgLocalAddressLabel          messageID = "detail.local_address"
	msgPublicAddressLabel         messageID = "detail.public_address"
	msgExpiresAtLabel             messageID = "detail.expires_at"
	msgHTTPRequestInstruction     messageID = "instruction.http_requests"
	msgTrafficInstruction         messageID = "instruction.traffic"
	msgTrafficStats               messageID = "traffic.stats"
	msgUnsupportedMonitorProtocol messageID = "monitor.unsupported_protocol"
	msgStartHTTPMonitorFailed     messageID = "monitor.start_http_failed"
	msgHTTPServiceUnavailable     messageID = "monitor.http_service_unavailable"
	msgHTTPServiceUnavailableBody messageID = "monitor.http_service_unavailable_body"
	msgHTTPMonitorStopped         messageID = "monitor.http_stopped"
	msgStartTCPMonitorFailed      messageID = "monitor.start_tcp_failed"
	msgTCPListenFailed            messageID = "monitor.tcp_listen_failed"
	msgTCPServiceUnavailable      messageID = "monitor.tcp_service_unavailable"
	msgStartUDPMonitorFailed      messageID = "monitor.start_udp_failed"
	msgUDPListenFailed            messageID = "monitor.udp_listen_failed"
	msgUDPServiceUnavailable      messageID = "monitor.udp_service_unavailable"
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
