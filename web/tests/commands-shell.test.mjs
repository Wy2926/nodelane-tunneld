import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { generateAnonymousCommand, generateLaunchCommand, validateTarget } from '../src/lib/commands.ts';

const nonce = '0123456789abcdef0123456789abcdef';
const code = `nlc_${'a'.repeat(26)}.${'A'.repeat(43)}`;

test('command targets normalize ASCII hosts and reject shell syntax and malformed addresses', () => {
  assert.deepEqual(validateTarget(' LOCALHOST ', 3000), { host: 'localhost', port: 3000 });
  assert.deepEqual(validateTarget('[::1]', 3000), { host: '::1', port: 3000 });
  for (const host of ['x&whoami', '%TEMP%', '!PATH!', 'x"y', "x'y", 'x\ny', '-flag', 'a..b', 'a-', '::invalid', '[localhost]', '256.1.1.1', 'x'.repeat(64)]) {
    assert.throws(() => validateTarget(host, 3000), /invalid_target/);
  }
  for (const port of [0, -0, NaN, 1.5, 65536]) assert.throws(() => validateTarget('localhost', port), /invalid_target/);
  assert.throws(() => generateLaunchCommand('cmd', `${code.slice(0, -1)}B`, 'localhost', 3000), /invalid_launch_code/);
});

test('CMD fresh commands reserve an exclusive random directory before writing the downloaded installer', () => {
  const command = generateAnonymousCommand('cmd', 'tcp', 'localhost', 22, true, { nonce });
  assert.equal(command, `mkdir "%TEMP%\\nodelane-tunnel-${nonce}" && curl.exe -fsSL https://tunnel.nodelane.net/install.cmd -o "%TEMP%\\nodelane-tunnel-${nonce}\\install.cmd" && call "%TEMP%\\nodelane-tunnel-${nonce}\\install.cmd" anonymous "tcp" "localhost" "22"`);
  assert.throws(() => generateAnonymousCommand('cmd', 'http', 'localhost', 3000, true, { nonce: 'bad&nonce' }), /invalid_nonce/);
  const first = generateAnonymousCommand('cmd', 'http', 'localhost', 3000);
  const second = generateAnonymousCommand('cmd', 'http', 'localhost', 3000);
  assert.match(first, /nodelane-tunnel-[0-9a-f]{32}/);
  assert.notEqual(first, second);
  assert.equal(generateLaunchCommand('cmd', code, 'localhost', 3000, false), `nt launch "${code}" "localhost" "3000"`);
});

function run(file, args, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, { env, windowsHide: true, windowsVerbatimArguments: file === 'cmd.exe', timeout: 20000 });
    let output = '';
    child.stdout.on('data', value => { output += value; });
    child.stderr.on('data', value => { output += value; });
    child.on('error', reject);
    child.on('close', status => resolve({ status, output }));
  });
}

test('fresh CMD commands forward anonymous and launch argv and preserve installer failure status', { skip: process.platform !== 'win32' }, async t => {
  const root = await mkdtemp(join(tmpdir(), 'nt command fixture '));
  t.after(() => rm(root, { recursive: true, force: true }));
  const record = join(root, 'argv.txt');
  let downloads = 0;
  const server = createServer((_request, response) => {
    downloads++;
    response.end('@echo off\r\nsetlocal DisableDelayedExpansion\r\n>"%NT_ARGV_RECORD%" echo %~1\r\n>>"%NT_ARGV_RECORD%" echo %~2\r\n>>"%NT_ARGV_RECORD%" echo %~3\r\n>>"%NT_ARGV_RECORD%" echo %~4\r\nexit /b 23\r\n');
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const endpoint = `http://127.0.0.1:${server.address().port}/install.cmd`;
  const env = { ...process.env, TEMP: root, TMP: root, NT_ARGV_RECORD: record };
  for (const [index, expected, builder] of [
    [1, ['anonymous', 'udp', '::1', '5353'], () => generateAnonymousCommand('cmd', 'udp', '::1', 5353, true, { nonce: '1'.repeat(32) })],
    [2, ['launch', code, 'localhost', '3000'], () => generateLaunchCommand('cmd', code, 'localhost', 3000, true, { nonce: '2'.repeat(32) })],
  ]) {
    const command = builder().replace('https://tunnel.nodelane.net/install.cmd', endpoint);
    assert.ok(command.startsWith('mkdir '), 'fresh command must not execute a preinstalled nt from the real PATH');
    const result = await run('cmd.exe', ['/d', '/v:off', '/s', '/c', command], env);
    assert.equal(result.status, 23, result.output);
    assert.deepEqual((await readFile(record, 'utf8')).trim().split(/\r?\n/), expected);
    assert.equal(downloads, index);
    assert.deepEqual(await readdir(join(root, `nodelane-tunnel-${String(index).repeat(32)}`)), ['install.cmd']);
  }
  const reserved = join(root, `nodelane-tunnel-${nonce}`);
  await mkdir(reserved);
  await writeFile(join(reserved, 'install.cmd'), 'owned by someone else');
  const collision = await run('cmd.exe', ['/d', '/v:off', '/s', '/c', generateAnonymousCommand('cmd', 'http', 'localhost', 3000, true, { nonce }).replace('https://tunnel.nodelane.net/install.cmd', endpoint)], env);
  assert.notEqual(collision.status, 0);
  assert.equal(downloads, 2);
  assert.equal(await readFile(join(reserved, 'install.cmd'), 'utf8'), 'owned by someone else');
});

test('PowerShell fresh commands pass an explicit argument array without ending the interactive caller', { skip: process.platform !== 'win32' }, async t => {
  const server = createServer((_request, response) => response.end('param([string[]]$TunnelArguments)\nWrite-Output ("ARGV=" + (ConvertTo-Json -Compress -InputObject @($TunnelArguments)))\n$global:LASTEXITCODE = 23\n'));
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const endpoint = `http://127.0.0.1:${server.address().port}/run.ps1`;
  for (const [command, expected] of [
    [generateAnonymousCommand('powershell', 'tcp', '::1', 22), ['anonymous', 'tcp', '::1', '22']],
    [generateLaunchCommand('powershell', code, 'localhost', 3000), ['launch', code, 'localhost', '3000']],
  ]) {
    const result = await run('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', `${command.replace('https://tunnel.nodelane.net/run.ps1', endpoint)}; Write-Output 'CALLER_ALIVE'; exit $LASTEXITCODE`], process.env);
    assert.equal(result.status, 23, result.output);
    assert.ok(result.output.includes(`ARGV=${JSON.stringify(expected)}`), result.output);
    assert.match(result.output, /CALLER_ALIVE/);
  }
});

test('POSIX fresh commands forward anonymous and launch arguments and preserve the invoked client status', { skip: process.platform !== 'linux' }, async t => {
  const server = createServer((_request, response) => response.end('#!/bin/sh\nprintf "ARG=<%s>\\n" "$@"\nexit 23\n'));
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const endpoint = `http://127.0.0.1:${server.address().port}/run.sh`;
  for (const [command, expected] of [
    [generateAnonymousCommand('linux', 'tcp', '::1', 22), ['anonymous', 'tcp', '::1', '22']],
    [generateLaunchCommand('linux', code, 'localhost', 3000), ['launch', code, 'localhost', '3000']],
  ]) {
    const result = await run('sh', ['-c', command.replace('https://tunnel.nodelane.net/run.sh', endpoint)], process.env);
    assert.equal(result.status, 23, result.output);
    assert.equal(result.output, expected.map(value => `ARG=<${value}>\n`).join(''));
  }
});
