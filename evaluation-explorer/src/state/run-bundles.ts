// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Loads one settled run's assignment results from its run bundle: a gzipped
// JSONL proof artifact addressed by the SHA-256 that the run's evaluation
// summary publishes. Every byte is verified before anything reaches the store:
// the bundle digest, then each record's own hash. Loaded runs are kept in a
// bounded least-recently-used set so the store holds only recent runs' results.

import { RUN_BUNDLE_LRU_LIMIT } from '../constants';
import type { AssignmentResult, ProjectionRecord } from '../contract/types';
import { sha256Hex } from '../contract/agent-trace';
import { evalStore } from './store';
import { normalizeStreamRecord } from './feed';

/**
 * Why a bundle load failed. `digest_mismatch` and `record_hash_mismatch` are
 * integrity failures; `malformed` covers unparseable bytes and records that
 * are not assignment results of the requested run. `stale_source` means the
 * source or withdrawal digest changed while the bundle was in flight, so its
 * records were discarded without being ingested. `fetch_failed` covers a missing
 * mirror origin, a network error, and a non-2xx response.
 */
export type RunBundleFailure =
  | 'digest_mismatch'
  | 'record_hash_mismatch'
  | 'fetch_failed'
  | 'malformed'
  | 'stale_source';

export class RunBundleError extends Error {
  constructor(
    readonly reason: RunBundleFailure,
    message: string,
  ) {
    super(message);
    this.name = 'RunBundleError';
  }
}

export type RunAssignmentsLoad = { status: 'ready'; assignments: AssignmentResult[] } | { status: 'no_bundle' };

export interface RunAssignmentsOptions {
  fetchImpl?: typeof fetch;
}

interface RunRef {
  datasetId: string;
  runId: string;
}

interface SourceMarker {
  sourceId: string | null;
  withdrawalDigest: string | null;
}

function sameSource(a: SourceMarker, b: SourceMarker): boolean {
  return a.sourceId === b.sourceId && a.withdrawalDigest === b.withdrawalDigest;
}

interface BundleLine {
  sequence: number;
  recordHash: string;
  recordBytes: string;
}

/** Loaded runs in recency order: the first entry is the least recently used. */
const loadedRuns = new Map<string, RunRef>();
const inFlight = new Map<string, Promise<RunAssignmentsLoad>>();
/** Holder counts for runs a mounted view is using. Pinned runs are never evicted. */
const pins = new Map<string, { run: RunRef; holders: number }>();
/** Advances on reset so a load that started before the reset does not mark its run loaded. */
let generation = 0;
let subscribed = false;
let lastSource: SourceMarker | undefined;

function runKey(datasetId: string, runId: string): string {
  return JSON.stringify([datasetId, runId]);
}

function currentSource(): SourceMarker {
  const state = evalStore.getState();
  return { sourceId: state.sourceId, withdrawalDigest: state.currentSnapshot?.withdrawal_digest ?? null };
}

/**
 * Pins runs for as long as a mounted view uses them, so the LRU never evicts
 * them. Each call adds one holder per run; the returned release removes that
 * holder once and does nothing if called again.
 */
export function retainRuns(runs: RunRef[]): () => void {
  for (const run of runs) {
    const key = runKey(run.datasetId, run.runId);
    const pin = pins.get(key);
    if (pin) pin.holders++;
    else pins.set(key, { run: { datasetId: run.datasetId, runId: run.runId }, holders: 1 });
  }
  let released = false;
  return () => {
    if (released) return;
    released = true;
    for (const run of runs) {
      const key = runKey(run.datasetId, run.runId);
      const pin = pins.get(key);
      if (!pin) continue;
      pin.holders--;
      if (pin.holders === 0) pins.delete(key);
    }
  };
}

/** Clears loaded-run and in-flight bookkeeping. Pins belong to mounted views and are kept. */
export function resetRunBundleCache(): void {
  loadedRuns.clear();
  inFlight.clear();
  generation++;
}

function syncWithSource(): void {
  const source = currentSource();
  if (lastSource !== undefined && !sameSource(lastSource, source)) resetRunBundleCache();
  lastSource = source;
}

function ensureSubscribed(): void {
  if (subscribed) return;
  subscribed = true;
  evalStore.subscribe(syncWithSource);
}

function touchLoaded(key: string, run: RunRef): void {
  loadedRuns.delete(key);
  loadedRuns.set(key, run);
}

/** Evicts the least recently used unpinned runs until the set fits the limit. If every loaded run is pinned, the set stays above it. */
function markLoaded(key: string, run: RunRef): void {
  touchLoaded(key, run);
  while (loadedRuns.size > RUN_BUNDLE_LRU_LIMIT) {
    const victim = Array.from(loadedRuns).find(([candidate]) => !pins.has(candidate));
    if (!victim) break;
    const [victimKey, victimRun] = victim;
    loadedRuns.delete(victimKey);
    evalStore.removeRunAssignments(victimRun.datasetId, victimRun.runId);
  }
}

