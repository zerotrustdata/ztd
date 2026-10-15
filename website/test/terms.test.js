// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import test from 'node:test';
import { TERMS_PAGES, prepareTerms, renderTerms } from '../src/terms.js';

test('terms page renders at /terms/ with its canonical URL', async () => {
  const files = await prepareTerms();
  const page = files.get('terms/index.html');

  assert.ok(page);
  assert.match(page, /<link rel="canonical" href="https:\/\/zerotrustdata\.io\/terms\/">/);
  assert.match(page, /<h1>Commercial License and Services Terms<\/h1>/);
  assert.doesNotMatch(page, /\sstyle=/);
  assert.match(page, /href="\/terms\/oem\/"/);
});

test('OEM agreement template renders at /terms/oem/', async () => {
  const page = (await prepareTerms()).get('terms/oem/index.html');

  assert.ok(page);
  assert.match(page, /<link rel="canonical" href="https:\/\/zerotrustdata\.io\/terms\/oem\/">/);
  assert.match(page, /<h1>ztd Commercial OEM License Agreement<\/h1>/);
  assert.match(page, /\[LICENSEE LEGAL NAME\]/);
});

test('terms Markdown must open with the terms title', () => {
  assert.throws(() => renderTerms('# Something else\n\nBody', TERMS_PAGES[0]), /H1/);
});
