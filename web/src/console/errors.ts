import { APIError } from './api.ts';
import { getTranslation, type Locale, type Translation } from '../i18n/index.ts';

export function errorText(error: unknown, locale: Locale): string {
  const messages = getTranslation(locale).errors;
  const code = error instanceof APIError ? error.code : error instanceof Error ? error.message : '';
  const message = Object.hasOwn(messages, code)
    ? messages[code as keyof Translation['errors']]
    : messages.dependency_unavailable;
  if (error instanceof APIError && error.retryAfter > 0) {
    const delay = new Intl.NumberFormat(locale, { style: 'unit', unit: 'second', unitDisplay: 'short' }).format(error.retryAfter);
    return `${message} (${delay})`;
  }
  return message;
}
