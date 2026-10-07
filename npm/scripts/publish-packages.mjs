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

import { execFileSync } from 'node:child_process';
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

// publishAll is injectable for tests: exists(name, version) -> boolean and
// publish(dir, args) perform the two registry interactions.
export function publishAll({ buildDir, manifest, exists, publish, log = () => {} }) {
  const tag = distTag(manifest);
  for (const pkg of manifest.packages) {
    if (exists(pkg.name, manifest.version)) {
      throw new Error(`${pkg.name}@${manifest.version} already exists on the registry; refusing to republish`);
    }
    log(`publishing ${pkg.name}@${manifest.version} with tag ${tag}`);
    publish(path.join(buildDir, pkg.dir), ['publish', '--provenance', '--access', 'public', '--tag', tag]);
  }
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
      publish: (dir, args) => execFileSync('npm', args, { cwd: dir, stdio: 'inherit' }),
      log: (line) => process.stderr.write(`${line}\n`),
    });
  } catch (err) {
    process.stderr.write(`publish-packages: ${err.message}\n`);
    process.exit(1);
  }
}
