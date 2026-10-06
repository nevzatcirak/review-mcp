import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import { test } from 'node:test';

import { MAIN_NAME } from '../scripts/build-packages.mjs';
import { HOST_FAKE_BINARY, buildFixture, sourceLauncher } from './helpers.mjs';

const require = createRequire(import.meta.url);
const launcher = require(sourceLauncher);
const isWindows = process.platform === 'win32';

// A fake process object: records exit and kill calls, holds signal listeners.
function fakeProc() {
  const proc = new EventEmitter();
  proc.pid = 4242;
  proc.exited = [];
  proc.killed = [];
  proc.exit = (code) => proc.exited.push(code);
  proc.kill = (pid, signal) => proc.killed.push([pid, signal]);
  return proc;
}

// A fake child: records kill calls; the test emits 'exit' and 'error'.
function fakeChild() {
  const child = new EventEmitter();
  child.killed = [];
  child.kill = (signal) => child.killed.push(signal);
  return child;
}

function runWithFakes(extra = {}) {
  const proc = fakeProc();
  const child = fakeChild();
  const spawned = [];
  const stderr = [];
  launcher.run({
    proc,
    spawn: (file, args, options) => {
      spawned.push({ file, args, options });
      return child;
    },
    writeErr: (text) => stderr.push(text),
    platform: 'linux',
    arch: 'x64',
    env: {},
    argv: ['diag', 'pr', '--flag'],
    mainName: MAIN_NAME,
    resolve: (specifier) => `/resolved/${specifier}`,
    fs: { constants: fs.constants, accessSync() {}, chmodSync() {} },
    ...extra,
  });
  return { proc, child, spawned, stderr };
}

test('platform to package name mapping', () => {
  assert.equal(launcher.platformPackageName(MAIN_NAME, 'linux', 'x64'), `${MAIN_NAME}-linux-x64`);
  assert.equal(launcher.platformPackageName(MAIN_NAME, 'darwin', 'arm64'), `${MAIN_NAME}-darwin-arm64`);
  assert.equal(launcher.platformPackageName(MAIN_NAME, 'win32', 'x64'), `${MAIN_NAME}-win32-x64`);
  assert.equal(launcher.binaryFileName('win32'), 'review-mcp.exe');
  assert.equal(launcher.binaryFileName('linux'), 'review-mcp');
  assert.equal(launcher.binaryFileName('darwin'), 'review-mcp');
});

test('the binary specifier follows the platform and the package name', () => {
  const { spawned } = runWithFakes({ platform: 'win32', arch: 'arm64' });
  assert.equal(spawned[0].file, `/resolved/${MAIN_NAME}-win32-arm64/bin/review-mcp.exe`);
  const linux = runWithFakes();
  assert.equal(linux.spawned[0].file, `/resolved/${MAIN_NAME}-linux-x64/bin/review-mcp`);
});

test('REVIEW_MCP_BINARY overrides the platform package', () => {
  const { spawned } = runWithFakes({
    env: { REVIEW_MCP_BINARY: '/custom/review-mcp' },
    resolve: () => assert.fail('must not resolve the platform package'),
  });
  assert.equal(spawned[0].file, '/custom/review-mcp');
});

test('arguments are passed unchanged with inherited stdio', () => {
  const { spawned } = runWithFakes();
  assert.deepEqual(spawned[0].args, ['diag', 'pr', '--flag']);
  assert.deepEqual(spawned[0].options, { stdio: 'inherit' });
});

test('missing platform package: fixed stderr message, exit 1, nothing spawned', () => {
  const { proc, spawned, stderr } = runWithFakes({
    resolve: () => {
      throw new Error('MODULE_NOT_FOUND with /some/secret/path');
    },
  });
  assert.deepEqual(spawned, []);
  assert.deepEqual(proc.exited, [1]);
  assert.equal(stderr.join(''), launcher.missingPackageMessage(MAIN_NAME, 'linux', 'x64'));
  const text = stderr.join('');
  assert.match(text, new RegExp(`"${MAIN_NAME}-linux-x64"`));
  assert.match(text, /--omit=optional/);
  assert.doesNotMatch(text, /secret/);
});

