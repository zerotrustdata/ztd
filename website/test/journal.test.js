// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';
import { prepareJournal, renderMarkdown, validatePosts } from '../src/journal.js';

const post = { slug: 'first-note', title: 'First note', date: '2026-10-12', description: 'Experiments & evidence', author: 'Danny', tags: ['Engineering'] };

test('post metadata rejects duplicate routes, path traversal, and invalid dates', () => {
  validatePosts([post]);
  for (const invalid of [[], [post, post], [{ ...post, slug: '../private' }], [{ ...post, date: '2026-02-30' }], [{ ...post, date: 'not-a-date' }], [{ ...post, title: '' }], [{ ...post, tags: [] }]]) {
    assert.throws(() => validatePosts(invalid));
  }
});

test('Markdown supports tables and stable unique section links while keeping the strict CSP', () => {
  const rendered = renderMarkdown('# First note\n\nIntro.\n\n## Results\n\n| Host | Count |\n| :--- | ---: |\n| local | 2,000 |\n\n## Results\n\n`<script>` and **evidence**.\n\n<script>alert(1)</script>\n', 'First note');
  assert.deepEqual(rendered.headings.map(heading => heading.id), ['section-results', 'section-results-2']);
  assert.match(rendered.html, /<table>/);
  assert.match(rendered.html, /class="align-right"/);
  assert.match(rendered.html, /role="region".*tabindex="0"/);
  assert.match(rendered.html, /&lt;script&gt;/);
  assert.doesNotMatch(rendered.html, /<script|\sstyle=/);
  assert.throws(() => renderMarkdown('# Wrong title\n\ntext', 'First note'), /must match/);
  assert.throws(() => renderMarkdown('# First note\n\n# Another H1\n', 'First note'), /H2/);
});

test('Markdown refuses executable URLs and private editorial links', () => {
  for (const href of ['javascript:alert', 'data:text/html,hello', '../../../scale/out/report.json', '//unknown.example/path']) {
    assert.throws(() => renderMarkdown(`# First note\n\n[link](${href})`, 'First note'), /public URL/);
  }
  assert.match(renderMarkdown('# First note\n\n[results](#section-results)', 'First note').html, /href="#section-results"/);
});

test('daily posts generate matching pages, chronological index, feed, and sitemap', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'ztd-journal-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await mkdir(join(directory, 'posts'));
  const newer = { ...post, slug: 'second-note', title: 'Second note', date: '2026-10-13' };
  await writeFile(join(directory, 'posts.json'), JSON.stringify([post, newer]));
  await writeFile(join(directory, 'posts/first-note.md'), '# First note\n\n## Results\n\nMeasured once.');
  await writeFile(join(directory, 'posts/second-note.md'), '# Second note\n\n## Results\n\nMeasured twice.');
  const output = await prepareJournal(pathToFileURL(`${directory}/`));
  const index = output.get('build-in-public/index.html');
  assert.ok(index.indexOf('Second note') < index.indexOf('First note'));
  const article = output.get('build-in-public/second-note/index.html');
  assert.match(article, /href="\/build-in-public\/first-note\/"/);
  assert.match(article, /href="#section-results"/);
  assert.match(article, /id="section-results"/);
  for (const item of [post, newer]) {
    const url = `https://zerotrustdata.io/build-in-public/${item.slug}/`;
    assert.ok(output.get('sitemap.xml').includes(`<loc>${url}</loc>`));
    assert.ok(output.get('build-in-public/feed.xml').includes(`<link>${url}</link>`));
    assert.ok(output.get(`build-in-public/${item.slug}/index.html`).includes(`<link rel="canonical" href="${url}">`));
  }
  assert.match(output.get('build-in-public/feed.xml'), /Experiments &amp; evidence/);
});

test('published field note preserves measurements, exposes no local sources, and has complete section navigation', async () => {
  const output = await prepareJournal();
  const article = output.get('build-in-public/enrolling-two-thousand-operators/index.html');
  assert.match(article, /54\.82 s; 2,000 workers/);
  assert.match(article, /2,001\/2,001 succeeded; 0 failures/);
  assert.match(article, /Neither establishes a maximum supported fleet size/);
  assert.match(article, /raw reports are not published/);
  assert.equal((article.match(/<h1>/g) ?? []).length, 1);
  const headings = [...article.matchAll(/<h2 id="([^"]+)"/g)].map(match => match[1]);
  assert.ok(headings.length > 20);
  for (const id of headings) assert.ok(article.includes(`href="#${id}"`));
  for (const content of output.values()) {
    assert.doesNotMatch(content, /g8e|\.local\.dev|\.\.\/\.\.\/|<script(?! src=)|\sstyle=|\sonclick=/i);
  }
  const source = await readFile(new URL('../src/index.html', import.meta.url), 'utf8');
  assert.match(source, /class="journal-nav"><a href="\/build-in-public\/"/);
  const config = await readFile(new URL('../wrangler.jsonc', import.meta.url), 'utf8');
  assert.match(config, /"not_found_handling": "404-page"/);
});
