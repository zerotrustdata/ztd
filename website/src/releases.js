// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Releases pages, rendered by the Worker from the ztd-releases R2 bucket: the same immutable v<version>/ sets the
// install scripts download. A version is listed only when its ztd-binaries.json manifest exists (release-r2.sh
// uploads the manifest last, after every other object is verified) and it is not newer than latest.json, so an
// uploaded but unpromoted set never appears as a release. Release notes are rendered at build time from
// docs/release_notes/ into /_release-notes/<version>.html fragments (src/release-notes.js).
// This module must stay free of Node.js imports because worker.js bundles it.

import { ORIGIN, RELEASES_PATH, REPOSITORY_URL, escape, layout } from './layout.js';

export const RELEASE_VERSION = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?$/;
export const RELEASE_NOTES_PATH = '/_release-notes/';
const MANIFEST = 'ztd-binaries.json';
const MANIFEST_KEY = /^v([^/]+)\/ztd-binaries\.json$/;
const SHA256 = /^[0-9a-f]{64}$/;
const CERTIFICATE_ISSUER = 'https://token.actions.githubusercontent.com';
const DESCRIPTION = 'Download signed ztd release binaries, checksums, and Sigstore bundles, or install and start a Gateway with one command.';
const PLATFORMS = {
  'linux/amd64': 'Linux x86-64',
  'linux/arm64': 'Linux ARM64',
  'linux/386': 'Linux x86 (32-bit)',
  'darwin/arm64': 'macOS Apple silicon',
  'darwin/amd64': 'macOS Intel',
  'windows/amd64': 'Windows x86-64',
  'windows/arm64': 'Windows ARM64'
};
const PLATFORM_ORDER = Object.keys(PLATFORMS);

export const startCommand = version => `curl -fsSL ${ORIGIN}/start.sh | ${version ? `ZTD_VERSION=${version} ` : ''}sh`;
export const installCommand = version => `curl -fsSL ${ORIGIN}/ztd.sh | ${version ? `ZTD_VERSION=${version} ` : ''}sh`;
export const windowsStartCommand = version => `${version ? `$env:ZTD_VERSION='${version}'; ` : ''}irm ${ORIGIN}/start.ps1 | iex`;
export const windowsInstallCommand = version => `${version ? `$env:ZTD_VERSION='${version}'; ` : ''}irm ${ORIGIN}/ztd.ps1 | iex`;
export const releasePath = version => `${RELEASES_PATH}v${version}/`;
const downloadPath = (version, file) => `/v/${version}/${file}`;
const tagUrl = version => `${REPOSITORY_URL}/releases/tag/v${version}`;

// Orders X.Y.Z[-prerelease] versions; a prerelease sorts before its release (identifiers compared per SemVer).
export function compareVersions(a, b) {
  const [coreA, preA] = splitVersion(a);
  const [coreB, preB] = splitVersion(b);
  for (let i = 0; i < 3; i++) if (coreA[i] !== coreB[i]) return coreA[i] - coreB[i];
  if (!preA || !preB) return preA ? -1 : preB ? 1 : 0;
  const partsA = preA.split('.');
  const partsB = preB.split('.');
  for (let i = 0; i < Math.max(partsA.length, partsB.length); i++) {
    if (partsA[i] === undefined) return -1;
    if (partsB[i] === undefined) return 1;
    const numericA = /^\d+$/.test(partsA[i]);
    const numericB = /^\d+$/.test(partsB[i]);
    if (numericA && numericB && Number(partsA[i]) !== Number(partsB[i])) return Number(partsA[i]) - Number(partsB[i]);
    if (numericA !== numericB) return numericA ? -1 : 1;
    if (partsA[i] !== partsB[i]) return partsA[i] < partsB[i] ? -1 : 1;
  }
  return 0;
}

function splitVersion(version) {
  const dash = version.indexOf('-');
  const core = (dash === -1 ? version : version.slice(0, dash)).split('.').map(Number);
  return [core, dash === -1 ? '' : version.slice(dash + 1)];
}

// Lists every published version, newest first, with the manifest upload time as the publication time.
export async function listReleases(bucket, latest) {
  const releases = [];
  let cursor;
  do {
    const page = await bucket.list({ prefix: 'v', cursor });
    for (const object of page.objects) {
      const match = MANIFEST_KEY.exec(object.key);
      if (!match || !RELEASE_VERSION.test(match[1]) || compareVersions(match[1], latest) > 0) continue;
      releases.push({ version: match[1], published: object.uploaded });
    }
    cursor = page.truncated ? page.cursor : undefined;
  } while (cursor);
  return releases.sort((a, b) => compareVersions(b.version, a.version));
}

