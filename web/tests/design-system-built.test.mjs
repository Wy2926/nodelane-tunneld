import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { Window } from 'happy-dom';

async function render(path, width) {
  const window = new Window({ width, height: 900, settings: { disableJavaScriptFileLoading: true, disableCSSFileLoading: true, enableJavaScriptEvaluation: false } });
  window.document.write(await readFile(new URL(`../dist/${path}`, import.meta.url), 'utf8'));
  for (const link of window.document.querySelectorAll('link[rel="stylesheet"]')) {
    const style = window.document.createElement('style');
    style.textContent = await readFile(new URL(`../dist${link.getAttribute('href')}`, import.meta.url), 'utf8');
    link.replaceWith(style);
  }
  return window;
}

function styles(window, selector, properties) {
  const element = window.document.querySelector(selector);
  assert.ok(element, selector);
  const computed = window.getComputedStyle(element);
  return Object.fromEntries(properties.map(property => [property, computed[property]]));
}

for (const width of [1440, 768, 390, 320]) {
  test(`public and console pages share header, brand and language styles at ${width}px`, async () => {
    const site = await render('zh-cn/index.html', width);
    const console = await render('console/_shells/zh-cn/index.html', width);
    try {
      assert.deepEqual(styles(console, 'header', ['height', 'position', 'backgroundColor', 'borderBottom', 'backdropFilter']), styles(site, 'header', ['height', 'position', 'backgroundColor', 'borderBottom', 'backdropFilter']));
      assert.deepEqual(styles(console, 'header .brand-name, header .console-brand strong', ['fontFamily', 'fontSize', 'fontWeight', 'letterSpacing']), styles(site, 'header .brand-name', ['fontFamily', 'fontSize', 'fontWeight', 'letterSpacing']));
      assert.deepEqual(styles(console, 'header img', ['width', 'height']), styles(site, 'header img', ['width', 'height']));
      assert.deepEqual(styles(console, '#console-language', ['height', 'width', 'fontFamily', 'fontSize', 'border', 'backgroundColor']), styles(site, '[data-language-select]', ['height', 'width', 'fontFamily', 'fontSize', 'border', 'backgroundColor']));
      assert.deepEqual(styles(console, '#console-main', ['width']), styles(site, '.header-inner', ['width']));
    } finally { await site.happyDOM.abort(); await console.happyDOM.abort(); }
  });
}

test('anonymous and registered launch controls use the same visual primitives', async () => {
  const site = await render('index.html', 1440);
  const console = await render('console/_shells/en/index.html', 1440);
  try {
    const button = ['fontFamily', 'fontSize', 'fontWeight', 'minHeight', 'padding', 'border', 'borderRadius', 'backgroundColor', 'color'];
    assert.deepEqual(styles(console, '#copy-launch', button), styles(site, '[data-copy]', button));
    const input = ['fontFamily', 'fontSize', 'minHeight', 'padding', 'border', 'borderRadius', 'backgroundColor', 'color'];
    assert.deepEqual(styles(console, '#local-host', input), styles(site, '[data-host]', input));
    const segment = ['fontFamily', 'fontSize', 'padding', 'border', 'borderRadius', 'backgroundColor', 'color'];
    assert.deepEqual(styles(console, '[data-command-shell="linux"]', segment), styles(site, '[data-os="linux"]', segment));
  } finally { await site.happyDOM.abort(); await console.happyDOM.abort(); }
});
