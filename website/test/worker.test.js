// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import test from 'node:test';
import worker, { releaseKey } from '../worker.js';
import PAYMENT_LINKS from '../src/payment-links.json' with { type: 'json' };

const noAssets = { fetch: async () => { throw new Error('unexpected asset lookup'); } };

// In-memory stand-in for the R2 binding. Every requested key is recorded so tests can assert which objects a route read.
function releases(objects, requested = []) {
  return {
    get: async key => {
      requested.push(key);
      if (!Object.hasOwn(objects, key)) return null;
      const body = objects[key];
      return { body, size: body.length, httpEtag: 'etag', text: async () => body };
    }
  };
}

const PUBLISHED = {
  'latest.json': '{"version": "2.3.2"}',
  'v2.3.2/ztd-linux-amd64': 'linux-amd64-binary',
  'v2.3.2/ztd-linux-amd64.sha256': 'ztd-linux-amd64.sha256-hash',
  'v2.3.2/ztd-linux-arm64': 'linux-arm64-binary',
  'v2.3.2/ztd-windows-amd64.exe.sigstore.json': 'bundle',
  'v2.3.2/ztd-binaries.json': 'ztd-binaries.json',
  'v2.3.2/checksums-sha256.txt': 'checksums-sha256.txt'
};

test('releaseKey selects a Linux artifact for the convenience endpoint', () => {
  assert.equal(releaseKey('/ztd', 'curl aarch64'), 'ztd-linux-arm64');
  assert.equal(releaseKey('/ztd', 'curl x86_64'), 'ztd-linux-amd64');
});

test('releaseKey accepts only known release artifact names', () => {
  assert.equal(releaseKey('/ztd-windows-amd64.exe.sha256'), 'ztd-windows-amd64.exe.sha256');
  assert.equal(releaseKey('/private-file'), null);
  assert.equal(releaseKey('/ztd-linux-ppc64'), null);
});

test('worker serves site paths from static assets with security headers', async () => {
  const assetsResponse = new Response('<h1>ztd</h1>', { headers: { 'content-type': 'text/html' } });
  const response = await worker.fetch(new Request('https://zerotrustdata.io/'), {
    ASSETS: { fetch: async () => assetsResponse },
    RELEASES: { get: async () => { throw new Error('unexpected release lookup'); } }
  });
  assert.equal(await response.text(), '<h1>ztd</h1>');
  assert.equal(response.headers.get('x-content-type-options'), 'nosniff');
  assert.match(response.headers.get('content-security-policy'), /frame-ancestors 'none'/);
});

test('worker redirects www to the canonical hostname and preserves the request target', async () => {
  const response = await worker.fetch(new Request('https://www.zerotrustdata.io/docs/start?mode=local'), {
    ASSETS: { fetch: async () => { throw new Error('unexpected asset lookup'); } },
    RELEASES: { get: async () => { throw new Error('unexpected release lookup'); } }
  });
  assert.equal(response.status, 301);
  assert.equal(response.headers.get('location'), 'https://zerotrustdata.io/docs/start?mode=local');
  assert.equal(response.headers.get('x-content-type-options'), 'nosniff');
});

test('worker serves unversioned release artifacts from the version named by latest.json', async () => {
  const requested = [];
  const response = await worker.fetch(new Request('https://zerotrustdata.io/ztd-linux-amd64.sha256'), { ASSETS: noAssets, RELEASES: releases(PUBLISHED, requested) });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('access-control-allow-origin'), '*');
  assert.equal(await response.text(), 'ztd-linux-amd64.sha256-hash');
  assert.deepEqual(requested, ['latest.json', 'v2.3.2/ztd-linux-amd64.sha256']);
});

