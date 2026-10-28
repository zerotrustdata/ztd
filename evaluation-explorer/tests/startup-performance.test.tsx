// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useMirrorOrigin } from '../src/state/mirror';
import { evalStore } from '../src/state/store';
import { startFeed, stopFeed } from '../src/state/startup';
import { BOOTSTRAP_DATABASE_NAME, BOOTSTRAP_STORAGE_KEY, saveCachedBootstrap, type CachedBootstrapRecord } from '../src/state/cache';
import { fixtureCatalogExploratory } from '../src/fixtures/fixtures';
import type { FeedSnapshot } from '../src/contract/types';
import type { StreamHandlers } from '../src/state/feed';

const origin = 'https://mirror.example';
const snapshot: FeedSnapshot = {
  protocol_version: '1.0.0', source_id: 'performance-test', high_water_sequence: 100,
  feed_chain_hash: '1'.repeat(64), withdrawal_digest: '0'.repeat(64), batch_count: 1,
  generated_at: '2026-10-23T00:00:00Z', freshness: 'active',
};
function json(value: unknown) { return new Response(JSON.stringify(value), { status: 200 }); }
function fetchMirror(currentSnapshot: FeedSnapshot = snapshot) {
  return vi.fn(async (input: Parameters<typeof fetch>[0]) => {
    const path = new URL(String(input), origin).pathname;
    if (path === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: origin });
    if (path === '/snapshot') return json(currentSnapshot);
    if (path === '/bootstrap') return json({ protocol_version: '1.0.0', snapshot: currentSnapshot, source_freshness: 'active',
      recent_projections: [{ ...fixtureCatalogExploratory, sequence: 50 }],
      proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 }, generated_at: currentSnapshot.generated_at });
    throw new Error(`Unexpected startup request: ${path}`);
  });
}
function Origin() { return <span>{useMirrorOrigin()}</span>; }
beforeEach(() => evalStore.loadFixtures([], []));
afterEach(() => { stopFeed(); vi.unstubAllGlobals(); });

