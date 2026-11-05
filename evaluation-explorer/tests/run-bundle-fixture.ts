// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { vi } from 'vitest';
import { fixtureCampaignResultEnvelope, fixtureEvaluationSummaries } from '../src/fixtures/fixtures';
import type { SnapshotRecord } from '../src/contract/types';
import { evalStore } from '../src/state/store';

export const BUNDLE_ORIGIN = 'https://mirror.example';

export interface BundleFixture {
  bytes: Uint8Array;
  sha256: string;
}

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

/** A run bundle: gzip of JSONL lines whose record_hash covers each record_bytes text. */
export function buildRunBundle(runId: string, assignmentIds: string[]): BundleFixture {
  const lines = assignmentIds.map((assignmentId, index) => {
    const recordBytes = resultRecordBytes(runId, assignmentId);
    return JSON.stringify({ sequence: index + 1, record_hash: sha256Hex(recordBytes), record_bytes: recordBytes });
  });
  const bytes = new Uint8Array(gzipSync(`${lines.join('\n')}\n`));
  return { bytes, sha256: sha256Hex(bytes) };
}

/** Serves bundles by content address; any other URL is a 404. Returns the mock so tests can count requests. */
export function stubBundleFetch(bundles: BundleFixture[]) {
  const byUrl = new Map(bundles.map((bundle) => [`${BUNDLE_ORIGIN}/proofs/${bundle.sha256}`, bundle.bytes]));
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

/** Installs the snapshot records and the mirror origin the bundle loader reads. */
export function loadBundleFixtures(records: SnapshotRecord[]): void {
  evalStore.loadFixtures(records, []);
  evalStore.setMirrorOrigin(BUNDLE_ORIGIN);
}

/** A settled run's summary record that names its bundle. */
export function settledRunRecord(runId: string, sha256: string, datasetId = `ds-live-${runId}`): SnapshotRecord {
  const { native_result: _native, ...summary } = fixtureEvaluationSummaries[0]!;
  void _native;
  return {
    ...summary,
    dataset_id: datasetId,
    run_id: runId,
    lifecycle_state: 'completed',
    assignment_bundle_sha256: sha256,
  } as SnapshotRecord;
}
