import assert from 'node:assert/strict';
import test from 'node:test';
import { readFile } from 'node:fs/promises';
import { Window } from 'happy-dom';
import { mountConsole } from '../src/console/app.ts';

const id = 'rte_aaaaaaaaaaaaaaaaaaaaaaaaaa';
const secondID = 'rte_bbbbbbbbbbbbbbbbbbbbbbbbbb';
const route = { id, protocol: 'http', subdomain: 'demo', public_url: 'https://demo.tunnel.nodelane.net', status: 'active', created_at: '2026-09-06T00:00:00Z' };
const secondRoute = { ...route, id: secondID, subdomain: 'second', public_url: 'https://second.tunnel.nodelane.net' };
const markup = await readFile(new URL('../dist/console/_shells/en/index.html', import.meta.url), 'utf8');
const flush = () => new Promise(resolve => setImmediate(resolve));
const settle = async () => { await flush(); await flush(); };

async function fixture(t, suffix = '', responder = () => undefined) {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const window = new Window({ url: `https://tunnel.nodelane.net/console/tunnels${suffix}`, settings: { disableJavaScriptFileLoading: true, enableJavaScriptEvaluation: false } });
  window.document.write(markup);
  const network = async (path, options) => {
    const response = await Promise.resolve(responder(path, options));
    if (response !== undefined) return response;
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only', name: 'Alice' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path === '/api/v1/routes') return Response.json({ routes: [route, secondRoute] });
    if (path.endsWith('/stats')) return Response.json({ route_id: path.split('/')[4], availability: 'available', current_connections: 1, upload_bytes_today: 25, download_bytes_today: 12, observed_at: '2026-09-06T00:00:00Z' });
    throw new Error(`unexpected request: ${options.method} ${path}`);
  };
  const originals = new Map();
  for (const [name, value] of Object.entries({ window, document: window.document, navigator: window.navigator, fetch: network })) {
    originals.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
  }
  t.after(async () => {
    window.dispatchEvent(new window.Event('pagehide'));
    await window.happyDOM.abort();
    for (const [name, descriptor] of originals) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  });
  mountConsole(); await settle();
  return window;
}

test('scheduled refresh retains the focused route link while updating live statistics', async t => {
  let connections = 1;
  const window = await fixture(t, '', path => {
    if (path.endsWith(`/${id}/stats`)) return Response.json({ route_id: id, availability: 'available', current_connections: connections, upload_bytes_today: 2048, download_bytes_today: 1024, observed_at: '2026-09-06T00:00:00Z' });
  });
  const document = window.document;
  const link = document.querySelector('#route-rows .route-link');
  const row = link.closest('tr');
  link.focus();
  connections = 7;
  t.mock.timers.tick(5000); await settle();
  assert.equal(link.isConnected, true, 'polling must retain the focused route control');
  assert.ok(document.activeElement === link, 'the same route link must retain keyboard focus');
  assert.equal(row.cells[2].textContent, '7');
  assert.equal(row.cells[3].textContent, '2 KiB');
  assert.equal(document.getElementById('total-connections').textContent, '8');
  let activated;
  document.getElementById('route-rows').addEventListener('click', event => { event.preventDefault(); activated = event.target.closest('a')?.getAttribute('href'); });
  link.click();
  assert.equal(activated, `/console/tunnels/${id}?lang=en`);
});

test('list reconciliation retains route controls when rows are inserted removed and reordered', async t => {
  let list = [route, secondRoute];
  const window = await fixture(t, '', path => {
    if (path === '/api/v1/routes') return Response.json({ routes: list });
  });
  const document = window.document;
  const link = document.querySelector('#route-rows .route-link');
  link.focus();
  list = [secondRoute, route];
  t.mock.timers.tick(5000); await settle();
  assert.ok(document.activeElement === link, 'reordering must preserve the focused route');
  assert.ok(document.querySelectorAll('#route-rows .route-link')[1] === link, 'reordering must reuse the route link');
  const third = { ...route, id: `rte_${'c'.repeat(26)}`, subdomain: 'third', public_url: 'https://third.tunnel.nodelane.net' };
  list = [third, route];
  t.mock.timers.tick(5000); await settle();
  assert.ok(document.activeElement === link, 'insertion and removal must preserve the focused route');
  assert.deepEqual(Array.from(document.querySelectorAll('#route-rows .route-link'), anchor => anchor.textContent), ['third.tunnel.nodelane.net', 'demo.tunnel.nodelane.net']);
  assert.ok(document.querySelectorAll('#route-rows .route-link')[1] === link, 'route controls must remain keyed to route identity');
});