// Reads and validates one version's manifest. Returns null when the object is absent and throws when it is malformed.
export async function readManifest(bucket, version) {
  const object = await bucket.get(`v${version}/${MANIFEST}`);
  if (!object) return null;
  const manifest = JSON.parse(await object.text());
  if (manifest?.schema_version !== 1 || manifest.version !== `v${version}` || !Array.isArray(manifest.targets) || !manifest.targets.length) {
    throw new Error(`v${version}/${MANIFEST} is not a valid schema 1 manifest for this version`);
  }
  for (const target of manifest.targets) {
    const filename = `ztd-${target?.os}-${target?.arch}${target?.os === 'windows' ? '.exe' : ''}`;
    if (!/^[a-z0-9]+$/.test(target?.os ?? '') || !/^[a-z0-9]+$/.test(target?.arch ?? '') || target.filename !== filename ||
      target.checksum !== `${filename}.sha256` || !SHA256.test(target.sha256) || !Number.isSafeInteger(target.size) || target.size <= 0) {
      throw new Error(`v${version}/${MANIFEST} has an invalid target entry`);
    }
  }
  return {
    version,
    revision: /^[0-9a-f]{40}$/.test(manifest.source_revision) ? manifest.source_revision : null,
    built: Number.isFinite(Date.parse(manifest.build_time)) ? new Date(manifest.build_time) : null,
    targets: [...manifest.targets].sort((a, b) => platformRank(a) - platformRank(b))
  };
}

function platformRank(target) {
  const rank = PLATFORM_ORDER.indexOf(`${target.os}/${target.arch}`);
  return rank === -1 ? PLATFORM_ORDER.length : rank;
}

const platformLabel = target => PLATFORMS[`${target.os}/${target.arch}`] ?? `${target.os}/${target.arch}`;
const dateLabel = date => date ? new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(date) : 'Unknown date';
const time = date => date ? `<time datetime="${date.toISOString()}">${dateLabel(date)}</time>` : '<span>Unknown date</span>';
export const formatSize = bytes => bytes >= 1048576 ? `${(bytes / 1048576).toFixed(1)} MiB` : bytes >= 1024 ? `${(bytes / 1024).toFixed(1)} KiB` : `${bytes} B`;

// A copyable command line. The copy button stays hidden until journal.js confirms clipboard access.
function command(text, label) {
  return `<div class="command"><span class="command-label">${escape(label)}</span><pre><code>${escape(text)}</code></pre><button type="button" class="copy-command" data-copy="${escape(text)}" hidden>Copy</button></div>`;
}

function downloadTable(manifest) {
  const { version } = manifest;
  const rows = manifest.targets.map(target => `<tr>
<th scope="row">${escape(platformLabel(target))}${target.fips_module ? ' <span class="badge" title="Built with the Go Cryptographic Module ' + escape(target.fips_module) + '">FIPS 140-3</span>' : ''}</th>
<td><a class="download" href="${downloadPath(version, target.filename)}" download>${escape(target.filename)}</a></td>
<td class="size">${formatSize(target.size)}</td>
<td class="digest"><code>${target.sha256}</code></td>
<td class="aux"><a href="${downloadPath(version, target.checksum)}">.sha256</a><a href="${downloadPath(version, `${target.filename}.sigstore.json`)}">.sigstore.json</a></td>
</tr>`).join('');
  return `<div class="table-scroll" role="region" aria-label="ztd ${escape(version)} downloads" tabindex="0"><table class="downloads">
<thead><tr><th scope="col">Platform</th><th scope="col">Binary</th><th scope="col">Size</th><th scope="col">SHA-256</th><th scope="col">Verify</th></tr></thead>
<tbody>${rows}</tbody></table></div>
<p class="release-files">Whole set: <a href="${downloadPath(version, 'checksums-sha256.txt')}">checksums-sha256.txt</a><a href="${downloadPath(version, MANIFEST)}">${MANIFEST}</a><a href="${tagUrl(version)}">GitHub release ↗</a></p>`;
}

function installSection(version, pinned) {
  return `<section class="install" aria-labelledby="install-title"><h2 id="install-title" class="label">${pinned ? `INSTALL ${escape(version)}` : 'ONE COMMAND'}</h2>
${command(startCommand(pinned ? version : ''), 'Install, start a Gateway, and sign in — Linux and macOS')}
${command(windowsStartCommand(pinned ? version : ''), 'Install, start a Gateway, and sign in — Windows PowerShell')}
${command(installCommand(pinned ? version : ''), 'Install the binary only — Linux and macOS')}
${command(windowsInstallCommand(pinned ? version : ''), 'Install the binary only — Windows PowerShell')}
<p class="install-note">The installers verify each binary against its published SHA-256 checksum before writing it and never use sudo or Administrator. <a href="${REPOSITORY_URL}/blob/main/docs/guides/getting_started.md">Getting started ↗</a></p></section>`;
}

