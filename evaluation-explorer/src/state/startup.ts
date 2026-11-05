// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The public feed. startFeed loads the mirror's current read model through
// pointer.json, then polls: the pointer on a slow cadence, and live/status.json
// while the pointer names an active run. Every response is checked against the
// generation it was requested in, so a response that outlives its generation
// never reaches the store. History and proof artifacts are evidence queries,
// not initialization inputs.
import type { PublicPointer } from '../../../ztd-adapter/src/public';
import type { FeedBootstrap, FeedSnapshot } from '../contract/types';
import { isFeedSnapshot } from '../contract/validators';
import { evalStore } from './store';
import {
  fetchLiveStatus,
  fetchPointer,
  fetchReadModel,
  loadRuntimeConfig,
  normalizeRecentProjection,
  normalizeStreamRecord,
} from './feed';
import { liveStatusIntervalFor, startFeedPolling, type FeedPoller } from './feed-poll';
import { purgeLegacyBootstrapCaches } from './legacy-cache';

export interface StartOptions {
  fetchImpl?: typeof fetch;
}

const POLL_FAILURE_MESSAGE = 'The public mirror is unreachable. Last accepted data remains visible.';
const UPDATING_MESSAGE = 'Updating from the public feed.';

let generation = 0;
let poller: FeedPoller | null = null;
/** read_model_sha256 of the read model the store holds in this generation. */
let loadedReadModelSha: string | null = null;
/** The highest sequence applied to the store in this generation: the read model's cursor, then each live tail record. */
let appliedSequence = 0;

interface Session {
  gen: number;
  origin: string;
  fetchImpl: typeof fetch;
  opts: StartOptions;
}

interface CurrentMirror {
  pointer: PublicPointer;
  bootstrap: FeedBootstrap;
}

/** A missing withdrawal digest and an empty one describe the same withdrawal state. */
function digestOf(value: string | undefined): string {
  return value ?? '';
}

function sameSource(reference: FeedSnapshot, next: { source_id: string; withdrawal_digest?: string }): boolean {
  return reference.source_id === next.source_id && digestOf(reference.withdrawal_digest) === digestOf(next.withdrawal_digest);
}

/** Loads the pointer and the read model it names. The mirror serves only its current digest, so a
 *  read model that fails is retried once against a freshly read pointer. */
async function loadCurrent(origin: string, fetchImpl: typeof fetch, pointer?: PublicPointer): Promise<CurrentMirror> {
  const first = pointer ?? (await fetchPointer(origin, fetchImpl));
  try {
    return { pointer: first, bootstrap: await fetchReadModel(origin, first, fetchImpl) };
  } catch {
    const retry = await fetchPointer(origin, fetchImpl);
    return { pointer: retry, bootstrap: await fetchReadModel(origin, retry, fetchImpl) };
  }
}

/** Installs a read model. The store is rebuilt only when the snapshot changed. */
function applyReadModel(bootstrap: FeedBootstrap): void {
  const current = evalStore.getState().currentSnapshot;
  const next = bootstrap.snapshot;
  const isSameSnapshot =
    current !== null &&
    current.source_id === next.source_id &&
    current.high_water_sequence === next.high_water_sequence &&
    current.feed_chain_hash === next.feed_chain_hash &&
    digestOf(current.withdrawal_digest) === digestOf(next.withdrawal_digest);

  evalStore.beginBatch();
  try {
    if (!isSameSnapshot) {
      evalStore.initBootstrap(
        next,
        bootstrap.recent_projections.map(normalizeRecentProjection),
        bootstrap.proof_catalog_summary.artifact_count,
      );
    }
    evalStore.acceptTaskIndex(bootstrap.task_index);
  } finally {
    evalStore.endBatch();
  }
  appliedSequence = next.high_water_sequence;
}

