// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test } from '@playwright/test';
import { fixtureCatalogExploratory } from '../src/fixtures/fixtures';

test('large bootstraps replace stale localStorage and reload through IndexedDB', async ({ page }) => {
  const mirrorOrigin = 'http://127.0.0.1:18082';
  const snapshot = {
    protocol_version: '1.0.0', source_id: 'browser-cache-test', high_water_sequence: 251,
    feed_chain_hash: 'b'.repeat(64), withdrawal_digest: '0'.repeat(64), batch_count: 1,
    generated_at: '2026-10-26T00:00:00Z', freshness: 'active',
  };
  const bootstrap = {
    protocol_version: '1.0.0', snapshot, source_freshness: 'active',
    recent_projections: Array.from({ length: 251 }, (_, index) => ({
      ...fixtureCatalogExploratory, dataset_id: `cache-dataset-${index}`, sequence: index + 1,
    })),
    proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 },
    generated_at: snapshot.generated_at,
  };
  let bootstrapRequests = 0;
  let snapshotRequests = 0;
  let streamRequests = 0;
  await page.addInitScript(({ origin, stale }) => {
    if (sessionStorage.getItem('cache-test-seeded')) return;
    sessionStorage.setItem('cache-test-seeded', 'true');
    localStorage.setItem('opendevops.feed.bootstrap.v1', JSON.stringify({
      schema_version: '1.0.0', mirror_origin: origin,
      bootstrap: { ...stale, snapshot: { ...stale.snapshot, source_id: 'obsolete-source', high_water_sequence: 0 }, recent_projections: [] },
      cached_at: '2026-10-25T00:00:00Z',
    }));
  }, { origin: mirrorOrigin, stale: bootstrap });
  await page.route('**/runtime.json', (route) => route.fulfill({
    json: { schema_version: '1.0.0', mirror_origin: mirrorOrigin },
  }));
  await page.route(`${mirrorOrigin}/**`, async (route) => {
    switch (new URL(route.request().url()).pathname) {
      case '/bootstrap':
        bootstrapRequests++;
        await route.fulfill({ json: bootstrap });
        break;
      case '/snapshot':
        snapshotRequests++;
        await route.fulfill({ json: snapshot });
        break;
      case '/stream':
        streamRequests++;
        await route.fulfill({ contentType: 'text/event-stream', body: ': connected\n\n' });
        break;
      default:
        await route.abort();
    }
  });

  await page.goto('/');
  await expect.poll(() => streamRequests).toBeGreaterThan(0);
  await expect.poll(() => page.evaluate(async () => {
    if (!indexedDB.databases) return null;
    if (!(await indexedDB.databases()).some((db) => db.name === 'opendevops.feed.cache.v1')) return null;
    return new Promise<number | null>((resolve, reject) => {
      const request = indexedDB.open('opendevops.feed.cache.v1');
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        const db = request.result;
        const transaction = db.transaction('feed', 'readonly');
        const read = transaction.objectStore('feed').get('current');
        read.onsuccess = () => resolve(read.result?.bootstrap?.recent_projections?.length ?? null);
        read.onerror = () => reject(read.error);
        transaction.oncomplete = () => db.close();
      };
    });
  })).toBe(251);
  expect(await page.evaluate(() => localStorage.getItem('opendevops.feed.bootstrap.v1'))).toBeNull();
  expect(bootstrapRequests).toBe(1);

  const priorStreams = streamRequests;
  const priorSnapshots = snapshotRequests;
  await page.reload();
  await expect.poll(() => streamRequests).toBeGreaterThan(priorStreams);
  expect(snapshotRequests).toBeGreaterThan(priorSnapshots);
  expect(bootstrapRequests).toBe(1);
  expect(await page.evaluate(() => localStorage.getItem('opendevops.feed.bootstrap.v1'))).toBeNull();
});
