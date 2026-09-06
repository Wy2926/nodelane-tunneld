import assert from 'node:assert/strict';
import test from 'node:test';
import { readFile } from 'node:fs/promises';
import { Window } from 'happy-dom';
import { mountConsole } from '../src/console/app.ts';

const id = 'rte_aaaaaaaaaaaaaaaaaaaaaaaaaa';
const route = { id, protocol: 'http', subdomain: 'demo', public_url: 'https://demo.tunnel.nodelane.net', status: 'active', created_at: '2026-09-06T00:00:00Z' };
const markup = await readFile(new URL('../dist/console/_shells/en/index.html', import.meta.url), 'utf8');
const flush = () => new Promise(resolve => setImmediate(resolve));

test('identity settings links to the configured Logto account center and preserves the console destination', async t => {
  const window = await fixture(t, `/${id}?lang=zh-CN`, async path => {
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path === '/api/v1/routes') return Response.json({ routes: [route] });
    if (path.endsWith('/stats')) return Response.json({ route_id: id, availability: 'not_observed' });
    return Response.json(route);
  });
  const link = window.document.getElementById('identity-settings');
  assert.ok(link, 'identity settings entry is present');
  assert.equal(link.hidden, false);
  const destination = new URL(link.href);
  assert.equal(destination.origin, 'https://identity.test');
  assert.equal(destination.pathname, '/account/security');
  assert.equal(destination.searchParams.get('ui_locales'), 'zh-CN');
  assert.equal(destination.searchParams.get('redirect'), `https://tunnel.nodelane.net/console/tunnels/${id}?lang=zh-CN`);
  assert.equal(destination.searchParams.size, 2);
  assert.ok(link.getAttribute('aria-label'));
});

test('Escape cancels confirmation without issuing a destructive request', async t => {
  let deleted = false;
  const window = await fixture(t, `/${id}`, async (path, options) => {
    if (options.method === 'DELETE') deleted = true;
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path === '/api/v1/routes') return Response.json({ routes: [route] });
    if (path.endsWith('/stats')) return Response.json({ route_id: id, availability: 'not_observed' });
    return Response.json(route);
  });
  const button = window.document.getElementById('delete-route'); button.focus(); button.click();
  const dialog = window.document.getElementById('confirm-dialog');
  assert.equal(dialog.open, true);
  dialog.dispatchEvent(new window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  await flush();
  assert.equal(dialog.open, false); assert.equal(deleted, false);
});

async function fixture(t, suffix, responder) {
  const window = new Window({ url: `https://tunnel.nodelane.net/console/tunnels${suffix}`, settings: { disableJavaScriptFileLoading: true, enableJavaScriptEvaluation: false } });
  window.document.write(markup);
  const originals = new Map();
  for (const [name,value] of Object.entries({ window, document: window.document, navigator: window.navigator, fetch: responder })) {
    originals.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
  }
  t.after(async () => {
    window.dispatchEvent(new window.Event('pagehide'));
    await window.happyDOM.abort();
    for (const [name,descriptor] of originals) { if (descriptor) Object.defineProperty(globalThis, name, descriptor); else delete globalThis[name]; }
  });
  mountConsole(); await flush(); await flush();
  return window;
}

test('built detail shell loads owned route data without issuing a code and submits copy through CSRF', async t => {
  const calls = [];
  const window = await fixture(t, `/${id}?lang=en`, async (path, options) => {
    calls.push([path, options]);
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only', name: '<b>Alice</b>' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path === '/api/v1/routes') return Response.json({ routes: [route] });
    if (path === `/api/v1/routes/${id}`) return Response.json(route);
    if (path.endsWith('/stats')) return Response.json({ route_id: id, availability: 'not_observed', current_connections: null, upload_bytes_today: null, download_bytes_today: null });
    if (path.endsWith('/launch-codes')) return Response.json({ route_id: id, launch_code: `nlc_${'a'.repeat(26)}.${'A'.repeat(43)}`, expires_at: new Date(Date.now() + 600000).toISOString() });
    throw Error(`unexpected path ${path}`);
  });
  const document = window.document;
  assert.equal(document.getElementById('detail-view').hidden, false);
  assert.equal(document.getElementById('account-name').textContent, '<b>Alice</b>');
  assert.equal(document.querySelector('#account-name b'), null);
  assert.equal(document.getElementById('detail-connections').textContent, '--');
  assert.equal(calls.filter(([path]) => path.endsWith('/launch-codes')).length, 0);
  document.getElementById('launch-form').dispatchEvent(new window.Event('submit', { cancelable: true })); await flush(); await flush();
  const launch = calls.find(([path]) => path.endsWith('/launch-codes'));
  assert.equal(launch[1].headers.get('X-CSRF-Token'), 'csrf-only');
  assert.match(document.getElementById('launch-command').textContent, /sh -s -- launch 'nlc_/);
});

test('recently deleted empty state does not offer an unrelated create action', async t => {
  const window = await fixture(t, '?view=deleted', async path => {
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    return Response.json({ routes: [] });
  });
  assert.equal(window.document.getElementById('empty-title').textContent, 'No recently deleted tunnels');
  assert.equal(window.document.getElementById('empty-create').hidden, true);
  assert.equal(window.document.getElementById('account-stats').hidden, true);
});

test('failed logout is not reported as a confirmed local sign-out', async t => {
  const window = await fixture(t, '', async path => {
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path === '/auth/logout') throw new Error('offline before request');
    return Response.json({ routes: [] });
  });
  window.document.getElementById('logout').click(); await flush(); await flush();
  assert.doesNotMatch(window.document.getElementById('page-alert').textContent, /Signed out locally/);
});