test('convenience endpoint resolves through latest.json with a short cache and a named attachment', async () => {
  const requested = [];
  const response = await worker.fetch(new Request('https://zerotrustdata.io/ztd', { headers: { 'user-agent': 'curl aarch64' } }), { ASSETS: noAssets, RELEASES: releases(PUBLISHED, requested) });
  assert.equal(response.status, 200);
  assert.equal(await response.text(), 'linux-arm64-binary');
  assert.equal(response.headers.get('cache-control'), 'public, max-age=300');
  assert.equal(response.headers.get('content-disposition'), 'attachment; filename="ztd-linux-arm64"');
  assert.deepEqual(requested, ['latest.json', 'v2.3.2/ztd-linux-arm64']);
});

test('rewriting latest.json moves unversioned routes to another complete set (rollback)', async () => {
  const objects = { ...PUBLISHED, 'latest.json': '{"version": "2.3.1"}', 'v2.3.1/ztd-linux-amd64': 'previous-binary' };
  const response = await worker.fetch(new Request('https://zerotrustdata.io/ztd-linux-amd64'), { ASSETS: noAssets, RELEASES: releases(objects) });
  assert.equal(await response.text(), 'previous-binary');
});

test('versioned routes serve the immutable version prefix without reading latest.json', async () => {
  const requested = [];
  const response = await worker.fetch(new Request('https://zerotrustdata.io/v/2.3.2/ztd-linux-amd64'), { ASSETS: noAssets, RELEASES: releases(PUBLISHED, requested) });
  assert.equal(response.status, 200);
  assert.equal(await response.text(), 'linux-amd64-binary');
  assert.equal(response.headers.get('cache-control'), 'public, max-age=31536000, immutable');
  assert.equal(response.headers.get('content-type'), 'application/octet-stream');
  assert.equal(response.headers.get('content-disposition'), 'attachment; filename="ztd-linux-amd64"');
  assert.deepEqual(requested, ['v2.3.2/ztd-linux-amd64']);
});

test('versioned routes accept prerelease versions and return 404 for an absent object', async () => {
  const objects = { 'v2.3.2-rc.1/ztd-linux-amd64': 'rc-binary' };
  const env = { ASSETS: noAssets, RELEASES: releases(objects) };
  const rc = await worker.fetch(new Request('https://zerotrustdata.io/v/2.3.2-rc.1/ztd-linux-amd64'), env);
  assert.equal(await rc.text(), 'rc-binary');
  const absent = await worker.fetch(new Request('https://zerotrustdata.io/v/2.3.2/ztd-linux-arm64.sha256'), env);
  assert.equal(absent.status, 404);
  assert.equal(await absent.text(), 'not found');
});

test('versioned routes reject malformed versions and unknown files with 404 before touching R2 or assets', async () => {
  const requested = [];
  const env = { ASSETS: noAssets, RELEASES: releases(PUBLISHED, requested) };
  for (const path of [
    '/v/2.3/ztd-linux-amd64', '/v/latest/ztd-linux-amd64', '/v/2.3.2-/ztd-linux-amd64', '/v/2.3.2/ztd-linux-ppc64',
    '/v/2.3.2/latest.json', '/v/2.3.2/ztd-linux-amd64/extra', '/v/2.3.2', '/v/', '/v'
  ]) {
    const response = await worker.fetch(new Request(`https://zerotrustdata.io${path}`), env);
    assert.equal(response.status, 404, path);
    assert.equal(await response.text(), 'not found', path);
  }
  assert.deepEqual(requested, []);
});

test('a missing or invalid latest.json fails with a clear 503 instead of site HTML', async () => {
  const cases = {
    missing: {},
    malformed: { 'latest.json': '{not json' },
    badVersion: { 'latest.json': '{"version": "latest"}' }
  };
  for (const [name, objects] of Object.entries(cases)) {
    const response = await worker.fetch(new Request('https://zerotrustdata.io/ztd-linux-amd64'), { ASSETS: noAssets, RELEASES: releases(objects) });
    assert.equal(response.status, 503, name);
    assert.equal(response.headers.get('content-type'), 'text/plain; charset=utf-8', name);
    assert.equal(response.headers.get('cache-control'), 'no-store', name);
    assert.match(await response.text(), /latest\.json/, name);
  }
});

