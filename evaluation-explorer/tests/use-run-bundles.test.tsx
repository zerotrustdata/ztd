// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { LIVE_RUN_BUNDLE_RELOAD_MS, RUN_BUNDLE_LRU_LIMIT } from '../src/constants';
import { evalStore } from '../src/state/store';
import * as runBundles from '../src/state/run-bundles';
import { resetRunBundleCache } from '../src/state/run-bundles';
import { useRunBundles } from '../src/state/use-run-bundles';
import { BUNDLE_ORIGIN, buildRunBundle, loadBundleFixtures, settledRunRecord, stubBundleFetch, type BundleFixture } from './run-bundle-fixture';

vi.mock('../src/state/run-bundles', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../src/state/run-bundles')>();
  return { ...actual, loadRunAssignments: vi.fn(actual.loadRunAssignments) };
});

const CONCURRENCY = 3;

function run(runId: string) {
  return { datasetId: `ds-live-${runId}`, runId };
}

/** Holds every bundle request until released, and tracks how many are in flight at once. */
function deferredResponses(bundles: BundleFixture[]) {
  const byUrl = new Map(bundles.map((bundle) => [`${BUNDLE_ORIGIN}/proofs/${bundle.sha256}`, bundle.bytes]));
  const released: Array<() => void> = [];
  let inFlight = 0;
  let maxInFlight = 0;
  return {
    get maxInFlight() {
      return maxInFlight;
    },
    fetch: vi.fn(async (url: string) => {
      inFlight++;
      maxInFlight = Math.max(maxInFlight, inFlight);
      await new Promise<void>((resolve) => released.push(resolve));
      inFlight--;
      const bytes = byUrl.get(url);
      return {
        ok: bytes !== undefined,
        status: bytes === undefined ? 404 : 200,
        arrayBuffer: async () => (bytes ?? new Uint8Array()).slice().buffer,
      };
    }),
    releaseAll: () => {
      for (const release of released.splice(0)) release();
    },
  };
}

