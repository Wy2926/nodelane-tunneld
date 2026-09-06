import assert from 'node:assert/strict';
import test from 'node:test';
import { Window } from 'happy-dom';
import { generateAnonymousCommand, generateLaunchCommand, validateTarget } from '../src/lib/commands.ts';
import { consoleLocation, localeURL, validateSubdomain, summarizeStats, runState, safePublicURL, safeLogoutURL } from '../src/console/model.ts';
import { ConsoleAPI, APIError } from '../src/console/api.ts';
import { LatestRequest, pollVisible } from '../src/console/polling.ts';
import { copyCommand, routeRow } from '../src/console/dom.ts';
import { getTranslation } from '../src/i18n/index.ts';
import { initializeQuickStart } from '../src/lib/quickstart.ts';
import { errorText } from '../src/console/errors.ts';

const id = 'rte_aaaaaaaaaaaaaaaaaaaaaaaaaa';
const route = { id, protocol: 'http', subdomain: 'demo', public_url: 'https://demo.tunnel.nodelane.net', status: 'active', created_at: '2026-09-06T00:00:00Z' };

test('shell commands keep anonymous parameters separate and reject injection targets', () => {
  assert.equal(generateAnonymousCommand('linux', 'http', 'localhost', 3000, false), "nt anonymous 'http' 'localhost' '3000'");
  assert.equal(generateAnonymousCommand('powershell', 'tcp', '::1', 22, false), "nt anonymous 'tcp' '::1' '22'");
  assert.equal(generateAnonymousCommand('cmd', 'udp', '127.0.0.1', 5353, false), 'nt anonymous "udp" "127.0.0.1" "5353"');
  for (const host of ['x;id', 'x&whoami', '%PATH%', '$(id)', '-flag', 'foo\nbar', 'https://foo']) assert.throws(() => validateTarget(host, 3000));
  for (const port of [0, 65536, 3.5, NaN]) assert.throws(() => validateTarget('localhost', port));
  assert.match(generateAnonymousCommand('linux', 'http', '', 3000), /\| sh -s -- anonymous 'http' 'localhost' '3000'$/);
});

test('launch commands only accept the documented single-use code shape', () => {
  const token = `nlc_${'a'.repeat(26)}.${'A'.repeat(43)}`;
  assert.equal(generateLaunchCommand('linux', token, 'localhost', 3000, false), `nt launch '${token}' 'localhost' '3000'`);
  assert.throws(() => generateLaunchCommand('cmd', 'bad&code', 'localhost', 3000));
});

test('console routing preserves identifiers and unrelated query values on language change', () => {
  const source = `https://tunnel.nodelane.net/console/tunnels/${id}?view=deleted&lang=zh-CN&x=1`;
  assert.deepEqual(consoleLocation(source), { view: 'detail', routeID: id, locale: 'zh-CN' });
  assert.equal(localeURL(source, 'ar'), `/console/tunnels/${id}?view=deleted&lang=ar&x=1`);
  assert.equal(consoleLocation('https://tunnel.nodelane.net/console/tunnels/new').view, 'new');
  assert.equal(consoleLocation('https://tunnel.nodelane.net/console/tunnels?view=deleted').view, 'deleted');
  assert.equal(consoleLocation('https://tunnel.nodelane.net/console/tunnels/not-a-route').view, 'invalid');
  assert.equal(safePublicURL('javascript:alert(1)', 'demo'), null);
  assert.equal(safePublicURL('https://evil.test', 'demo'), null);
  assert.equal(safePublicURL('https://demo.tunnel.test/', 'demo', 'tunnel.test'), 'https://demo.tunnel.test/');
  assert.equal(safePublicURL('https://demo.tunnel.nodelane.net/', 'demo', 'tunnel.test'), null);
});

test('domain validation matches reserved namespaces and strict ASCII label boundaries', () => {
  assert.equal(validateSubdomain('demo-123'), null);
  for (const value of ['ab', 'a'.repeat(33), '-abc', 'abc-', 'ABC', 'x.y', 'xn--abc', '测试']) assert.equal(validateSubdomain(value), 'subdomain_invalid');
  for (const value of ['www', 'auth', 'api', 'console', 'anon-demo']) assert.equal(validateSubdomain(value), 'subdomain_reserved');
});

