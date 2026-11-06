// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Builds run bundles the way the mirror publishes them: gzip of JSONL lines whose
// record_hash covers each record_bytes text, addressed by the SHA-256 of the gzip bytes.

import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { fixtureCampaignResultEnvelope, fixtureLiveEvaluationSummary } from '../src/fixtures/fixtures';
import type { EvaluationSummary } from '../src/contract/types';

export interface RunBundle {
  body: Buffer;
  sha256: string;
}

export function sha256Hex(bytes: Buffer | string): string {
  return createHash('sha256').update(bytes).digest('hex');
}

/** The dataset a campaign run's assignments belong to. */
export function campaignDatasetFor(runId: string): string {
  return `ds-live-${runId}`;
}

function resultRecordBytes(runId: string, assignmentId: string): string {
  return JSON.stringify({
    ...fixtureCampaignResultEnvelope,
    idempotency_key: `${runId}:${assignmentId}:result`,
    record: { ...fixtureCampaignResultEnvelope.record, run_id: runId, assignment_id: assignmentId },
  });
}

/** A bundle with one assignment-result record per assignment id, in the given order. */
export function buildRunBundle(runId: string, assignmentIds: string[]): RunBundle {
  const lines = assignmentIds.map((assignmentId, index) => {
    const recordBytes = resultRecordBytes(runId, assignmentId);
    return JSON.stringify({ sequence: index + 1, record_hash: sha256Hex(recordBytes), record_bytes: recordBytes });
  });
  const body = gzipSync(`${lines.join('\n')}\n`);
  return { body, sha256: sha256Hex(body) };
}

/**
 * The run's evaluation summary as the mirror publishes it for a campaign run:
 * model-unit fields only, on the run's campaign dataset, with its bundle digest.
 */
export function campaignEvaluationSummary(runId: string, assignmentBundleSha256?: string): EvaluationSummary {
  // Stack-routing fields are rejected on model-unit evaluations, so they are omitted here.
  const omittedFields = new Set(['primary_invocation_share', 'correlated_failure_rate']);
  const modelUnit = Object.fromEntries(
    Object.entries(fixtureLiveEvaluationSummary).filter(([field]) => !omittedFields.has(field)),
  ) as EvaluationSummary;
  const summary: EvaluationSummary = {
    ...modelUnit,
    schema_version: '1.14.0',
    dataset_id: campaignDatasetFor(runId),
    run_id: runId,
    quality_state: 'live_in_progress',
    headline_metrics: {
      pass_rate: { value: 0.5, unit: 'ratio', observed_count: 2, eligible_count: 2, unavailable_count: 0 },
      latency_p50_ms: { unavailable_reason: 'historical_not_captured', unit: 'milliseconds', observed_count: 0, eligible_count: 2, unavailable_count: 2 },
      output_throughput_p50_tokens_per_second: { unavailable_reason: 'historical_not_captured', unit: 'tokens_per_second', observed_count: 0, eligible_count: 2, unavailable_count: 2 },
    },
  };
  if (assignmentBundleSha256 !== undefined) summary.assignment_bundle_sha256 = assignmentBundleSha256;
  return summary;
}