test('Escape returns focus to the same restore button after a background refresh', async t => {
  let restores = 0;
  const deleted = { ...route, status: 'deleted', recoverable_until: new Date(Date.now() + 86400000).toISOString() };
  const window = await fixture(t, '?view=deleted', (path, options) => {
    if (path === '/api/v1/routes') return Response.json({ routes: [] });
    if (path === '/api/v1/routes?deleted=true') return Response.json({ routes: [deleted] });
    if (options.method === 'POST') { restores++; return Response.json(deleted); }
  });
  const document = window.document;
  const restore = document.querySelector('[data-restore]');
  restore.focus(); restore.click();
  const dialog = document.getElementById('confirm-dialog');
  assert.equal(dialog.open, true);
  t.mock.timers.tick(5000); await settle();
  dialog.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  await settle();
  assert.equal(restore.isConnected, true, 'dialog return focus must not reference a discarded row');
  assert.ok(document.activeElement === restore, 'Escape must restore keyboard focus to the invoking button');
  assert.equal(dialog.open, false);
  assert.equal(restores, 0);
  assert.equal(restore.querySelectorAll('svg').length, 1, 'refresh must not duplicate the restore icon');
});

test('ambiguous logout rechecks the session and resumes live updates when still signed in', async t => {
  let logoutAttempted = false;
  let connections = 1;
  let sessionChecks = 0;
  const window = await fixture(t, '', (path, options) => {
    if (path === '/api/v1/session') { sessionChecks++; return Response.json({ authenticated: true, csrf_token: logoutAttempted ? 'fresh-csrf' : 'csrf-only', name: 'Alice' }); }
    if (path === '/auth/logout') { assert.equal(options.method, 'POST'); logoutAttempted = true; throw new Error('response lost'); }
    if (path.endsWith(`/${id}/stats`)) return Response.json({ route_id: id, availability: 'available', current_connections: connections, upload_bytes_today: 25, download_bytes_today: 12, observed_at: '2026-09-06T00:00:00Z' });
  });
  const document = window.document;
  connections = 7;
  document.getElementById('logout').click(); await settle();
  assert.equal(sessionChecks, 2, 'unknown logout outcome must be resolved through the session endpoint');
  assert.equal(document.getElementById('list-view').hidden, false);
  assert.equal(document.querySelector('#route-rows tr').cells[2].textContent, '7');
  connections = 9;
  t.mock.timers.tick(5000); await settle();
  assert.equal(document.querySelector('#route-rows tr').cells[2].textContent, '9');
  assert.equal(document.getElementById('page-alert').hidden, false);
  assert.doesNotMatch(document.getElementById('page-alert').textContent, /Signed out locally/);
});

test('ambiguous logout does not restart login when the session recheck returns 401', async t => {
  let logoutAttempted = false;
  const window = await fixture(t, '', path => {
    if (path === '/auth/logout') { logoutAttempted = true; throw new Error('response lost after logout'); }
    if (path === '/api/v1/session' && logoutAttempted) return Response.json({ error: { code: 'unauthorized' } }, { status: 401 });
  });
  window.document.getElementById('logout').click(); await settle();
  const confirmedSignedOut = window.document.getElementById('list-view').hidden && /Signed out locally/.test(window.document.getElementById('page-alert').textContent);
  assert.equal(confirmedSignedOut, true, 'a confirmed expired session must not leave the signed-in console active');
  assert.equal(window.location.pathname, '/console/tunnels', 'sign-out must not reenter an active provider SSO session');
});

