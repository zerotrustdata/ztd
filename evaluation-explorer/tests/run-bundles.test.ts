// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RUN_BUNDLE_LRU_LIMIT } from '../src/constants';
import { fixtureCampaignResultEnvelope, fixtureLiveEvaluationSummary } from '../src/fixtures/fixtures';
import type { EvaluationSummary, FeedSnapshot, ProjectionRecord } from '../src/contract/types';
import { normalizeRecentProjection } from '../src/state/feed';
import { evalStore } from '../src/state/store';
import { loadRunAssignments, resetRunBundleCache, retainRuns, RunBundleError } from '../src/state/run-bundles';

const ORIGIN = 'https://mirror.example';

function sha256Hex(bytes: Uint8Array | string): string {
  return createHash('sha256').update(bytes).digest('hex');
}

function resultRecordBytes(runId: string, assignmentId: string): string {
  return JSON.stringify({
    ...fixtureCampaignResultEnvelope,
    idempotency_key: `${runId}:${assignmentId}:result`,
    record: { ...fixtureCampaignResultEnvelope.record, run_id: runId, assignment_id: assignmentId },
  });
}

interface BundleFixture {
  bytes: Uint8Array;
  sha256: string;
}

/** Real bundle bytes: gzip of JSONL lines whose record_hash covers each record_bytes text. */
function buildBundle(runId: string, assignmentIds: string[]): BundleFixture {
  const lines = assignmentIds.map((assignmentId, index) => {
    const recordBytes = resultRecordBytes(runId, assignmentId);
    return JSON.stringify({ sequence: index + 1, record_hash: sha256Hex(recordBytes), record_bytes: recordBytes });
  });
  const bytes = new Uint8Array(gzipSync(`${lines.join('\n')}\n`));
  return { bytes, sha256: sha256Hex(bytes) };
}

function summaryFor(runId: string, sha256: string | undefined): EvaluationSummary {
  // Stack-routing fields are rejected on model-unit evaluations, so they are omitted here.
  const omittedFields = new Set(['primary_invocation_share', 'correlated_failure_rate']);
  const modelUnit = Object.fromEntries(
    Object.entries(fixtureLiveEvaluationSummary).filter(([field]) => !omittedFields.has(field)),
  ) as EvaluationSummary;
  const summary: EvaluationSummary = {
    ...modelUnit,
    schema_version: '1.14.0',
    dataset_id: `ds-live-${runId}`,
    run_id: runId,
    quality_state: 'live_in_progress',
    headline_metrics: {
      pass_rate: { value: 0.5, unit: 'ratio', observed_count: 2, eligible_count: 2, unavailable_count: 0 },
      latency_p50_ms: { unavailable_reason: 'historical_not_captured', unit: 'milliseconds', observed_count: 0, eligible_count: 2, unavailable_count: 2 },
      output_throughput_p50_tokens_per_second: { unavailable_reason: 'historical_not_captured', unit: 'tokens_per_second', observed_count: 0, eligible_count: 2, unavailable_count: 2 },
    },
  };
  if (sha256 !== undefined) summary.assignment_bundle_sha256 = sha256;
  return summary;
}

function snapshot(withdrawalDigest: string, highWaterSequence: number): FeedSnapshot {
  return {
    protocol_version: '1.0.0',
    source_id: 'source-bundles',
    high_water_sequence: highWaterSequence,
    feed_chain_hash: '0'.repeat(64),
    batch_count: 1,
    withdrawal_digest: withdrawalDigest,
    generated_at: '2026-09-20T00:00:00Z',
    freshness: 'active',
  };
}

/** Installs one bootstrap whose evaluation summaries carry the given bundle digests. */
function seedRuns(runs: Array<{ runId: string; sha256?: string }>, withdrawalDigest = 'a'.repeat(64)): void {
  evalStore.setMirrorOrigin(ORIGIN);
  const summaries: ProjectionRecord[] = runs.map((run, index) =>
    normalizeRecentProjection({ sequence: index + 1, ...summaryFor(run.runId, run.sha256) }),
  );
  evalStore.initBootstrap(snapshot(withdrawalDigest, runs.length), summaries, 0);
}

