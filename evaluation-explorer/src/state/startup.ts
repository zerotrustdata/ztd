// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// One current snapshot, followed by SSE deltas through the same store decoder.
// History and proof artifacts are evidence queries, not initialization inputs.
import { evalStore } from './store';
import { fetchBootstrap, loadRuntimeConfig, normalizeRecentProjection, openStream, type StreamHandle } from './feed';

let activeStream: StreamHandle | null = null;
let generation = 0;

export interface StartOptions {
  fetchImpl?: typeof fetch;
  streamFactory?: typeof openStream;
}

export async function startFeed(opts: StartOptions = {}): Promise<void> {
  const gen = ++generation;
  activeStream?.close();
  activeStream = null;
  const fetchImpl = opts.fetchImpl ?? fetch.bind(globalThis);
  const streamFactory = opts.streamFactory ?? openStream;
  try {
    evalStore.setConnection('connecting', 'Connecting to the public mirror.');
    const runtime = await loadRuntimeConfig(fetchImpl);
    if (gen !== generation) return;
    evalStore.setMirrorOrigin(runtime.mirror_origin);
    const bootstrap = await fetchBootstrap(runtime.mirror_origin, fetchImpl);
    if (gen !== generation) return;
    evalStore.initBootstrap(bootstrap.snapshot, bootstrap.recent_projections.map(normalizeRecentProjection), bootstrap.proof_catalog_summary.artifact_count);
    activeStream = streamFactory(runtime.mirror_origin, bootstrap.snapshot.source_id, bootstrap.snapshot.high_water_sequence, {
      onSnapshot: (snapshot) => {
        if (gen !== generation) return;
        const reference = evalStore.getState().currentSnapshot;
        if (reference && (reference.source_id !== snapshot.source_id || reference.withdrawal_digest !== snapshot.withdrawal_digest)) {
          void startFeed(opts);
          return;
        }
        evalStore.acceptSnapshot(snapshot);
      },
      onProjection: (record) => {
        if (gen === generation) evalStore.acceptProjection(record);
      },
      onTruncated: () => { if (gen === generation) void startFeed(opts); },
      onError: () => {
        if (gen === generation) evalStore.setOffline('The mirror stream is unreachable. Last accepted data remains visible.');
      },
      onStateChange: (streamState) => {
        if (gen !== generation) return;
        evalStore.setStreamConnection(streamState);
        if (streamState === 'connected') evalStore.setConnection('live', 'Streaming live updates via SSE.');
      },
    });
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