describe('useRunBundles', () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    vi.mocked(runBundles.loadRunAssignments).mockClear();
    resetRunBundleCache();
    evalStore.loadFixtures([], []);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('loads each run bundle once, with at most the given number in flight, and reports progress', async () => {
    const runIds = ['run-a', 'run-b', 'run-c', 'run-d', 'run-e'];
    const bundles = runIds.map((runId) => buildRunBundle(runId, [`${runId}-assignment`]));
    loadBundleFixtures(runIds.map((runId, index) => settledRunRecord(runId, bundles[index]!.sha256)));
    const fetchMock = stubBundleFetch(bundles);

    const { result } = renderHook(() => useRunBundles(runIds.map(run), CONCURRENCY));

    expect(result.current).toEqual({ loaded: 0, total: runIds.length, failures: [] });
    await waitFor(() => expect(result.current.loaded).toBe(runIds.length));
    expect(result.current.failures).toEqual([]);
    expect(fetchMock).toHaveBeenCalledTimes(runIds.length);
    expect(runBundles.loadRunAssignments).toHaveBeenCalledTimes(runIds.length);
    for (const runId of runIds) {
      expect(evalStore.getAssignments(runId, `ds-live-${runId}`)).toHaveLength(1);
    }
  });

  it('never has more fetches in flight than the concurrency allows', async () => {
    const runIds = ['run-a', 'run-b', 'run-c', 'run-d', 'run-e'];
    const bundles = runIds.map((runId) => buildRunBundle(runId, ['x']));
    loadBundleFixtures(runIds.map((runId, index) => settledRunRecord(runId, bundles[index]!.sha256)));
    const pending = deferredResponses(runIds.map((runId) => buildRunBundle(runId, ['x'])));
    vi.stubGlobal('fetch', pending.fetch);

    renderHook(() => useRunBundles(runIds.map(run), CONCURRENCY));

    await waitFor(() => expect(pending.fetch).toHaveBeenCalledTimes(CONCURRENCY));
    expect(pending.maxInFlight).toBe(CONCURRENCY);
    pending.releaseAll();
    await waitFor(() => expect(pending.fetch).toHaveBeenCalledTimes(runIds.length));
    expect(pending.maxInFlight).toBe(CONCURRENCY);
  });

  it('reports a digest mismatch as a failure and ingests none of its records', async () => {
    const good = buildRunBundle('run-good', ['assign-good']);
    const tampered = buildRunBundle('run-bad', ['assign-bad']);
    const other = buildRunBundle('run-other', ['assign-other']);
    loadBundleFixtures([
      settledRunRecord('run-good', good.sha256),
      settledRunRecord('run-bad', tampered.sha256),
    ]);
    // The served bytes do not hash to the digest the run's summary publishes.
    stubBundleFetch([good, { bytes: other.bytes, sha256: tampered.sha256 }]);

    const { result } = renderHook(() => useRunBundles([run('run-good'), run('run-bad')], CONCURRENCY));

    await waitFor(() => expect(result.current.failures).toEqual([
      'Run run-bad: run bundle bytes do not match the published digest',
    ]));
    expect(result.current.loaded).toBe(1);
    expect(result.current.total).toBe(2);
    expect(evalStore.getAssignments('run-bad', 'ds-live-run-bad')).toHaveLength(0);
    expect(evalStore.getAssignments('run-good', 'ds-live-run-good')).toHaveLength(1);
  });

  it('keeps its runs loaded while other runs load past the bundle limit', async () => {
    const pinnedId = 'run-pinned';
    const otherIds = Array.from({ length: RUN_BUNDLE_LRU_LIMIT + 2 }, (_, index) => `run-other-${index}`);
    const runIds = [pinnedId, ...otherIds];
    const bundles = runIds.map((runId) => buildRunBundle(runId, [`${runId}-assignment`]));
    loadBundleFixtures(runIds.map((runId, index) => settledRunRecord(runId, bundles[index]!.sha256)));
    stubBundleFetch(bundles);

    const { result } = renderHook(() => useRunBundles([run(pinnedId)], CONCURRENCY));
    await waitFor(() => expect(result.current.loaded).toBe(1));

    for (const runId of otherIds) {
      await runBundles.loadRunAssignments(`ds-live-${runId}`, runId);
    }

    expect(evalStore.getAssignments(pinnedId, `ds-live-${pinnedId}`)).toHaveLength(1);
    expect(evalStore.getAssignments(otherIds[0]!, `ds-live-${otherIds[0]}`)).toHaveLength(0);
  });

  it('does not restart when called again with an equal-content array', async () => {
    const runIds = ['run-a', 'run-b'];
    const bundles = runIds.map((runId) => buildRunBundle(runId, [`${runId}-assignment`]));
    loadBundleFixtures(runIds.map((runId, index) => settledRunRecord(runId, bundles[index]!.sha256)));
    const fetchMock = stubBundleFetch(bundles);

    const { result, rerender } = renderHook(
      ({ runs }) => useRunBundles(runs, CONCURRENCY),
      { initialProps: { runs: runIds.map(run) } },
    );
    await waitFor(() => expect(result.current.loaded).toBe(runIds.length));

    rerender({ runs: runIds.map(run) });
    rerender({ runs: [...runIds].reverse().map(run) });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(runBundles.loadRunAssignments).toHaveBeenCalledTimes(runIds.length);
    expect(fetchMock).toHaveBeenCalledTimes(runIds.length);
    expect(result.current).toEqual({ loaded: runIds.length, total: runIds.length, failures: [] });
  });

  it('ignores results that arrive after unmount', async () => {
    const bundle = buildRunBundle('run-late', ['assign-late']);
    loadBundleFixtures([settledRunRecord('run-late', bundle.sha256)]);
    const pending = deferredResponses([bundle]);
    vi.stubGlobal('fetch', pending.fetch);
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);

    const { unmount } = renderHook(() => useRunBundles([run('run-late')], CONCURRENCY));
    await waitFor(() => expect(pending.fetch).toHaveBeenCalledTimes(1));
    unmount();
    pending.releaseAll();
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(consoleError).not.toHaveBeenCalled();
    consoleError.mockRestore();
  });

  describe('runs that are still executing', () => {
    const LIVE_URL = `${BUNDLE_ORIGIN}/live/runs/run-live.jsonl.gz`;

    function liveRecord(runId: string) {
      const { assignment_bundle_sha256: _sha, ...summary } = settledRunRecord(runId, '0'.repeat(64)) as unknown as Record<string, unknown>;
      void _sha;
      return { ...summary, lifecycle_state: 'running' } as ReturnType<typeof settledRunRecord>;
    }

    /** Serves the given URLs and counts requests per URL. */
    function stubUrls(byUrl: Map<string, Uint8Array>) {
      const mock = vi.fn(async (url: string) => {
        const bytes = byUrl.get(url);
        return {
          ok: bytes !== undefined,
          status: bytes === undefined ? 404 : 200,
          arrayBuffer: async () => (bytes ?? new Uint8Array()).slice().buffer,
        };
      });
      vi.stubGlobal('fetch', mock);
      return mock;
    }

    function callsTo(mock: { mock: { calls: unknown[][] } }, url: string): number {
      return mock.mock.calls.filter(([requested]) => requested === url).length;
    }

    afterEach(() => {
      vi.useRealTimers();
      delete (document as unknown as { visibilityState?: unknown }).visibilityState;
    });

    it('reads a live run again every reload interval, and never a settled one', async () => {
      const settled = buildRunBundle('run-done', ['done-1']);
      const live = buildRunBundle('run-live', ['live-1']);
      loadBundleFixtures([settledRunRecord('run-done', settled.sha256), liveRecord('run-live')]);
      const byUrl = new Map([
        [`${BUNDLE_ORIGIN}/proofs/${settled.sha256}`, settled.bytes],
        [LIVE_URL, live.bytes],
      ]);
      const mock = stubUrls(byUrl);
      const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');

      const { result, unmount } = renderHook(() => useRunBundles([run('run-done'), run('run-live')], CONCURRENCY));
      await waitFor(() => expect(result.current.loaded).toBe(2));
      expect(callsTo(mock, LIVE_URL)).toBe(1);

      const reload = setIntervalSpy.mock.calls.find(([, ms]) => ms === LIVE_RUN_BUNDLE_RELOAD_MS)?.[0] as () => void;
      expect(reload).toBeTypeOf('function');
      byUrl.set(LIVE_URL, buildRunBundle('run-live', ['live-1', 'live-2']).bytes);
      act(() => reload());

      await waitFor(() => expect(evalStore.getAssignments('run-live', 'ds-live-run-live')).toHaveLength(2));
      expect(callsTo(mock, LIVE_URL)).toBe(2);
      expect(callsTo(mock, `${BUNDLE_ORIGIN}/proofs/${settled.sha256}`)).toBe(1);
      expect(result.current.loaded).toBe(2);
      unmount();
      setIntervalSpy.mockRestore();
    });

    it('does not read a live run while the tab is hidden', async () => {
      const live = buildRunBundle('run-live', ['live-1']);
      loadBundleFixtures([liveRecord('run-live')]);
      const mock = stubUrls(new Map([[LIVE_URL, live.bytes]]));
      const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');

      const { result } = renderHook(() => useRunBundles([run('run-live')], CONCURRENCY));
      await waitFor(() => expect(result.current.loaded).toBe(1));
      const reload = setIntervalSpy.mock.calls.find(([, ms]) => ms === LIVE_RUN_BUNDLE_RELOAD_MS)?.[0] as () => void;

      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      act(() => reload());
      await Promise.resolve();
      expect(callsTo(mock, LIVE_URL)).toBe(1);
      setIntervalSpy.mockRestore();
    });

    it('sets no reload timer when every run is settled', async () => {
      const settled = buildRunBundle('run-done', ['done-1']);
      loadBundleFixtures([settledRunRecord('run-done', settled.sha256)]);
      stubBundleFetch([settled]);
      const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');

      const { result } = renderHook(() => useRunBundles([run('run-done')], CONCURRENCY));
      await waitFor(() => expect(result.current.loaded).toBe(1));
      expect(setIntervalSpy.mock.calls.filter(([, ms]) => ms === LIVE_RUN_BUNDLE_RELOAD_MS)).toHaveLength(0);
      setIntervalSpy.mockRestore();
    });

    it('loads the immutable bundle when a mounted live run settles, without resetting progress', async () => {
      const live = buildRunBundle('run-live', ['live-1']);
      const settled = buildRunBundle('run-live', ['live-1', 'final-2']);
      const other = buildRunBundle('run-other', ['other-1']);
      const otherRecord = settledRunRecord('run-other', other.sha256);
      loadBundleFixtures([liveRecord('run-live'), otherRecord]);
      const mock = stubUrls(new Map([
        [LIVE_URL, live.bytes],
        [`${BUNDLE_ORIGIN}/proofs/${settled.sha256}`, settled.bytes],
        [`${BUNDLE_ORIGIN}/proofs/${other.sha256}`, other.bytes],
      ]));

      const seen: number[] = [];
      const { result } = renderHook(() => {
        const progress = useRunBundles([run('run-live'), run('run-other')], CONCURRENCY);
        seen.push(progress.loaded);
        return progress;
      });
      await waitFor(() => expect(result.current.loaded).toBe(2));
      expect(evalStore.getAssignments('run-live', 'ds-live-run-live')).toHaveLength(1);
      seen.length = 0;

      act(() => loadBundleFixtures([settledRunRecord('run-live', settled.sha256), otherRecord]));

      await waitFor(() => expect(evalStore.getAssignments('run-live', 'ds-live-run-live')).toHaveLength(2));
      expect(callsTo(mock, `${BUNDLE_ORIGIN}/proofs/${settled.sha256}`)).toBe(1);
      await waitFor(() => expect(result.current.loaded).toBe(2));
      expect(seen.filter((loaded) => loaded < 2 && loaded !== 0)).toEqual([]);
    });

    it('loads a run whose summary arrives after the hook mounted', async () => {
      const settled = buildRunBundle('run-late', ['late-1']);
      loadBundleFixtures([]);
      stubBundleFetch([settled]);

      const { result } = renderHook(() => useRunBundles([run('run-late')], CONCURRENCY));
      await waitFor(() => expect(result.current.loaded).toBe(1));
      expect(evalStore.getAssignments('run-late', 'ds-live-run-late')).toHaveLength(0);

      act(() => loadBundleFixtures([settledRunRecord('run-late', settled.sha256)]));

      await waitFor(() => expect(evalStore.getAssignments('run-late', 'ds-live-run-late')).toHaveLength(1));
    });
  });

  it('reports no runs and requests nothing for an empty list', () => {
    const fetchMock = stubBundleFetch([]);
    const { result } = renderHook(() => useRunBundles([], CONCURRENCY));
    expect(result.current).toEqual({ loaded: 0, total: 0, failures: [] });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
