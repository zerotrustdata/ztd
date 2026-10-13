// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Renders docs/release_notes/v<major>.<minor>.x/v<version>.md into /_release-notes/<version>.html fragments that
// the Worker composes into /releases/v<version>/. Relative links resolve to the file at that release tag on GitHub.

import { readdir, readFile } from 'node:fs/promises';
import { join, posix } from 'node:path';
import { REPOSITORY_URL, escape } from './layout.js';
import { renderMarkdownBody } from './journal.js';
import { RELEASE_NOTES_PATH, RELEASE_VERSION, releasePath } from './releases.js';

const NOTES_DIRECTORY = 'docs/release_notes';
const SERIES = /^v(\d+)\.(\d+)\.x$/;
const NOTES_FILE = /^v(.+)\.md$/;
// Release compliance projections (INV-REL-COMP-01) are committed beside the notes but are not release notes.
const EVIDENCE_FILE = /^v.+-compliance-evidence\.md$/;

// Resolves a link in a release note. Public URLs and anchors pass through, links to other release notes point at
// their Releases page, and other repository-relative links point at the file as of the release tag. Links that
// escape the repository are rejected.
export function releaseLinkResolver(version, notePath) {
  return href => {
    if (/^(?:https?:\/\/|mailto:|#)/i.test(href)) return href;
    if (/^[a-z][a-z0-9+.-]*:|^\/\//i.test(href) || href.startsWith('/')) throw new Error(`release note ${notePath} has an unsupported link: ${href}`);
    const [path, anchor = ''] = href.split(/(?=#)/);
    const target = posix.normalize(posix.join(posix.dirname(notePath), path));
    if (target.startsWith('..')) throw new Error(`release note ${notePath} links outside the repository: ${href}`);
    // Another version's notes may postdate this tag, so those links stay on the Releases pages.
    const notes = /^docs\/release_notes\/v\d+\.\d+\.x\/v(.+)\.md$/.exec(target);
    if (notes && RELEASE_VERSION.test(notes[1])) return `${releasePath(notes[1])}${anchor}`;
    return `${REPOSITORY_URL}/blob/v${version}/${target}${anchor}`;
  };
}

// The first line is "## [X.Y.Z] - YYYY-MM-DD", optionally followed by " - <title>". The page renders the version and
// date itself, so only the title is kept.
export function renderReleaseNotes(markdown, version, notePath) {
  const normalized = markdown.replace(/\r\n/g, '\n').trim();
  const header = normalized.slice(0, normalized.indexOf('\n'));
  const match = new RegExp(`^## \\[${version.replace(/\./g, '\\.')}\\] - \\d{4}-\\d{2}-\\d{2}(?: - (.+))?$`).exec(header);
  if (!match) throw new Error(`release note ${notePath} must start with "## [${version}] - YYYY-MM-DD"`);
  const title = match[1] ? `<p class="release-title">${escape(match[1].trim())}</p>\n` : '';
  return title + renderMarkdownBody(normalized.slice(header.length + 1).trim(), releaseLinkResolver(version, notePath)).html;
}

export async function prepareReleaseNotes(repositoryDirectory) {
  const files = new Map();
  const root = join(repositoryDirectory, NOTES_DIRECTORY);
  for (const series of (await readdir(root)).sort()) {
    const seriesMatch = SERIES.exec(series);
    if (!seriesMatch) continue;
    for (const name of (await readdir(join(root, series))).sort()) {
      if (EVIDENCE_FILE.test(name)) continue;
      const version = NOTES_FILE.exec(name)?.[1];
      if (!version) continue;
      const notePath = `${NOTES_DIRECTORY}/${series}/${name}`;
      if (!RELEASE_VERSION.test(version) || !version.startsWith(`${seriesMatch[1]}.${seriesMatch[2]}.`)) throw new Error(`release note ${notePath} is not a ${series} version`);
      const markdown = await readFile(join(repositoryDirectory, notePath), 'utf8');
      files.set(`${RELEASE_NOTES_PATH.slice(1)}${version}.html`, renderReleaseNotes(markdown, version, notePath));
    }
  }
  return files;
}
