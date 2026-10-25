// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// One current snapshot, followed by SSE deltas through the same store decoder.
// History and proof artifacts are evidence queries, not initialization inputs.
import { evalStore } from './store';
import {
  fetchBootstrap,
  fetchSnapshot,
  loadRuntimeConfig,
  normalizeRecentProjection,
  openStream,
  type StreamHandle,
} from './feed';
import { loadCachedBootstrap, saveCachedBootstrap } from './cache';

let activeStream: StreamHandle | null = null;
let generation = 0;

export interface StartOptions {
  fetchImpl?: typeof fetch;
  streamFactory?: typeof openStream;
  skipCache?: boolean;
}

export async function startFeed(opts: StartOptions = {}): Promise<void> {
  const gen = ++generation;
  activeStream?.close();
  activeStream = null;
  const fetchImpl = opts.fetchImpl ?? fetch.bind(globalThis);
  const streamFactory = opts.streamFactory ?? openStream;
  try {
    evalStore.setConnection('connecting', 'Connecting to the public mirror.');
    const cachedRecord = !opts.skipCache ? loadCachedBootstrap() : null;
    if (cachedRecord) {
      evalStore.setMirrorOrigin(cachedRecord.mirror_origin);
      evalStore.initBootstrap(
        cachedRecord.bootstrap.snapshot,
        cachedRecord.bootstrap.recent_projections.map(normalizeRecentProjection),
        cachedRecord.bootstrap.proof_catalog_summary.artifact_count,
      );
      evalStore.setConnection('connecting', 'Connecting to the public mirror.');
    }

    const runtime = await loadRuntimeConfig(fetchImpl);
    if (gen !== generation) return;
    evalStore.setMirrorOrigin(runtime.mirror_origin);

    let needsFullBootstrap = true;
    let targetSourceId = cachedRecord?.bootstrap.snapshot.source_id ?? '';
    let targetHighWater = cachedRecord?.bootstrap.snapshot.high_water_sequence ?? 0;

    if (cachedRecord && cachedRecord.mirror_origin === runtime.mirror_origin) {
      try {
        const remoteSnapshot = await fetchSnapshot(
          runtime.mirror_origin,
          '',
          fetchImpl,
        );
        if (gen !== generation) return;
        const sameSource = remoteSnapshot.source_id === cachedRecord.bootstrap.snapshot.source_id;
        const sameWithdrawal = remoteSnapshot.withdrawal_digest === cachedRecord.bootstrap.snapshot.withdrawal_digest;
        const cachedHighWater = cachedRecord.bootstrap.snapshot.high_water_sequence;
        const sequenceDelta = remoteSnapshot.high_water_sequence - cachedHighWater;
        // The SSE stream replays up to 100 records directly without a multi-megabyte full bootstrap download.
        const canCatchUp = sameSource && sameWithdrawal && sequenceDelta >= 0 && sequenceDelta <= 100;

        if (canCatchUp) {
          needsFullBootstrap = false;
          evalStore.acceptSnapshot(remoteSnapshot);
          targetSourceId = remoteSnapshot.source_id;
          targetHighWater = cachedHighWater;
        }
      } catch {
        needsFullBootstrap = true;
      }
    }

    if (needsFullBootstrap) {
      const bootstrap = await fetchBootstrap(runtime.mirror_origin, fetchImpl);
      if (gen !== generation) return;
      saveCachedBootstrap(runtime.mirror_origin, bootstrap);

      const current = evalStore.getState().currentSnapshot;
      const isSameSnapshot =
        current !== null &&
        current.source_id === bootstrap.snapshot.source_id &&
        current.high_water_sequence === bootstrap.snapshot.high_water_sequence &&
        current.feed_chain_hash === bootstrap.snapshot.feed_chain_hash &&
        current.withdrawal_digest === bootstrap.snapshot.withdrawal_digest;

      if (!isSameSnapshot) {
        evalStore.initBootstrap(
          bootstrap.snapshot,
          bootstrap.recent_projections.map(normalizeRecentProjection),
          bootstrap.proof_catalog_summary.artifact_count,
        );
      }
      targetSourceId = bootstrap.snapshot.source_id;
      targetHighWater = bootstrap.snapshot.high_water_sequence;
    }

    if (targetSourceId) {
      activeStream = streamFactory(runtime.mirror_origin, targetSourceId, targetHighWater, {
        onSnapshot: (snapshot) => {
          if (gen !== generation) return;
          const reference = evalStore.getState().currentSnapshot;
          if (
            reference &&
            (reference.source_id !== snapshot.source_id ||
              reference.withdrawal_digest !== snapshot.withdrawal_digest)
          ) {
            void startFeed(opts);
            return;
          }
          evalStore.acceptSnapshot(snapshot);
        },
        onProjection: (record) => {
          if (gen === generation) evalStore.acceptProjection(record);
        },
        onTruncated: () => {
          if (gen === generation) void startFeed({ ...opts, skipCache: true });
        },
        onError: () => {
          if (gen === generation)
            evalStore.setOffline('The mirror stream is unreachable. Last accepted data remains visible.');
        },
        onStateChange: (streamState) => {
          if (gen !== generation) return;
          evalStore.setStreamConnection(streamState);
          if (streamState === 'connected') evalStore.setConnection('live', 'Streaming live updates via SSE.');
        },
      });
    }
  } catch (error) {
    if (gen !== generation) return;
    const message = error instanceof Error ? error.message : 'public mirror is unreachable';
    evalStore.setOffline(`The public mirror is unreachable: ${message}. Last accepted data remains visible.`);
  }
}

export function stopFeed(): void {
  generation++;
  activeStream?.close();
  activeStream = null;
}
