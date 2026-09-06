import assert from 'node:assert/strict';
import test from 'node:test';
import { getTranslation, locales } from '../src/i18n/index.ts';
import { APIError } from '../src/console/api.ts';
import { errorText } from '../src/console/errors.ts';

test('the central translation entry supplies complete console and error catalogs for every locale', () => {
  const english = getTranslation('en');
  assert.ok(english.console, 'Console labels must be part of the central catalog');
  assert.ok(english.errors, 'API errors must be part of the central catalog');
  for (const locale of locales) {
    const copy = getTranslation(locale);
    for (const section of ['console', 'errors']) {
      assert.ok(copy[section], `Missing ${locale}.${section}`);
      assert.deepEqual(Object.keys(copy[section]).sort(), Object.keys(english[section]).sort(), `${locale}.${section}`);
      for (const [key, value] of Object.entries(copy[section])) {
        assert.equal(typeof value, 'string', `${locale}.${section}.${key}`);
        assert.notEqual(value.trim(), '', `${locale}.${section}.${key}`);
      }
    }
    if (locale !== 'en') assert.notEqual(copy.console.title, english.console.title, locale);
  }
});

test('identity settings has a localized label in every supported console locale', () => {
  const englishLabel = getTranslation('en').console?.identitySettings;
  for (const locale of locales) {
    const label = getTranslation(locale).console?.identitySettings;
    assert.equal(typeof label, 'string', locale);
    assert.notEqual(label.trim(), '', locale);
    if (locale !== 'en') assert.notEqual(label, englishLabel, locale);
  }
});

test('all API error codes resolve through the central error catalog without becoming console labels', () => {
  const codes = [
    'subdomain_invalid', 'subdomain_reserved', 'subdomain_conflict', 'route_limit_reached',
    'route_not_found', 'invalid_target', 'dependency_unavailable', 'insufficient_scope',
    'rate_limited', 'unauthorized', 'invalid_request', 'route_deleted', 'run_already_active',
    'run_stopped', 'idempotency_conflict', 'launch_code_expired', 'launch_code_used', 'launch_code_revoked',
  ];
  for (const locale of locales) {
    const copy = getTranslation(locale);
    assert.ok(copy.errors, `Missing ${locale}.errors`);
    for (const code of codes) {
      assert.ok(Object.hasOwn(copy.errors, code), `${locale}.errors.${code}`);
      assert.equal(Object.hasOwn(copy.console, code), false, `${locale}.console.${code}`);
      assert.equal(errorText(new APIError(code, 400), locale), copy.errors[code]);
    }
  }
});

test('unknown error codes never resolve a console label or inherited object property', () => {
  for (const locale of locales) {
    const copy = getTranslation(locale);
    assert.ok(copy.errors, `Missing ${locale}.errors`);
    for (const code of ['title', 'copy', '__proto__', 'constructor', 'toString', 'new_server_error']) {
      assert.equal(errorText(new APIError(code, 503), locale), copy.errors.dependency_unavailable);
    }
  }
});
