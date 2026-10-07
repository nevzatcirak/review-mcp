import assert from 'node:assert/strict';
import path from 'node:path';
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
    // publishAll joins with the platform's separator (backslashes on Windows).
    ['a-linux-x64', 'a-win32-x64', 'a'].map((d) => path.join('/build', d)),
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
  assert.deepEqual(published, [path.join('/b', 'a-linux-x64')], 'the packages after the existing one are not published');
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

// ---- verification after publishing (WP-PR-9b) ----

import { backoffSchedule, isStagedOutput, npmVersionStatus, verifyAll } from '../scripts/publish-packages.mjs';

// harness returns publishAll options with a fake registry: visibleAfter maps a
// package name to the number of lookups after which it becomes visible
// (Infinity: never). Sleeping advances a fake clock; publishes are counted.
function harness(visibleAfter = {}, { out = '', lookup } = {}) {
  let clock = 0;
  const lookups = {};
  const published = [];
  const sleeps = [];
  const opts = {
    buildDir: '/b',
    manifest: manifest('1.2.3', false),
    exists: () => false,
    publish: (dir) => {
      published.push(dir);
      return out;
    },
    status: (name) => {
      lookups[name] = (lookups[name] || 0) + 1;
      if (lookup) return lookup(name, lookups[name]);
      return lookups[name] > (visibleAfter[name] || 0) ? 'present' : 'missing';
    },
    sleep: (ms) => {
      sleeps.push(ms);
      clock += ms;
    },
    now: () => clock,
  };
  return { opts, published, sleeps, lookups };
}

test('verify: all present passes without sleeping and without republishing', () => {
  const h = harness();
  publishAll(h.opts);
  assert.deepEqual(h.sleeps, []);
  assert.equal(h.published.length, 3);
});

test('verify: a version missing after the retries fails and is named, with no republish', () => {
  const h = harness({ 'scope/a-win32-x64': Infinity });
  assert.throws(
    () => publishAll(h.opts),
    (err) => {
      assert.match(err.message, /Missing after the retry window: scope\/a-win32-x64$/m);
      assert.doesNotMatch(err.message, /Missing[^\n]*scope\/a-linux-x64/);
      assert.match(err.message, /staged publish/);
      assert.match(err.message, /Do not republish/);
      return true;
    },
  );
  assert.equal(h.published.length, 3, 'publish ran once per package, never again');
  assert.equal(h.sleeps.reduce((a, b) => a + b, 0), 5 * 60 * 1000);
});

test('verify: present only after two retries passes', () => {
  const h = harness({ 'scope/a-linux-x64': 2 });
  publishAll(h.opts);
  assert.deepEqual(h.sleeps, [10000, 20000]);
  assert.equal(h.published.length, 3);
});

test('verify: staged publish output fails with the same guidance and is not retried', () => {
  const h = harness({}, { out: 'npm notice 1.2kB package.json\nnpm notice Version 1.2.3 was staged and awaits approval\n' });
  assert.throws(() => publishAll(h.opts), /Staged, not published: scope\/a-linux-x64, scope\/a-win32-x64, scope\/a\n[\s\S]*staged publish/);
  assert.equal(h.published.length, 3);
  assert.deepEqual(h.sleeps, []);
});

test('verify: a lookup that keeps failing is reported as not verified, not as missing', () => {
  const h = harness({}, { lookup: (name) => (name === 'scope/a' ? 'unknown' : 'present') });
  assert.throws(
    () => publishAll(h.opts),
    (err) => {
      assert.match(err.message, /Not verified \(the registry lookup kept failing\): scope\/a$/m);
      assert.doesNotMatch(err.message, /Missing after/);
      return true;
    },
  );
});

test('verify: a transient lookup error is retried and can still pass', () => {
  const h = harness({}, { lookup: (name, n) => (n < 3 ? 'unknown' : 'present') });
  publishAll(h.opts);
  assert.deepEqual(h.sleeps, [10000, 20000]);
});

test('backoff schedule: 10 s, 20 s, 40 s, ... and the total is at most 5 minutes', () => {
  const s = backoffSchedule();
  assert.deepEqual(s.slice(0, 3), [10000, 20000, 40000]);
  assert.ok(s.reduce((a, b) => a + b, 0) <= 5 * 60 * 1000);
});

test('verifyAll only looks up; it has no publish path', () => {
  let clock = 0;
  const r = verifyAll({
    packages: [{ name: 'x' }],
    version: '1.0.0',
    status: () => 'missing',
    sleep: (ms) => (clock += ms),
    now: () => clock,
  });
  assert.deepEqual(r, { missing: ['x'], unverified: [] });
});

test('staged detection is narrow', () => {
  assert.equal(isStagedOutput('+ scope/a@1.2.3\n'), false);
  assert.equal(isStagedOutput('npm notice 1.1kB dist/staged-notes.txt\n'), false);
  assert.equal(isStagedOutput('npm notice Published scope/a@1.2.3\n'), false);
  assert.equal(isStagedOutput('npm notice scope/a@1.2.3 staged, approve it on npmjs.com\n'), true);
  assert.equal(isStagedOutput('Staged scope/a@1.2.3'), true);
});

test('npmVersionStatus: output present, E404 or empty missing, anything else unknown', () => {
  const fail = (stderr) => () => {
    throw Object.assign(new Error('x'), { stderr });
  };
  assert.equal(npmVersionStatus('p', '1', () => '1.0.0\n'), 'present');
  assert.equal(npmVersionStatus('p', '1', () => '\n'), 'missing');
  assert.equal(npmVersionStatus('p', '1', fail('npm error code E404')), 'missing');
  assert.equal(npmVersionStatus('p', '1', fail('npm error code E503')), 'unknown');
});
