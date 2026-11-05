// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// A fake public mirror that serves the static layout the explorer reads:
// pointer.json, the digest-addressed read model, live/status.json, and live run
// bundles. Tests mutate the mirror object to change what the next request sees.

import { createHash } from 'node:crypto';
import { vi } from 'vitest';
import { fixtureCatalogExploratory } from '../src/fixtures/fixtures';
import type { FeedSnapshot, FeedTaskIndexEntry } from '../src/contract/types';

export const MIRROR_ORIGIN = 'https://mirror.example';

export interface LayoutTailRecord {
  sequence: number;
  record_type: string;
  record_bytes: string;
}

export interface LayoutMirror {
  snapshot: FeedSnapshot;
  /** Read-model recent projections, each carrying its sequence. */
  recent: Array<Record<string, unknown>>;
  taskIndex: FeedTaskIndexEntry[];
  /** pointer.active, and whether live/status.json lists a running run. */
  active: boolean;
  tail: LayoutTailRecord[];
  /** Live run bundle bytes by run id. A run absent here is a 404. */
  liveBundles: Map<string, Uint8Array>;
}

export function sha256(bytes: string | Uint8Array): string {
  return createHash('sha256').update(bytes).digest('hex');
}

export function makeSnapshot(overrides: Partial<FeedSnapshot> = {}): FeedSnapshot {
  return {
    protocol_version: '1.0.0',
    source_id: 'performance-test',
    high_water_sequence: 100,
    feed_chain_hash: '1'.repeat(64),
    withdrawal_digest: '0'.repeat(64),
    batch_count: 1,
    generated_at: '2026-10-23T00:00:00Z',
    freshness: 'active',
    ...overrides,
  };
}

export function makeMirror(overrides: Partial<LayoutMirror> = {}): LayoutMirror {
  return {
    snapshot: makeSnapshot(),
    recent: [{ ...fixtureCatalogExploratory, sequence: 50 }],
    taskIndex: [],
    active: false,
    tail: [],
    liveBundles: new Map(),
    ...overrides,
  };
}

export function readModelBody(mirror: LayoutMirror): string {
  return JSON.stringify({
    protocol_version: '1.0.0',
    snapshot: mirror.snapshot,
    source_freshness: mirror.snapshot.freshness,
    recent_projections: mirror.recent,
    task_index: mirror.taskIndex,
    proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 },
    generated_at: mirror.snapshot.generated_at,
  });
}

export function pointerOf(mirror: LayoutMirror) {
  return {
    schema_version: '1.0.0',
    source_id: mirror.snapshot.source_id,
    revision: 1,
    read_model_sha256: sha256(readModelBody(mirror)),
    proof_segment_head: 0,
    high_water_sequence: mirror.snapshot.high_water_sequence,
    history_first_sequence: 1,
    feed_chain_hash: mirror.snapshot.feed_chain_hash,
    withdrawal_digest: mirror.snapshot.withdrawal_digest,
    active: mirror.active,
    generated_at: mirror.snapshot.generated_at,
  };
}

function liveStatus(mirror: LayoutMirror) {
  return {
    schema_version: '1.0.0',
    snapshot: mirror.snapshot,
    active_runs: mirror.active ? [{ dataset_id: 'ds-live', run_id: 'run-live', lifecycle_state: 'running' }] : [],
    tail: mirror.tail,
  };
}

function json(value: unknown): Response {
  return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } });
}

export function requestedPaths(fetchImpl: { mock: { calls: unknown[][] } }): string[] {
  return fetchImpl.mock.calls.map(([input]) => new URL(String(input), MIRROR_ORIGIN).pathname);
}

/** Serves the layout for the mirror's current state. Any other path is a 404. */
export function serveLayout(mirror: LayoutMirror) {
  return vi.fn(async (input: Parameters<typeof fetch>[0]): Promise<Response> => {
    const url = new URL(String(input), MIRROR_ORIGIN);
    const path = url.pathname;
    if (path === '/runtime.json') return json({ schema_version: '1.0.0', mirror_origin: MIRROR_ORIGIN });
    if (path === '/pointer.json') return json(pointerOf(mirror));
    if (path === '/live/status.json') return json(liveStatus(mirror));
    const readModel = /^\/read-model\/([0-9a-f]{64})\.json\.gz$/u.exec(path);
    if (readModel) {
      const body = readModelBody(mirror);
      return sha256(body) === readModel[1] ? new Response(body, { status: 200 }) : new Response('', { status: 404 });
    }
    const liveRun = /^\/live\/runs\/([^/]+)\.jsonl\.gz$/u.exec(path);
    if (liveRun) {
      const bytes = mirror.liveBundles.get(decodeURIComponent(liveRun[1]!));
      return bytes ? new Response(new Uint8Array(bytes), { status: 200 }) : new Response('', { status: 404 });
    }
    return new Response('', { status: 404 });
  });
}
