// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LIVE_STATUS_POLL_LOOPBACK_MS, LIVE_STATUS_POLL_MS, POINTER_POLL_MS } from '../src/constants';
import { fixtureModelSummaries } from '../src/fixtures/fixtures';
import { evalStore } from '../src/state/store';
import { startFeed, stopFeed } from '../src/state/startup';
import { loadRunAssignments, resetRunBundleCache } from '../src/state/run-bundles';
import { liveStatusIntervalFor } from '../src/state/feed-poll';
import { settledRunRecord, buildRunBundle } from './run-bundle-fixture';
import { MIRROR_ORIGIN, makeMirror, makeSnapshot, requestedPaths, serveLayout } from './mirror-layout-fixture';

const POLL_TIMERS = ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] as const;

/** Sets document.visibilityState and dispatches the change the browser would fire. */
function setVisibility(state: 'visible' | 'hidden'): void {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

function count(fetchImpl: { mock: { calls: unknown[][] } }, path: string): number {
  return requestedPaths(fetchImpl).filter((requested) => requested === path).length;
}

/** Lets pending poll work settle. Response bodies resolve on macrotasks, which fake timers do not cover. */
async function flush(): Promise<void> {
  for (let turn = 0; turn < 10; turn++) await new Promise<void>((resolve) => setImmediate(resolve));
}

const LIVE_TAIL_RECORD = (sequence: number) => ({
  sequence,
  record_type: 'projection',
  record_bytes: JSON.stringify({ ...fixtureModelSummaries[0]!, observed_at: `2026-10-23T00:00:${String(sequence % 60).padStart(2, '0')}Z` }),
});

beforeEach(() => {
  vi.useFakeTimers({ toFake: [...POLL_TIMERS] });
  evalStore.loadFixtures([], []);
  resetRunBundleCache();
  setVisibility('visible');
});

afterEach(() => {
  stopFeed();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  // Restore the browser's own visibilityState accessor.
  delete (document as unknown as { visibilityState?: unknown }).visibilityState;
});

describe('pointer polling', () => {
  it('pauses while the tab is hidden and resumes with an immediate poll when visible', async () => {
    const fetchImpl = serveLayout(makeMirror());
    await startFeed({ fetchImpl });
    expect(count(fetchImpl, '/pointer.json')).toBe(0);

    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS);
    await flush();
    expect(count(fetchImpl, '/pointer.json')).toBe(1);

    setVisibility('hidden');
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS * 3);
    await flush();
    expect(count(fetchImpl, '/pointer.json')).toBe(1);

    setVisibility('visible');
    await flush();
    expect(count(fetchImpl, '/pointer.json')).toBe(2);

    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS);
    await flush();
    expect(count(fetchImpl, '/pointer.json')).toBe(3);
  });

  it('starts no pointer timer when the tab is hidden at startup', async () => {
    setVisibility('hidden');
    const fetchImpl = serveLayout(makeMirror());
    await startFeed({ fetchImpl });
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS * 2);
    await flush();
    expect(count(fetchImpl, '/pointer.json')).toBe(1);
  });
});

describe('live status polling', () => {
  it('stops when the pointer says active is false', async () => {
    const mirror = makeMirror({ active: true });
    const fetchImpl = serveLayout(mirror);
    await startFeed({ fetchImpl });

    await vi.advanceTimersByTimeAsync(LIVE_STATUS_POLL_MS);
    await flush();
    expect(count(fetchImpl, '/live/status.json')).toBeGreaterThan(0);

    mirror.active = false;
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS + 1);
    await flush();
    const afterPointer = count(fetchImpl, '/live/status.json');

    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS * 2);
    await flush();
    expect(count(fetchImpl, '/live/status.json')).toBe(afterPointer);
  });

  it('polls at the loopback cadence for a loopback mirror and the default cadence otherwise', () => {
    expect(liveStatusIntervalFor('http://127.0.0.1:8082')).toBe(LIVE_STATUS_POLL_LOOPBACK_MS);
    expect(liveStatusIntervalFor('http://localhost:8082')).toBe(LIVE_STATUS_POLL_LOOPBACK_MS);
    expect(liveStatusIntervalFor('http://[::1]:8082')).toBe(LIVE_STATUS_POLL_LOOPBACK_MS);
    expect(liveStatusIntervalFor(MIRROR_ORIGIN)).toBe(LIVE_STATUS_POLL_MS);
  });
});

