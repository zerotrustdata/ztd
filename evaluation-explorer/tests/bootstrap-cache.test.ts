// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { evalStore } from '../src/state/store';
import { startFeed, stopFeed } from '../src/state/startup';
import {
  BOOTSTRAP_STORAGE_KEY,
  clearCachedBootstrap,
  loadCachedBootstrap,
  saveCachedBootstrap,
} from '../src/state/cache';
import { fixtureCatalogExploratory } from '../src/fixtures/fixtures';
import type { FeedBootstrap, FeedSnapshot } from '../src/contract/types';

const testOrigin = 'https://mirror.example';

const baseSnapshot: FeedSnapshot = {
  protocol_version: '1.0.0',
  source_id: 'cache-test-source',
  high_water_sequence: 50,
  feed_chain_hash: '2'.repeat(64),
  withdrawal_digest: '0'.repeat(64),
  batch_count: 1,
  generated_at: '2026-10-25T00:00:00Z',
  freshness: 'active',
};

const baseBootstrap: FeedBootstrap = {
  protocol_version: '1.0.0',
  snapshot: baseSnapshot,
  source_freshness: 'active',
  recent_projections: [{ ...fixtureCatalogExploratory, sequence: 50 }],
  proof_catalog_summary: { artifact_count: 2, total_byte_size: 1024 },
  generated_at: baseSnapshot.generated_at,
};

function json(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200 });
}

describe('bootstrap localStorage cache', () => {
  beforeEach(() => {
    localStorage.clear();
    evalStore.loadFixtures([], []);
  });

  afterEach(() => {
    stopFeed();
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  it('saves, loads, and clears a valid bootstrap record', () => {
    saveCachedBootstrap(testOrigin, baseBootstrap);
    const cached = loadCachedBootstrap();
    expect(cached).not.toBeNull();
    expect(cached?.mirror_origin).toBe(testOrigin);
    expect(cached?.bootstrap.snapshot.high_water_sequence).toBe(50);

    clearCachedBootstrap();
    expect(loadCachedBootstrap()).toBeNull();
  });

  it('clears invalid or corrupted entries gracefully', () => {
    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, 'not-valid-json');
    expect(loadCachedBootstrap()).toBeNull();
    expect(localStorage.getItem(BOOTSTRAP_STORAGE_KEY)).toBeNull();

    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, JSON.stringify({ schema_version: '9.9.9' }));
    expect(loadCachedBootstrap()).toBeNull();
    expect(localStorage.getItem(BOOTSTRAP_STORAGE_KEY)).toBeNull();
  });

  it('tolerates localStorage.setItem throwing (e.g. quota exceeded)', () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('QuotaExceededError');
    });
    expect(() => saveCachedBootstrap(testOrigin, baseBootstrap)).not.toThrow();
    spy.mockRestore();
  });

  it('skips fetching full bootstrap when cached snapshot is already up to date', async () => {
    saveCachedBootstrap(testOrigin, baseBootstrap);

    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      const url = new URL(String(input), testOrigin);
      if (url.pathname === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: testOrigin });
      if (url.pathname === '/snapshot') return json(baseSnapshot);
      if (url.pathname === '/bootstrap') throw new Error('should not download bootstrap when snapshot matches!');
      throw new Error(`Unexpected request: ${url.pathname}`);
    });

    const streamFactory = vi.fn(() => ({ close() {} }));

    await startFeed({ fetchImpl, streamFactory });

    // Store is populated from cache, /bootstrap was never called, stream connected directly.
    expect(evalStore.getState().observedSequence).toBe(50);
    expect(streamFactory).toHaveBeenCalledWith(testOrigin, 'cache-test-source', 50, expect.any(Object));
    expect(fetchImpl.mock.calls.some(([req]) => String(req).includes('/bootstrap'))).toBe(false);
  });

  it('hydrates store immediately from cache and fetches newer bootstrap when snapshot advanced', async () => {
    saveCachedBootstrap(testOrigin, baseBootstrap);

    const newerSnapshot: FeedSnapshot = {
      ...baseSnapshot,
      high_water_sequence: 60,
      feed_chain_hash: '3'.repeat(64),
    };
    const newerBootstrap: FeedBootstrap = {
      ...baseBootstrap,
      snapshot: newerSnapshot,
      recent_projections: [{ ...fixtureCatalogExploratory, sequence: 60 }],
    };

    let resolveBootstrap!: (res: Response) => void;
    const bootstrapDeferred = new Promise<Response>((resolve) => {
      resolveBootstrap = resolve;
    });

    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      const url = new URL(String(input), testOrigin);
      if (url.pathname === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: testOrigin });
      if (url.pathname === '/snapshot') return json(newerSnapshot);
      if (url.pathname === '/bootstrap') return bootstrapDeferred;
      throw new Error(`Unexpected request: ${url.pathname}`);
    });

    const streamFactory = vi.fn(() => ({ close() {} }));

    // Start feed without awaiting so we can assert on initial cached hydration.
    const startPromise = startFeed({ fetchImpl, streamFactory });

    // Store should already have data from localStorage cache!
    expect(evalStore.getState().observedSequence).toBe(50);
    expect(evalStore.getState().currentSnapshot?.source_id).toBe('cache-test-source');
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toEqual(fixtureCatalogExploratory);
    expect(evalStore.getState().connection).toBe('connecting');

    // Complete background network request with newer sequence.
    resolveBootstrap(json(newerBootstrap));
    await startPromise;

    // After network finishes, store has updated sequence and stream connected.
    expect(evalStore.getState().observedSequence).toBe(60);
    expect(streamFactory).toHaveBeenCalledWith(testOrigin, 'cache-test-source', 60, expect.any(Object));

    // Cache should be updated to the new bootstrap.
    const updatedCache = loadCachedBootstrap();
    expect(updatedCache?.bootstrap.snapshot.high_water_sequence).toBe(60);
  });

  it('keeps cached data visible if mirror is unreachable on startup', async () => {
    saveCachedBootstrap(testOrigin, baseBootstrap);

    const fetchImpl = vi.fn(async () => {
      throw new Error('network down');
    });

    await startFeed({ fetchImpl, streamFactory: () => ({ close() {} }) });

    // Cached data remains displayed, connection marked offline
    expect(evalStore.getState().observedSequence).toBe(50);
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toEqual(fixtureCatalogExploratory);
    expect(evalStore.getState().connection).toBe('offline');
  });

  it('skips loading cache when skipCache is requested', async () => {
    saveCachedBootstrap(testOrigin, baseBootstrap);

    const newerSnapshot: FeedSnapshot = {
      ...baseSnapshot,
      high_water_sequence: 99,
    };
    const newerBootstrap: FeedBootstrap = {
      ...baseBootstrap,
      snapshot: newerSnapshot,
      recent_projections: [{ ...fixtureCatalogExploratory, sequence: 99 }],
    };

    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      const path = new URL(String(input), testOrigin).pathname;
      if (path === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: testOrigin });
      if (path === '/bootstrap') return json(newerBootstrap);
      throw new Error(`Unexpected request: ${path}`);
    });

    await startFeed({ fetchImpl, streamFactory: () => ({ close() {} }), skipCache: true });

    expect(evalStore.getState().observedSequence).toBe(99);
  });
});