/** Serves bundles by their content address; any other URL is a 404. */
function servingFetch(bundles: BundleFixture[]) {
  const byPath = new Map(bundles.map((bundle) => [`${ORIGIN}/proofs/${bundle.sha256}`, bundle.bytes]));
  return vi.fn(async (url: string) => {
    const bytes = byPath.get(url);
    return {
      ok: bytes !== undefined,
      status: bytes === undefined ? 404 : 200,
      arrayBuffer: async () => (bytes ?? new Uint8Array()).slice().buffer,
    };
  });
}

function fetchOf(mock: ReturnType<typeof servingFetch>): typeof fetch {
  return mock as unknown as typeof fetch;
}

const DATASET = 'ds-live-run-one';

describe('loadRunAssignments', () => {
  beforeEach(() => {
    resetRunBundleCache();
  });

  it('concurrent calls for one run share a single fetch', async () => {
    const bundle = buildBundle('run-one', ['assign-1', 'assign-2']);
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);
    const fetchImpl = servingFetch([bundle]);

    const [first, second] = await Promise.all([
      loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) }),
      loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) }),
    ]);

    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(first.status).toBe('ready');
    expect(second.status).toBe('ready');
    if (first.status === 'ready') expect(first.assignments).toHaveLength(2);
  });

  it('a second call after the run is loaded resolves from the store without fetching', async () => {
    const bundle = buildBundle('run-one', ['assign-1', 'assign-2']);
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);
    const fetchImpl = servingFetch([bundle]);
    await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) });

    const again = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) });

    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(again.status).toBe('ready');
    if (again.status === 'ready') expect(again.assignments).toHaveLength(2);
  });

  it('a digest mismatch throws digest_mismatch and ingests nothing', async () => {
    const bundle = buildBundle('run-one', ['assign-1']);
    const other = buildBundle('run-one', ['assign-9']);
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);
    const fetchImpl = vi.fn(async () => ({
      ok: true,
      status: 200,
      arrayBuffer: async () => other.bytes.slice().buffer,
    }));

    const error = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) }).then(
      () => undefined,
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RunBundleError);
    expect((error as RunBundleError).reason).toBe('digest_mismatch');
    expect(evalStore.getAssignments('run-one', DATASET)).toHaveLength(0);
    expect(evalStore.getEvents('run-one', DATASET)).toHaveLength(0);
  });

  it('a record_hash mismatch throws record_hash_mismatch and ingests nothing', async () => {
    const recordBytes = resultRecordBytes('run-one', 'assign-1');
    const lines = [
      JSON.stringify({ sequence: 1, record_hash: sha256Hex(recordBytes), record_bytes: recordBytes }),
      JSON.stringify({ sequence: 2, record_hash: '0'.repeat(64), record_bytes: resultRecordBytes('run-one', 'assign-2') }),
    ];
    const bytes = new Uint8Array(gzipSync(`${lines.join('\n')}\n`));
    const bundle: BundleFixture = { bytes, sha256: sha256Hex(bytes) };
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);

    const error = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(servingFetch([bundle])) }).then(
      () => undefined,
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RunBundleError);
    expect((error as RunBundleError).reason).toBe('record_hash_mismatch');
    expect(evalStore.getAssignments('run-one', DATASET)).toHaveLength(0);
  });

  it('a failed fetch throws fetch_failed', async () => {
    const bundle = buildBundle('run-one', ['assign-1']);
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);
    const fetchImpl = servingFetch([]);

    const error = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) }).then(
      () => undefined,
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RunBundleError);
    expect((error as RunBundleError).reason).toBe('fetch_failed');
  });

  it('a settled run without assignment_bundle_sha256 returns no_bundle and fetches nothing', async () => {
    const summary = { ...summaryFor('run-one', undefined), lifecycle_state: 'completed' as const };
    evalStore.setMirrorOrigin(ORIGIN);
    evalStore.initBootstrap(snapshot('a'.repeat(64), 1), [normalizeRecentProjection({ sequence: 1, ...summary })], 0);
    const fetchImpl = servingFetch([]);

    const result = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) });

    expect(result).toEqual({ status: 'no_bundle' });
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it('evicts the least recently used run beyond RUN_BUNDLE_LRU_LIMIT', async () => {
    const runIds = Array.from({ length: RUN_BUNDLE_LRU_LIMIT + 1 }, (_, index) => `run-lru-${index}`);
    const bundles = runIds.map((runId) => buildBundle(runId, ['assign-1', 'assign-2']));
    seedRuns(runIds.map((runId, index) => ({ runId, sha256: bundles[index]?.sha256 })));
    const fetchImpl = servingFetch(bundles);

    for (const runId of runIds) {
      await loadRunAssignments(`ds-live-${runId}`, runId, { fetchImpl: fetchOf(fetchImpl) });
    }

    expect(evalStore.getAssignments('run-lru-0', 'ds-live-run-lru-0')).toHaveLength(0);
    for (const runId of runIds.slice(1)) {
      expect(evalStore.getAssignments(runId, `ds-live-${runId}`)).toHaveLength(2);
    }
  });

  it('touching a loaded run makes it the most recently used', async () => {
    const runIds = Array.from({ length: RUN_BUNDLE_LRU_LIMIT + 1 }, (_, index) => `run-touch-${index}`);
    const bundles = runIds.map((runId) => buildBundle(runId, ['assign-1']));
    seedRuns(runIds.map((runId, index) => ({ runId, sha256: bundles[index]?.sha256 })));
    const fetchImpl = servingFetch(bundles);
    const datasetOf = (runId: string) => `ds-live-${runId}`;

    for (const runId of runIds.slice(0, RUN_BUNDLE_LRU_LIMIT)) {
      await loadRunAssignments(datasetOf(runId), runId, { fetchImpl: fetchOf(fetchImpl) });
    }
    await loadRunAssignments(datasetOf('run-touch-0'), 'run-touch-0', { fetchImpl: fetchOf(fetchImpl) });
    await loadRunAssignments(datasetOf('run-touch-8'), 'run-touch-8', { fetchImpl: fetchOf(fetchImpl) });

    expect(evalStore.getAssignments('run-touch-0', datasetOf('run-touch-0'))).toHaveLength(1);
    expect(evalStore.getAssignments('run-touch-1', datasetOf('run-touch-1'))).toHaveLength(0);
  });

  it('a changed withdrawal_digest clears the loaded-run bookkeeping so the run is fetched again', async () => {
    const bundle = buildBundle('run-one', ['assign-1']);
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);
    const fetchImpl = servingFetch([bundle]);
    await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) });
    expect(fetchImpl).toHaveBeenCalledTimes(1);

    evalStore.acceptSnapshot(snapshot('b'.repeat(64), 1));
    await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) });

    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('a bundle that arrives after the source changed is discarded as stale_source', async () => {
    const bundle = buildBundle('run-one', ['assign-1']);
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);
    const byPath = new Map([[`${ORIGIN}/proofs/${bundle.sha256}`, bundle.bytes]]);
    const fetchImpl = vi.fn(async (url: string) => {
      evalStore.acceptSnapshot(snapshot('c'.repeat(64), 1));
      const bytes = byPath.get(url);
      return { ok: true, status: 200, arrayBuffer: async () => (bytes ?? new Uint8Array()).slice().buffer };
    });

    const error = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) }).then(
      () => undefined,
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RunBundleError);
    expect((error as RunBundleError).reason).toBe('stale_source');
    expect(evalStore.getAssignments('run-one', DATASET)).toHaveLength(0);
  });

  it('a bundle holding a record for another run throws malformed and ingests nothing', async () => {
    const foreign = resultRecordBytes('run-other', 'assign-2');
    const lines = [
      JSON.stringify({ sequence: 1, record_hash: sha256Hex(resultRecordBytes('run-one', 'assign-1')), record_bytes: resultRecordBytes('run-one', 'assign-1') }),
      JSON.stringify({ sequence: 2, record_hash: sha256Hex(foreign), record_bytes: foreign }),
    ];
    const bytes = new Uint8Array(gzipSync(`${lines.join('\n')}\n`));
    const bundle: BundleFixture = { bytes, sha256: sha256Hex(bytes) };
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);

    const error = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(servingFetch([bundle])) }).then(
      () => undefined,
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RunBundleError);
    expect((error as RunBundleError).reason).toBe('malformed');
    expect(evalStore.getAssignments('run-one', DATASET)).toHaveLength(0);
  });

  it('a loaded run whose results left the store is fetched again', async () => {
    const bundle = buildBundle('run-one', ['assign-1']);
    seedRuns([{ runId: 'run-one', sha256: bundle.sha256 }]);
    const fetchImpl = servingFetch([bundle]);
    await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) });

    evalStore.removeRunAssignments(DATASET, 'run-one');
    const reloaded = await loadRunAssignments(DATASET, 'run-one', { fetchImpl: fetchOf(fetchImpl) });

    expect(fetchImpl).toHaveBeenCalledTimes(2);
    if (reloaded.status === 'ready') expect(reloaded.assignments).toHaveLength(1);
  });
});

