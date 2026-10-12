// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import test from 'node:test';
import worker, { releaseKey } from '../worker.js';

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