test('SIGINT, SIGTERM and SIGHUP are forwarded to the child', () => {
  const { proc, child } = runWithFakes();
  proc.emit('SIGINT');
  proc.emit('SIGTERM');
  proc.emit('SIGHUP');
  assert.deepEqual(child.killed, ['SIGINT', 'SIGTERM', 'SIGHUP']);
  assert.deepEqual(proc.exited, []);
});

test('exit code of the child becomes the exit code of the launcher', () => {
  const { proc, child } = runWithFakes();
  child.emit('exit', 3, null);
  assert.deepEqual(proc.exited, [3]);
  assert.equal(proc.listenerCount('SIGINT'), 0, 'signal handlers are removed after the child exits');
});

test('a signal that ended the child is re-raised on the launcher', () => {
  const { proc, child } = runWithFakes();
  child.emit('exit', null, 'SIGTERM');
  assert.deepEqual(proc.killed, [[4242, 'SIGTERM']]);
});

test('a spawn error is reported on stderr with exit 1', () => {
  const { proc, child, stderr } = runWithFakes();
  const err = new Error('spawn /x ENOENT');
  err.code = 'ENOENT';
  child.emit('error', err);
  assert.deepEqual(proc.exited, [1]);
  assert.equal(stderr.join(''), 'review-mcp: failed to start the binary (ENOENT).\n');
});

test('a non-executable packaged binary gets one best-effort chmod 0o755', () => {
  const chmods = [];
  runWithFakes({
    fs: {
      constants: fs.constants,
      accessSync() {
        throw new Error('EACCES');
      },
      chmodSync: (file, mode) => chmods.push([file, mode]),
    },
  });
  assert.deepEqual(chmods, [[`/resolved/${MAIN_NAME}-linux-x64/bin/review-mcp`, 0o755]]);
});

test('no chmod for REVIEW_MCP_BINARY and none on win32', () => {
  const chmods = [];
  const failing = {
    constants: fs.constants,
    accessSync() {
      throw new Error('EACCES');
    },
    chmodSync: (file, mode) => chmods.push([file, mode]),
  };
  runWithFakes({ fs: failing, env: { REVIEW_MCP_BINARY: '/custom/review-mcp' } });
  runWithFakes({ fs: failing, platform: 'win32' });
  assert.deepEqual(chmods, []);
});

// ---- real processes -------------------------------------------------------

// The fake binary is the node executable itself, driven through unchanged
// arguments ("-e <script>"), so it works the same on every platform.
// The source tree has no package.json for the launcher, so these tests use the
// assembled package (a real build of the fixture).
function assembledLauncher() {
  const { mainDir } = buildFixture();
  return path.join(mainDir, 'bin', 'review-mcp.js');
}

const KNOWN_BYTES = [0x66, 0x6f, 0xff, 0xfe, 0x00, 0x80, 0x0d, 0x0a, 0xc3, 0x28, 0x61]; // not UTF-8, ends without a newline

// [canary] stdout purity: stdout of the launcher is exactly the bytes the
// child wrote. A console.log (or any other stdout write) in the launcher adds
// bytes and fails this test.
test('[canary] stdout purity: stdout equals exactly the child bytes', () => {
  const launcherPath = assembledLauncher();
  const script = `process.stdout.write(Buffer.from([${KNOWN_BYTES.join(',')}]))`;
  const result = spawnSync(process.execPath, [launcherPath, '-e', script], {
    env: { ...process.env, REVIEW_MCP_BINARY: process.execPath },
  });
  assert.equal(result.status, 0, result.stderr.toString());
  assert.deepEqual([...result.stdout], KNOWN_BYTES);
  assert.equal(result.stderr.length, 0, 'a successful run writes nothing to stderr either');
});

