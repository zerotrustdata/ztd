// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useMirrorOrigin } from '../src/state/mirror';
import { evalStore } from '../src/state/store';
import { startFeed, stopFeed } from '../src/state/startup';
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
function fetchMirror() {
  return vi.fn(async (input: Parameters<typeof fetch>[0]) => {
    const path = new URL(String(input), origin).pathname;
    if (path === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: origin });
    if (path === '/bootstrap') return json({ protocol_version: '1.0.0', snapshot, source_freshness: 'active',
      recent_projections: [{ ...fixtureCatalogExploratory, sequence: 50 }],
      proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 }, generated_at: snapshot.generated_at });
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

  it('keeps accepted state visible when a new snapshot is unreachable', async () => {
    await startFeed({ fetchImpl: fetchMirror(), streamFactory: () => ({ close() {} }) });
    await startFeed({ fetchImpl: async () => { throw new Error('offline'); } });
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toEqual(fixtureCatalogExploratory);
    expect(evalStore.getState().connection).toBe('offline');
  });
});
