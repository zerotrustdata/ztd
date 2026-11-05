// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test } from '@playwright/test';

const mirrorOrigin = process.env.ZTD_PUBLIC_MIRROR_ORIGIN ?? 'http://127.0.0.1:8082';
const legacyStorageKey = 'opendevops.feed.bootstrap.v1';
const legacyDatabaseName = 'opendevops.feed.cache.v1';
const seedPath = '/__legacy-cache-seed';

test('startup removes the bootstrap caches written by earlier builds', async ({ page }) => {
  const sourceId = 'legacy-cache-source';
  await page.route(`**${seedPath}`, (route) => route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>seed</title>' }));
  await page.route(`${mirrorOrigin}/**`, async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/bootstrap') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          protocol_version: '1.0.0',
          snapshot: {
            protocol_version: '1.0.0', source_id: sourceId, high_water_sequence: 0, feed_chain_hash: 'd'.repeat(64),
            withdrawal_digest: '0'.repeat(64), batch_count: 1, generated_at: '2026-09-20T00:00:00Z', freshness: 'active',
          },
          source_freshness: 'active',
          recent_projections: [],
          task_index: [],
          proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 },
          generated_at: '2026-09-20T00:00:00Z',
        }),
      });
      return;
    }
    if (url.pathname === '/stream') {
      await route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': connected\n\n' });
      return;
    }
    await route.abort();
  });
  await page.route('**/runtime.json', (route) => route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({ schema_version: '1.0.0', mirror_origin: mirrorOrigin }),
  }));

  // Seed both legacy stores on the app's own origin before the app loads.
  await page.goto(seedPath);
  await page.evaluate(async ({ storageKey, databaseName }) => {
    localStorage.setItem(storageKey, JSON.stringify({ schema_version: '1.0.0', mirror_origin: 'https://stale.example', bootstrap: {}, cached_at: '2026-10-25T00:00:00Z' }));
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open(databaseName, 1);
      request.onupgradeneeded = () => request.result.createObjectStore('feed');
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        request.result.close();
        resolve();
      };
    });
  }, { storageKey: legacyStorageKey, databaseName: legacyDatabaseName });
  expect(await page.evaluate(async (databaseName) => (await indexedDB.databases()).some((db) => db.name === databaseName), legacyDatabaseName)).toBe(true);

  await page.goto('/');

  await expect.poll(() => page.evaluate((storageKey) => localStorage.getItem(storageKey), legacyStorageKey)).toBeNull();
  await expect.poll(() => page.evaluate(async (databaseName) => (await indexedDB.databases()).some((db) => db.name === databaseName), legacyDatabaseName)).toBe(false);
});
