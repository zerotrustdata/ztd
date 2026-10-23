// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useMirrorOrigin } from '../src/state/mirror';
import { evalStore } from '../src/state/store';
import { startFeed, stopFeed } from '../src/state/startup';
import { createCachedFeed, type FeedCacheStorage } from '../src/state/feed-cache';
import { fixtureCatalogExploratory } from '../src/fixtures/fixtures';
import type { FeedSnapshot } from '../src/contract/types';

const origin = 'https://mirror.example';
const snapshot: FeedSnapshot = {
  protocol_version: '1.0.0', source_id: 'performance-test', high_water_sequence: 100,
  feed_chain_hash: '1'.repeat(64), withdrawal_digest: '0'.repeat(64), batch_count: 1,
  generated_at: '2026-10-23T00:00:00Z', freshness: 'active',
};
const cached = createCachedFeed(origin, snapshot, Array.from({ length: 100 }, (_, index) => ({
  sequence: index + 1, record_type: 'projection', record_bytes: JSON.stringify(fixtureCatalogExploratory),
})));
function json(value: unknown) { return new Response(JSON.stringify(value), { status: 200 }); }
function fetchMirror() {
  return vi.fn(async (input: Parameters<typeof fetch>[0]) => {
    const path = new URL(String(input), origin).pathname;
    if (path === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: origin });
    if (path === '/bootstrap') return json({ protocol_version: '1.0.0', snapshot, source_freshness: 'active', recent_projections: [], proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 }, generated_at: snapshot.generated_at });
    if (path === '/history') return json({ protocol_version: '1.0.0', items: [], has_more: false, limit: 500 });
    return json(snapshot);
  });
}
function storage(): FeedCacheStorage {
  return { load: async () => cached, save: async () => undefined, clear: async () => undefined };
}
function Origin() { return <span>{useMirrorOrigin()}</span>; }

beforeEach(() => evalStore.loadFixtures([], []));
afterEach(() => { stopFeed(); vi.unstubAllGlobals(); });

describe('feed startup performance', () => {
  it('shares startup runtime configuration with every mounted consumer', async () => {
    const fetchImpl = fetchMirror();
    vi.stubGlobal('fetch', fetchImpl);
    await startFeed({ fetchImpl, cacheStorage: storage(), streamFactory: () => ({ close() {} }) });
    render(<><Origin /><Origin /><Origin /></>);
    expect(await screen.findAllByText(origin)).toHaveLength(3);
    expect(fetchImpl.mock.calls.filter(([input]) => String(input) === '/runtime.json')).toHaveLength(1);
  });

  it('restores cached records without notifying subscribers for each record', async () => {
    const observed: number[] = [];
    const unsubscribe = evalStore.subscribe(() => observed.push(evalStore.getState().observedSequence));
    try {
      await startFeed({ fetchImpl: fetchMirror(), cacheStorage: storage(), streamFactory: () => ({ close() {} }) });
      expect(observed.filter((sequence) => sequence > 0 && sequence < 100)).toEqual([]);
      expect(evalStore.getState().observedSequence).toBe(100);
    } finally { unsubscribe(); }
  });

  it('closes the previous stream before waiting for a restarted feed', async () => {
    const close = vi.fn();
    await startFeed({ fetchImpl: fetchMirror(), cacheStorage: storage(), streamFactory: () => ({ close }) });
    let release!: (response: Response) => void;
    const restarting = startFeed({ fetchImpl: () => new Promise<Response>((resolve) => { release = resolve; }) });
    try { expect(close).toHaveBeenCalledOnce(); }
    finally { stopFeed(); release(new Response('', { status: 503 })); await restarting; }
  });

  it('publishes each history page while later pages are still loading', async () => {
    const fetchImpl = fetchMirror();
    let release!: (response: Response) => void;
    let waiting!: () => void;
    const nextPage = new Promise<void>((resolve) => { waiting = resolve; });
    const baseFetch = fetchImpl.getMockImplementation()!;
    fetchImpl.mockImplementation(async (input) => {
      const url = new URL(String(input), origin);
      if (url.pathname !== '/history') return baseFetch(input);
      if (url.searchParams.get('cursor') === '0') return json({
        protocol_version: '1.0.0', items: [{ ...fixtureCatalogExploratory, sequence: 50, record_type: 'projection' }], has_more: true, limit: 500,
      });
      waiting();
      return new Promise<Response>((resolve) => { release = resolve; });
    });
    const observed: number[] = [];
    const unsubscribe = evalStore.subscribe(() => observed.push(evalStore.getState().observedSequence));
    const cacheStorage = storage();
    cacheStorage.load = async () => null;
    const started = startFeed({ fetchImpl, cacheStorage, streamFactory: () => ({ close() {} }) });
    await nextPage;
    try { expect(observed).toContain(50); }
    finally {
      release(json({ protocol_version: '1.0.0', items: [{ ...fixtureCatalogExploratory, sequence: 100, record_type: 'projection' }], has_more: false, limit: 500 }));
      await started;
      unsubscribe();
    }
  });

  it('connects live updates before waiting for persistent cache writes', async () => {
    let release!: () => void;
    let saving!: () => void;
    const saveStarted = new Promise<void>((resolve) => { saving = resolve; });
    const cacheStorage = storage();
    cacheStorage.save = () => { saving(); return new Promise<void>((resolve) => { release = resolve; }); };
    const streamFactory = vi.fn(() => ({ close() {} }));
    const started = startFeed({ fetchImpl: fetchMirror(), cacheStorage, streamFactory });
    await saveStarted;
    try { expect(streamFactory).toHaveBeenCalledOnce(); }
    finally { release(); await started; }
  });
});
