// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { RELEASES_PATH } from './src/layout.js';
import { BUY_PATH, paymentLink } from './src/payments.js';
import { RELEASE_NOTES_PATH, RELEASE_VERSION, listReleases, readManifest, releasePath, renderReleasePage, renderReleasesIndex } from './src/releases.js';

const RELEASE_PATH = /^(?:ztd-(?:linux-(?:amd64|arm64|386)|darwin-(?:amd64|arm64)|windows-(?:amd64|arm64)\.exe)(?:\.sha256|\.sigstore\.json)?|checksums-sha256\.txt|ztd-binaries\.json)$/;
const TEXT_ARTIFACT = /\.(?:sha256|sig)$|^checksums-sha256\.txt$/;
const JSON_ARTIFACT = /\.sigstore\.json$|^ztd-binaries\.json$/;
const METADATA_ARTIFACT = /\.(?:sha256|sig|sigstore\.json)$|^(?:checksums-sha256\.txt|ztd-binaries\.json|latest\.json)$/;
const VERSIONED_PATH = /^\/v\/([^/]+)\/([^/]+)$/;
const RELEASE_PAGE_PATH = /^\/releases\/v([^/]+)(\/?)$/;
// Bucket-root pointer that names the release set served by unversioned routes. Written last during promotion.
const LATEST_POINTER = 'latest.json';
// Versioned objects are immutable once published, so browsers and CDNs may keep them for a year.
const IMMUTABLE_CACHE = 'public, max-age=31536000, immutable';
// Unversioned routes follow latest.json, so they are cached briefly to pick up promotion and rollback quickly.
const LATEST_CACHE = 'public, max-age=300';
// Release pages list versions from R2 and follow latest.json, so they share the short cache.
const PAGE_CACHE = 'public, max-age=300';
const WWW_HOSTNAME = 'www.zerotrustdata.io';
const WORKER_VERSION_HEADER = 'x-ztd-worker-version';
const SECURITY_HEADERS = {
  'content-security-policy': "default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' https: data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
  'permissions-policy': 'camera=(), microphone=(), geolocation=()',
  'referrer-policy': 'strict-origin-when-cross-origin',
  'strict-transport-security': 'max-age=31536000; includeSubDomains',
  'x-content-type-options': 'nosniff',
  'x-frame-options': 'DENY'
};

export function releaseKey(pathname, userAgent = '') {
  const key = pathname.replace(/^\/+/, '');
  if (key !== 'ztd') return RELEASE_PATH.test(key) ? key : null;
  if (/arm64|aarch64/i.test(userAgent)) return 'ztd-linux-arm64';
  if (/i386|i686/i.test(userAgent)) return 'ztd-linux-386';
  return 'ztd-linux-amd64';
}

// Every response also names the Worker version that served it, so the post-deploy smoke can wait for the edge to
// serve the version it just deployed instead of testing the previous one.
function withSecurityHeaders(response, env) {
  const secured = new Response(response.body, response);
  Object.entries(SECURITY_HEADERS).forEach(([name, value]) => secured.headers.set(name, value));
  if (env.CF_VERSION_METADATA?.id) secured.headers.set(WORKER_VERSION_HEADER, env.CF_VERSION_METADATA.id);
  return secured;
}

function contentType(file) {
  if (TEXT_ARTIFACT.test(file)) return 'text/plain; charset=utf-8';
  if (JSON_ARTIFACT.test(file)) return 'application/json; charset=utf-8';
  return 'application/octet-stream';
}

function notFound() {
  return new Response('not found', { status: 404 });
}

// Answers OPTIONS preflights and rejects non-GET/HEAD methods. Returns null when the request should continue.
function earlyResponse(request, file) {
  if (request.method === 'OPTIONS' && METADATA_ARTIFACT.test(file)) {
    return new Response(null, { status: 204, headers: { 'access-control-allow-origin': '*', 'access-control-allow-methods': 'GET, HEAD, OPTIONS' } });
  }
  if (!['GET', 'HEAD'].includes(request.method)) return new Response('method not allowed', { status: 405, headers: { allow: 'GET, HEAD, OPTIONS' } });
  return null;
}

async function objectResponse(request, env, objectKey, file, cacheControl) {
  const object = await env.RELEASES.get(objectKey);
  if (!object) return notFound();
  const headers = new Headers({
    'cache-control': cacheControl,
    'content-length': String(object.size),
    'content-type': contentType(file),
    etag: object.httpEtag
  });
  if (METADATA_ARTIFACT.test(file)) headers.set('access-control-allow-origin', '*');
  else headers.set('content-disposition', `attachment; filename="${file}"`);
  return new Response(request.method === 'HEAD' ? null : object.body, { headers });
}

// Reads latest.json. A missing or malformed pointer is a deployment fault, so it returns a plain-text 503
// rather than falling through to the site assets (which would answer with HTML).
async function readLatestVersion(env) {
  const pointer = await env.RELEASES.get(LATEST_POINTER);
  if (!pointer) return { failure: unavailable('release pointer latest.json is missing from the release bucket') };
  let version;
  try {
    version = JSON.parse(await pointer.text()).version;
  } catch {
    version = undefined;
  }
  if (typeof version !== 'string' || !RELEASE_VERSION.test(version)) return { failure: unavailable('release pointer latest.json does not name a valid release version') };
  return { version };
}

