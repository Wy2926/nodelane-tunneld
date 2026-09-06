import type { Locale, Translation } from '../i18n/index.ts';
import { consoleURL, formatBytes, formatNumber, runState, type Route, type Stats } from './model.ts';

export function text(document: Document, tag: string, content: string, className = ''): HTMLElement {
  const element = document.createElement(tag); element.textContent = content; element.className = className; return element;
}
export function routeRow(document: Document, route: Route, stats: Stats | null, copy: Translation['console'], locale: Locale, deleted: boolean, publicDomain = 'tunnel.nodelane.net'): HTMLTableRowElement {
  const row = document.createElement('tr');
  row.append(...Array.from({ length: deleted ? 5 : 6 }, () => document.createElement('td')));
  const link = document.createElement('a'); link.className = 'route-link'; link.dir = 'ltr'; row.cells[0].append(link);
  row.cells[1].append(text(document, 'span', ''));
  updateRouteRow(row, route, stats, copy, locale, deleted, publicDomain);
  return row;
}
export function updateRouteRow(row: HTMLTableRowElement, route: Route, stats: Stats | null, copy: Translation['console'], locale: Locale, deleted: boolean, publicDomain = 'tunnel.nodelane.net'): void {
  const document = row.ownerDocument;
  row.dataset.routeId = route.id;
  const link = row.cells[0].firstElementChild as HTMLAnchorElement;
  link.href = consoleURL(locale, route.id); link.textContent = `${route.subdomain}.${publicDomain}`;
  const state = deleted ? (route.name_released_at ? 'nameReleased' : 'routeDeleted') : runState(route);
  const status = row.cells[1].firstElementChild as HTMLElement;
  status.textContent = copy[state as keyof Translation['console']] ?? copy.unavailable; status.className = `state state-${state}`;
  if (deleted) {
    const until = route.recoverable_until ? new Date(route.recoverable_until) : null;
    row.cells[2].textContent = until ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(until) : '--';
    const remaining = until ? Math.max(0, Math.ceil((until.getTime() - Date.now()) / 86400000)) : 0;
    row.cells[3].textContent = route.name_released_at || remaining === 0 ? copy.nameReleased : new Intl.NumberFormat(locale, { style: 'unit', unit: 'day', unitDisplay: 'short' }).format(remaining);
  } else {
    const usable = stats?.availability === 'available';
    const values = [usable ? formatNumber(stats.current_connections, locale) : '--', usable ? formatBytes(stats.upload_bytes_today, locale) : '--', usable ? formatBytes(stats.download_bytes_today, locale) : '--'];
    values.forEach((value, index) => { row.cells[index + 2].textContent = value; row.cells[index + 2].className = 'number'; });
  }
  const actions = row.cells[row.cells.length - 1];
  if (deleted && !route.name_released_at && route.recoverable_until && Date.parse(route.recoverable_until) > Date.now()) {
    let restore = actions.querySelector<HTMLButtonElement>('[data-restore]');
    if (!restore) { restore = document.createElement('button'); restore.type = 'button'; restore.className = 'button compact'; restore.textContent = copy.restore; actions.replaceChildren(restore); }
    restore.dataset.restore = route.id; restore.disabled = false;
  } else {
    let detail = actions.querySelector<HTMLAnchorElement>('a');
    if (!detail) { detail = document.createElement('a'); detail.className = 'text-link'; actions.replaceChildren(detail); }
    detail.href = consoleURL(locale, route.id); detail.textContent = copy.details;
  }
}
export async function copyCommand(command: string, fallback: HTMLTextAreaElement, clipboard?: Pick<Clipboard, 'writeText'>): Promise<boolean> {
  fallback.value = command;
  try { if (!clipboard) throw Error('clipboard_unavailable'); await clipboard.writeText(command); fallback.hidden = true; return true; }
  catch { fallback.hidden = false; fallback.focus(); fallback.select(); return false; }
}