test('statistics never convert missing or unavailable samples into zero', () => {
  const good = { availability: 'available', current_connections: 2, upload_bytes_today: 100, download_bytes_today: 20 };
  assert.deepEqual(summarizeStats([good, { availability: 'unavailable', current_connections: null, upload_bytes_today: null, download_bytes_today: null }]), { connections: 2, upload: 100, download: 20, partial: true });
  assert.deepEqual(summarizeStats([]), { connections: null, upload: null, download: null, partial: false });
  assert.equal(runState({ ...route, current_run: { status: 'stopping', stop_requested_at: '2026-09-06T00:00:00Z' } }, Date.parse('2026-09-06T00:00:16Z')), 'stop_timeout');
});

test('API writes are same-origin CSRF protected and preserve an explicit idempotency key', async () => {
  const calls = [];
  const api = new ConsoleAPI(async (path, options) => { calls.push([path, options]); return Response.json(path === '/api/v1/session' ? { authenticated: true, csrf_token: 'csrf-only', name: 'Person' } : { route, replayed: false }); });
  await api.session();
  await api.create('demo', 'same-key');
  assert.equal(calls[1][0], '/api/v1/routes');
  assert.equal(calls[1][1].credentials, 'same-origin');
  assert.equal(calls[1][1].headers.get('X-CSRF-Token'), 'csrf-only');
  assert.equal(calls[1][1].headers.get('Idempotency-Key'), 'same-key');
  assert.deepEqual(JSON.parse(calls[1][1].body), { protocol: 'http', subdomain: 'demo' });
  assert.equal(calls[1][1].headers.has('Authorization'), false);
});

test('API errors retain stable code and retry-after but not server-provided message text', async () => {
  const api = new ConsoleAPI(async () => Response.json({ error: { code: 'rate_limited', message: '<script>secret</script>' } }, { status: 429, headers: { 'Retry-After': '12' } }));
  await assert.rejects(api.routes(), (error) => error instanceof APIError && error.code === 'rate_limited' && error.retryAfter === 12 && !error.message.includes('script'));
  const anonymous = new ConsoleAPI(async () => Response.json({ authenticated: false }));
  await assert.rejects(anonymous.session(), (error) => error.code === 'unauthorized');
});

test('superseded reads are aborted and cannot overwrite a newer view', async () => {
  const latest = new LatestRequest();
  const committed = [];
  let finishOld;
  let oldSignal;
  const old = latest.run((signal) => { oldSignal = signal; return new Promise(resolve => { finishOld = resolve; }); }, value => committed.push(value));
  await latest.run(async () => 'new', value => committed.push(value));
  finishOld('old');
  await old;
  assert.equal(oldSignal.aborted, true);
  assert.deepEqual(committed, ['new']);
});

test('DOM renders route labels as text and manual clipboard fallback remains visible', async () => {
  const window = new Window();
  const document = window.document;
  const copy = getTranslation('en').console;
  const row = routeRow(document, { ...route, subdomain: '<img src=x onerror=alert(1)>' }, null, copy, 'en', false);
  document.body.append(row);
  assert.equal(document.querySelector('img'), null);
  assert.match(row.textContent, /<img src=x/);
  const area = document.createElement('textarea'); document.body.append(area);
  assert.equal(await copyCommand('nt anonymous http localhost 3000', area, { writeText: async () => { throw Error('denied'); } }), false);
  assert.equal(area.hidden, false);
  assert.equal(area.value, 'nt anonymous http localhost 3000');
  assert.equal(document.activeElement, area);
  window.happyDOM.abort();
});

test('logout redirect trusts only the configured HTTPS issuer origin', () => {
  assert.equal(safeLogoutURL('https://identity.staging.test/oidc/session/end?a=1', 'https://identity.staging.test/oidc'), 'https://identity.staging.test/oidc/session/end?a=1');
  for (const url of ['https://other.test/end', 'javascript:alert(1)', 'http://identity.staging.test/end', 'https://user:password@identity.staging.test/end']) assert.equal(safeLogoutURL(url, 'https://identity.staging.test/oidc'), null);
});

