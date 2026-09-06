import { localeDefinitions, type Locale } from '../i18n/config.ts';

export interface Run {
  id: string; route_id: string; status: 'starting' | 'online' | 'stopping' | 'offline';
  desired_state: 'running' | 'stopped'; created_at: string; stop_requested_at?: string;
  connected_at?: string; lease_expires_at?: string; stop_reason?: string;
}
export interface Route {
  id: string; protocol: 'http'; subdomain: string; public_url: string; status: 'active' | 'deleted';
  created_at: string; current_run?: Run; recoverable_until?: string; name_released_at?: string;
}
export interface Stats {
  route_id: string; availability: 'available' | 'not_observed' | 'unavailable';
  current_connections: number | null; upload_bytes_today: number | null; download_bytes_today: number | null;
  proxy_state?: string | null; observed_at?: string; time_zone?: string;
}
export type View = 'active' | 'deleted' | 'new' | 'detail' | 'invalid';
export const validRouteID = (value: string): boolean => /^rte_[a-z2-7]{26}$/.test(value);
export function canonicalLocale(raw: string | null): Locale {
  return localeDefinitions.find(item => item.code.toLowerCase() === raw?.toLowerCase())?.code ?? 'en';
}
export function consoleLocation(raw: string): { view: View; routeID: string; locale: Locale } {
  const url = new URL(raw, 'https://tunnel.nodelane.net');
  const path = url.pathname.replace(/\/$/, '');
  let view: View = 'invalid';
  let routeID = '';
  if (path === '/console/tunnels') view = url.searchParams.get('view') === 'deleted' ? 'deleted' : 'active';
  else if (path === '/console/tunnels/new') view = 'new';
  else if (path.startsWith('/console/tunnels/') && validRouteID(path.split('/').at(-1) ?? '')) {
    view = 'detail'; routeID = path.split('/').at(-1)!;
  }
  return { view, routeID, locale: canonicalLocale(url.searchParams.get('lang')) };
}
export function consoleURL(locale: Locale, routeID = '', deleted = false): string {
  const path = routeID === 'new' || validRouteID(routeID) ? `/${routeID}` : '';
  const query = new URLSearchParams({ lang: locale });
  if (deleted) query.set('view', 'deleted');
  return `/console/tunnels${path}?${query}`;
}
export function localeURL(raw: string, locale: Locale): string {
  const url = new URL(raw, 'https://tunnel.nodelane.net');
  if (consoleLocation(url.href).view === 'invalid') return consoleURL(locale);
  url.searchParams.set('lang', locale);
  return url.pathname + url.search + url.hash;
}
export function loginURL(raw: string, locale: Locale): string {
  const url = new URL(raw, 'https://tunnel.nodelane.net');
  const returnTo = consoleLocation(url.href).view === 'invalid' ? consoleURL(locale) : url.pathname + url.search;
  return `/auth/login?${new URLSearchParams({ locale, return_to: returnTo })}`;
}
export function validateSubdomain(value: string): 'subdomain_invalid' | 'subdomain_reserved' | null {
  if (!/^[a-z0-9][a-z0-9-]{1,30}[a-z0-9]$/.test(value) || value.startsWith('xn--')) return 'subdomain_invalid';
  if (value.startsWith('anon-') || ['www','auth','api','admin','console','status','support','mail','smtp','frp','tunnel'].includes(value)) return 'subdomain_reserved';
  return null;
}
export function safePublicURL(value: string, subdomain: string, publicDomain = 'tunnel.nodelane.net'): string | null {
  try {
    const url = new URL(value);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.port || url.pathname !== '/' || url.search || url.hash || url.hostname !== `${subdomain}.${publicDomain}` || validateSubdomain(subdomain)) return null;
    return url.href;
  } catch { return null; }
}
export function safeLogoutURL(value: string, issuer: string): string | null {
  try {
    const url = new URL(value), authority = new URL(issuer);
    return url.protocol === 'https:' && authority.protocol === 'https:' && url.origin === authority.origin && !url.username && !url.password ? url.href : null;
  } catch { return null; }
}
export function activeRun(route: Route): boolean { return !!route.current_run && route.current_run.status !== 'offline'; }
export function runState(route: Route, now = Date.now()): string {
  const run = route.current_run;
  // Completed runs are omitted; absence of a current run is not historical evidence.
  if (!run) return 'never';
  if (run.status === 'stopping' && run.stop_requested_at && now - Date.parse(run.stop_requested_at) > 15_000) return 'stop_timeout';
  return run.status;
}
export function summarizeStats(samples: (Stats | null)[]): { connections: number | null; upload: number | null; download: number | null; partial: boolean } {
  const available = samples.filter((sample): sample is Stats => !!sample && sample.availability === 'available' && [sample.current_connections, sample.upload_bytes_today, sample.download_bytes_today].every(value => typeof value === 'number' && Number.isFinite(value) && value >= 0));
  if (!available.length) return { connections: null, upload: null, download: null, partial: samples.length > 0 };
  return { connections: available.reduce((sum, s) => sum + s.current_connections!, 0), upload: available.reduce((sum, s) => sum + s.upload_bytes_today!, 0), download: available.reduce((sum, s) => sum + s.download_bytes_today!, 0), partial: available.length !== samples.length };
}
export function formatNumber(value: number | null | undefined, locale: Locale): string { return value == null ? '--' : new Intl.NumberFormat(locale).format(value); }
export function formatBytes(value: number | null | undefined, locale: Locale): string {
  if (value == null) return '--';
  const exponent = value > 0 ? Math.min(4, Math.floor(Math.log(value) / Math.log(1024))) : 0;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value / 1024 ** exponent)} ${['B','KiB','MiB','GiB','TiB'][exponent]}`;
}
