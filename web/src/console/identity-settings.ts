import type { Locale } from '../i18n/config.ts';
import { consoleLocation, consoleURL } from './model.ts';

export function mountIdentitySettings(document: Document, issuer: string, locale: Locale, currentURL: string): void {
  const link = document.getElementById('identity-settings') as HTMLAnchorElement | null;
  if (!link) return;
  link.hidden = true;
  link.removeAttribute('href');
  try {
    const authority = new URL(issuer);
    const current = new URL(currentURL);
    const page = consoleLocation(currentURL);
    if (authority.protocol !== 'https:' || authority.username || authority.password || authority.search || authority.hash || !authority.pathname.endsWith('/oidc')) return;
    if (!['https:', 'http:'].includes(current.protocol) || current.username || current.password || page.view === 'invalid') return;
    const destination = new URL('account/security', authority);
    const returnPath = consoleURL(locale, page.view === 'new' ? 'new' : page.routeID, page.view === 'deleted');
    destination.searchParams.set('ui_locales', locale);
    destination.searchParams.set('redirect', new URL(returnPath, current.origin).href);
    link.href = destination.href;
    link.hidden = false;
  } catch { /* Invalid deployment identity must not become a navigation target. */ }
}
