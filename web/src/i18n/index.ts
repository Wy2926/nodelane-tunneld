import ar from "./locales/ar";
import de from "./locales/de";
import en from "./locales/en";
import es from "./locales/es";
import fr from "./locales/fr";
import hi from "./locales/hi";
import ja from "./locales/ja";
import ko from "./locales/ko";
import ptBR from "./locales/pt-BR";
import ru from "./locales/ru";
import zhCN from "./locales/zh-CN";
import zhTW from "./locales/zh-TW";
import type { Locale } from "./config";
import type { Translation } from "./types";

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
} from "./config";
export type { Locale, LocaleDefinition } from "./config";
export type { Translation } from "./types";

export function getTranslation(locale: Locale): Translation {
  return translations[locale];
}
