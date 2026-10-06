#!/usr/bin/env node
// End-to-end smoke test of the assembled npm packages on the current OS.
//
// Usage: node npm/scripts/smoke.mjs <build-dir> <work-dir>
//
//   <build-dir>  output of build-packages.mjs (contains manifest.json)
//   <work-dir>   a directory that does not exist yet or is empty; tarballs go to
//                <work-dir>/tarballs and the install happens in <work-dir>/install
//
// Steps: npm pack every package, install the main package and the package for
// this platform into an empty directory with --ignore-scripts, run
// "npx review-mcp version", then an MCP initialize -> tools/list round trip
// through the launcher (mcp-roundtrip.mjs). Nothing here publishes anything.

import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const shell = process.platform === 'win32'; // npm and npx are .cmd shims on Windows

function fail(message) {
  process.stderr.write(`smoke: ${message}\n`);
  process.exit(1);
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: 'utf8', shell, ...options });
  if (result.error) fail(`${command} could not start: ${result.error.code || result.error.message}`);
  return result;
}

const [buildArg, workArg] = process.argv.slice(2);
if (!buildArg || !workArg) fail('usage: smoke.mjs <build-dir> <work-dir>');
const buildDir = path.resolve(buildArg);
const workDir = path.resolve(workArg);
const manifestFile = path.join(buildDir, 'manifest.json');
const manifest = JSON.parse(fs.readFileSync(manifestFile, 'utf8'));

const tarballDir = path.join(workDir, 'tarballs');
const installDir = path.join(workDir, 'install');
fs.mkdirSync(tarballDir, { recursive: true });
fs.mkdirSync(installDir, { recursive: true });
if (fs.readdirSync(installDir).length !== 0) fail(`${installDir} is not empty`);

// 1. npm pack every package.
const tarballs = new Map();
for (const pkg of manifest.packages) {
  const result = run('npm', ['pack', path.join(buildDir, pkg.dir), '--pack-destination', tarballDir, '--json', '--ignore-scripts']);
  if (result.status !== 0) fail(`npm pack ${pkg.name} failed:\n${result.stderr}`);
  const [info] = JSON.parse(result.stdout);
  tarballs.set(pkg.name, path.join(tarballDir, info.filename));
  process.stderr.write(`packed ${pkg.name}: ${info.filename} (${info.size} bytes, ${info.entryCount} files)\n`);
}

// 2. Install the main package and this platform's package into an empty directory.
const mainPkg = manifest.packages[manifest.packages.length - 1];
const hostSuffix = `-${process.platform}-${process.arch}`;
const hostPkg = manifest.packages.find((p) => p.name.endsWith(hostSuffix));
if (!hostPkg) fail(`no package for ${process.platform}-${process.arch} in the manifest`);
const install = run(
  'npm',
  ['install', '--ignore-scripts', '--no-audit', '--no-fund', tarballs.get(mainPkg.name), tarballs.get(hostPkg.name)],
  { cwd: installDir },
);
process.stderr.write(install.stdout + install.stderr);
if (install.status !== 0) fail('npm install of the tarballs failed');

// 3. npx review-mcp version.
const versionRun = run('npx', ['--no-install', 'review-mcp', 'version'], { cwd: installDir });
process.stderr.write(`npx review-mcp version -> ${JSON.stringify(versionRun.stdout)}\n`);
if (versionRun.status !== 0) fail(`npx review-mcp version exited ${versionRun.status}:\n${versionRun.stderr}`);
if (!versionRun.stdout.startsWith(`review-mcp ${manifest.version} (`)) {
  fail(`unexpected version output, wanted it to start with "review-mcp ${manifest.version} ("`);
}

// 4. stdio initialize -> tools/list through the launcher.
const roundtrip = spawnSync(process.execPath, [path.join(scriptDir, 'mcp-roundtrip.mjs'), installDir, manifestFile], {
  stdio: 'inherit',
});
if (roundtrip.status !== 0) fail('the MCP round trip failed');
process.stderr.write('smoke: ok\n');
