// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { FeedBootstrap } from '../src/contract/types';
import { BOOTSTRAP_DATABASE_NAME, BOOTSTRAP_STORAGE_KEY, loadCachedBootstrap, saveCachedBootstrap } from '../src/state/cache';

const bootstrap = (count: number): FeedBootstrap => ({
  protocol_version: '1.0.0',
  snapshot: {
    protocol_version: '1.0.0', source_id: 'cache-test', high_water_sequence: count,
    feed_chain_hash: '1'.repeat(64), withdrawal_digest: '0'.repeat(64), batch_count: 1,
    generated_at: '2026-10-26T00:00:00Z', freshness: 'active',
  },
  source_freshness: 'active',
  recent_projections: Array.from({ length: count }, (_, index) => ({ sequence: index + 1, model_response: 'x'.repeat(64) })),
  proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 },
  generated_at: '2026-10-26T00:00:00Z',
});

describe('bootstrap cache', () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('removes a stale localStorage snapshot when writing a small cache hits quota', async () => {
    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, 'stale');
    const remove = vi.spyOn(Storage.prototype, 'removeItem');
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation((key, value) => {
      if (key === BOOTSTRAP_STORAGE_KEY) throw new DOMException('quota exceeded', 'QuotaExceededError');
      Storage.prototype.setItem.call(localStorage, key, value);
    });

    await saveCachedBootstrap('https://mirror.example', bootstrap(1));

    expect(remove).toHaveBeenCalledWith(BOOTSTRAP_STORAGE_KEY);
  });

  it('does not write a large bootstrap to synchronous localStorage', async () => {
    installFakeIndexedDB();
    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, 'stale');
    const setItem = vi.spyOn(Storage.prototype, 'setItem');
    const removeItem = vi.spyOn(Storage.prototype, 'removeItem');
    const stringify = vi.spyOn(JSON, 'stringify');

    await saveCachedBootstrap('https://mirror.example', bootstrap(251));

    expect(setItem).not.toHaveBeenCalledWith(BOOTSTRAP_STORAGE_KEY, expect.any(String));
    expect(removeItem).toHaveBeenCalledWith(BOOTSTRAP_STORAGE_KEY);
    expect(localStorage.getItem(BOOTSTRAP_STORAGE_KEY)).toBeNull();
    expect(stringify).not.toHaveBeenCalled();
    expect(await loadCachedBootstrap()).toMatchObject({ bootstrap: { recent_projections: expect.any(Array) } });
    expect((await loadCachedBootstrap())?.bootstrap.recent_projections).toHaveLength(251);
  });

  it('uses the newest storage tier when bootstrap size changes in both directions', async () => {
    installFakeIndexedDB();
    const origin = 'https://mirror.example';

    await saveCachedBootstrap(origin, bootstrap(1));
    expect((await loadCachedBootstrap())?.bootstrap.recent_projections).toHaveLength(1);

    await saveCachedBootstrap(origin, bootstrap(251));
    expect(localStorage.getItem(BOOTSTRAP_STORAGE_KEY)).toBeNull();
    expect((await loadCachedBootstrap())?.bootstrap.recent_projections).toHaveLength(251);

    await saveCachedBootstrap(origin, bootstrap(2));
    expect((await loadCachedBootstrap())?.bootstrap.recent_projections).toHaveLength(2);
  });

  it('degrades to no cache when IndexedDB is unavailable for a large bootstrap', async () => {
    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, 'stale');
    vi.stubGlobal('indexedDB', undefined);

    await expect(saveCachedBootstrap('https://mirror.example', bootstrap(251))).resolves.toBeUndefined();

    expect(localStorage.getItem(BOOTSTRAP_STORAGE_KEY)).toBeNull();
    await expect(loadCachedBootstrap()).resolves.toBeNull();
  });

  it('degrades to no cache when IndexedDB rejects the write', async () => {
    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, 'stale');
    const factory = {
      open: vi.fn(() => {
        const request: { onerror?: () => void } = {};
        queueMicrotask(() => request.onerror?.());
        return request;
      }),
    };
    vi.stubGlobal('indexedDB', factory as unknown as IDBFactory);

    await expect(saveCachedBootstrap('https://mirror.example', bootstrap(251))).resolves.toBeUndefined();

    expect(localStorage.getItem(BOOTSTRAP_STORAGE_KEY)).toBeNull();
    await expect(loadCachedBootstrap()).resolves.toBeNull();
  });
});

function installFakeIndexedDB(delayFirstOpen = false): { records: Map<string, unknown>; releaseOpen: () => void; open: ReturnType<typeof vi.fn> } {
  const records = new Map<string, unknown>();
  let hasStore = false;
  let firstOpenDelayed = false;
  let releasePendingOpen: (() => void) | undefined;
  const database = {
    objectStoreNames: { contains: (name: string) => name === 'feed' && hasStore },
    createObjectStore: () => { hasStore = true; return {}; },
    close: () => {},
    transaction: () => {
      const transaction: { oncomplete?: () => void; onerror?: () => void; onabort?: () => void } = {};
      const store = {
        get: (key: string) => {
          const request: { result?: unknown; onsuccess?: () => void; onerror?: () => void } = {};
          queueMicrotask(() => {
            request.result = records.get(key);
            request.onsuccess?.();
            transaction.oncomplete?.();
          });
          return request;
        },
        put: (value: unknown, key: string) => {
          records.set(key, structuredClone(value));
          queueMicrotask(() => transaction.oncomplete?.());
          return {};
        },
        delete: (key: string) => {
          records.delete(key);
          queueMicrotask(() => transaction.oncomplete?.());
          return {};
        },
      };
      Object.assign(transaction, { objectStore: () => store });
      return transaction;
    },
  };
  const factory = {
    open: vi.fn((name: string, version: number) => {
      expect(name).toBe(BOOTSTRAP_DATABASE_NAME);
      expect(version).toBe(1);
      const request: { result: typeof database; onsuccess?: () => void; onerror?: () => void; onblocked?: () => void; onupgradeneeded?: () => void } = { result: database };
      const completeOpen = () => {
        request.onupgradeneeded?.();
        request.onsuccess?.();
      };
      if (delayFirstOpen && !firstOpenDelayed) {
        firstOpenDelayed = true;
        releasePendingOpen = completeOpen;
      } else {
        queueMicrotask(completeOpen);
      }
      return request;
    }),
  };
  vi.stubGlobal('indexedDB', factory as unknown as IDBFactory);
  return { records, releaseOpen: () => releasePendingOpen?.(), open: factory.open };
}