test('failed refresh revocation keeps the confirmed local sign-out visible and stops polling', async t => {
  let logoutAttempted = false;
  let routeRequests = 0;
  const window = await fixture(t, '', path => {
    if (path === '/auth/logout') {
      logoutAttempted = true;
      return Response.json({ error: { code: 'dependency_unavailable' } }, { status: 503 });
    }
    if (path === '/api/v1/session' && logoutAttempted) return Response.json({ authenticated: false });
    if (path === '/api/v1/routes') routeRequests++;
  });
  const document = window.document;
  document.getElementById('logout').click(); await settle();
  assert.equal(window.location.pathname, '/console/tunnels', 'revocation failure must not trigger automatic login');
  for (const id of ['list-view', 'detail-view', 'new-view', 'page-retry', 'page-loading', 'logout']) {
    assert.equal(document.getElementById(id).hidden, true, `${id} must not remain active after local sign-out`);
  }
  assert.match(document.getElementById('page-alert').textContent, /Signed out locally/);
  assert.match(document.getElementById('page-alert').textContent, /not confirmed/);
  assert.equal(document.getElementById('page-alert').hidden, false);
  const requestsAtLogout = routeRequests;
  t.mock.timers.tick(15000); await settle();
  assert.equal(routeRequests, requestsAtLogout, 'local sign-out must not resume authenticated polling');
  assert.equal(window.location.pathname, '/console/tunnels');
});

test('unavailable logout session check offers retry and successful retry restores polling', async t => {
  let logoutAttempted = false;
  let sessionAvailable = false;
  let connections = 1;
  const window = await fixture(t, '', path => {
    if (path === '/auth/logout') { logoutAttempted = true; throw new Error('response lost'); }
    if (path === '/api/v1/session' && logoutAttempted && !sessionAvailable) return Response.json({ error: { code: 'dependency_unavailable' } }, { status: 503 });
    if (path.endsWith(`/${id}/stats`)) return Response.json({ route_id: id, availability: 'available', current_connections: connections, upload_bytes_today: 25, download_bytes_today: 12, observed_at: '2026-09-06T00:00:00Z' });
  });
  const document = window.document;
  document.getElementById('logout').click(); await settle();
  assert.equal(document.getElementById('page-retry').hidden, false, 'unverified logout must have a session-recovery action');
  assert.doesNotMatch(document.getElementById('page-alert').textContent, /Signed out locally/);
  sessionAvailable = true; connections = 7;
  document.getElementById('page-retry').click(); await settle();
  assert.equal(document.getElementById('page-retry').hidden, true);
  assert.equal(document.querySelector('#route-rows tr').cells[2].textContent, '7');
  connections = 9;
  t.mock.timers.tick(5000); await settle();
  assert.equal(document.querySelector('#route-rows tr').cells[2].textContent, '9');
});

test('logout session retry that confirms sign-out never navigates to login', async t => {
  let logoutAttempted = false;
  let sessionAvailable = false;
  const window = await fixture(t, '', path => {
    if (path === '/auth/logout') { logoutAttempted = true; throw new Error('response lost'); }
    if (path === '/api/v1/session' && logoutAttempted) {
      return sessionAvailable
        ? Response.json({ authenticated: false })
        : Response.json({ error: { code: 'dependency_unavailable' } }, { status: 503 });
    }
  });
  const document = window.document;
  document.getElementById('logout').click(); await settle();
  assert.equal(document.getElementById('page-retry').hidden, false);
  sessionAvailable = true;
  document.getElementById('page-retry').click(); await settle();
  assert.equal(window.location.pathname, '/console/tunnels');
  assert.equal(document.getElementById('list-view').hidden, true);
  assert.equal(document.getElementById('page-retry').hidden, true);
  assert.match(document.getElementById('page-alert').textContent, /Signed out locally/);
});