describe('current snapshot startup', () => {
  it('loads current entities in two requests and streams directly after the snapshot cursor', async () => {
    const fetchImpl = fetchMirror();
    const streamFactory = vi.fn(() => ({ close() {} }));
    await startFeed({ fetchImpl, streamFactory });
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(evalStore.getState().observedSequence).toBe(100);
    expect(evalStore.getState().currentSnapshot).toEqual(snapshot);
    expect(evalStore.getState().pendingSnapshot).toBeNull();
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toEqual(fixtureCatalogExploratory);
    expect(streamFactory).toHaveBeenCalledWith(origin, snapshot.source_id, 100, expect.any(Object));
  });

  it('shares runtime configuration with mounted consumers', async () => {
    const fetchImpl = fetchMirror(); vi.stubGlobal('fetch', fetchImpl);
    await startFeed({ fetchImpl, streamFactory: () => ({ close() {} }) });
    render(<><Origin /><Origin /><Origin /></>);
    expect(await screen.findAllByText(origin)).toHaveLength(3);
    expect(fetchImpl.mock.calls.filter(([input]) => String(input) === '/runtime.json')).toHaveLength(1);
  });

  it('installs snapshot entities and the cursor in one notification', async () => {
    const observed: number[] = [];
    const unsubscribe = evalStore.subscribe(() => observed.push(evalStore.getState().observedSequence));
    try {
      await startFeed({ fetchImpl: fetchMirror(), streamFactory: () => ({ close() {} }) });
      expect(observed.filter((sequence) => sequence > 0)).toEqual([100]);
    } finally { unsubscribe(); }
  });

  it('opens the stream before persisting a fetched bootstrap', async () => {
    let cacheAtStreamOpen: string | null | undefined;
    const streamFactory = vi.fn(() => {
      cacheAtStreamOpen = localStorage.getItem(BOOTSTRAP_STORAGE_KEY);
      return { close() {} };
    });

    await startFeed({ fetchImpl: fetchMirror(), streamFactory });

    expect(cacheAtStreamOpen).toBeNull();
    expect(localStorage.getItem(BOOTSTRAP_STORAGE_KEY)).not.toBeNull();
  });

  it('does not hydrate a cache for a different configured mirror origin', async () => {
    const oldSnapshot: FeedSnapshot = { ...snapshot, source_id: 'old-origin' };
    await saveCachedBootstrap('https://old.example', {
      protocol_version: '1.0.0', snapshot: oldSnapshot, source_freshness: 'active',
      recent_projections: [{ ...fixtureCatalogExploratory, sequence: 50 }],
      proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 }, generated_at: oldSnapshot.generated_at,
    });

    let releaseBootstrap!: (response: Response) => void;
    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      const path = new URL(String(input), origin).pathname;
      if (path === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: 'https://new.example' });
      if (path === '/bootstrap') return new Promise<Response>((resolve) => { releaseBootstrap = resolve; });
      throw new Error(`Unexpected startup request: ${path}`);
    });
    const starting = startFeed({ fetchImpl, streamFactory: () => ({ close() {} }) });
    try {
      await vi.waitFor(() => expect(releaseBootstrap).toBeTypeOf('function'));
      expect(evalStore.getState().mirrorOrigin).toBe('https://new.example');
      expect(evalStore.getState().currentSnapshot).toBeNull();
    } finally {
      releaseBootstrap(json({ protocol_version: '1.0.0', snapshot, source_freshness: 'active', recent_projections: [],
        proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 }, generated_at: snapshot.generated_at }));
      await starting;
    }
  });

  it('does not hydrate an IndexedDB cache after a newer startup wins during the async read', async () => {
    const records = new Map<string, unknown>();
    let hasStore = false;
    const database = {
      objectStoreNames: { contains: (name: string) => name === 'feed' && hasStore },
      createObjectStore: () => { hasStore = true; return {}; },
      close: () => {},
      transaction: () => {
        const transaction: { oncomplete?: () => void; onerror?: () => void; onabort?: () => void } = {};
        Object.assign(transaction, { objectStore: () => ({
          get: (key: string) => {
            const request: { result?: unknown; onsuccess?: () => void } = {};
            queueMicrotask(() => { request.result = records.get(key); request.onsuccess?.(); transaction.oncomplete?.(); });
            return request;
          },
          put: (value: unknown, key: string) => { records.set(key, structuredClone(value)); return {}; },
          delete: (key: string) => { records.delete(key); return {}; },
        }) });
        return transaction;
      },
    };
    let releaseOpen!: () => void;
    const indexedDB = {
      open: vi.fn((name: string, version: number) => {
        expect(name).toBe(BOOTSTRAP_DATABASE_NAME);
        expect(version).toBe(1);
        const request: { result: typeof database; onsuccess?: () => void; onupgradeneeded?: () => void } = { result: database };
        releaseOpen = () => { request.onupgradeneeded?.(); request.onsuccess?.(); };
        return request;
      }),
    };
    vi.stubGlobal('indexedDB', indexedDB as unknown as IDBFactory);
    const staleBootstrap = {
      protocol_version: '1.0.0' as const, snapshot: { ...snapshot, source_id: 'stale-cache' }, source_freshness: 'active' as const,
      recent_projections: [{ ...fixtureCatalogExploratory, sequence: 50 }],
      proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 }, generated_at: snapshot.generated_at,
    };
    const staleRecord: CachedBootstrapRecord = {
      schema_version: '1.0.0', mirror_origin: origin, bootstrap: staleBootstrap, cached_at: new Date().toISOString(),
    };
    records.set('current', staleRecord);

    const firstFetch = fetchMirror();
    const first = startFeed({ fetchImpl: firstFetch, streamFactory: () => ({ close() {} }) });
    await vi.waitFor(() => expect(indexedDB.open).toHaveBeenCalledOnce());

    const secondFetch = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      const path = new URL(String(input), origin).pathname;
      if (path === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: origin });
      if (path === '/bootstrap') throw new Error('newer startup is offline');
      throw new Error(`Unexpected startup request: ${path}`);
    });
    await startFeed({ fetchImpl: secondFetch, streamFactory: () => ({ close() {} }), skipCache: true });
    expect(evalStore.getState().currentSnapshot).toBeNull();

    releaseOpen();
    await first;
    expect(evalStore.getState().currentSnapshot).toBeNull();
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toBeUndefined();
    expect(firstFetch.mock.calls.map(([input]) => new URL(String(input), origin).pathname)).toEqual(['/runtime.json']);
  });

  it('closes the previous stream before waiting for a replacement snapshot', async () => {
    const close = vi.fn();
    await startFeed({ fetchImpl: fetchMirror(), streamFactory: () => ({ close }) });
    let release!: (response: Response) => void;
    const restarting = startFeed({ fetchImpl: () => new Promise<Response>((resolve) => { release = resolve; }) });
    try { expect(close).toHaveBeenCalledOnce(); }
    finally { stopFeed(); release(new Response('', { status: 503 })); await restarting; }
  });

  it('replaces state from the mirror on replay gaps instead of reconstructing history', async () => {
    const fetchImpl = fetchMirror(); let handlers!: StreamHandlers;
    const streamFactory = vi.fn((_origin, _source, _cursor, callbacks: StreamHandlers) => { handlers = callbacks; return { close() {} }; });
    await startFeed({ fetchImpl, streamFactory });
    handlers.onTruncated();
    await vi.waitFor(() => expect(streamFactory).toHaveBeenCalledTimes(2));
    expect(fetchImpl).toHaveBeenCalledTimes(4);
  });

  it('reuses the cached bootstrap on reload and verifies cursor freshness via snapshot without re-downloading bootstrap', async () => {
    const fetchImpl = fetchMirror();
    const streamFactory = vi.fn(() => ({ close() {} }));
    await startFeed({ fetchImpl, streamFactory });
    expect(fetchImpl).toHaveBeenCalledTimes(2); // /runtime.json and /bootstrap
    expect(evalStore.getState().observedSequence).toBe(100);

    // Second startup simulates page refresh / reload with cached bootstrap
    const reloadFetch = fetchMirror();
    const reloadStreamFactory = vi.fn(() => ({ close() {} }));
    await startFeed({ fetchImpl: reloadFetch, streamFactory: reloadStreamFactory });

    // Should only query /runtime.json and /snapshot — NOT /bootstrap!
    expect(reloadFetch).toHaveBeenCalledTimes(2);
    const requestedPaths = reloadFetch.mock.calls.map(([req]) => new URL(String(req), origin).pathname);
    expect(requestedPaths).toEqual(['/runtime.json', '/snapshot']);
    expect(reloadStreamFactory).toHaveBeenCalledWith(origin, snapshot.source_id, 100, expect.any(Object));
    expect(evalStore.getState().observedSequence).toBe(100);
    expect(evalStore.getState().connection).toBe('live');

    // Minor cursor advance (within SSE replay bound of 100): stream catches up without re-downloading bootstrap
    const advancedSnapshot: FeedSnapshot = { ...snapshot, high_water_sequence: 105 };
    const catchUpFetch = fetchMirror(advancedSnapshot);
    const catchUpStreamFactory = vi.fn(() => ({ close() {} }));
    await startFeed({ fetchImpl: catchUpFetch, streamFactory: catchUpStreamFactory });
    expect(catchUpFetch).toHaveBeenCalledTimes(2);
    expect(catchUpStreamFactory).toHaveBeenCalledWith(origin, snapshot.source_id, 100, expect.any(Object));

    // Large cursor advance (>100): falls back to downloading bootstrap
    const distantSnapshot: FeedSnapshot = { ...snapshot, high_water_sequence: 300 };
    const distantFetch = fetchMirror(distantSnapshot);
    const distantStreamFactory = vi.fn(() => ({ close() {} }));
    await startFeed({ fetchImpl: distantFetch, streamFactory: distantStreamFactory });
    expect(distantFetch).toHaveBeenCalledTimes(3);
    const distantPaths = distantFetch.mock.calls.map(([req]) => new URL(String(req), origin).pathname);
    expect(distantPaths).toEqual(['/runtime.json', '/snapshot', '/bootstrap']);
    expect(evalStore.getState().observedSequence).toBe(300);
  });

  it('keeps accepted state visible when a new snapshot is unreachable', async () => {
    await startFeed({ fetchImpl: fetchMirror(), streamFactory: () => ({ close() {} }) });
    await startFeed({ fetchImpl: async () => { throw new Error('offline'); } });
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toEqual(fixtureCatalogExploratory);
    expect(evalStore.getState().connection).toBe('offline');
  });
});
