// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { prepareHomepage, validateHomepage } from '../src/build.js';

async function sourceHomepage() {
  const [source, script] = await Promise.all([
    readFile(new URL('../src/index.html', import.meta.url), 'utf8'),
    readFile(new URL('../src/site.js', import.meta.url), 'utf8')
  ]);
  return prepareHomepage(source, script);
}

test('prepareHomepage extracts executable assets for the strict CSP', () => {
  const source = '<html><head><style>body { color: cyan; }</style></head><body><script src="/site.js" defer></script></body></html>';
  const homepage = prepareHomepage(source, 'window.ready = true;\n\n');

  assert.match(homepage.html, /href="\/styles\.css"/);
  assert.match(homepage.html, /src="\/site\.js" defer/);
  assert.doesNotMatch(homepage.html, /<style>/);
  assert.equal(homepage.styles, 'body { color: cyan; }\n');
  assert.equal(homepage.script, 'window.ready = true;\n');
});

test('validateHomepage rejects an inline script, which the strict CSP would block', async () => {
  const source = await readFile(new URL('../src/index.html', import.meta.url), 'utf8');
  const homepage = prepareHomepage(source.replace('</body>', '<script>window.inline = true;</script></body>'), '');
  assert.throws(() => validateHomepage(homepage), /validation failed/);
});

test('homepage uses only the ztd identity and canonical locations', async () => {
  const homepage = await sourceHomepage();

  validateHomepage(homepage);
  assert.doesNotMatch(`${homepage.html}${homepage.styles}${homepage.script}`, /g8e/i);
  assert.match(homepage.html, /https:\/\/zerotrustdata\.io\//);
  assert.match(homepage.html, /https:\/\/github\.com\/zerotrustdata\/ztd/);
  assert.match(homepage.html, />ztd</);
});

test('homepage status chip displays the current platform version from VERSION', async () => {
  const [homepage, version] = await Promise.all([
    sourceHomepage(),
    readFile(new URL('../../VERSION', import.meta.url), 'utf8')
  ]);
  const expected = version.trim();
  assert.match(homepage.html, new RegExp(`<span class="chip"><span class="dot"></span>ztd <b>${expected.replace(/\\./g, '\\\\.')}</b></span>`));
});

test('validateHomepage rejects legacy branding in any generated asset', () => {
  assert.throws(() => validateHomepage({
    html: '<link rel="canonical" href="https://zerotrustdata.io/"><a href="https://github.com/zerotrustdata/ztd"></a><div class="wordmark" id="pipeline"><div id="start"></div></div><link rel="stylesheet" href="/styles.css"><script src="/site.js"></script>',
    styles: '.legacy::after { content: "g8e"; }',
    script: ''
  }), /validation failed/);
});

test('homepage quick start leads with the signed installers and links the Releases pages', async () => {
  const { html } = await sourceHomepage();
  assert.match(html, /<button role="tab" aria-selected="true" aria-controls="tp-mac" id="tb-mac">macOS<\/button>/);
  assert.match(html, /id="tb-0"[^>]*>Linux<\/button>/);
  assert.match(html, /id="tb-w"[^>]*>Windows<\/button>/);
  assert.match(html, /data-copy="curl -fsSL https:\/\/zerotrustdata\.io\/start\.sh -o \/tmp\/ztd-start\.sh&#10;sh \/tmp\/ztd-start\.sh"/);
  assert.match(html, /Mac login password/);
  assert.match(html, /data-copy="irm https:\/\/zerotrustdata\.io\/start\.ps1 \| iex"/);
  assert.match(html, /<li class="nav-keep"><a href="\/releases\/">Releases<\/a><\/li>/);
  assert.match(html, /<a href="\/releases\/latest\/">Release notes<\/a>/);
});

test('homepage reduced-motion CSS drops offsets and jitter without freezing the page', async () => {
  const { styles } = await sourceHomepage();
  const block = styles.slice(styles.indexOf('@media (prefers-reduced-motion:reduce)'));
  assert.match(block, /\.rv,\.trace \.row,\.feat:hover\{transform:none\}/);
  assert.match(block, /\.ticker-track\{animation-duration:144s\}/);
  assert.doesNotMatch(block, /\*,\*::before,\*::after/, 'a blanket animation/transition kill freezes the page for Windows "Animation effects: off" users');
});