/** Records a successful poll. Restores the live state after an offline period. */
function markReachable(): void {
  const state = evalStore.getState();
  if (state.connection !== 'live') {
    // Re-accept the held snapshot so its freshness is derived again rather than left at source_offline.
    if (state.connection === 'offline' && state.currentSnapshot && state.pendingSnapshot === null) {
      evalStore.acceptSnapshot(state.currentSnapshot);
    }
    evalStore.setConnection('live', UPDATING_MESSAGE);
  }
  if (state.streamConnection !== 'connected') evalStore.setStreamConnection('connected');
}

async function pollPointer(session: Session): Promise<void> {
  try {
    const latest = await fetchPointer(session.origin, session.fetchImpl);
    if (session.gen !== generation) return;
    const reference = evalStore.getState().currentSnapshot;
    if (reference && !sameSource(reference, latest)) {
      void startFeed(session.opts);
      return;
    }
    let pointer = latest;
    if (latest.read_model_sha256 !== loadedReadModelSha) {
      const current = await loadCurrent(session.origin, session.fetchImpl, latest);
      if (session.gen !== generation) return;
      applyReadModel(current.bootstrap);
      pointer = current.pointer;
      loadedReadModelSha = pointer.read_model_sha256;
    }
    poller?.setLiveEnabled(pointer.active);
    markReachable();
  } catch {
    if (session.gen === generation) evalStore.setOffline(POLL_FAILURE_MESSAGE);
  }
}

async function pollLiveStatus(session: Session): Promise<void> {
  try {
    const status = await fetchLiveStatus(session.origin, session.fetchImpl);
    if (session.gen !== generation) return;
    const snapshot: unknown = status.snapshot;
    isFeedSnapshot(snapshot);
    const reference = evalStore.getState().currentSnapshot;
    if (reference && !sameSource(reference, snapshot)) {
      void startFeed(session.opts);
      return;
    }
    evalStore.beginBatch();
    try {
      evalStore.acceptSnapshot(snapshot);
      for (const record of [...status.tail].sort((left, right) => left.sequence - right.sequence)) {
        // acceptProjection drops a record at or below the store's cursor, so a replayed tail is harmless.
        if (record.sequence <= appliedSequence) continue;
        evalStore.acceptProjection(normalizeStreamRecord(record.record_type, String(record.sequence), record.record_bytes));
        appliedSequence = record.sequence;
      }
    } finally {
      evalStore.endBatch();
    }
    markReachable();
  } catch {
    if (session.gen === generation) evalStore.setOffline(POLL_FAILURE_MESSAGE);
  }
}

export async function startFeed(opts: StartOptions = {}): Promise<void> {
  const gen = ++generation;
  poller?.stop();
  poller = null;
  loadedReadModelSha = null;
  appliedSequence = 0;
  purgeLegacyBootstrapCaches();
  const fetchImpl = opts.fetchImpl ?? fetch.bind(globalThis);
  try {
    evalStore.setConnection('connecting', 'Connecting to the public mirror.');
    const runtime = await loadRuntimeConfig(fetchImpl);
    if (gen !== generation) return;
    evalStore.setMirrorOrigin(runtime.mirror_origin);

    const current = await loadCurrent(runtime.mirror_origin, fetchImpl);
    if (gen !== generation) return;
    applyReadModel(current.bootstrap);
    loadedReadModelSha = current.pointer.read_model_sha256;
    markReachable();

    const session: Session = { gen, origin: runtime.mirror_origin, fetchImpl, opts };
    const started = startFeedPolling({
      pollPointer: () => pollPointer(session),
      pollLiveStatus: () => pollLiveStatus(session),
      liveIntervalMs: liveStatusIntervalFor(runtime.mirror_origin),
    });
    poller = started;
    started.setLiveEnabled(current.pointer.active);
  } catch (error) {
    if (gen !== generation) return;
    const message = error instanceof Error ? error.message : 'public mirror is unreachable';
    evalStore.setOffline(`The public mirror is unreachable: ${message}. Last accepted data remains visible.`);
  }
}

export function stopFeed(): void {
  generation++;
  poller?.stop();
  poller = null;
  loadedReadModelSha = null;
  appliedSequence = 0;
}