test('route-limit failures remain visible while ordinary polling succeeds', async t => {
  const window = await fixture(t, '/new', async path => {
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    return Response.json({ routes: [] });
  });
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (path, options) => options.method === 'POST' ? Response.json({ error: { code: 'subdomain_conflict' } }, { status: 409 }) : originalFetch(path, options);
  const document = window.document;
  document.getElementById('subdomain').value = 'demo';
  document.getElementById('create-form').dispatchEvent(new window.Event('submit', { cancelable: true })); await flush(); await flush();
  assert.equal(document.getElementById('page-alert').hidden, false);
  document.dispatchEvent(new window.Event('visibilitychange')); await flush(); await flush();
  assert.equal(document.getElementById('page-alert').hidden, false);
  assert.equal(document.getElementById('page-alert').textContent, 'This name is already in use.');
});

test('five active routes disable create but never count deleted routes toward the cap', async t => {
  const window = await fixture(t, '?view=deleted', async path => {
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path.endsWith('?deleted=true')) return Response.json({ routes: [] });
    return Response.json({ routes: ['a','b','c','d','e'].map(letter => ({ ...route, id: `rte_${letter.repeat(26)}`, subdomain: `demo-${letter}` })) });
  });
  assert.equal(window.document.getElementById('new-route-link').getAttribute('aria-disabled'), 'true');
  assert.equal(window.document.getElementById('route-count').textContent, '5 / 5');
});

test('stopping an active run requires confirmation and refreshes its authoritative state', async t => {
  let run = { id: `run_${'b'.repeat(26)}`, route_id: id, status: 'online', desired_state: 'running', created_at: '2026-09-06T00:00:00Z' };
  let stops = 0, codes = 0;
  const window = await fixture(t, `/${id}`, async (path, options) => {
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path.endsWith('/runs/current/stop')) { stops++; assert.equal(options.headers.get('X-CSRF-Token'), 'csrf-only'); run = { ...run, status: 'stopping', desired_state: 'stopped', stop_requested_at: new Date().toISOString() }; return Response.json({ run, stopped: true }); }
    if (path.endsWith('/launch-codes')) { codes++; return Response.json({}); }
    if (path === '/api/v1/routes') return Response.json({ routes: [{ ...route, current_run: run }] });
    if (path.endsWith('/stats')) return Response.json({ route_id: id, availability: 'available', current_connections: 1, upload_bytes_today: 25, download_bytes_today: 12, observed_at: '2026-09-06T00:00:00Z' });
    return Response.json({ ...route, current_run: run });
  });
  const document = window.document;
  assert.equal(document.getElementById('copy-launch').disabled, true);
  assert.equal(document.getElementById('active-run-note').hidden, false);
  document.getElementById('stop-run').click(); await flush();
  assert.equal(stops, 0); assert.equal(document.getElementById('confirm-dialog').open, true);
  document.getElementById('confirm-dialog').close('confirm'); await flush(); await flush();
  assert.equal(stops, 1); assert.equal(codes, 0);
  assert.equal(document.getElementById('detail-run-state').textContent, 'Stopping');
  assert.equal(document.getElementById('stop-run').disabled, true);
});

test('each copy issues a new code while its CMD preview keeps the copied nonce', async t => {
  let issuedCount = 0;
  const window = await fixture(t, `/${id}`, async path => {
    if (path === '/api/v1/session') return Response.json({ authenticated: true, csrf_token: 'csrf-only' });
    if (path === '/api/v1/client-config') return Response.json({ public_domain: 'tunnel.nodelane.net', oidc: { issuer: 'https://identity.test/oidc' } });
    if (path.endsWith('/launch-codes')) { issuedCount++; return Response.json({ route_id:id, launch_code:`nlc_${'a'.repeat(26)}.${String.fromCharCode(64+issuedCount).repeat(42)}A`, expires_at:new Date(Date.now()+600000).toISOString() }); }
    if (path === '/api/v1/routes') return Response.json({ routes:[route] });
    if (path.endsWith('/stats')) return Response.json({ route_id:id, availability:'not_observed' });
    return Response.json(route);
  });
  const document = window.document;
  document.querySelector('[data-command-shell="cmd"]').click();
  const form = document.getElementById('launch-form');
  form.dispatchEvent(new window.Event('submit',{cancelable:true})); await flush(); await flush();
  const first = document.getElementById('launch-command').textContent;
  document.getElementById('local-host').dispatchEvent(new window.Event('input'));
  assert.equal(document.getElementById('launch-command').textContent, first);
  form.dispatchEvent(new window.Event('submit',{cancelable:true})); await flush(); await flush();
  assert.equal(issuedCount,2); assert.notEqual(document.getElementById('launch-command').textContent,first);
});
