package main

import (
	"regexp"
	"strings"
	"testing"
)

var formatDirectivePattern = regexp.MustCompile(`%[-+#0-9.]*[a-zA-Z%]`)

func TestNormalizeLocale(t *testing.T) {
	tests := map[string]string{
		"zh_CN.UTF-8": "zh-CN",
		"zh-Hant-HK":  "zh-TW",
		"en_US":       "en",
		"pt-PT":       "pt-BR",
		"ja-JP":       "ja",
	}
	for input, want := range tests {
		got, ok := normalizeLocale(input)
		if !ok || got != want {
			t.Errorf("normalizeLocale(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	if got, ok := normalizeLocale("xx-ZZ"); ok || got != "" {
		t.Fatalf("normalizeLocale for unsupported locale = %q, %v", got, ok)
	}
}

func TestParseLanguageOptions(t *testing.T) {
	lookup := environment(map[string]string{"LANG": "zh_TW.UTF-8"})
	args, localizer, err := parseLanguageOptions([]string{"http", "3000", "--lang", "ja-JP"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if localizer.locale != "ja" {
		t.Fatalf("locale = %q, want ja", localizer.locale)
	}
	if got := strings.Join(args, " "); got != "http 3000" {
		t.Fatalf("remaining args = %q", got)
	}

	_, automatic, err := parseLanguageOptions([]string{"--lang=auto", "tcp", "22"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if automatic.locale != "zh-TW" {
		t.Fatalf("automatic locale = %q, want zh-TW", automatic.locale)
	}
}

func TestParseLanguageOptionsRejectsInvalidValues(t *testing.T) {
	lookup := environment(map[string]string{"NT_LANG": "zh-CN"})
	if _, _, err := parseLanguageOptions([]string{"--lang"}, lookup); err == nil || !strings.Contains(err.Error(), "语言代码") {
		t.Fatalf("missing language error = %v", err)
	}
	if _, _, err := parseLanguageOptions([]string{"--lang=xx"}, lookup); err == nil || !strings.Contains(err.Error(), "xx") {
		t.Fatalf("unsupported language error = %v", err)
	}
}

func TestCatalogsContainEveryEnglishMessage(t *testing.T) {
	english := messageCatalogs["en"]
	for _, locale := range supportedLocales {
		catalog, ok := messageCatalogs[locale]
		if !ok {
			t.Errorf("missing catalog %q", locale)
			continue
		}
		for id := range english {
			if strings.TrimSpace(catalog[id]) == "" {
				t.Errorf("catalog %q is missing %q", locale, id)
			}
			if got, want := formatSignature(catalog[id]), formatSignature(english[id]); got != want {
				t.Errorf("catalog %q message %q format directives = %q, want %q", locale, id, got, want)
			}
		}
	}
}

func TestEveryTranslationLocalizesInteractiveFormHelp(t *testing.T) {
	translations := map[string]map[messageID]string{
		"zh-CN": catalogZhCN,
		"zh-TW": catalogZhTW,
		"es":    catalogES,
		"fr":    catalogFR,
		"de":    catalogDE,
		"ja":    catalogJA,
		"ko":    catalogKO,
		"pt-BR": catalogPtBR,
		"ru":    catalogRU,
		"ar":    catalogAR,
		"hi":    catalogHI,
	}
	for locale, catalog := range translations {
		for _, id := range []messageID{msgProtocolNavigation, msgLocalAddressDefaultHelp, msgPortRangeHelp} {
			if strings.TrimSpace(catalog[id]) == "" {
				t.Errorf("locale %s does not translate %s", locale, id)
			}
		}
	}
}

func formatSignature(value string) string {
	return strings.Join(formatDirectivePattern.FindAllString(value, -1), " ")
}

func TestLocalizedConsoleMessages(t *testing.T) {
	ui := newConsoleUI(&strings.Builder{}, &strings.Builder{})
	ui.setLocalizer(newLocalizer("zh-CN"))
	if got := ui.text(msgInvalidPort); !strings.Contains(got, "本地端口") {
		t.Fatalf("Simplified Chinese message = %q", got)
	}
	ui.setLocalizer(newLocalizer("fr"))
	if got := ui.text(msgInvalidPort); !strings.Contains(got, "port local") {
		t.Fatalf("French message = %q", got)
	}
}

func environment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