describe('retainRuns', () => {
  const releases: Array<() => void> = [];

  function retain(...runIds: string[]): () => void {
    const release = retainRuns(runIds.map((runId) => ({ datasetId: `ds-live-${runId}`, runId })));
    releases.push(release);
    return release;
  }

  function setupRuns(runIds: string[]) {
    const bundles = runIds.map((runId) => buildBundle(runId, ['assign-1']));
    seedRuns(runIds.map((runId, index) => ({ runId, sha256: bundles[index]?.sha256 })));
    return servingFetch(bundles);
  }

  async function loadRun(runId: string, fetchImpl: ReturnType<typeof servingFetch>): Promise<void> {
    await loadRunAssignments(`ds-live-${runId}`, runId, { fetchImpl: fetchOf(fetchImpl) });
  }

  function resultCount(runId: string): number {
    return evalStore.getAssignments(runId, `ds-live-${runId}`).length;
  }

  function runIdsWithPrefix(prefix: string, count: number): string[] {
    return Array.from({ length: count }, (_, index) => `${prefix}-${index}`);
  }

  beforeEach(() => {
    resetRunBundleCache();
  });

  afterEach(() => {
    for (const release of releases.splice(0)) release();
  });

  it('eviction skips a pinned run and evicts the next oldest unpinned run', async () => {
    const runIds = runIdsWithPrefix('pin-skip', RUN_BUNDLE_LRU_LIMIT + 2);
    const fetchImpl = setupRuns(runIds);
    retain(runIds[0]!);

    for (const runId of runIds) await loadRun(runId, fetchImpl);

    expect(resultCount(runIds[0]!)).toBe(1);
    expect(resultCount(runIds[1]!)).toBe(0);
    expect(resultCount(runIds[2]!)).toBe(0);
    expect(resultCount(runIds[runIds.length - 1]!)).toBe(1);
  });

  it('a set of only pinned runs grows past the limit without evicting', async () => {
    const runIds = runIdsWithPrefix('pin-all', RUN_BUNDLE_LRU_LIMIT + 2);
    const fetchImpl = setupRuns(runIds);
    retain(...runIds);

    for (const runId of runIds) await loadRun(runId, fetchImpl);

    for (const runId of runIds) expect(resultCount(runId)).toBe(1);
  });

  it('a run released after loading becomes evictable on the next load past the limit', async () => {
    const runIds = runIdsWithPrefix('pin-release', RUN_BUNDLE_LRU_LIMIT + 1);
    const fetchImpl = setupRuns(runIds);
    const release = retain(runIds[0]!);
    await loadRun(runIds[0]!, fetchImpl);
    release();

    for (const runId of runIds.slice(1)) await loadRun(runId, fetchImpl);

    expect(resultCount(runIds[0]!)).toBe(0);
  });

  it('a run stays pinned until every holder releases, and a repeated release does not drop another holder', async () => {
    const runIds = runIdsWithPrefix('pin-count', RUN_BUNDLE_LRU_LIMIT + 2);
    const fetchImpl = setupRuns(runIds);
    const first = retain(runIds[0]!);
    retain(runIds[0]!);
    first();
    first();

    for (const runId of runIds) await loadRun(runId, fetchImpl);

    expect(resultCount(runIds[0]!)).toBe(1);
  });

  it('resetRunBundleCache keeps pins held by mounted views', async () => {
    const runIds = runIdsWithPrefix('pin-reset', RUN_BUNDLE_LRU_LIMIT + 1);
    const fetchImpl = setupRuns(runIds);
    retain(runIds[0]!);
    resetRunBundleCache();

    for (const runId of runIds) await loadRun(runId, fetchImpl);

    expect(resultCount(runIds[0]!)).toBe(1);
    expect(resultCount(runIds[1]!)).toBe(0);
  });
});

