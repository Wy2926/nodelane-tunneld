import assert from 'node:assert/strict';
import test from 'node:test';
import { Window } from 'happy-dom';
import { mountIdentitySettings } from '../src/console/identity-settings.ts';

test('identity settings return target contains only the current console view and locale', async () => {
  const window = new Window();
  try {
    const link = window.document.createElement('a');
    link.id = 'identity-settings';
    window.document.body.append(link);
    mountIdentitySettings(window.document, 'https://login.test/tenant/oidc', 'ar', 'https://tunnel.test/console/tunnels?view=deleted&secret=not-forwarded#private');
    const destination = new URL(link.href);
    assert.equal(destination.pathname, '/tenant/account/security');
    assert.equal(destination.searchParams.get('redirect'), 'https://tunnel.test/console/tunnels?lang=ar&view=deleted');
    assert.equal(destination.searchParams.get('ui_locales'), 'ar');
    assert.doesNotMatch(link.href, /secret|private|not-forwarded/);
  } finally { await window.happyDOM.abort(); }
});

test('invalid identity configuration removes any previous navigation target', async () => {
  const window = new Window();
  try {
    const link = window.document.createElement('a');
    link.id = 'identity-settings';
    window.document.body.append(link);
    for (const issuer of ['javascript:alert(1)', 'http://login.test/oidc', 'https://user:password@login.test/oidc', 'https://login.test/oidc?token=secret', 'https://login.test/oidc#fragment', 'https://login.test/other', 'not-a-url']) {
      link.href = 'https://previous.test/account/security';
      link.hidden = false;
      mountIdentitySettings(window.document, issuer, 'en', 'https://tunnel.test/console/tunnels');
      assert.equal(link.hidden, true, issuer);
      assert.equal(link.hasAttribute('href'), false, issuer);
    }
  } finally { await window.happyDOM.abort(); }
});
