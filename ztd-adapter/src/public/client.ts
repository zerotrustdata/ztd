// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { createPublicFetch, decodePublicJSON, type PublicFetch, type PublicRequest } from './fetch.js';
import type { PublicRuntimeConfig } from './runtime_config.js';
import type {
  PublicBootstrap,
  PublicLiveStatus,
  PublicPointer,
  PublicRecentProjection,
  PublicSnapshot,
} from './state.js';

export class PublicClientError extends Error {
  constructor(public readonly operation: string, public readonly status: number, message: string) {
    super(`public client ${operation}: ${message}`);
    this.name = 'PublicClientError';
  }
}

export interface PublicClient {
  /** The mutable pointer. Its signature is not verified here; use verify-proof --pointer for that. */
  pointer(signal?: AbortSignal): Promise<PublicPointer>;
  /** The read model the pointer names. Throws PublicClientError when the decoded bytes do not hash to read_model_sha256. */
  readModel(pointer: PublicPointer, signal?: AbortSignal): Promise<PublicBootstrap>;
  liveStatus(signal?: AbortSignal): Promise<PublicLiveStatus>;

}

function object(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function string(value: unknown): value is string {
  return typeof value === 'string';
}

function integer(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

function positiveInteger(value: unknown): value is number {
  return integer(value) && value > 0;
}

function digest(value: unknown): value is string {
  return string(value) && /^[0-9a-f]{64}$/.test(value);
}

function optional(check: (value: unknown) => boolean): (value: unknown) => boolean {
  return (value) => value === undefined || check(value);
}

function signature(value: unknown): value is string {
  return string(value) && /^[0-9a-f]{128}$/.test(value);
}

function snapshot(value: unknown): value is PublicSnapshot {
  const candidate = object(value);
  return candidate !== null
    && string(candidate.protocol_version)
    && string(candidate.source_id)
    && integer(candidate.high_water_sequence)
    && digest(candidate.feed_chain_hash)
    && integer(candidate.batch_count)
    && (candidate.withdrawal_digest === undefined || digest(candidate.withdrawal_digest))
    && string(candidate.generated_at)
    && ['active', 'delayed', 'stale', 'intentionally_stopped', 'safety_stopped', 'source_offline'].includes(String(candidate.freshness));
}

function recentProjection(value: unknown): value is PublicRecentProjection {
  const candidate = object(value);
  return candidate !== null && integer(candidate.sequence) && candidate.record_type === undefined;
}

function bootstrap(value: unknown): value is PublicBootstrap {
  const candidate = object(value);
  return candidate !== null
    && candidate.schema_version === '2.0.0'
    && string(candidate.protocol_version)
    && snapshot(candidate.snapshot)
    && string(candidate.source_freshness)
    && Array.isArray(candidate.recent_projections)
    && candidate.recent_projections.every(recentProjection)
    && string(candidate.generated_at);
}

function pointer(value: unknown): value is PublicPointer {
  const candidate = object(value);
  return candidate !== null
    && candidate.schema_version === '2.0.0'
    && string(candidate.source_id)
    && candidate.source_id !== ''
    && positiveInteger(candidate.revision)
    && digest(candidate.read_model_sha256)
    && integer(candidate.high_water_sequence)
    && integer(candidate.history_first_sequence)
    && digest(candidate.feed_chain_hash)
    && optional(digest)(candidate.withdrawal_digest)
    && typeof candidate.active === 'boolean'
    && string(candidate.generated_at)
    && optional(digest)(candidate.signing_key_id)
    && optional(digest)(candidate.signing_public_key)
    && optional(signature)(candidate.signature);
}

function liveRun(value: unknown): boolean {
  const candidate = object(value);
  return candidate !== null && string(candidate.dataset_id) && string(candidate.run_id) && string(candidate.lifecycle_state);
}

function liveRecord(value: unknown): boolean {
  const candidate = object(value);
  return candidate !== null && integer(candidate.sequence) && string(candidate.record_type) && string(candidate.record_bytes);
}

function liveStatus(value: unknown): value is PublicLiveStatus {
  const candidate = object(value);
  return candidate !== null
    && candidate.schema_version === '1.0.0'
    && snapshot(candidate.snapshot)
    && Array.isArray(candidate.active_runs)
    && candidate.active_runs.every(liveRun)
    && Array.isArray(candidate.tail)
    && candidate.tail.every(liveRecord);
}

async function sha256Hex(bytes: ArrayBuffer): Promise<string> {
  const hash = await crypto.subtle.digest('SHA-256', bytes);
  return Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

async function readJSON<T>(fetchPublic: PublicFetch, operation: string, request: PublicRequest, validator: (value: unknown) => value is T, signal?: AbortSignal): Promise<T> {
  const response = await fetchPublic(request, signal);
  if (!response.ok) throw new PublicClientError(operation, response.status, 'mirror request failed');
  const body = decodePublicJSON(response.bytes);
  if (!validator(body)) throw new PublicClientError(operation, response.status, 'invalid response');
  return body;
}

async function readDigested<T>(fetchPublic: PublicFetch, operation: string, request: Extract<PublicRequest, { readonly digest: string }>, validator: (value: unknown) => value is T, signal?: AbortSignal): Promise<T> {
  const response = await fetchPublic(request, signal);
  if (!response.ok) throw new PublicClientError(operation, response.status, 'mirror request failed');
  if (await sha256Hex(response.bytes) !== request.digest) throw new PublicClientError(operation, response.status, 'digest mismatch');
  const body = decodePublicJSON(response.bytes);
  if (!validator(body)) throw new PublicClientError(operation, response.status, 'invalid response');
  return body;
}

export function createPublicClient(config: PublicRuntimeConfig, fetchImpl: typeof fetch = fetch): PublicClient {
  const fetchPublic = createPublicFetch(config, fetchImpl);
  return {
    pointer: (signal) => readJSON(fetchPublic, 'pointer', { endpoint: 'pointer' }, pointer, signal),
    readModel: (current, signal) => readDigested(fetchPublic, 'readModel', { endpoint: 'readModel', digest: current.read_model_sha256 }, bootstrap, signal),
    liveStatus: (signal) => readJSON(fetchPublic, 'liveStatus', { endpoint: 'liveStatus' }, liveStatus, signal),

  };
}