/** Installs one run whose summary is still executing, so it names no bundle digest. */
function seedLiveRun(runId: string): void {
  evalStore.setMirrorOrigin(ORIGIN);
  const summary = { ...summaryFor(runId, undefined), lifecycle_state: 'running' as const };
  evalStore.initBootstrap(snapshot('a'.repeat(64), 1), [normalizeRecentProjection({ sequence: 1, ...summary })], 0);
}

/** Serves the given bytes by full URL; any other URL is a 404. */
function liveFetch(bytesByUrl: Map<string, Uint8Array>) {
  return vi.fn(async (url: string) => {
    const bytes = bytesByUrl.get(url);
    return {
      ok: bytes !== undefined,
      status: bytes === undefined ? 404 : 200,
      arrayBuffer: async () => (bytes ?? new Uint8Array()).slice().buffer,
    };
  });
}

describe('loadRunAssignments for queued or running runs', () => {
  beforeEach(() => {
    resetRunBundleCache();
  });

  it('loads the live object of an active run and does not keep the result as settled', async () => {
    const bundle = buildBundle('run-live', ['assign-1', 'assign-2']);
    seedLiveRun('run-live');
    const fetchImpl = liveFetch(new Map([[`${ORIGIN}/live/runs/run-live.jsonl.gz`, bundle.bytes]]));

    const first = await loadRunAssignments('ds-live-run-live', 'run-live', { fetchImpl: fetchOf(fetchImpl) });
    expect(first.status).toBe('ready');
    expect(evalStore.getAssignments('run-live', 'ds-live-run-live')).toHaveLength(2);

    await loadRunAssignments('ds-live-run-live', 'run-live', { fetchImpl: fetchOf(fetchImpl) });
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('rejects a live record whose hash does not match its bytes', async () => {
    const line = JSON.stringify({ sequence: 1, record_hash: '0'.repeat(64), record_bytes: resultRecordBytes('run-bad', 'assign-1') });
    const bytes = new Uint8Array(gzipSync(`${line}\n`));
    seedLiveRun('run-bad');
    const fetchImpl = liveFetch(new Map([[`${ORIGIN}/live/runs/run-bad.jsonl.gz`, bytes]]));

    await expect(loadRunAssignments('ds-live-run-bad', 'run-bad', { fetchImpl: fetchOf(fetchImpl) }))
      .rejects.toMatchObject({ reason: 'record_hash_mismatch' });
    expect(evalStore.getAssignments('run-bad', 'ds-live-run-bad')).toEqual([]);
  });

  it('reports no_bundle when the live object is absent', async () => {
    seedLiveRun('run-none');
    const fetchImpl = liveFetch(new Map());
    expect(await loadRunAssignments('ds-live-run-none', 'run-none', { fetchImpl: fetchOf(fetchImpl) })).toEqual({ status: 'no_bundle' });
  });

  it('never requests the live object for a settled run that names a digest', async () => {
    const bundle = buildBundle('run-settled', ['assign-1']);
    seedRuns([{ runId: 'run-settled', sha256: bundle.sha256 }]);
    const fetchImpl = servingFetch([bundle]);

    const result = await loadRunAssignments('ds-live-run-settled', 'run-settled', { fetchImpl: fetchOf(fetchImpl) });
    expect(result.status).toBe('ready');
    const urls = fetchImpl.mock.calls.map(([url]) => String(url));
    expect(urls.some((url) => url.includes('/live/runs/'))).toBe(false);
  });
});
