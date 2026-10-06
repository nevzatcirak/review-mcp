import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

import { MAIN_NAME, SCOPE, buildPackages, parseVersion } from '../scripts/build-packages.mjs';
import { buildFixture, makeDist, repoRoot, tmpDir } from './helpers.mjs';

const PLATFORM_NAMES = ['linux-x64', 'linux-arm64', 'darwin-x64', 'darwin-arm64', 'win32-x64', 'win32-arm64'].map(
  (p) => `${MAIN_NAME}-${p}`,
);
const INSTALL_HOOKS = ['preinstall', 'install', 'postinstall', 'prepare', 'prepublish', 'prepublishOnly', 'prepack', 'postpack'];

function readJSON(...parts) {
  return JSON.parse(fs.readFileSync(path.join(...parts), 'utf8'));
}

// walk lists files under dir, skipping generated build output and node_modules.
function walk(dir, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules' || entry.name === 'build' || entry.name === 'tarballs') continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full, out);
    else out.push(full);
  }
  return out;
}

test('parseVersion strips one leading v and validates the rest', () => {
  assert.equal(parseVersion('v1.2.3'), '1.2.3');
  assert.equal(parseVersion('1.2.3'), '1.2.3');
  assert.equal(parseVersion('v1.0.0-rc.1'), '1.0.0-rc.1');
  assert.equal(parseVersion('v0.0.0-SNAPSHOT-aadced4'), '0.0.0-SNAPSHOT-aadced4');
  for (const bad of ['', 'v', 'vv1.2.3', '1.2', 'v1.2.3.4', '01.2.3', 'v1.2.3+build', 'v1.2.3-', 'v1.2.3-01', 'latest', ' v1.2.3', undefined]) {
    assert.throws(() => parseVersion(bad), /version|tag/, `should reject ${JSON.stringify(bad)}`);
  }
});

test('the seven packages are assembled with lockstep versions', () => {
  const { outDir, mainDir } = buildFixture('1.2.3');
  const main = readJSON(mainDir, 'package.json');
  assert.equal(main.name, MAIN_NAME);
  assert.equal(main.version, '1.2.3');
  assert.equal(main.license, 'MIT');
  assert.deepEqual(main.bin, { 'review-mcp': 'bin/review-mcp.js' });
  assert.equal(main.engines.node, '>=18');
  assert.deepEqual(main.files, ['bin/review-mcp.js', 'LICENSE', 'NOTICE', 'THIRD_PARTY_LICENSES', 'README.md']);
  assert.deepEqual(Object.keys(main.optionalDependencies).sort(), [...PLATFORM_NAMES].sort());
  for (const version of Object.values(main.optionalDependencies)) assert.equal(version, '1.2.3');
  for (const f of main.files) assert.ok(fs.existsSync(path.join(mainDir, f)), `${f} is in the main package`);

  for (const name of PLATFORM_NAMES) {
    const dir = path.join(outDir, name.slice(MAIN_NAME.length - 'review-mcp'.length));
    const pkg = readJSON(dir, 'package.json');
    const [platform, arch] = name.slice(`${MAIN_NAME}-`.length).split('-');
    assert.equal(pkg.name, name);
    assert.equal(pkg.version, '1.2.3');
    assert.equal(pkg.license, 'MIT');
    assert.deepEqual(pkg.os, [platform]);
    assert.deepEqual(pkg.cpu, [arch]);
    const binary = path.join(dir, 'bin', platform === 'win32' ? 'review-mcp.exe' : 'review-mcp');
    assert.ok(fs.existsSync(binary), `${binary} exists`);
    for (const l of ['LICENSE', 'NOTICE', 'THIRD_PARTY_LICENSES']) assert.ok(fs.existsSync(path.join(dir, l)));
    if (process.platform !== 'win32') assert.notEqual(fs.statSync(binary).mode & 0o111, 0, 'binary is executable');
  }

  const manifest = readJSON(outDir, 'manifest.json');
  assert.equal(manifest.version, '1.2.3');
  assert.equal(manifest.prerelease, false);
  assert.deepEqual(
    manifest.packages.map((p) => p.name),
    [...PLATFORM_NAMES, MAIN_NAME],
    'platform packages first, main package last',
  );
});

test('a pre-release tag is marked in the manifest', () => {
  const root = tmpDir();
  const distDir = makeDist(root, '1.0.0-rc.1');
  const { manifest } = buildPackages({ version: 'v1.0.0-rc.1', distDir, outDir: path.join(root, 'build') });
  assert.equal(manifest.prerelease, true);
});

test('a missing binary fails the build and writes nothing', () => {
  for (const missing of ['linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64', 'windows/amd64', 'windows/arm64']) {
    const root = tmpDir();
    const distDir = makeDist(root, '1.2.3', { omit: [missing] });
    const outDir = path.join(root, 'build');
    assert.throws(() => buildPackages({ version: 'v1.2.3', distDir, outDir }), new RegExp(`no ${missing} binary`));
    assert.equal(fs.existsSync(outDir), false, `nothing written when ${missing} is missing`);
  }
});

test('a recorded binary that is not on disk fails the build', () => {
  const root = tmpDir();
  const distDir = makeDist(root, '1.2.3');
  fs.rmSync(path.join(distDir, 'review-mcp_windows_arm64_v1'), { recursive: true });
  assert.throws(
    () => buildPackages({ version: 'v1.2.3', distDir, outDir: path.join(root, 'build') }),
    /windows\/arm64 binary is missing/,
  );
});

test('the version must match the GoReleaser build', () => {
  const root = tmpDir();
  const distDir = makeDist(root, '1.2.3');
  assert.throws(
    () => buildPackages({ version: 'v1.2.4', distDir, outDir: path.join(root, 'build') }),
    /version mismatch/,
  );
});

test('an invalid tag is rejected before anything is read', () => {
  const root = tmpDir();
  assert.throws(() => buildPackages({ version: 'nightly', distDir: path.join(root, 'nope'), outDir: root }), /invalid version/);
});

test('no package.json has install-type scripts (sources and generated packages)', () => {
  const { outDir } = buildFixture('1.2.3');
  const files = [...walk(path.join(repoRoot, 'npm')), ...walk(outDir)].filter((f) => path.basename(f) === 'package.json');
  assert.ok(files.length >= 7, 'the seven generated package.json files are checked');
  for (const file of files) {
    const scripts = readJSON(file).scripts || {};
    for (const hook of INSTALL_HOOKS) assert.equal(scripts[hook], undefined, `${file} must not define ${hook}`);
    assert.equal(readJSON(file).gypfile, undefined, `${file} must not trigger node-gyp`);
  }
});

test('the npm scope is written down once, in the build script', () => {
  const hits = walk(path.join(repoRoot, 'npm')).filter((f) => fs.readFileSync(f, 'utf8').includes(SCOPE));
  assert.deepEqual(
    hits.map((f) => path.relative(repoRoot, f).split(path.sep).join('/')),
    ['npm/scripts/build-packages.mjs'],
  );
});