test('visibility changes cancel obsolete polling cycles without creating duplicate timers', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const window = new Window();
  let hidden = false;
  Object.defineProperty(window.document, 'hidden', { get: () => hidden });
  let calls = 0, release;
  const stop = pollVisible(window.document, () => { calls++; return calls === 1 ? new Promise(resolve => { release = resolve; }) : Promise.resolve(); }, () => {});
  hidden = true; window.document.dispatchEvent(new window.Event('visibilitychange'));
  hidden = false; window.document.dispatchEvent(new window.Event('visibilitychange'));
  release(); await Promise.resolve(); await Promise.resolve();
  t.mock.timers.tick(5000); await Promise.resolve(); await Promise.resolve();
  assert.equal(calls, 3);
  hidden = true; window.document.dispatchEvent(new window.Event('visibilitychange'));
  t.mock.timers.tick(15000); assert.equal(calls, 3);
  stop(); await window.happyDOM.abort();
});

test('anonymous builder responds to local target edits and provides visible manual copying', async () => {
  const window = new Window();
  const root = window.document.createElement('section'); root.dataset.locale = 'en';
  root.innerHTML = '<select data-protocol><option>http</option><option>tcp</option></select><input data-host value="localhost"><input data-port value="3000"><button data-os="linux"></button><button data-os="windows"></button><button data-shell="powershell"></button><button data-shell="cmd"></button><div data-shell-group></div><code data-command></code><button data-copy></button><span data-command-status></span><textarea data-manual hidden></textarea>';
  window.document.body.append(root);
  initializeQuickStart(root, undefined, 'Linux');
  assert.match(root.querySelector('[data-command]').textContent, /anonymous 'http' 'localhost' '3000'$/);
  root.querySelector('[data-host]').value = '::1'; root.querySelector('[data-host]').dispatchEvent(new window.Event('input'));
  assert.match(root.querySelector('[data-command]').textContent, /'::1' '3000'$/);
  root.querySelector('[data-copy]').click(); await Promise.resolve(); await Promise.resolve();
  assert.equal(root.querySelector('[data-manual]').hidden, false);
  root.querySelector('[data-host]').value = 'x&whoami'; root.querySelector('[data-host]').dispatchEvent(new window.Event('input'));
  assert.equal(root.querySelector('[data-copy]').disabled, true);
  assert.equal(root.querySelector('[data-command]').textContent, '');
  await window.happyDOM.abort();
});

test('launch errors retain distinct user-facing expired used and revoked states', () => {
  assert.equal(errorText(new APIError('launch_code_used', 410), 'en'), 'This launch code has already been used.');
  assert.equal(errorText(new APIError('launch_code_expired', 410), 'zh-CN'), '启动码已过期。');
  assert.notEqual(errorText(new APIError('launch_code_revoked', 410), 'en'), errorText(new APIError('launch_code_used', 410), 'en'));
  assert.equal(errorText(new APIError('new_server_error', 503), 'en'), 'Service unavailable. Try again.');
});

test('API boundary rejects non-JSON and oversized responses before trusting session data', async () => {
  const textAPI = new ConsoleAPI(async () => new Response('{"authenticated":true,"csrf_token":"x"}', { headers: { 'Content-Type': 'text/html' } }));
  await assert.rejects(textAPI.session(), error => error.code === 'dependency_unavailable');
  const oversized = new ConsoleAPI(async () => Response.json({ authenticated: true, csrf_token: 'x', name: 'x'.repeat(1048576) }));
  await assert.rejects(oversized.session(), error => error.code === 'dependency_unavailable');
});

test('malformed route IDs and active run identities are rejected at the API boundary', async () => {
  const api = new ConsoleAPI(async () => Response.json({ routes: [{ ...route, current_run: { id: 'wrong', route_id: 'other', status: 'online' } }] }));
  await assert.rejects(api.routes(), error => error.code === 'dependency_unavailable');
});
