import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [designSystemCss, tunnelCss] = await Promise.all([
  readFile(new URL("../src/styles/design-system.css", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/tunnel.css", import.meta.url), "utf8"),
]);

const compact = (css) => css.replace(/\s+/g, " ");

test("100% desktop CSS reproduces the approved 150% viewing scale", () => {
  const design = compact(designSystemCss);
  const tunnel = compact(tunnelCss);

  assert.match(design, /--max: 1770px;/);
  assert.match(design, /--header: 84px;/);
  assert.match(design, /font: 400 24px\/1\.65 var\(--sans\);/);
  assert.match(tunnel, /width: min\(var\(--max\), calc\(100% - 96px\)\);/);
  assert.match(tunnel, /font-size: clamp\(69px, 5vw, 96px\);/);
  assert.match(tunnel, /font-size: clamp\(51px, 3\.5vw, 72px\);/);
});

test("responsive thresholds preserve the same physical layout transitions", () => {
  const design = compact(designSystemCss);
  const tunnel = compact(tunnelCss);

  assert.match(design, /@media \(max-width: 1260px\)/);
  assert.match(design, /@media \(max-width: 930px\)/);
  assert.match(design, /@media \(max-width: 690px\)/);
  assert.match(tunnel, /@media \(max-width: 1560px\)/);
  assert.match(tunnel, /@media \(max-width: 1260px\)/);
  assert.match(tunnel, /@media \(max-width: 1020px\)/);
  assert.match(tunnel, /@media \(min-width: 1261px\) and \(max-height: 1350px\)/);
});

test("phone layout reflows enlarged facts and commands instead of clipping text", () => {
  const tunnel = compact(tunnelCss);

  assert.match(tunnel, /@media \(max-width: 690px\)/);
  assert.match(tunnel, /\.hero-facts \{ grid-template-columns: 1fr; gap: 0; \}/);
  assert.match(tunnel, /\.hero-facts div \{ padding: 18px 0; display: grid; grid-template-columns: 120px minmax\(0, 1fr\);/);
  assert.match(tunnel, /\.hero-facts dd \{ margin: 0; font-size: 17px; white-space: normal; \}/);
  assert.match(tunnel, /\.command \{ grid-template-columns: 1fr; \}/);
  assert.match(tunnel, /\.copy \{ grid-column: auto; margin-top: 5px; justify-self: start; \}/);
});
