// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { prepareHomepage, validateHomepage } from '../src/build.js';

test('prepareHomepage extracts executable assets for the strict CSP', () => {
  const source = '<html><head><style>body { color: cyan; }</style></head><body><script>window.ready = true;</script></body></html>';
  const homepage = prepareHomepage(source);

  assert.match(homepage.html, /href="\/styles\.css"/);
  assert.match(homepage.html, /src="\/site\.js" defer/);
  assert.doesNotMatch(homepage.html, /<style>|<script>window/);
  assert.equal(homepage.styles, 'body { color: cyan; }\n');
  assert.equal(homepage.script, 'window.ready = true;\n');
});

test('homepage uses only the ztd identity and canonical locations', async () => {
  const source = await readFile(new URL('../src/index.html', import.meta.url), 'utf8');
  const homepage = prepareHomepage(source);

  validateHomepage(homepage);
  assert.doesNotMatch(`${homepage.html}${homepage.styles}${homepage.script}`, /g8e/i);
  assert.match(homepage.html, /https:\/\/zerotrustdata\.io\//);
  assert.match(homepage.html, /https:\/\/github\.com\/zerotrustdata\/ztd/);
  assert.match(homepage.html, />ztd</);
});

test('validateHomepage rejects legacy branding in any generated asset', () => {
  assert.throws(() => validateHomepage({
    html: '<link rel="canonical" href="https://zerotrustdata.io/"><a href="https://github.com/zerotrustdata/ztd"></a><div class="wordmark" id="pipeline"><div id="start"></div></div><link rel="stylesheet" href="/styles.css"><script src="/site.js"></script>',
    styles: '.legacy::after { content: "g8e"; }',
    script: ''
  }), /validation failed/);
});
