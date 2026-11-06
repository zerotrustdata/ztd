// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const FEED_ORIGIN = 'https://feed.opendevops.ai';

interface RedirectRule {
  from: string;
  to: string;
  status: number;
}

function redirectRules(): RedirectRule[] {
  const text = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../public/_redirects'), 'utf8');
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '' && !line.startsWith('#'))
    .map((line) => {
      const [from = '', to = '', status = ''] = line.split(/\s+/u);
      return { from, to, status: Number(status) };
    });
}

// resolve applies the first matching rule, as the static site does.
function resolve(rules: RedirectRule[], path: string): RedirectRule & { target: string } | undefined {
  for (const rule of rules) {
    if (rule.from.endsWith('/*')) {
      const prefix = rule.from.slice(0, -1);
      if (path.startsWith(prefix)) return { ...rule, target: rule.to.replace(':splat', path.slice(prefix.length)) };
    } else if (rule.from === path) {
      return { ...rule, target: rule.to };
    }
  }
  return undefined;
}

// Removed anonymous proof routes and their former static projections. The feed
// bucket never holds these paths, so redirecting to the same path ends at 404
// there rather than at the SPA fallback's HTML or at another public object.
const REMOVED_PATHS = [
  '/proof-catalog',
  '/proof-head',
  '/proof-head.json',
  '/proof-segments',
  '/proofs/0a1b2c',
  '/proof-catalog/0a1b2c.json.gz',
  '/proof-meta/0a1b2c.json',
  '/proof-segments/0.jsonl.gz',
];

describe('production site redirects', () => {
  it.each(REMOVED_PATHS)('sends removed path %s to the same path on the feed host', (path) => {
    const resolved = resolve(redirectRules(), path);

    expect(resolved?.status).toBe(301);
    expect(resolved?.target).toBe(`${FEED_ORIGIN}${path}`);
  });

  it('redirects only removed proof paths', () => {
    for (const rule of redirectRules()) {
      expect(rule.from).toMatch(/^\/proof(?:s|-catalog|-head|-meta|-segments)\b/u);
    }
  });
});