function readyFromStore(datasetId: string, runId: string): RunAssignmentsLoad {
  return { status: 'ready', assignments: evalStore.getAssignments(runId, datasetId) };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function malformed(message: string): RunBundleError {
  return new RunBundleError('malformed', message);
}

async function fetchBundleBytes(sha256: string, fetchImpl: typeof fetch): Promise<Uint8Array> {
  const origin = evalStore.getState().mirrorOrigin;
  if (!origin) throw new RunBundleError('fetch_failed', 'mirror origin is not configured');
  let response: Response;
  try {
    response = await fetchImpl(`${origin}/proofs/${sha256}`, {
      method: 'GET',
      credentials: 'omit',
      headers: { Accept: 'application/gzip' },
    });
  } catch {
    throw new RunBundleError('fetch_failed', 'run bundle request failed');
  }
  if (!response.ok) throw new RunBundleError('fetch_failed', `run bundle request returned ${response.status}`);
  try {
    return new Uint8Array(await response.arrayBuffer());
  } catch {
    throw new RunBundleError('fetch_failed', 'run bundle body could not be read');
  }
}

async function gunzipToText(bytes: Uint8Array): Promise<string> {
  try {
    const body = new Response(bytes.slice().buffer as ArrayBuffer).body;
    if (!body) throw malformed('run bundle has no body');
    return await new Response(body.pipeThrough(new DecompressionStream('gzip'))).text();
  } catch (error) {
    if (error instanceof RunBundleError) throw error;
    throw malformed('run bundle is not gzip');
  }
}

function parseBundleLine(line: string): BundleLine {
  let parsed: unknown;
  try {
    parsed = JSON.parse(line);
  } catch {
    throw malformed('run bundle line is not JSON');
  }
  if (!isRecord(parsed) || Object.keys(parsed).sort().join(',') !== 'record_bytes,record_hash,sequence') {
    throw malformed('run bundle line must hold exactly sequence, record_hash, and record_bytes');
  }
  const { sequence, record_hash: recordHash, record_bytes: recordBytes } = parsed;
  if (typeof sequence !== 'number' || !Number.isSafeInteger(sequence) || sequence < 1) {
    throw malformed('run bundle sequence must be a positive integer');
  }
  if (typeof recordHash !== 'string' || !/^[0-9a-f]{64}$/u.test(recordHash)) {
    throw malformed('run bundle record_hash must be sha256 hex');
  }
  if (typeof recordBytes !== 'string') throw malformed('run bundle record_bytes must be a string');
  return { sequence, recordHash, recordBytes };
}

/** Fetches, verifies, and decodes a bundle. Nothing is ingested here. */
async function readBundleRecords(sha256: string, fetchImpl: typeof fetch): Promise<ProjectionRecord[]> {
  const bytes = await fetchBundleBytes(sha256, fetchImpl);
  if ((await sha256Hex(bytes)) !== sha256) {
    throw new RunBundleError('digest_mismatch', 'run bundle bytes do not match the published digest');
  }
  const text = await gunzipToText(bytes);
  const lines = text.split('\n');
  if (lines[lines.length - 1] === '') lines.pop();
  const records: ProjectionRecord[] = [];
  for (const line of lines) {
    const entry = parseBundleLine(line);
    const encoder = new TextEncoder();
    if ((await sha256Hex(encoder.encode(entry.recordBytes))) !== entry.recordHash) {
      throw new RunBundleError('record_hash_mismatch', `run bundle record ${entry.sequence} does not match its hash`);
    }
    records.push(normalizeStreamRecord('projection', String(entry.sequence), entry.recordBytes));
  }
  return records;
}

async function loadBundle(
  scope: RunRef,
  sha256: string,
  fetchImpl: typeof fetch,
): Promise<RunAssignmentsLoad> {
  const { datasetId, runId } = scope;
  const generationAtStart = generation;
  const sourceAtStart = currentSource();
  const records = await readBundleRecords(sha256, fetchImpl);
  if (!sameSource(sourceAtStart, currentSource())) {
    throw new RunBundleError('stale_source', 'the source changed while the run bundle was loading');
  }
  try {
    evalStore.acceptRunBundle(records, { datasetId, runId });
  } catch (error) {
    throw new RunBundleError('malformed', error instanceof Error ? error.message : 'run bundle is not valid');
  }
  if (generationAtStart === generation) markLoaded(runKey(datasetId, runId), { datasetId, runId });
  return readyFromStore(datasetId, runId);
}

/** Loads one run's assignment results from its bundle, once per run until evicted. */
export async function loadRunAssignments(
  datasetId: string,
  runId: string,
  options: RunAssignmentsOptions = {},
): Promise<RunAssignmentsLoad> {
  ensureSubscribed();
  syncWithSource();
  const key = runKey(datasetId, runId);
  const pending = inFlight.get(key);
  if (pending) return pending;
  // The store is the authority on residency. A same-source bootstrap empties it
  // without a source change, so a tracked run with no stored results drops its
  // bookkeeping and is loaded again. A bundle with no results also reloads here.
  if (loadedRuns.has(key)) {
    if (evalStore.getAssignments(runId, datasetId).length > 0) {
      touchLoaded(key, { datasetId, runId });
      return readyFromStore(datasetId, runId);
    }
    loadedRuns.delete(key);
  }
  const sha256 = evalStore.getEvaluation(datasetId, runId)?.assignment_bundle_sha256;
  if (sha256 === undefined) return { status: 'no_bundle' };
  const fetchImpl = options.fetchImpl ?? fetch.bind(globalThis);
  const load = loadBundle({ datasetId, runId }, sha256, fetchImpl);
  inFlight.set(key, load);
  try {
    return await load;
  } finally {
    if (inFlight.get(key) === load) inFlight.delete(key);
  }
}
