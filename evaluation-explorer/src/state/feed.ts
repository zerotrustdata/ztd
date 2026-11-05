// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Public mirror feed client. Runtime validation, anonymous fetch construction,
// and endpoint allowlisting come from the audited ztd adapter, which reads the
// mirror's static layout: pointer.json names the digest-addressed read model,
// and live/status.json carries the active runs and the newest records.
// Explorer-specific validators normalize those wire shapes into the typed
// evaluation corpus store without reaching a private service.

import {
  createPublicClient,
  parsePublicRuntimeConfig,
  type PublicLiveStatus,
  type PublicPointer,
  type PublicRuntimeConfig,
} from '../../../ztd-adapter/src/public';
import type { FeedBootstrap, ProofCatalogEntry, ProjectionRecord } from '../contract/types';
import {
  isFeedBootstrap,
  isProjectionRecord,
  isProofCatalogEntry,
  createLazyProjectionRecord,
} from '../contract/validators';

export type RuntimeConfig = PublicRuntimeConfig;

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Normalize a /bootstrap recent_projections item ({...payload, sequence}). */
export function normalizeRecentProjection(item: unknown): ProjectionRecord {
  if (!isObject(item)) throw new Error('invalid public bootstrap record');
  const payload = { ...item };
  const sequence = payload.sequence;
  delete payload.sequence;
  delete payload.record_type;
  const record = createLazyProjectionRecord(Number(sequence), 'projection', payload);
  isProjectionRecord(record);
  return record;
}

/** Normalize one live record ({sequence, record_type, record_bytes}) into a projection record. */
export function normalizeStreamRecord(recordType: string, lastEventId: string, data: string): ProjectionRecord {
  const record: ProjectionRecord = {
    sequence: Number(lastEventId),
    record_type: recordType as ProjectionRecord['record_type'],
    record_bytes: data,
  };
  isProjectionRecord(record);
  return record;
}

export async function loadRuntimeConfig(fetchImpl: typeof fetch = fetch.bind(globalThis)): Promise<RuntimeConfig> {
  const response = await fetchImpl('/runtime.json', {
    method: 'GET',
    credentials: 'omit',
    headers: { Accept: 'application/json' },
  });
  if (!response.ok) throw new Error(`runtime config returned ${response.status}`);
  return parsePublicRuntimeConfig(await response.json());
}

function client(mirrorOrigin: string, fetchImpl: typeof fetch) {
  return createPublicClient(parsePublicRuntimeConfig({ schema_version: '1.0.0', mirror_origin: mirrorOrigin }), fetchImpl);
}

/** The mutable pointer: the digest of the current read model and the source's withdrawal state. */
export async function fetchPointer(
  mirrorOrigin: string,
  fetchImpl: typeof fetch = fetch.bind(globalThis),
): Promise<PublicPointer> {
  return client(mirrorOrigin, fetchImpl).pointer();
}

/** The read model the pointer names. The adapter checks its bytes against read_model_sha256. */
export async function fetchReadModel(
  mirrorOrigin: string,
  pointer: PublicPointer,
  fetchImpl: typeof fetch = fetch.bind(globalThis),
): Promise<FeedBootstrap> {
  const bootstrap: unknown = await client(mirrorOrigin, fetchImpl).readModel(pointer);
  isFeedBootstrap(bootstrap);
  return bootstrap;
}

/** The active runs, the snapshot, and the newest records, oldest first. */
export async function fetchLiveStatus(
  mirrorOrigin: string,
  fetchImpl: typeof fetch = fetch.bind(globalThis),
): Promise<PublicLiveStatus> {
  return client(mirrorOrigin, fetchImpl).liveStatus();
}

/** Resolve one evidence binding's active catalog entry by content address, or null when the
 *  mirror holds none (unknown or withdrawn). */
export async function fetchProofCatalogEntry(
  mirrorOrigin: string,
  sha256: string,
  fetchImpl: typeof fetch = fetch.bind(globalThis),
): Promise<ProofCatalogEntry | null> {
  const entry: unknown = await client(mirrorOrigin, fetchImpl).proofCatalogEntry(sha256);
  if (entry === null) return null;
  isProofCatalogEntry(entry);
  return entry;
}