// /v/<version>/<file> serves the immutable set for one version. Anything under /v/ that does not validate is a 404.
async function versionedResponse(request, env, pathname) {
  const match = VERSIONED_PATH.exec(pathname);
  if (!match || !RELEASE_VERSION.test(match[1]) || !RELEASE_PATH.test(match[2])) return notFound();
  const [, version, file] = match;
  return earlyResponse(request, file) ?? objectResponse(request, env, `v${version}/${file}`, file, IMMUTABLE_CACHE);
}

// Unversioned release routes resolve through latest.json to one version prefix, so a binary, its checksum,
// and its signature bundle are always read from the same published set.
async function latestResponse(request, env, file) {
  const early = earlyResponse(request, file);
  if (early) return early;
  const latest = await readLatestVersion(env);
  if (latest.failure) return latest.failure;
  return objectResponse(request, env, `v${latest.version}/${file}`, file, LATEST_CACHE);
}

// /latest.json publishes the validated pointer so installers can pin one version before fetching its files.
// The body is re-serialized from the validated version rather than proxied raw.
async function latestPointerResponse(request, env) {
  const early = earlyResponse(request, LATEST_POINTER);
  if (early) return early;
  const latest = await readLatestVersion(env);
  if (latest.failure) return latest.failure;
  const body = `${JSON.stringify({ version: latest.version })}\n`;
  return new Response(request.method === 'HEAD' ? null : body, {
    headers: {
      'access-control-allow-origin': '*',
      'cache-control': LATEST_CACHE,
      'content-length': String(body.length),
      'content-type': 'application/json; charset=utf-8'
    }
  });
}

function htmlResponse(request, html, status = 200) {
  return new Response(request.method === 'HEAD' ? null : html, {
    status,
    headers: { 'cache-control': status === 200 ? PAGE_CACHE : 'no-store', 'content-type': 'text/html; charset=utf-8' }
  });
}

function redirect(url, pathname, status) {
  const target = new URL(url);
  target.pathname = pathname;
  return Response.redirect(target, status);
}

// /releases/ lists every published version; /releases/v<version>/ shows one version's downloads and notes;
// /releases/latest/ redirects to the version named by latest.json. Unknown paths fall through to the 404 page.
async function releasesResponse(request, env, url) {
  const { pathname } = url;
  if (pathname === '/releases') return redirect(url, RELEASES_PATH, 301);
  const page = RELEASE_PAGE_PATH.exec(pathname);
  if (pathname !== RELEASES_PATH && pathname !== `${RELEASES_PATH}latest/` && !(page && RELEASE_VERSION.test(page[1]))) return env.ASSETS.fetch(request);
  if (!['GET', 'HEAD'].includes(request.method)) return new Response('method not allowed', { status: 405, headers: { allow: 'GET, HEAD' } });
  const latest = await readLatestVersion(env);
  if (latest.failure) return latest.failure;
  if (pathname === `${RELEASES_PATH}latest/`) return redirect(url, releasePath(latest.version), 302);
  if (page && !page[2]) return redirect(url, releasePath(page[1]), 301);
  const releases = await listReleases(env.RELEASES, latest.version);
  if (!page) {
    const manifest = await readManifest(env.RELEASES, latest.version);
    if (!manifest) return unavailable(`release v${latest.version} named by latest.json has no ztd-binaries.json manifest`);
    return htmlResponse(request, renderReleasesIndex({ releases, latest: latest.version, manifest }));
  }
  const release = releases.find(item => item.version === page[1]);
  if (!release) return env.ASSETS.fetch(request);
  const [manifest, notes] = await Promise.all([readManifest(env.RELEASES, release.version), releaseNotes(env, url, release.version)]);
  if (!manifest) return env.ASSETS.fetch(request);
  return htmlResponse(request, renderReleasePage({ manifest, latest: latest.version, published: release.published, notes }));
}

// Release notes are build-time fragments in the static assets. A version without notes still gets a page.
async function releaseNotes(env, url, version) {
  const response = await env.ASSETS.fetch(new Request(new URL(`${RELEASE_NOTES_PATH}${version}.html`, url)));
  return response.ok ? response.text() : null;
}

function unavailable(message) {
  return new Response(`${message}\n`, { status: 503, headers: { 'cache-control': 'no-store', 'content-type': 'text/plain; charset=utf-8' } });
}

async function route(request, env) {
  const url = new URL(request.url);
  if (url.hostname === WWW_HOSTNAME) {
    url.hostname = 'zerotrustdata.io';
    return Response.redirect(url, 301);
  }
  const buy = BUY_PATH.exec(url.pathname);
  if (buy) {
    const target = paymentLink(buy[1]);
    return target ? Response.redirect(target, 303) : notFound();
  }
  if (url.pathname === `/${LATEST_POINTER}`) return latestPointerResponse(request, env);
  if (url.pathname === '/v' || url.pathname.startsWith('/v/')) return versionedResponse(request, env, url.pathname);
  if (url.pathname === '/releases' || url.pathname.startsWith(RELEASES_PATH)) {
    try {
      return await releasesResponse(request, env, url);
    } catch (error) {
      console.error('releases page failed', error);
      return unavailable('release listing is temporarily unavailable');
    }
  }
  // Release-note fragments are composed into /releases/ pages and are not pages of their own.
  if (url.pathname.startsWith(RELEASE_NOTES_PATH)) return notFound();
  const file = releaseKey(url.pathname, request.headers.get('user-agent') ?? '');
  if (file) return latestResponse(request, env, file);
  return env.ASSETS.fetch(request);
}

export default {
  async fetch(request, env) {
    return withSecurityHeaders(await route(request, env), env);
  }
};
