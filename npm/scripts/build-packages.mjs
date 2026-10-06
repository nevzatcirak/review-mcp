#!/usr/bin/env node
// Assembles the seven npm package directories (one main package and six
// platform packages) from a GoReleaser output directory.
//
// Usage: node npm/scripts/build-packages.mjs <tag-or-version> [--dist <dir>] [--out <dir>]
//
//   <tag-or-version>  the git tag (v1.2.3) or the bare version (1.2.3); the
//                     leading "v" is stripped and the rest must be a semver
//                     version without build metadata.
//   --dist            GoReleaser output directory (default: dist).
//   --out             where the package directories are written (default: npm/build).
//
// The binaries are located through <dist>/artifacts.json, so the script does
// not depend on GoReleaser's directory naming. It fails when any of the six
// binaries is missing. Nothing here runs at install time.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

// The npm scope. This is the ONLY place the scope is written down: the
// launcher derives the platform package name from its own package.json.
export const SCOPE = '@nevzatcirak';
export const MAIN_NAME = `${SCOPE}/review-mcp`;

// GoReleaser goos/goarch -> Node process.platform/process.arch.
const PLATFORMS = [
  { goos: 'linux', goarch: 'amd64', os: 'linux', cpu: 'x64' },
  { goos: 'linux', goarch: 'arm64', os: 'linux', cpu: 'arm64' },
  { goos: 'darwin', goarch: 'amd64', os: 'darwin', cpu: 'x64' },
  { goos: 'darwin', goarch: 'arm64', os: 'darwin', cpu: 'arm64' },
  { goos: 'windows', goarch: 'amd64', os: 'win32', cpu: 'x64' },
  { goos: 'windows', goarch: 'arm64', os: 'win32', cpu: 'arm64' },
];

const LICENSE_FILES = ['LICENSE', 'NOTICE', 'THIRD_PARTY_LICENSES'];
const REPOSITORY_URL = 'git+https://github.com/nevzatcirak/review-mcp.git';
const HOMEPAGE = 'https://github.com/nevzatcirak/review-mcp';

const IDENT = '(?:0|[1-9]\\d*|\\d*[A-Za-z-][0-9A-Za-z-]*)';
const SEMVER = new RegExp(`^(?:0|[1-9]\\d*)\\.(?:0|[1-9]\\d*)\\.(?:0|[1-9]\\d*)(?:-${IDENT}(?:\\.${IDENT})*)?$`);

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const defaultRepoRoot = path.resolve(scriptDir, '..', '..');

// parseVersion strips one leading "v" and validates the rest.
export function parseVersion(input) {
  if (typeof input !== 'string' || input === '') {
    throw new Error('a version or tag argument is required (for example v1.2.3)');
  }
  const version = input.startsWith('v') ? input.slice(1) : input;
  if (!SEMVER.test(version)) {
    throw new Error(`invalid version "${input}": expected vMAJOR.MINOR.PATCH with an optional -prerelease suffix`);
  }
  return version;
}

export function platformPackageName(p) {
  return `${MAIN_NAME}-${p.os}-${p.cpu}`;
}

export function binaryFileName(p) {
  return p.os === 'win32' ? 'review-mcp.exe' : 'review-mcp';
}

// Directory name of a package inside the output directory (no scope).
function dirNameOf(packageName) {
  return packageName.slice(packageName.indexOf('/') + 1);
}

function findBinary(artifacts, distDir, p) {
  const matches = artifacts.filter((a) => a.type === 'Binary' && a.goos === p.goos && a.goarch === p.goarch);
  if (matches.length === 0) {
    throw new Error(`no ${p.goos}/${p.goarch} binary in artifacts.json`);
  }
  if (matches.length > 1) {
    throw new Error(`more than one ${p.goos}/${p.goarch} binary in artifacts.json`);
  }
  const recorded = matches[0].path.split('\\').join('/');
  const file = path.isAbsolute(recorded)
    ? recorded
    : path.join(distDir, recorded.startsWith('dist/') ? recorded.slice('dist/'.length) : recorded);
  let stat;
  try {
    stat = fs.statSync(file);
  } catch {
    throw new Error(`the ${p.goos}/${p.goarch} binary is missing: ${file}`);
  }
  if (!stat.isFile() || stat.size === 0) {
    throw new Error(`the ${p.goos}/${p.goarch} binary is not a non-empty file: ${file}`);
  }
  return file;
}

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);
}

function copyFile(from, to, mode) {
  fs.copyFileSync(from, to);
  if (mode !== undefined) fs.chmodSync(to, mode);
}