test('/latest.json publishes the validated pointer as JSON with a short cache and CORS', async () => {
  const requested = [];
  const response = await worker.fetch(new Request('https://zerotrustdata.io/latest.json'), { ASSETS: noAssets, RELEASES: releases(PUBLISHED, requested) });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('content-type'), 'application/json; charset=utf-8');
  assert.equal(response.headers.get('cache-control'), 'public, max-age=300');
  assert.equal(response.headers.get('access-control-allow-origin'), '*');
  assert.equal(response.headers.get('x-content-type-options'), 'nosniff');
  assert.deepEqual(JSON.parse(await response.text()), { version: '2.3.2' });
  assert.deepEqual(requested, ['latest.json']);
});

test('/latest.json re-serializes a pretty-printed pointer and answers HEAD without a body', async () => {
  const env = { ASSETS: noAssets, RELEASES: releases({ 'latest.json': '{\n  "version": "2.3.2-rc.1"\n}\n' }) };
  const response = await worker.fetch(new Request('https://zerotrustdata.io/latest.json'), env);
  assert.equal(await response.text(), '{"version":"2.3.2-rc.1"}\n');
  const head = await worker.fetch(new Request('https://zerotrustdata.io/latest.json', { method: 'HEAD' }), env);
  assert.equal(head.status, 200);
  assert.equal(head.headers.get('content-type'), 'application/json; charset=utf-8');
  assert.equal(await head.text(), '');
});

test('/latest.json answers CORS preflight without reading R2 and rejects other methods', async () => {
  const requested = [];
  const env = { ASSETS: noAssets, RELEASES: releases(PUBLISHED, requested) };
  const options = await worker.fetch(new Request('https://zerotrustdata.io/latest.json', { method: 'OPTIONS' }), env);
  assert.equal(options.status, 204);
  assert.equal(options.headers.get('access-control-allow-origin'), '*');
  const post = await worker.fetch(new Request('https://zerotrustdata.io/latest.json', { method: 'POST' }), env);
  assert.equal(post.status, 405);
  assert.deepEqual(requested, []);
});

test('/latest.json is a plain-text 503 when the pointer is missing or malformed', async () => {
  const cases = {
    missing: {},
    malformed: { 'latest.json': '{not json' },
    badVersion: { 'latest.json': '{"version": "latest"}' }
  };
  for (const [name, objects] of Object.entries(cases)) {
    const response = await worker.fetch(new Request('https://zerotrustdata.io/latest.json'), { ASSETS: noAssets, RELEASES: releases(objects) });
    assert.equal(response.status, 503, name);
    assert.equal(response.headers.get('content-type'), 'text/plain; charset=utf-8', name);
    assert.equal(response.headers.get('cache-control'), 'no-store', name);
    assert.match(await response.text(), /latest\.json/, name);
  }
});

test('CORS preflight for metadata answers without reading latest.json', async () => {
  const requested = [];
  const env = { ASSETS: noAssets, RELEASES: releases({}, requested) };
  const unversioned = await worker.fetch(new Request('https://zerotrustdata.io/ztd-binaries.json', { method: 'OPTIONS' }), env);
  assert.equal(unversioned.status, 204);
  assert.equal(unversioned.headers.get('access-control-allow-origin'), '*');
  const versioned = await worker.fetch(new Request('https://zerotrustdata.io/v/2.3.2/ztd-binaries.json', { method: 'OPTIONS' }), env);
  assert.equal(versioned.status, 204);
  assert.deepEqual(requested, []);
});

test('releaseKey accepts every published release-set file and nothing else', () => {
  for (const name of [
    'ztd-linux-386', 'ztd-darwin-arm64', 'ztd-windows-arm64.exe.sigstore.json',
    'ztd-darwin-amd64.sha256', 'checksums-sha256.txt', 'ztd-binaries.json'
  ]) {
    assert.equal(releaseKey(`/${name}`), name, name);
  }
  assert.equal(releaseKey('/ztd-linux-amd64.sigstore'), null);
  assert.equal(releaseKey('/ztd-darwin-amd64.sig'), null);
  assert.equal(releaseKey('/ztd-binaries.json.bak'), null);
  assert.equal(releaseKey('/ztd-windows-386.exe'), null);
});

