// Shared fixtures for the npm tests: a fake GoReleaser dist directory and a
// helper that assembles the packages from it with the real build script.

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { buildPackages } from '../scripts/build-packages.mjs';

export const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
export const sourceLauncher = path.join(repoRoot, 'npm', 'review-mcp', 'bin', 'review-mcp.js');

const TARGETS = [
  ['linux', 'amd64'],
  ['linux', 'arm64'],
  ['darwin', 'amd64'],
  ['darwin', 'arm64'],
  ['windows', 'amd64'],
  ['windows', 'arm64'],
];

// Every temp dir is removed when the test process exits, so a test run leaves
// nothing behind (node --test runs each file in its own process).
const created = [];
process.on('exit', () => {
  for (const dir of created) fs.rmSync(dir, { recursive: true, force: true });
});

export function tmpDir(prefix = 'review-mcp-npm-') {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  created.push(dir);
  return dir;
}

// The host's fake binary is a tiny shell script so the packaged launcher can
// run it for real (not on Windows, where the tests skip that case).
export const HOST_FAKE_BINARY = '#!/bin/sh\nprintf "fake-binary:%s" "$*"\n';

// makeDist writes <root>/dist with artifacts.json, metadata.json and one fake
// binary per target. `omit` lists "<goos>/<goarch>" targets to leave out.
export function makeDist(root, version, { omit = [] } = {}) {
  const dist = path.join(root, 'dist');
  fs.mkdirSync(dist, { recursive: true });
  const artifacts = [];
  for (const [goos, goarch] of TARGETS) {
    if (omit.includes(`${goos}/${goarch}`)) continue;
    const name = goos === 'windows' ? 'review-mcp.exe' : 'review-mcp';
    const rel = `dist/review-mcp_${goos}_${goarch}_v1/${name}`;
    const file = path.join(root, rel);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, HOST_FAKE_BINARY);
    artifacts.push({ name, path: rel, goos, goarch, type: 'Binary' });
  }
  fs.writeFileSync(path.join(dist, 'artifacts.json'), JSON.stringify(artifacts));
  fs.writeFileSync(path.join(dist, 'metadata.json'), JSON.stringify({ version }));
  return dist;
}

// buildFixture builds all seven packages from a fake dist into a temp dir.
export function buildFixture(version = '1.2.3') {
  const root = tmpDir();
  const distDir = makeDist(root, version);
  const outDir = path.join(root, 'build');
  buildPackages({ version: `v${version}`, distDir, outDir });
  return { root, distDir, outDir, mainDir: path.join(outDir, 'review-mcp') };
}