export function buildPackages({ version: input, distDir = 'dist', outDir = path.join(defaultRepoRoot, 'npm', 'build'), repoRoot = defaultRepoRoot } = {}) {
  const version = parseVersion(input);

  const artifactsFile = path.join(distDir, 'artifacts.json');
  if (!fs.existsSync(artifactsFile)) {
    throw new Error(`${artifactsFile} not found: run GoReleaser first`);
  }
  const artifacts = JSON.parse(fs.readFileSync(artifactsFile, 'utf8'));

  // Lockstep guard: the binaries must have been built for this version.
  const metadataFile = path.join(distDir, 'metadata.json');
  if (fs.existsSync(metadataFile)) {
    const built = JSON.parse(fs.readFileSync(metadataFile, 'utf8')).version;
    if (built !== version) {
      throw new Error(`version mismatch: dist/metadata.json says "${built}", requested "${version}"`);
    }
  }

  // Resolve every binary before writing anything.
  const binaries = PLATFORMS.map((p) => ({ p, file: findBinary(artifacts, distDir, p) }));

  const sources = [...LICENSE_FILES, 'README.md'].map((name) => path.join(repoRoot, name));
  for (const s of sources) {
    if (!fs.existsSync(s)) throw new Error(`required file missing: ${s}`);
  }
  const launcher = path.join(repoRoot, 'npm', 'review-mcp', 'bin', 'review-mcp.js');
  if (!fs.existsSync(launcher)) throw new Error(`launcher missing: ${launcher}`);

  fs.mkdirSync(outDir, { recursive: true });
  const platformEntries = [];

  for (const { p, file } of binaries) {
    const name = platformPackageName(p);
    const dirName = dirNameOf(name);
    const dir = path.join(outDir, dirName);
    fs.rmSync(dir, { recursive: true, force: true });
    fs.mkdirSync(path.join(dir, 'bin'), { recursive: true });
    copyFile(file, path.join(dir, 'bin', binaryFileName(p)), 0o755);
    for (const l of LICENSE_FILES) copyFile(path.join(repoRoot, l), path.join(dir, l));
    writeJSON(path.join(dir, 'package.json'), {
      name,
      version,
      description: `The review-mcp binary for ${p.os}-${p.cpu}. Install ${MAIN_NAME} instead of this package.`,
      license: 'MIT',
      repository: { type: 'git', url: REPOSITORY_URL },
      homepage: HOMEPAGE,
      os: [p.os],
      cpu: [p.cpu],
      files: ['bin', ...LICENSE_FILES],
    });
    platformEntries.push({ name, dir: dirName });
  }

  const mainDir = path.join(outDir, dirNameOf(MAIN_NAME));
  fs.rmSync(mainDir, { recursive: true, force: true });
  fs.mkdirSync(path.join(mainDir, 'bin'), { recursive: true });
  copyFile(launcher, path.join(mainDir, 'bin', 'review-mcp.js'), 0o755);
  for (const l of [...LICENSE_FILES, 'README.md']) copyFile(path.join(repoRoot, l), path.join(mainDir, l));
  writeJSON(path.join(mainDir, 'package.json'), {
    name: MAIN_NAME,
    version,
    description: 'MCP server for AI-assisted pull-request review on Gitea and Bitbucket Server.',
    keywords: ['mcp', 'model-context-protocol', 'code-review', 'pull-request', 'gitea', 'bitbucket'],
    license: 'MIT',
    repository: { type: 'git', url: REPOSITORY_URL },
    homepage: HOMEPAGE,
    bin: { 'review-mcp': 'bin/review-mcp.js' },
    engines: { node: '>=18' },
    optionalDependencies: Object.fromEntries(platformEntries.map((e) => [e.name, version])),
    files: ['bin/review-mcp.js', ...LICENSE_FILES, 'README.md'],
  });

  // Platform packages first, then the main package: the publish order.
  const manifest = {
    version,
    prerelease: version.includes('-'),
    packages: [...platformEntries, { name: MAIN_NAME, dir: dirNameOf(MAIN_NAME) }],
  };
  writeJSON(path.join(outDir, 'manifest.json'), manifest);
  return { version, outDir, manifest };
}

function parseArgs(argv) {
  const opts = {};
  const positional = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--dist' || a === '--out') {
      const value = argv[++i];
      if (!value) throw new Error(`${a} needs a value`);
      opts[a.slice(2)] = value;
    } else if (a.startsWith('--')) {
      throw new Error(`unknown option ${a}`);
    } else {
      positional.push(a);
    }
  }
  if (positional.length !== 1) throw new Error('usage: build-packages.mjs <tag-or-version> [--dist <dir>] [--out <dir>]');
  return { version: positional[0], distDir: opts.dist, outDir: opts.out };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const result = buildPackages({
      version: args.version,
      ...(args.distDir ? { distDir: path.resolve(args.distDir) } : { distDir: path.resolve('dist') }),
      ...(args.outDir ? { outDir: path.resolve(args.outDir) } : {}),
    });
    process.stderr.write(`built ${result.manifest.packages.length} packages at version ${result.version} in ${result.outDir}\n`);
  } catch (err) {
    process.stderr.write(`build-packages: ${err.message}\n`);
    process.exit(1);
  }
}