test('arguments reach the child unchanged', () => {
  const launcherPath = assembledLauncher();
  const script = 'process.stdout.write(JSON.stringify(process.argv.slice(1)))';
  const result = spawnSync(process.execPath, [launcherPath, '-e', script, 'a b', '--flag=1', ''], {
    env: { ...process.env, REVIEW_MCP_BINARY: process.execPath },
  });
  assert.equal(result.status, 0, result.stderr.toString());
  assert.deepEqual(JSON.parse(result.stdout.toString()), ['a b', '--flag=1', '']);
});

test('the exit code of the child is the exit code of the launcher', () => {
  const launcherPath = assembledLauncher();
  const result = spawnSync(process.execPath, [launcherPath, '-e', 'process.exit(5)'], {
    env: { ...process.env, REVIEW_MCP_BINARY: process.execPath },
  });
  assert.equal(result.status, 5);
});

test('missing platform package: stdout stays empty, message on stderr, exit 1', () => {
  const launcherPath = assembledLauncher();
  const env = { ...process.env };
  delete env.REVIEW_MCP_BINARY;
  const result = spawnSync(process.execPath, [launcherPath, 'version'], { env });
  assert.equal(result.status, 1);
  assert.equal(result.stdout.length, 0);
  assert.equal(
    result.stderr.toString(),
    launcher.missingPackageMessage(MAIN_NAME, process.platform, process.arch),
  );
});

test('the platform package is found through node_modules and made executable', { skip: isWindows }, () => {
  const { outDir, mainDir, root } = buildFixture();
  const installed = path.join(root, 'node_modules', ...MAIN_NAME.split('/'));
  fs.mkdirSync(path.dirname(installed), { recursive: true });
  fs.cpSync(mainDir, installed, { recursive: true });
  const platformName = launcher.platformPackageName(MAIN_NAME, process.platform, process.arch);
  const platformDir = path.join(root, 'node_modules', ...platformName.split('/'));
  fs.mkdirSync(path.dirname(platformDir), { recursive: true });
  fs.cpSync(path.join(outDir, path.basename(platformName)), platformDir, { recursive: true });
  const binary = path.join(platformDir, 'bin', 'review-mcp');
  fs.writeFileSync(binary, HOST_FAKE_BINARY, { mode: 0o644 });
  fs.chmodSync(binary, 0o644);

  const env = { ...process.env };
  delete env.REVIEW_MCP_BINARY;
  const result = spawnSync(process.execPath, [path.join(installed, 'bin', 'review-mcp.js'), 'version', 'x'], { env });
  assert.equal(result.status, 0, result.stderr.toString());
  assert.equal(result.stdout.toString(), 'fake-binary:version x');
  assert.notEqual(fs.statSync(binary).mode & 0o111, 0, 'the launcher restored the execute bit');
});

function waitForLine(stream, needle) {
  return new Promise((resolve, reject) => {
    let seen = '';
    stream.on('data', (chunk) => {
      seen += chunk;
      if (seen.includes(needle)) resolve();
    });
    stream.on('end', () => reject(new Error(`stream ended before ${needle}`)));
  });
}

function exitOf(child) {
  return new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));
}

test('real SIGTERM reaches the child and its exit code comes back', { skip: isWindows }, async () => {
  const launcherPath = assembledLauncher();
  const script = "process.on('SIGTERM', () => process.exit(7)); process.stdout.write('ready'); setInterval(() => {}, 1000)";
  const child = spawn(process.execPath, [launcherPath, '-e', script], {
    env: { ...process.env, REVIEW_MCP_BINARY: process.execPath },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  const done = exitOf(child);
  await waitForLine(child.stdout, 'ready');
  child.kill('SIGTERM');
  assert.deepEqual(await done, { code: 7, signal: null });
});

test('a signal that kills the child is re-raised on the launcher', { skip: isWindows }, async () => {
  const launcherPath = assembledLauncher();
  const script = "process.stdout.write('ready'); setInterval(() => {}, 1000)";
  const child = spawn(process.execPath, [launcherPath, '-e', script], {
    env: { ...process.env, REVIEW_MCP_BINARY: process.execPath },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  const done = exitOf(child);
  await waitForLine(child.stdout, 'ready');
  child.kill('SIGHUP');
  assert.deepEqual(await done, { code: null, signal: 'SIGHUP' });
});
