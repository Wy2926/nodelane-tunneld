import { validRouteID, type Route, type Stats } from './model.ts';

export interface Session { authenticated: true; csrf_token: string; account_id?: string; name?: string; email?: string }
export interface LaunchCode { launch_code: string; route_id: string; expires_at: string }
type Fetch = (input: string, init: RequestInit) => Promise<Response>;
export class APIError extends Error {
  code: string; status: number; retryAfter: number;
  constructor(code: string, status = 0, retryAfter = 0) { super(code); this.name = 'APIError'; this.code = code; this.status = status; this.retryAfter = retryAfter; }
}
export class ConsoleAPI {
  #fetch: Fetch; #csrf = '';
  constructor(fetcher: Fetch = (path, init) => fetch(path, init)) { this.#fetch = fetcher; }
  async request<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal, key?: string): Promise<T> {
    const headers = new Headers({ Accept: 'application/json' });
    if (method !== 'GET') {
      if (!this.#csrf) throw new APIError('unauthorized', 401);
      headers.set('Content-Type', 'application/json'); headers.set('X-CSRF-Token', this.#csrf);
      if (key) headers.set('Idempotency-Key', key);
    }
    const controller = new AbortController();
    const abort = () => controller.abort();
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) controller.abort();
    const timeout = setTimeout(abort, 15_000);
    try {
      const response = await this.#fetch(path, { method, headers, credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: controller.signal, body: method === 'GET' ? undefined : JSON.stringify(body ?? {}) });
      const data = await boundedJSON(response);
      if (!response.ok) {
        const error = isObject(data) && isObject(data.error) ? data.error : {};
        throw new APIError(typeof error.code === 'string' ? error.code : 'dependency_unavailable', response.status, Math.max(0, Number(response.headers.get('Retry-After')) || 0));
      }
      return data as T;
    } catch (error) { if (signal?.aborted || error instanceof APIError) throw error; throw new APIError('dependency_unavailable'); }
    finally { clearTimeout(timeout); signal?.removeEventListener('abort', abort); }
  }
  async session(signal?: AbortSignal): Promise<Session> {
    const session = await this.request<Session>('/api/v1/session', 'GET', undefined, signal);
    if (session?.authenticated !== true || typeof session.csrf_token !== 'string' || !session.csrf_token) throw new APIError('unauthorized', 401);
    this.#csrf = session.csrf_token; return session;
  }
  async routes(deleted = false, signal?: AbortSignal): Promise<Route[]> {
    const result = await this.request<{ routes: Route[] }>(`/api/v1/routes${deleted ? '?deleted=true' : ''}`, 'GET', undefined, signal);
    if (!Array.isArray(result?.routes) || !result.routes.every(validRoute)) throw new APIError('dependency_unavailable');
    return result.routes;
  }
  async route(id: string, signal?: AbortSignal): Promise<Route> {
    const route = await this.request<Route>(this.path(id), 'GET', undefined, signal);
    if (!validRoute(route) || route.id !== id) throw new APIError('dependency_unavailable');
    return route;
  }
  async stats(id: string, signal?: AbortSignal): Promise<Stats> {
    const sample = await this.request<Stats>(this.path(id) + '/stats', 'GET', undefined, signal);
    if (sample?.route_id !== id || !['available','not_observed','unavailable'].includes(sample.availability)) throw new APIError('dependency_unavailable');
    if (sample.availability === 'available' && ![sample.current_connections, sample.upload_bytes_today, sample.download_bytes_today].every(number => typeof number === 'number' && Number.isFinite(number) && number >= 0)) throw new APIError('dependency_unavailable');
    return sample;
  }
  create(subdomain: string, key: string): Promise<{ route: Route }> { return this.request('/api/v1/routes', 'POST', { protocol: 'http', subdomain }, undefined, key); }
  launch(id: string): Promise<LaunchCode> { return this.request(this.path(id) + '/launch-codes', 'POST'); }
  stop(id: string): Promise<unknown> { return this.request(this.path(id) + '/runs/current/stop', 'POST'); }
  delete(id: string): Promise<Route> { return this.request(this.path(id), 'DELETE'); }
  restore(id: string): Promise<Route> { return this.request(this.path(id) + '/restore', 'POST'); }
  logout(): Promise<{ logged_out: boolean; end_session_url: string }> { return this.request('/auth/logout', 'POST'); }
  async config(signal?: AbortSignal): Promise<{ public_domain: string; oidc: { issuer: string } }> {
    const config = await this.request<{ public_domain: string; oidc: { issuer: string } }>('/api/v1/client-config', 'GET', undefined, signal);
    if (typeof config.public_domain !== 'string' || config.public_domain.length > 253 || !/^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/.test(config.public_domain) || !config.public_domain.includes('.') || typeof config.oidc?.issuer !== 'string') throw new APIError('dependency_unavailable');
    return config;
  }
  private path(id: string): string { if (!validRouteID(id)) throw new APIError('route_not_found', 404); return `/api/v1/routes/${id}`; }
}
function validRoute(route: Route): boolean {
  return !!route && validRouteID(route.id) && route.protocol === 'http' && typeof route.subdomain === 'string' && typeof route.public_url === 'string' && ['active','deleted'].includes(route.status) && (!route.current_run || /^run_[a-z2-7]{26}$/.test(route.current_run.id) && route.current_run.route_id === route.id && ['starting','online','stopping','offline'].includes(route.current_run.status));
}
function isObject(value: unknown): value is Record<string, unknown> { return !!value && typeof value === 'object' && !Array.isArray(value); }
async function boundedJSON(response: Response): Promise<unknown> {
  if (response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase() !== 'application/json' || !response.body) throw new APIError('dependency_unavailable', response.status);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let size = 0, content = '';
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      size += chunk.value.byteLength;
      if (size > 1048576) { await reader.cancel(); throw new APIError('dependency_unavailable', response.status); }
      content += decoder.decode(chunk.value, { stream: true });
    }
    return JSON.parse(content + decoder.decode());
  } finally { reader.releaseLock(); }
}