test('worker serves signature bundles and manifests from R2 as JSON and checksum lists as text', async () => {
  const env = { ASSETS: noAssets, RELEASES: releases(PUBLISHED) };
  const bundle = await worker.fetch(new Request('https://zerotrustdata.io/ztd-windows-amd64.exe.sigstore.json', { method: 'HEAD' }), env);
  assert.equal(bundle.status, 200);
  assert.equal(bundle.headers.get('content-type'), 'application/json; charset=utf-8');
  assert.equal(bundle.headers.get('access-control-allow-origin'), '*');
  const manifest = await worker.fetch(new Request('https://zerotrustdata.io/ztd-binaries.json'), env);
  assert.equal(manifest.headers.get('content-type'), 'application/json; charset=utf-8');
  const checksums = await worker.fetch(new Request('https://zerotrustdata.io/checksums-sha256.txt'), env);
  assert.equal(checksums.headers.get('content-type'), 'text/plain; charset=utf-8');
  assert.equal(await checksums.text(), 'checksums-sha256.txt');
});

test('worker passes unknown release-looking names to the site assets', async () => {
  const response = await worker.fetch(new Request('https://zerotrustdata.io/ztd-linux-amd64.sigstore'), {
    ASSETS: { fetch: async () => new Response('<html></html>', { headers: { 'content-type': 'text/html' } }) },
    RELEASES: { get: async () => { throw new Error('unexpected release lookup'); } }
  });
  assert.equal(response.headers.get('content-type'), 'text/html');
});

// Releases pages. The bucket stand-in adds list() with R2's pagination shape (objects, truncated, cursor).
const SHA = 'a'.repeat(64);
function manifest(version, overrides = {}) {
  return JSON.stringify({
    schema_version: 1,
    version: `v${version}`,
    source_revision: '0123456789abcdef0123456789abcdef01234567',
    build_time: '2026-10-01T12:00:00Z',
    targets: [
      { os: 'windows', arch: 'amd64', filename: 'ztd-windows-amd64.exe', checksum: 'ztd-windows-amd64.exe.sha256', mode: '0644', size: 2048, sha256: SHA },
      { os: 'linux', arch: 'amd64', filename: 'ztd-linux-amd64', checksum: 'ztd-linux-amd64.sha256', mode: '0755', size: 52428800, sha256: SHA, fips_module: 'v1.0.0' }
    ],
    ...overrides
  });
}

function releaseBucket(objects, { pageSize = 2, requested = [] } = {}) {
  const keys = Object.keys(objects).sort();
  return {
    ...releases(objects, requested),
    list: async ({ prefix = '', cursor } = {}) => {
      const matching = keys.filter(key => key.startsWith(prefix));
      const start = cursor ? Number(cursor) : 0;
      const end = start + pageSize;
      return {
        objects: matching.slice(start, end).map(key => ({ key, size: objects[key].length, uploaded: new Date(`2026-10-0${key.length % 9 + 1}T00:00:00Z`) })),
        truncated: end < matching.length,
        cursor: end < matching.length ? String(end) : undefined
      };
    }
  };
}

const RELEASE_SETS = {
  'latest.json': '{"version": "2.3.2"}',
  'v2.3.2/ztd-binaries.json': manifest('2.3.2'),
  'v2.3.2/ztd-linux-amd64': 'binary',
  'v2.3.10-rc.1/ztd-binaries.json': manifest('2.3.10-rc.1'),
  'v2.3.1/ztd-binaries.json': manifest('2.3.1'),
  'v2.3.0/ztd-linux-amd64': 'manifest not uploaded yet',
  'v2.4.0/ztd-binaries.json': manifest('2.4.0')
};

