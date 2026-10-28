// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { prepareReleaseNotes, releaseLinkResolver, renderReleaseNotes } from '../src/release-notes.js';
import { compareVersions, formatSize } from '../src/releases.js';

const REPOSITORY = fileURLToPath(new URL('../..', import.meta.url));
const NOTE = 'docs/release_notes/v2.3.x/v2.3.1.md';

test('compareVersions orders releases and prereleases by SemVer precedence', () => {
  const ordered = ['1.0.0-alpha', '1.0.0-alpha.1', '1.0.0-alpha.beta', '1.0.0-beta', '1.0.0-beta.2', '1.0.0-beta.11', '1.0.0-rc.1', '1.0.0', '1.0.10', '1.2.0', '10.0.0'];
  const shuffled = [...ordered].reverse().sort(compareVersions);
  assert.deepEqual(shuffled, ordered);
  assert.equal(compareVersions('2.3.2', '2.3.2'), 0);
});

test('formatSize uses binary units', () => {
  assert.equal(formatSize(512), '512 B');
  assert.equal(formatSize(2048), '2.0 KiB');
  assert.equal(formatSize(52428800), '50.0 MiB');
});

test('release-note links resolve to the release tag, other release pages, or pass through', () => {
  const resolve = releaseLinkResolver('2.3.1', NOTE);
  assert.equal(resolve('https://example.com/x'), 'https://example.com/x');
  assert.equal(resolve('#fixed'), '#fixed');
  assert.equal(resolve('mailto:security@example.com'), 'mailto:security@example.com');
  assert.equal(resolve('../../guides/getting_started.md#install'), 'https://github.com/zerotrustdata/ztd/blob/v2.3.1/docs/guides/getting_started.md#install');
  assert.equal(resolve('../../../internal/services/ztdo.go'), 'https://github.com/zerotrustdata/ztd/blob/v2.3.1/internal/services/ztdo.go');
  assert.equal(resolve('v2.3.0.md'), '/releases/v2.3.0/');
  assert.equal(resolve('../v2.4.x/v2.4.0-rc.1.md#breaking'), '/releases/v2.4.0-rc.1/#breaking');
});

test('release-note links that are absolute, protocol-relative, scripted, or escape the repository are rejected', () => {
  const resolve = releaseLinkResolver('2.3.1', NOTE);
  for (const href of ['/docs/x.md', '//evil.example/x', 'javascript:alert(1)', 'data:text/html,x', '../../../../etc/passwd']) {
    assert.throws(() => resolve(href), /release note docs\/release_notes\/v2\.3\.x\/v2\.3\.1\.md/, href);
  }
});

test('renderReleaseNotes keeps the optional title, drops the header, and escapes HTML', () => {
  const html = renderReleaseNotes('## [2.3.1] - 2026-10-01 - Patch <Release>\n\n### Fixed\n\n- See [the guide](../../guides/getting_started.md).\n- <script>x</script>\n', '2.3.1', NOTE);
  assert.match(html, /^<p class="release-title">Patch &lt;Release&gt;<\/p>\n/);
  assert.doesNotMatch(html, /2026-10-01|<script>/);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /href="https:\/\/github\.com\/zerotrustdata\/ztd\/blob\/v2\.3\.1\/docs\/guides\/getting_started\.md"/);
  assert.doesNotMatch(renderReleaseNotes('## [2.3.1] - 2026-10-01\n\nBody.\n', '2.3.1', NOTE), /release-title/);
});

test('renderReleaseNotes requires a header naming the same version', () => {
  assert.throws(() => renderReleaseNotes('## [2.3.2] - 2026-10-01\n\nBody.\n', '2.3.1', NOTE), /must start with "## \[2\.3\.1\]/);
  assert.throws(() => renderReleaseNotes('# Release 2.3.1\n\nBody.\n', '2.3.1', NOTE), /must start with/);
  assert.throws(() => renderReleaseNotes('## [2.3.1] - Oct 1\n\nBody.\n', '2.3.1', NOTE), /must start with/);
});

test('prepareReleaseNotes excludes explicit TBD drafts and rejects malformed release headers', async t => {
  const repository = await mkdtemp(join(tmpdir(), 'ztd-release-notes-'));
  t.after(() => rm(repository, { recursive: true, force: true }));
  const series = join(repository, 'docs/release_notes/v2.3.x');
  await mkdir(series, { recursive: true });
  await writeFile(join(series, 'v2.3.1.md'), '## [2.3.1] - 2026-10-01 - Published\n\nPublished notes.\n');
  const draftPath = join(series, 'v2.3.2.md');
  for (const header of ['## [2.3.2] - TBD', '## [2.3.2] - TBD - Upcoming', '## [2.3.2] - TBD - Upcoming (Draft)']) {
    await writeFile(draftPath, `${header}\r\n\r\nUnpublished notes.\r\n`);
    const fragments = await prepareReleaseNotes(repository);
    assert.deepEqual([...fragments.keys()], ['_release-notes/2.3.1.html']);
    assert.match(fragments.get('_release-notes/2.3.1.html'), /Published notes/);
  }
  for (const header of ['## [2.3.9] - TBD - Upcoming (Draft)', '## [2.3.2] - someday - Upcoming (Draft)']) {
    await writeFile(draftPath, `${header}\n\nUnpublished notes.\n`);
    await assert.rejects(prepareReleaseNotes(repository), /must start with/);
  }
  await writeFile(draftPath, '## [2.3.2] - 2026-10-02 - Released\n\nNew release.\n');
  assert.equal((await prepareReleaseNotes(repository)).has('_release-notes/2.3.2.html'), true);
});

test('committed dated release notes render into fragments', async () => {
  const fragments = await prepareReleaseNotes(REPOSITORY);
  assert.ok(fragments.size > 0);
  for (const [path, html] of fragments) {
    assert.match(path, /^_release-notes\/\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?\.html$/);
    assert.doesNotMatch(path, /compliance-evidence/);
    assert.doesNotMatch(html, /<script|\sstyle=|\son[a-z]+=/i, path);
  }
});
