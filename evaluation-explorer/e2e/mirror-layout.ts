// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Serves the public mirror's static layout to a Playwright page: pointer.json,
// the digest-addressed read model it names, live/status.json with no active runs,
// and a 404 for any proof catalog entry or live run bundle a spec does not supply.
// Specs add their own routes (proof bytes, for example) through `extra`.

import { createHash } from 'node:crypto';
import type { Page, Route } from '@playwright/test';

export interface MirrorLayoutOptions {
  mirrorOrigin: string;
  sourceId: string;
  /** Read-model recent projections. Each gets its sequence from its position, newest first. */
  records: unknown[];
  taskIndex?: unknown[];
  /** Serves a request the layout does not cover. Return true once the request is fulfilled. */
  extra?: (url: URL, route: Route) => Promise<boolean>;
}

const CHAIN_HASH = 'b'.repeat(64);
const ZERO_DIGEST = '0'.repeat(64);

function sha256(text: string): string {
  return createHash('sha256').update(text).digest('hex');
}

/** Installs the layout for `options.records` on the page and returns the mirror requests in order. */
export async function installMirrorLayout(page: Page, options: MirrorLayoutOptions): Promise<string[]> {
  const { mirrorOrigin, sourceId, records } = options;
  const generatedAt = '2026-09-20T00:00:00Z';
  const snapshot = {
    protocol_version: '1.0.0',
    source_id: sourceId,
    high_water_sequence: records.length,
    feed_chain_hash: CHAIN_HASH,
    withdrawal_digest: ZERO_DIGEST,
    batch_count: 1,
    generated_at: generatedAt,
    freshness: 'active',
  };
  const readModel = JSON.stringify({
    protocol_version: '1.0.0',
    snapshot,
    source_freshness: 'active',
    recent_projections: records.map((record, index) => ({ ...(record as object), sequence: index + 1 })).reverse(),
    task_index: options.taskIndex ?? [],
    schema_version: '2.0.0',
    generated_at: generatedAt,
  });
  const readModelSha256 = sha256(readModel);
  // Quiet pointer: the live poll stays off, so the page's requests are only the feed's own.
  const pointer = {
    schema_version: '1.0.0',
    source_id: sourceId,
    revision: 1,
    read_model_sha256: readModelSha256,
    high_water_sequence: snapshot.high_water_sequence,
    history_first_sequence: 1,
    feed_chain_hash: CHAIN_HASH,
    withdrawal_digest: ZERO_DIGEST,
    active: false,
    generated_at: generatedAt,
  };
  const liveStatus = { schema_version: '1.0.0', snapshot, active_runs: [], tail: [] };

  const requests: string[] = [];
  await page.route(`${mirrorOrigin}/**`, async (route: Route) => {
    const url = new URL(route.request().url());
    requests.push(url.origin + url.pathname + url.search);
    if (url.pathname === '/pointer.json') {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(pointer) });
      return;
    }
    if (url.pathname === `/read-model/${readModelSha256}.json.gz`) {
      await route.fulfill({ contentType: 'application/json', body: readModel });
      return;
    }
    if (url.pathname === '/live/status.json') {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify(liveStatus) });
      return;
    }
    if (url.pathname.startsWith('/proof-meta/') || url.pathname.startsWith('/live/runs/')) {
      await route.fulfill({ status: 404, body: 'not found' });
      return;
    }
    if (options.extra && await options.extra(url, route)) return;
    await route.abort();
  });
  await page.route('**/runtime.json', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ schema_version: '1.0.0', mirror_origin: mirrorOrigin }),
    });
  });
  return requests;
}
