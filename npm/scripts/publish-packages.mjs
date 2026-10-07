#!/usr/bin/env node
// Publishes the packages listed in <build-dir>/manifest.json (written by
// build-packages.mjs) in manifest order: the six platform packages first, the
// main package last.
//
// Usage: node npm/scripts/publish-packages.mjs <build-dir>
//
// Authentication is left to npm: the release workflow uses npm trusted
// publishing (GitHub OIDC), so the npm CLI obtains its own short-lived
// credential. This script never reads, prints or passes a token.
//
// Republishing guard: before each publish the registry is asked whether
// <name>@<version> already exists. If it does, the script stops with a
// non-zero exit code. There is no force, overwrite or unpublish logic, and a
// failed publish is not retried.
//
// Verification: after publishing, every <name>@<version> is looked up again
// with `npm view`, retrying with a backoff (10 s, 20 s, 40 s, ...) for at most
// 5 minutes in total. A version that is still missing, or whose lookup never
// gave an answer, fails the job. A version that npm reports as staged rather
// than published fails it too. Nothing is ever republished.

import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

export function distTag(manifest) {
  return manifest.prerelease ? 'next' : 'latest';
}

// versionExists asks the registry about one exact version. `npm view` exits
// non-zero with E404 when the package or version is unknown; any other
// failure (network, auth) is not an answer and stops the job.
export function npmVersionExists(name, version, exec = execFileSync) {
  try {
    const out = exec('npm', ['view', `${name}@${version}`, 'version'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
    return String(out).trim() !== '';
  } catch (err) {
    const stderr = String(err.stderr || '');
    if (/E404|404 Not Found/.test(stderr)) return false;
    throw new Error(`could not check ${name}@${version} on the registry`);
  }
}

// npmVersionStatus classifies one `npm view` lookup: 'present' (output),
// 'missing' (E404 or empty output) or 'unknown' (any other failure: network,
// 5xx, auth). Unknown is not treated as missing, because the lookup could not
// tell; the caller retries it and reports it as "not verified" if it persists.
// --prefer-online makes every retry ask the registry: the guard before the
// publish has already cached this package's document without the version.
export function npmVersionStatus(name, version, exec = execFileSync) {
  try {
    const out = exec('npm', ['view', '--prefer-online', `${name}@${version}`, 'version'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
    return String(out).trim() !== '' ? 'present' : 'missing';
  } catch (err) {
    return /E404|404 Not Found/.test(String(err.stderr || '')) ? 'missing' : 'unknown';
  }
}

// isStagedOutput reports whether the text npm printed for a publish says the
// version was staged instead of published. Matching is deliberately narrow:
// the word "staged" on a line, ignoring the tarball file listing ("npm notice
// 1.2kB path"). npm 11 documents `npm stage publish` but no output text, so
// this is a pattern, not a quoted message.
export function isStagedOutput(output) {
  return String(output)
    .split(/\r?\n/)
    .some((line) => !/^npm notice\s+[\d.]+\s*[kMG]?B\s/.test(line) && /\bstaged\b/i.test(line));
}

export const VERIFY_WINDOW_MS = 5 * 60 * 1000;
export const VERIFY_FIRST_DELAY_MS = 10 * 1000;

// backoffSchedule lists the waits between lookups: 10 s, 20 s, 40 s, ... with
// the last one shortened so the total equals the window.
export function backoffSchedule(windowMs = VERIFY_WINDOW_MS, firstMs = VERIFY_FIRST_DELAY_MS) {
  const waits = [];
  let total = 0;
  for (let d = firstMs; total < windowMs; d *= 2) {
    const w = Math.min(d, windowMs - total);
    waits.push(w);
    total += w;
  }
  return waits;
}

// verifyAll looks every package up until it is present or the window is used
// up. status(name, version) -> 'present' | 'missing' | 'unknown'; sleep(ms)
// and now() are injectable. Returns { missing, unverified } (name lists).
export function verifyAll({ packages, version, status, sleep, now = Date.now, log = () => {}, windowMs = VERIFY_WINDOW_MS }) {
  const start = now();
  let pending = packages.map((p) => ({ name: p.name, state: 'missing' }));
  let delay = VERIFY_FIRST_DELAY_MS;
  for (;;) {
    pending = pending.filter((p) => {
      p.state = status(p.name, version);
      return p.state !== 'present';
    });
    if (pending.length === 0) return { missing: [], unverified: [] };
    const remaining = windowMs - (now() - start);
    if (remaining <= 0) break;
    const wait = Math.min(delay, remaining);
    log(`${pending.map((p) => p.name).join(', ')} not visible on the registry yet; checking again in ${wait / 1000} s`);
    sleep(wait);
    delay *= 2;
  }
  return {
    missing: pending.filter((p) => p.state === 'missing').map((p) => p.name),
    unverified: pending.filter((p) => p.state === 'unknown').map((p) => p.name),
  };
}

export function verificationFailure({ version, staged = [], missing = [], unverified = [] }) {
  const lines = [`${version} is not fully available on the registry.`];
  if (staged.length) lines.push(`Staged, not published: ${staged.join(', ')}`);
  if (missing.length) lines.push(`Missing after the retry window: ${missing.join(', ')}`);
  if (unverified.length) lines.push(`Not verified (the registry lookup kept failing): ${unverified.join(', ')}`);
  lines.push(
    'Check npmjs.com for a pending staged publish to approve, or for a package-level publishing requirement (for example 2FA).',
    'Do not republish this version; see docs/release.md, "If a platform package is missing after release".',
  );
  return lines.join('\n');
}

// publishAll is injectable for tests: exists(name, version) -> boolean and
// publish(dir, args) -> the npm output (string) perform the registry
// interactions; status/sleep/now drive the verification. Nothing is
// republished in any path.
export function publishAll({ buildDir, manifest, exists, publish, status, sleep, now, log = () => {} }) {
  const tag = distTag(manifest);
  const staged = [];
  for (const pkg of manifest.packages) {
    if (exists(pkg.name, manifest.version)) {
      throw new Error(`${pkg.name}@${manifest.version} already exists on the registry; refusing to republish`);
    }
    log(`publishing ${pkg.name}@${manifest.version} with tag ${tag}`);
    const out = publish(path.join(buildDir, pkg.dir), ['publish', '--provenance', '--access', 'public', '--tag', tag]);
    if (isStagedOutput(out || '')) staged.push(pkg.name);
  }
  if (!status) return;
  // A staged version is known to be unpublished; it is not looked up again.
  const toVerify = manifest.packages.filter((p) => !staged.includes(p.name));
  const { missing, unverified } = verifyAll({ packages: toVerify, version: manifest.version, status, sleep, now, log });
  if (staged.length || missing.length || unverified.length) {
    throw new Error(verificationFailure({ version: manifest.version, staged, missing, unverified }));
  }
  log(`verified ${manifest.packages.length} packages at ${manifest.version} on the registry`);
}

// runPublish runs npm, echoes what it printed to the job log and returns the
// combined output for the staged check. No token is involved (OIDC).
function runPublish(dir, args) {
  const res = spawnSync('npm', args, { cwd: dir, encoding: 'utf8', stdio: ['inherit', 'pipe', 'pipe'] });
  if (res.stdout) process.stdout.write(res.stdout);
  if (res.stderr) process.stderr.write(res.stderr);
  if (res.error) throw res.error;
  if (res.status !== 0) throw new Error(`npm publish failed in ${dir} (exit ${res.status})`);
  return `${res.stdout || ''}\n${res.stderr || ''}`;
}

function sleepMs(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv.length !== 3) throw new Error('usage: publish-packages.mjs <build-dir>');
    const buildDir = path.resolve(process.argv[2]);
    const manifest = JSON.parse(fs.readFileSync(path.join(buildDir, 'manifest.json'), 'utf8'));
    publishAll({
      buildDir,
      manifest,
      exists: (name, version) => npmVersionExists(name, version),
      publish: runPublish,
      status: (name, version) => npmVersionStatus(name, version),
      sleep: sleepMs,
      log: (line) => process.stderr.write(`${line}\n`),
    });
  } catch (err) {
    process.stderr.write(`publish-packages: ${err.message}\n`);
    process.exit(1);
  }
}