function siteAssets(files = {}, requested = []) {
  return {
    fetch: async request => {
      const { pathname } = new URL(request.url);
      requested.push(pathname);
      if (Object.hasOwn(files, pathname)) return new Response(files[pathname], { headers: { 'content-type': 'text/html' } });
      return new Response('<h1>404</h1>', { status: 404, headers: { 'content-type': 'text/html' } });
    }
  };
}

const page = (path, env, init) => worker.fetch(new Request(`https://zerotrustdata.io${path}`, init), env);

test('/releases/ lists complete, promoted versions newest first with the latest downloads', async () => {
  const response = await page('/releases/', { ASSETS: noAssets, RELEASES: releaseBucket(RELEASE_SETS) });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('content-type'), 'text/html; charset=utf-8');
  assert.equal(response.headers.get('cache-control'), 'public, max-age=300');
  assert.match(response.headers.get('content-security-policy'), /script-src 'self'/);
  const html = await response.text();
  const listed = [...html.matchAll(/class="release-version" href="\/releases\/v([^/]+)\/"/g)].map(match => match[1]);
  assert.deepEqual(listed, ['2.3.2', '2.3.1'], 'unpromoted 2.4.0 and 2.3.10-rc.1 and the manifest-less 2.3.0 are hidden');
  assert.match(html, /curl -fsSL https:\/\/zerotrustdata\.io\/start\.sh \| sh</);
  assert.match(html, /href="\/v\/2\.3\.2\/ztd-linux-amd64" download/);
  assert.ok(html.indexOf('ztd-linux-amd64') < html.indexOf('ztd-windows-amd64.exe'), 'platforms are ordered Linux first');
  assert.match(html, /50\.0 MiB/);
  assert.match(html, /FIPS 140-3/);
  assert.match(html, /<a class="nav-current" href="\/releases\/" aria-current="page">Releases<\/a>/);
});

test('/releases/v<version>/ shows pinned commands, verification, and the build-time release notes', async () => {
  const requested = [];
  const env = { ASSETS: siteAssets({ '/_release-notes/2.3.1.html': '<p>Fixed <strong>things</strong>.</p>' }, requested), RELEASES: releaseBucket(RELEASE_SETS) };
  const response = await page('/releases/v2.3.1/', env);
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /<link rel="canonical" href="https:\/\/zerotrustdata\.io\/releases\/v2\.3\.1\/">/);
  assert.match(html, /start\.sh \| ZTD_VERSION=2\.3\.1 sh/);
  assert.match(html, /\$env:ZTD_VERSION=&#39;2\.3\.1&#39;; irm https:\/\/zerotrustdata\.io\/start\.ps1 \| iex/);
  assert.match(html, /\$env:ZTD_VERSION=&#39;2\.3\.1&#39;; irm https:\/\/zerotrustdata\.io\/ztd\.ps1 \| iex/);
  assert.match(html, /release-binary\.yml@refs\/tags\/v2\.3\.1/);
  assert.match(html, /<p>Fixed <strong>things<\/strong>\.<\/p>/);
  assert.match(html, /commit\/0123456789abcdef0123456789abcdef01234567/);
  assert.doesNotMatch(html, /badge latest/, 'only the latest version carries the Latest badge');
  assert.deepEqual(requested, ['/_release-notes/2.3.1.html']);
});

test('a release page without notes links to the GitHub release', async () => {
  const response = await page('/releases/v2.3.2/', { ASSETS: siteAssets(), RELEASES: releaseBucket(RELEASE_SETS) });
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /release notes for this version are on <a href="https:\/\/github\.com\/zerotrustdata\/ztd\/releases\/tag\/v2\.3\.2">/);
  assert.match(html, /badge latest/);
});

test('/releases redirects add the trailing slash and /releases/latest/ follows latest.json', async () => {
  const env = { ASSETS: noAssets, RELEASES: releaseBucket(RELEASE_SETS) };
  for (const [path, status, location] of [
    ['/releases', 301, 'https://zerotrustdata.io/releases/'],
    ['/releases/v2.3.1', 301, 'https://zerotrustdata.io/releases/v2.3.1/'],
    ['/releases/latest/', 302, 'https://zerotrustdata.io/releases/v2.3.2/']
  ]) {
    const response = await page(path, env);
    assert.equal(response.status, status, path);
    assert.equal(response.headers.get('location'), location, path);
  }
});

