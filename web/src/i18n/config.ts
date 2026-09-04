export const defaultLocale = "en" as const;

export const localeDefinitions = [
  {
    code: "zh-CN",
    route: "zh-cn",
    nativeName: "简体中文",
    direction: "ltr",
    ogLocale: "zh_CN",
  },
  {
    code: "zh-TW",
    route: "zh-tw",
    nativeName: "繁體中文",
    direction: "ltr",
    ogLocale: "zh_TW",
  },
  {
    code: "en",
    route: "",
    nativeName: "English",
    direction: "ltr",
    ogLocale: "en_US",
  },
  {
    code: "es",
    route: "es",
    nativeName: "Español",
    direction: "ltr",
    ogLocale: "es_ES",
  },
  {
    code: "fr",
    route: "fr",
    nativeName: "Français",
    direction: "ltr",
    ogLocale: "fr_FR",
  },
  {
    code: "de",
    route: "de",
    nativeName: "Deutsch",
    direction: "ltr",
    ogLocale: "de_DE",
  },
  {
    code: "ja",
    route: "ja",
    nativeName: "日本語",
    direction: "ltr",
    ogLocale: "ja_JP",
  },
  {
    code: "ko",
    route: "ko",
    nativeName: "한국어",
    direction: "ltr",
    ogLocale: "ko_KR",
  },
  {
    code: "pt-BR",
    route: "pt-br",
    nativeName: "Português (Brasil)",
    direction: "ltr",
    ogLocale: "pt_BR",
  },
  {
    code: "ru",
    route: "ru",
    nativeName: "Русский",
    direction: "ltr",
    ogLocale: "ru_RU",
  },
  {
    code: "ar",
    route: "ar",
    nativeName: "العربية",
    direction: "rtl",
    ogLocale: "ar_SA",
  },
  {
    code: "hi",
    route: "hi",
    nativeName: "हिन्दी",
    direction: "ltr",
    ogLocale: "hi_IN",
  },
] as const;

export type Locale = (typeof localeDefinitions)[number]["code"];
export type LocaleDefinition = (typeof localeDefinitions)[number];

export const locales = localeDefinitions.map(({ code }) => code) as readonly Locale[];
export const localizedLocaleDefinitions = localeDefinitions.filter(
  ({ code }) => code !== defaultLocale,
);

export function getLocaleDefinition(locale: Locale): LocaleDefinition {
  const definition = localeDefinitions.find(({ code }) => code === locale);
  if (!definition) throw new Error(`Unsupported locale: ${locale}`);
  return definition;
}

export function getLocalePath(locale: Locale): string {
  const { route } = getLocaleDefinition(locale);
  return route ? `/${route}/` : "/";
}
