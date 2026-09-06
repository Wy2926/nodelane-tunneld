import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readdir, readFile } from 'node:fs/promises';
import { join, relative, resolve } from 'node:path';
import test from 'node:test';

async function manifest(root) {
  const entries = await readdir(root, { recursive: true, withFileTypes: true });
  const result = new Map();
  for (const entry of entries.filter(entry => entry.isFile())) {
    const path = join(entry.parentPath, entry.name);
    result.set(relative(root, path), createHash('sha256').update(await readFile(path)).digest('hex'));
  }
  return result;
}

test('Go-embedded frontend files exactly match the fresh web build', async () => {
  const built = await manifest(resolve('dist'));
  const embedded = await manifest(resolve('../internal/server/assets/web'));
  assert.ok(built.size > 0, 'a fresh web build is required');
  const paths = new Set([...built.keys(), ...embedded.keys()]);
  const differences = [...paths].filter(path => built.get(path) !== embedded.get(path));
  assert.deepEqual(differences, [], 'rebuild and sync the generated frontend before delivery');
});
