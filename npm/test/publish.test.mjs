import assert from 'node:assert/strict';
import { test } from 'node:test';

import { distTag, npmVersionExists, publishAll } from '../scripts/publish-packages.mjs';

function manifest(version, prerelease) {
  return {
    version,
    prerelease,
    packages: [
      { name: 'scope/a-linux-x64', dir: 'a-linux-x64' },
      { name: 'scope/a-win32-x64', dir: 'a-win32-x64' },
      { name: 'scope/a', dir: 'a' },
    ],
  };
}

test('dist tag: next for pre-releases, latest otherwise', () => {
  assert.equal(distTag({ prerelease: true }), 'next');
  assert.equal(distTag({ prerelease: false }), 'latest');
});

test('platform packages are published first, the main package last, with provenance', () => {
  const calls = [];
  publishAll({
    buildDir: '/build',
    manifest: manifest('1.2.3', false),
    exists: () => false,
    publish: (dir, args) => calls.push([dir, args]),
  });
  assert.deepEqual(
    calls.map(([dir]) => dir),
    ['/build/a-linux-x64', '/build/a-win32-x64', '/build/a'],
  );
  for (const [, args] of calls) {
    assert.deepEqual(args, ['publish', '--provenance', '--access', 'public', '--tag', 'latest']);
  }
});

test('a pre-release is published with --tag next', () => {
  const calls = [];
  publishAll({ buildDir: '/b', manifest: manifest('1.0.0-rc.1', true), exists: () => false, publish: (d, a) => calls.push(a) });
  for (const args of calls) assert.deepEqual(args.slice(-2), ['--tag', 'next']);
});

test('an existing version stops the job before anything further is published', () => {
  const published = [];
  assert.throws(
    () =>
      publishAll({
        buildDir: '/b',
        manifest: manifest('1.2.3', false),
        exists: (name) => name === 'scope/a-win32-x64',
        publish: (dir) => published.push(dir),
      }),
    /scope\/a-win32-x64@1\.2\.3 already exists/,
  );
  assert.deepEqual(published, ['/b/a-linux-x64'], 'the packages after the existing one are not published');
});

test('a failing publish stops the job without retrying', () => {
  const published = [];
  assert.throws(
    () =>
      publishAll({
        buildDir: '/b',
        manifest: manifest('1.2.3', false),
        exists: () => false,
        publish: (dir) => {
          published.push(dir);
          throw new Error('npm publish failed');
        },
      }),
    /npm publish failed/,
  );
  assert.equal(published.length, 1);
});

test('npmVersionExists: output means it exists, E404 means it does not, anything else is an error', () => {
  assert.equal(npmVersionExists('p', '1.0.0', () => '1.0.0\n'), true);
  const notFound = () => {
    throw Object.assign(new Error('x'), { stderr: 'npm error code E404\nnpm error 404 No match found' });
  };
  assert.equal(npmVersionExists('p', '1.0.0', notFound), false);
  const network = () => {
    throw Object.assign(new Error('x'), { stderr: 'npm error code ECONNRESET' });
  };
  assert.throws(() => npmVersionExists('p', '1.0.0', network), /could not check p@1\.0\.0/);
});
