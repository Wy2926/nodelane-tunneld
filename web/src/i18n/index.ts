import ar from "./locales/ar.ts";
import de from "./locales/de.ts";
import en from "./locales/en.ts";
import es from "./locales/es.ts";
import fr from "./locales/fr.ts";
import hi from "./locales/hi.ts";
import ja from "./locales/ja.ts";
import ko from "./locales/ko.ts";
import ptBR from "./locales/pt-BR.ts";
import ru from "./locales/ru.ts";
import zhCN from "./locales/zh-CN.ts";
import zhTW from "./locales/zh-TW.ts";
import type { Locale } from "./config.ts";
import type { Translation } from "./types.ts";

const translations = {
  "zh-CN": zhCN,
  "zh-TW": zhTW,
  en,
  es,
  fr,
  de,
  ja,
  ko,
  "pt-BR": ptBR,
  ru,
  ar,
  hi,
} satisfies Record<Locale, Translation>;

export {
  defaultLocale,
  getLocaleDefinition,
  getLocalePath,
  localeDefinitions,
  locales,
  localizedLocaleDefinitions,
} from "./config.ts";
export type { Locale, LocaleDefinition } from "./config.ts";
export type { Translation } from "./types.ts";

export function getTranslation(locale: Locale): Translation {
  return translations[locale];
}