export function renderReleasesIndex({ releases, latest, manifest }) {
  const items = releases.map(release => `<li class="release-row">
<a class="release-version" href="${releasePath(release.version)}">v${escape(release.version)}</a>${release.version === latest ? '<span class="badge latest">Latest</span>' : ''}
<span class="release-date">${time(release.published)}</span>
<span class="release-links"><a href="${releasePath(release.version)}">Downloads and notes <span aria-hidden="true">→</span></a><a href="${tagUrl(release.version)}">GitHub ↗</a></span></li>`).join('');
  return layout({
    title: 'Releases',
    description: DESCRIPTION,
    path: RELEASES_PATH,
    section: 'releases',
    current: true,
    content: `
<section class="journal-hero releases-hero" aria-labelledby="releases-title"><div class="hero-kicker"><span class="status-dot" aria-hidden="true"></span>SIGNED RELEASES<span class="hero-edition">LATEST / v${escape(latest)}</span></div>
<h1 id="releases-title">Get <span>ztd.</span></h1>
<div class="hero-bottom"><p>One static binary for the Gateway, Operators, CLI, and TUI.<br><span>Every release is checksummed, signed with Sigstore, and published under an immutable version path.</span></p><a class="text-link" href="${releasePath(latest)}">v${escape(latest)} release notes <span aria-hidden="true">↗</span><small>Downloads, checksums, and verification</small></a></div></section>
${installSection(latest, false)}
<section class="release-latest" aria-labelledby="latest-title"><div class="section-topline"><h2 id="latest-title">Latest release <span>v${escape(latest)}</span></h2><span>${time(manifest.built)}</span></div>${downloadTable(manifest)}</section>
<section class="release-history" aria-labelledby="history-title"><div class="section-topline"><h2 id="history-title">All releases <span>${String(releases.length).padStart(2, '0')}</span></h2><span>NEWEST FIRST ↓</span></div><ol class="release-list">${items}</ol></section>`
  });
}

export function renderReleasePage({ manifest, latest, published, notes }) {
  const { version } = manifest;
  const identity = `${REPOSITORY_URL}/.github/workflows/release-binary.yml@refs/tags/v${version}`;
  const example = manifest.targets[0].filename;
  return layout({
    title: `ztd v${version}`,
    description: `ztd v${version} release binaries, checksums, Sigstore bundles, and release notes.`,
    path: releasePath(version),
    section: 'releases',
    content: `
<div class="article-breadcrumb"><a href="${RELEASES_PATH}">← All releases</a><span>RELEASES / v${escape(version)}</span></div>
<header class="release-header"><h1>ztd <span>v${escape(version)}</span>${version === latest ? '<span class="badge latest">Latest</span>' : ''}</h1>
<p class="post-meta">Published ${time(published)}${manifest.revision ? `<span aria-hidden="true">/</span><span>Source <a href="${REPOSITORY_URL}/commit/${manifest.revision}"><code>${manifest.revision.slice(0, 12)}</code></a></span>` : ''}<span aria-hidden="true">/</span><a href="${tagUrl(version)}">GitHub release ↗</a></p></header>
${installSection(version, true)}
<section aria-labelledby="downloads-title"><div class="section-topline"><h2 id="downloads-title">Downloads <span>${String(manifest.targets.length).padStart(2, '0')}</span></h2><span>IMMUTABLE / v${escape(version)}</span></div>${downloadTable(manifest)}</section>
<section class="verify" aria-labelledby="verify-title"><h2 id="verify-title" class="label">VERIFY A DOWNLOAD</h2>
${command(`sha256sum -c ${example}.sha256`, 'Checksum (macOS: shasum -a 256 -c)')}
${command(`cosign verify-blob --bundle ${example}.sigstore.json --certificate-identity '${identity}' --certificate-oidc-issuer ${CERTIFICATE_ISSUER} ${example}`, 'Sigstore signature')}</section>
<section class="release-notes" aria-labelledby="notes-title"><div class="section-topline"><h2 id="notes-title">Release notes</h2><span>v${escape(version)}</span></div>
${notes ? `<div class="article-body">${notes}</div>` : `<p>The release notes for this version are on <a href="${tagUrl(version)}">GitHub ↗</a>.</p>`}</section>`
  });
}
