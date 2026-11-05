// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { RUN_BUNDLE_LRU_LIMIT } from '../src/constants';
import { fixtureLiveEvaluationSummary } from '../src/fixtures/fixtures';
import type { EvaluationSummary, FeedSnapshot, ProjectionRecord } from '../src/contract/types';
import { normalizeRecentProjection } from '../src/state/feed';
import { evalStore } from '../src/state/store';
import { loadRunAssignments, resetRunBundleCache } from '../src/state/run-bundles';
import { useRunAssignments } from '../src/state/use-run-assignments';
import { buildRunBundle, type BundleFixture } from './run-bundle-fixture';

const DATASET = 'ds-live-hook-run';
const RUN = 'hook-run';

interface SeededRun {
  runId: string;
  sha256?: string;
  lifecycle?: EvaluationSummary['lifecycle_state'];
}

/** Installs one bootstrap whose evaluation summaries cover the given runs. */
function seed(runs: SeededRun[]): void {
  const { primary_invocation_share: _p, correlated_failure_rate: _c, ...modelUnit } = fixtureLiveEvaluationSummary;
  void _p;
  void _c;
  const records: ProjectionRecord[] = runs.map((run, index) => {
    const summary = {
      ...modelUnit,
      schema_version: '1.14.0',
      dataset_id: `ds-live-${run.runId}`,
      run_id: run.runId,
      lifecycle_state: run.lifecycle ?? modelUnit.lifecycle_state,
      quality_state: 'live_in_progress',
      headline_metrics: {
        pass_rate: { value: 0.5, unit: 'ratio', observed_count: 2, eligible_count: 2, unavailable_count: 0 },
        latency_p50_ms: { unavailable_reason: 'historical_not_captured', unit: 'milliseconds', observed_count: 0, eligible_count: 2, unavailable_count: 2 },
        output_throughput_p50_tokens_per_second: { unavailable_reason: 'historical_not_captured', unit: 'tokens_per_second', observed_count: 0, eligible_count: 2, unavailable_count: 2 },
      },
      ...(run.sha256 === undefined ? {} : { assignment_bundle_sha256: run.sha256 }),
    } as EvaluationSummary;
    return normalizeRecentProjection({ sequence: index + 1, ...summary });
  });
  const snapshot: FeedSnapshot = {
    protocol_version: '1.0.0',
    source_id: 'source-hook',
    high_water_sequence: runs.length,
    feed_chain_hash: '0'.repeat(64),
    batch_count: 1,
    withdrawal_digest: 'a'.repeat(64),
    generated_at: '2026-09-20T00:00:00Z',
    freshness: 'active',
  };
  evalStore.setMirrorOrigin('https://mirror.example');
  evalStore.initBootstrap(snapshot, records, 0);
}

function stubFetch(body: Uint8Array): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({ ok: true, status: 200, arrayBuffer: async () => body.slice().buffer })),
  );
}

function stubBundles(bundles: BundleFixture[]): void {
  const byUrl = new Map(bundles.map((bundle) => [`https://mirror.example/runs/${bundle.sha256}.jsonl.gz`, bundle.bytes]));
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      const bytes = byUrl.get(url);
      return { ok: bytes !== undefined, status: bytes === undefined ? 404 : 200, arrayBuffer: async () => (bytes ?? new Uint8Array()).slice().buffer };
    }),
  );
}

describe('useRunAssignments', () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    resetRunBundleCache();
  });

  it('reports loading, then ready with the run assignments', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1', 'assign-2']);
    seed([{ runId: RUN, sha256: bundle.sha256 }]);
    stubFetch(bundle.bytes);

    const { result } = renderHook(() => useRunAssignments(DATASET, RUN));
    expect(result.current.status).toBe('loading');

    await waitFor(() => expect(result.current.status).toBe('ready'));
    expect(result.current.assignments).toHaveLength(2);
  });

  it('loads the bundle when the run summary arrives after the view mounted', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1', 'assign-2']);
    seed([]);
    stubFetch(bundle.bytes);

    const { result } = renderHook(() => useRunAssignments(DATASET, RUN));
    await waitFor(() => expect(result.current.status).not.toBe('ready'));
    expect(result.current.assignments).toHaveLength(0);

    act(() => seed([{ runId: RUN, sha256: bundle.sha256 }]));

    await waitFor(() => expect(result.current.status).toBe('ready'));
    expect(result.current.assignments).toHaveLength(2);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('reports no_bundle for a settled run without a bundle digest and fetches nothing', async () => {
    seed([{ runId: RUN, lifecycle: 'completed' }]);
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    const { result } = renderHook(() => useRunAssignments(DATASET, RUN));

    await waitFor(() => expect(result.current.status).toBe('no_bundle'));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('reports error with the bundle failure on a digest mismatch', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1', 'assign-2']);
    const other = buildRunBundle(RUN, ['assign-9']);
    seed([{ runId: RUN, sha256: bundle.sha256 }]);
    stubFetch(other.bytes);

    const { result } = renderHook(() => useRunAssignments(DATASET, RUN));

    await waitFor(() => expect(result.current.status).toBe('error'));
    expect(result.current.error?.reason).toBe('digest_mismatch');
    expect(result.current.assignments).toHaveLength(0);
  });

  it('does not update state when the load settles after unmount', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1', 'assign-2']);
    seed([{ runId: RUN, sha256: bundle.sha256 }]);
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        await gate;
        return { ok: true, status: 200, arrayBuffer: async () => bundle.bytes.slice().buffer };
      }),
    );
    let renders = 0;
    const { result, unmount } = renderHook(() => {
      renders++;
      return useRunAssignments(DATASET, RUN);
    });
    const rendersBeforeUnmount = renders;
    unmount();

    await act(async () => {
      release?.();
    });
    await waitFor(() => expect(evalStore.getAssignments(RUN, DATASET)).toHaveLength(2));

    expect(renders).toBe(rendersBeforeUnmount);
    expect(result.current.status).toBe('loading');
  });

  it('keeps its run loaded while other runs load past the limit', async () => {
    const own = buildRunBundle(RUN, ['assign-1', 'assign-2']);
    const others = Array.from({ length: RUN_BUNDLE_LRU_LIMIT }, (_, index) => ({
      runId: `hook-other-${index}`,
      bundle: buildRunBundle(`hook-other-${index}`, ['assign-1']),
    }));
    seed([
      { runId: RUN, sha256: own.sha256 },
      ...others.map(({ runId, bundle }) => ({ runId, sha256: bundle.sha256 })),
    ]);
    stubBundles([own, ...others.map(({ bundle }) => bundle)]);

    const { result } = renderHook(() => useRunAssignments(DATASET, RUN));
    await waitFor(() => expect(result.current.status).toBe('ready'));
    for (const { runId } of others) await loadRunAssignments(`ds-live-${runId}`, runId);

    expect(evalStore.getAssignments(RUN, DATASET)).toHaveLength(2);
  });
});
