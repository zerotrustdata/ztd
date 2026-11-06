// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test } from '@playwright/test';
import { installMirrorLayout } from './mirror-layout';

const mirrorOrigin = process.env.ZTD_PUBLIC_MIRROR_ORIGIN ?? 'http://127.0.0.1:8082';
const legacyStorageKey = 'opendevops.feed.bootstrap.v1';
const legacyDatabaseName = 'opendevops.feed.cache.v1';
const seedPath = '/__legacy-cache-seed';

test('startup removes the bootstrap caches written by earlier builds', async ({ page }) => {
  const sourceId = 'legacy-cache-source';
  await page.route(`**${seedPath}`, (route) => route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>seed</title>' }));
  await installMirrorLayout(page, { mirrorOrigin, sourceId, records: [] });

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