describe('pointer changes', () => {
  it('replaces state and clears the bundle set when the withdrawal digest changes', async () => {
    const runId = 'run-withdrawal';
    const datasetId = `ds-live-${runId}`;
    const bundle = buildRunBundle(runId, ['assign-1']);
    const mirror = makeMirror({
      recent: [{ ...settledRunRecord(runId, bundle.sha256, datasetId), lifecycle_state: 'running', assignment_bundle_sha256: undefined, sequence: 50 }],
      active: true,
      liveBundles: new Map([[runId, bundle.bytes]]),
    });
    const fetchImpl = serveLayout(mirror);
    vi.stubGlobal('fetch', fetchImpl);
    await startFeed({ fetchImpl });
    await loadRunAssignments(datasetId, runId);
    expect(evalStore.getAssignments(runId, datasetId).length).toBeGreaterThan(0);

    mirror.snapshot = { ...mirror.snapshot, withdrawal_digest: 'b'.repeat(64) };
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS);
    await flush();

    expect(count(fetchImpl, '/runtime.json')).toBe(2);
    expect(evalStore.getState().currentSnapshot?.withdrawal_digest).toBe('b'.repeat(64));
    expect(evalStore.getAssignments(runId, datasetId)).toEqual([]);
  });

  it('applies a changed read model without restarting', async () => {
    const mirror = makeMirror();
    const fetchImpl = serveLayout(mirror);
    await startFeed({ fetchImpl });
    expect(evalStore.getState().observedSequence).toBe(100);

    mirror.snapshot = makeSnapshot({ high_water_sequence: 120, feed_chain_hash: '2'.repeat(64) });
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS);
    await flush();

    expect(count(fetchImpl, '/runtime.json')).toBe(1);
    expect(requestedPaths(fetchImpl).filter((path) => path.startsWith('/read-model/'))).toHaveLength(2);
    expect(evalStore.getState().observedSequence).toBe(120);
    expect(evalStore.getState().currentSnapshot?.feed_chain_hash).toBe('2'.repeat(64));
    expect(evalStore.getState().streamConnection).toBe('connected');
  });

  it('goes offline without restarting when a changed read model fails', async () => {
    const mirror = makeMirror();
    const base = serveLayout(mirror);
    let failing = false;
    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      if (failing && String(input).includes('/read-model/')) return new Response('', { status: 404 });
      return base(input);
    });
    await startFeed({ fetchImpl });

    mirror.snapshot = makeSnapshot({ high_water_sequence: 120, feed_chain_hash: '2'.repeat(64) });
    failing = true;
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS);
    await flush();

    expect(count(fetchImpl, '/runtime.json')).toBe(1);
    expect(evalStore.getState().connection).toBe('offline');
    expect(evalStore.getState().feedStatus?.message).toBe('The public mirror is unreachable. Last accepted data remains visible.');
    expect(evalStore.getState().observedSequence).toBe(100);

    failing = false;
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS);
    await flush();
    expect(evalStore.getState().connection).toBe('live');
    expect(evalStore.getState().observedSequence).toBe(120);
  });
});

describe('read model retry', () => {
  it('re-reads the pointer once and retries the read model when it fails', async () => {
    const mirror = makeMirror();
    const base = serveLayout(mirror);
    let failNextReadModel = true;
    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      if (failNextReadModel && String(input).includes('/read-model/')) {
        failNextReadModel = false;
        return new Response('', { status: 404 });
      }
      return base(input);
    });
    await startFeed({ fetchImpl });

    expect(count(fetchImpl, '/pointer.json')).toBe(2);
    expect(requestedPaths(fetchImpl).filter((path) => path.startsWith('/read-model/'))).toHaveLength(2);
    expect(evalStore.getState().connection).toBe('live');
    expect(evalStore.getState().observedSequence).toBe(100);
  });

  it('goes offline when the retry also fails', async () => {
    const base = serveLayout(makeMirror());
    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      if (String(input).includes('/read-model/')) return new Response('', { status: 404 });
      return base(input);
    });
    await startFeed({ fetchImpl });
    expect(count(fetchImpl, '/pointer.json')).toBe(2);
    expect(evalStore.getState().connection).toBe('offline');
  });
});

describe('live tail', () => {
  it('does not re-apply a tail record at or below the high mark', async () => {
    const mirror = makeMirror({ active: true });
    const fetchImpl = serveLayout(mirror);
    await startFeed({ fetchImpl });
    const acceptProjection = vi.spyOn(evalStore, 'acceptProjection');

    mirror.snapshot = makeSnapshot({ high_water_sequence: 101 });
    mirror.tail = [LIVE_TAIL_RECORD(99), LIVE_TAIL_RECORD(100), LIVE_TAIL_RECORD(101)];
    await vi.advanceTimersByTimeAsync(LIVE_STATUS_POLL_MS);
    await flush();
    expect(acceptProjection).toHaveBeenCalledTimes(1);
    expect(acceptProjection.mock.calls[0]![0].sequence).toBe(101);

    await vi.advanceTimersByTimeAsync(LIVE_STATUS_POLL_MS);
    await flush();
    expect(acceptProjection).toHaveBeenCalledTimes(1);
    expect(evalStore.getState().observedSequence).toBe(101);
  });
});

describe('stale responses', () => {
  it('discards a live status response that arrives after stopFeed', async () => {
    const mirror = makeMirror({ active: true });
    const base = serveLayout(mirror);
    let holdStatus = false;
    let release!: (response: Response) => void;
    const fetchImpl = vi.fn(async (input: Parameters<typeof fetch>[0]) => {
      if (holdStatus && String(input).endsWith('/live/status.json')) {
        return new Promise<Response>((resolve) => { release = resolve; });
      }
      return base(input);
    });
    await startFeed({ fetchImpl });
    const acceptProjection = vi.spyOn(evalStore, 'acceptProjection');
    const acceptSnapshot = vi.spyOn(evalStore, 'acceptSnapshot');

    holdStatus = true;
    await vi.advanceTimersByTimeAsync(LIVE_STATUS_POLL_MS);
    await flush();
    expect(release).toBeTypeOf('function');

    stopFeed();
    release(new Response(JSON.stringify({
      schema_version: '1.0.0',
      snapshot: makeSnapshot({ high_water_sequence: 150 }),
      active_runs: [],
      tail: [LIVE_TAIL_RECORD(150)],
    })));
    await flush();

    expect(acceptSnapshot).not.toHaveBeenCalled();
    expect(acceptProjection).not.toHaveBeenCalled();
    expect(evalStore.getState().observedSequence).toBe(100);
  });

  it('clears every timer and the visibility listener on stopFeed', async () => {
    const fetchImpl = serveLayout(makeMirror({ active: true }));
    await startFeed({ fetchImpl });
    stopFeed();
    const before = requestedPaths(fetchImpl).length;
    setVisibility('hidden');
    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(POINTER_POLL_MS * 2);
    await flush();
    expect(requestedPaths(fetchImpl).length).toBe(before);
  });
});