test('unlisted versions and unknown release paths are the site 404 page', async () => {
  const env = { ASSETS: siteAssets(), RELEASES: releaseBucket(RELEASE_SETS) };
  for (const path of ['/releases/v2.4.0/', '/releases/v2.3.0/', '/releases/v9.9.9/', '/releases/vlatest/', '/releases/feed.xml', '/releases/v2.3.2/extra']) {
    const response = await page(path, env);
    assert.equal(response.status, 404, path);
    assert.equal(await response.text(), '<h1>404</h1>', path);
  }
});

test('release pages answer HEAD without a body and reject other methods', async () => {
  const env = { ASSETS: siteAssets(), RELEASES: releaseBucket(RELEASE_SETS) };
  const head = await page('/releases/', env, { method: 'HEAD' });
  assert.equal(head.status, 200);
  assert.equal(await head.text(), '');
  const post = await page('/releases/', env, { method: 'POST' });
  assert.equal(post.status, 405);
  assert.equal(post.headers.get('allow'), 'GET, HEAD');
});

test('a malformed manifest or a missing latest manifest is a 503, never a partial page', async t => {
  t.mock.method(console, 'error', () => {});
  const cases = {
    malformed: { ...RELEASE_SETS, 'v2.3.2/ztd-binaries.json': '{not json' },
    wrongVersion: { ...RELEASE_SETS, 'v2.3.2/ztd-binaries.json': manifest('2.3.1') },
    badDigest: { ...RELEASE_SETS, 'v2.3.2/ztd-binaries.json': manifest('2.3.2', { targets: [{ os: 'linux', arch: 'amd64', filename: 'ztd-linux-amd64', checksum: 'ztd-linux-amd64.sha256', size: 1, sha256: 'xyz' }] }) },
    badFilename: { ...RELEASE_SETS, 'v2.3.2/ztd-binaries.json': manifest('2.3.2', { targets: [{ os: 'linux', arch: 'amd64', filename: '../evil', checksum: 'ztd-linux-amd64.sha256', size: 1, sha256: SHA }] }) },
    missingLatest: { 'latest.json': '{"version": "2.3.3"}', 'v2.3.2/ztd-binaries.json': manifest('2.3.2') },
    missingPointer: { 'v2.3.2/ztd-binaries.json': manifest('2.3.2') }
  };
  for (const [name, objects] of Object.entries(cases)) {
    const response = await page('/releases/', { ASSETS: siteAssets(), RELEASES: releaseBucket(objects) });
    assert.equal(response.status, 503, name);
    assert.equal(response.headers.get('cache-control'), 'no-store', name);
    assert.equal(response.headers.get('x-content-type-options'), 'nosniff', name);
  }
});

test('release-note fragments are not served as pages of their own', async () => {
  const response = await page('/_release-notes/2.3.2.html', { ASSETS: noAssets, RELEASES: releaseBucket(RELEASE_SETS) });
  assert.equal(response.status, 404);
  assert.equal(await response.text(), 'not found');
});

test('unknown /buy/ SKUs return 404 without touching assets', async () => {
  const response = await worker.fetch(new Request('https://zerotrustdata.io/buy/nonexistent'), { ASSETS: noAssets });
  assert.equal(response.status, 404);
  assert.equal(response.headers.get('x-frame-options'), 'DENY');
});

test('every configured /buy/ SKU redirects 303 to its live Payment Link without touching assets', async () => {
  for (const [sku, url] of Object.entries(PAYMENT_LINKS)) {
    const response = await worker.fetch(new Request(`https://zerotrustdata.io/buy/${sku}`), { ASSETS: noAssets });
    assert.equal(response.status, 303, sku);
    assert.equal(response.headers.get('location'), url, sku);
    assert.equal(response.headers.get('x-frame-options'), 'DENY', sku);
  }
});
