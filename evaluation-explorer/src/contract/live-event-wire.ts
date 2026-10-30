// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Wire decoder for live events, which the Gateway encodes from the protobuf
// message ztd.eval.v1.PublicLiveEvent with canonical protojson. This is the one
// place wire rules become view-contract values: enum names map to the view
// vocabulary, and the presence-tracked progress counts are required, because an
// omitted count is a malformed event and never a count of zero.

import { assertUint64String, assertWireReleaseProvenance, mapCampaignRelease, type WireReleaseProvenance } from './campaign-wire';
import { JUDGE_ACTIVITY_STATE_VALUES, JUDGE_FAILURE_VALUES, JUDGE_SUBJECT_VALUES } from './proto-enums.generated';
import { LIVE_EVENT_KINDS, type LiveEvent } from './types';
import { assertEnum, assertObject, isLiveEvent, rejectUnknown, ValidationError } from './validators';

/** Every field of PublicLiveEvent. A key outside this list is not a wire field. */
const LIVE_EVENT_WIRE_FIELDS: readonly string[] = [
  'schema_version', 'kind', 'dataset_id', 'quality_state', 'observed_at', 'source_revision_label',
  'release', 'release_basis', 'source_revision',
  'event_id', 'run_id', 'assignment_id', 'variant_id', 'role', 'lifecycle_status',
  'completed', 'total', 'stage_label', 'task_id', 'metric_delta', 'judge',
];

/** Every field of PublicLiveJudgeActivity. */
const JUDGE_WIRE_FIELDS: readonly string[] = [
  'judge_variant_id', 'served_model_tag', 'model_digest', 'subject', 'criterion_id', 'index', 'count',
  'rubric_version', 'state', 'score', 'rationale', 'failure', 'provider_attempt_id', 'transaction_id',
  'usage_reported', 'prompt_tokens', 'completion_tokens', 'thinking_tokens', 'cache_tokens',
  'load_duration_ns', 'prompt_eval_duration_ns', 'generation_duration_ns', 'total_duration_ns',
  'time_to_first_token_ns', 'finish_reason', 'executed_at_unix_ms', 'dispatch_elapsed_ns',
];

/** The judge block's 64-bit fields, which protojson writes as decimal strings. */
const JUDGE_WIRE_INT64_FIELDS = [
  'load_duration_ns', 'prompt_eval_duration_ns', 'generation_duration_ns', 'total_duration_ns',
  'time_to_first_token_ns', 'executed_at_unix_ms', 'dispatch_elapsed_ns',
] as const;

/** The judge block's enums: the wire name minus its prefix, lower-cased, is
 *  the view value (JUDGE_SUBJECT_FINAL_REPLY is final_reply). */
const JUDGE_WIRE_ENUMS = [
  { field: 'subject', values: JUDGE_SUBJECT_VALUES, prefix: 'JUDGE_SUBJECT_' },
  { field: 'state', values: JUDGE_ACTIVITY_STATE_VALUES, prefix: 'JUDGE_ACTIVITY_STATE_' },
  { field: 'failure', values: JUDGE_FAILURE_VALUES, prefix: 'JUDGE_FAILURE_' },
] as const;

/** Decode the judge block into view form; isLiveEvent then checks its rules. */
function decodeJudgeWire(value: unknown, path: string): Record<string, unknown> {
  assertObject(value, path);
  rejectUnknown(value, JUDGE_WIRE_FIELDS, path);
  const judge: Record<string, unknown> = { ...value, usage_reported: value.usage_reported ?? false };
  for (const { field, values, prefix } of JUDGE_WIRE_ENUMS) {
    const wireValue: unknown = value[field];
    if (wireValue === undefined) continue;
    assertEnum(wireValue, values as readonly string[], `${path}.${field}`);
    judge[field] = wireValue.slice(prefix.length).toLowerCase();
  }
  for (const field of JUDGE_WIRE_INT64_FIELDS) {
    const wireValue: unknown = value[field];
    if (wireValue === undefined) continue;
    assertUint64String(wireValue, `${path}.${field}`);
    judge[field] = Number(wireValue);
  }
  return judge;
}

const PRESENCE_TRACKED_FIELDS = ['completed', 'total'] as const;

export function isLiveEventKind(kind: string): boolean {
  return (LIVE_EVENT_KINDS as readonly string[]).includes(kind);
}

/** Decode one wire live event into the view contract's LiveEvent. */
export function decodeLiveEventWire(value: unknown): LiveEvent {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new ValidationError('expected object', 'live_event');
  }
  const wire = value as Record<string, unknown>;
  for (const key of Object.keys(wire)) {
    if (!LIVE_EVENT_WIRE_FIELDS.includes(key)) throw new ValidationError(`unknown field "${key}"`, `live_event.${key}`);
  }
  for (const field of PRESENCE_TRACKED_FIELDS) {
    if (wire[field] === undefined) {
      throw new ValidationError(`${field} is presence-tracked; an omitted count is not zero`, `live_event.${field}`);
    }
  }
  assertWireReleaseProvenance(wire, 'live_event');
  const { release, release_basis, source_revision, ...rest } = wire;
  void release; void release_basis; void source_revision;
  const identity = mapCampaignRelease(wire as WireReleaseProvenance);
  const event: Record<string, unknown> = { ...rest, release_basis: identity.release_basis };
  if (rest.judge !== undefined) event.judge = decodeJudgeWire(rest.judge, 'live_event.judge');
  if (identity.release !== undefined) event.release = identity.release;
  if (identity.source_revision !== undefined) event.source_revision = identity.source_revision;
  isLiveEvent(event);
  return event;
}
