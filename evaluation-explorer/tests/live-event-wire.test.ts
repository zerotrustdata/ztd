// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { decodeLiveEventWire, isLiveEventKind } from '../src/contract/live-event-wire';
import { decodeViewRecord, ValidationError } from '../src/contract/validators';
import { EvalStore } from '../src/state/store';

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '../..');
const liveEventVector = JSON.parse(
  readFileSync(join(repoRoot, 'protocol/vectors/eval/public_live_event.json'), 'utf8'),
) as { message_type: string; canonical_json: string };

const wire = (): Record<string, unknown> => JSON.parse(liveEventVector.canonical_json) as Record<string, unknown>;

const judgeEventVector = JSON.parse(
  readFileSync(join(repoRoot, 'protocol/vectors/eval/public_live_event_judge.json'), 'utf8'),
) as { message_type: string; canonical_json: string };

const judgeWire = (): Record<string, unknown> => JSON.parse(judgeEventVector.canonical_json) as Record<string, unknown>;
const withJudge = (patch: Record<string, unknown>, event: Record<string, unknown> = {}): Record<string, unknown> => {
  const body = { ...judgeWire(), ...event };
  return { ...body, judge: { ...(body.judge as Record<string, unknown>), ...patch } };
};
const startedJudge = (): Record<string, unknown> => {
  const body = judgeWire();
  const judge = body.judge as Record<string, unknown>;
  return {
    ...body,
    kind: 'judge_started',
    event_id: 'run-1:assign-1:judge-started:judge-criterion-1:event',
    judge: {
      judge_variant_id: judge.judge_variant_id, served_model_tag: judge.served_model_tag, model_digest: judge.model_digest,
      subject: judge.subject, criterion_id: judge.criterion_id, index: judge.index, count: judge.count,
      rubric_version: judge.rubric_version, state: 'JUDGE_ACTIVITY_STATE_JUDGING', provider_attempt_id: judge.provider_attempt_id,
    },
  };
};

describe('decodeLiveEventWire', () => {
  it('decodes the Go-produced vector, keeping zero progress and a measured zero', () => {
    expect(liveEventVector.message_type).toBe('PublicLiveEvent');
    const event = decodeLiveEventWire(wire());
    expect(event.kind).toBe('metric_updated');
    expect(event.completed).toBe(0);
    expect(event.total).toBe(5);
    expect(event.metric_delta?.pass).toEqual({ value: 0 });
    expect(event.release).toBe('v2.3.0');
    expect(event.release_basis).toBe('recorded');
    expect(event.source_revision).toBe('abc123');
    expect(() => decodeViewRecord(event.kind, event)).not.toThrow();
  });

  it.each([
    ['PUBLIC_RELEASE_BASIS_ASSERTED', 'asserted', 'v2.2.7'],
    ['PUBLIC_RELEASE_BASIS_UNKNOWN', 'unknown', undefined],
  ])('maps wire release basis %s to %s', (wireBasis, basis, release) => {
    const event = decodeLiveEventWire({ ...wire(), release, release_basis: wireBasis });
    expect(event.release_basis).toBe(basis);
    expect(event.release).toBe(release);
  });

  it('reads an event with no release identity as unknown, not as a missing field', () => {
    const { release, release_basis, source_revision, ...historical } = wire();
    void release; void release_basis; void source_revision;
    const event = decodeLiveEventWire(historical);
    expect(event.release_basis).toBe('unknown');
    expect(event.release).toBeUndefined();
  });

  it.each(['completed', 'total'])('rejects an event that omits %s instead of reading zero', (field) => {
    const body = wire();
    delete body[field];
    expect(() => decodeLiveEventWire(body)).toThrow(ValidationError);
    expect(() => decodeLiveEventWire(body)).toThrow(new RegExp(field));
  });

  it('rejects the view-form release basis, which is not a wire value', () => {
    expect(() => decodeLiveEventWire({ ...wire(), release_basis: 'recorded' })).toThrow(ValidationError);
  });

  it.each([
    ['a field outside the message', { feed_sequence: 3 }],
    ['a camelCase alias', { eventId: 'x' }],
    ['a provider-boundary field', { served_model_tag: 'qwen3' }],
  ])('rejects %s', (_label, extra) => {
    expect(() => decodeLiveEventWire({ ...wire(), ...extra })).toThrow(ValidationError);
  });

  it('rejects a metric with neither a value nor a reason', () => {
    expect(() => decodeLiveEventWire({ ...wire(), metric_delta: { pass: {} } })).toThrow(ValidationError);
  });

  it('rejects a non-object payload', () => {
    expect(() => decodeLiveEventWire('not an event')).toThrow(ValidationError);
  });
});

describe('isLiveEventKind', () => {
  it('names exactly the live event kinds', () => {
    expect(isLiveEventKind('stage_updated')).toBe(true);
    expect(isLiveEventKind('metric_updated')).toBe(true);
    expect(isLiveEventKind('evaluation_summary')).toBe(false);
  });
});

describe('EvalStore live event ingest', () => {
  it('indexes the wire event from the feed with its measured zero', () => {
    const store = new EvalStore();
    store.acceptProjection({ sequence: 7, record_type: 'event', record_bytes: liveEventVector.canonical_json });
    expect(store.getState().errors).toEqual([]);
    const event = store.getState().events.find((candidate) => candidate.event_id === 'run-1:assign-failed:metric:pass:event');
    expect(event).toBeDefined();
    expect(event?.feed_sequence).toBe(7);
    expect(event?.metric_delta?.pass).toEqual({ value: 0 });
    expect(event?.release_basis).toBe('recorded');
  });

  it('indexes a judgment\'s start and completion beside its assignment\'s other events', () => {
    const store = new EvalStore();
    store.acceptProjection({ sequence: 9, record_type: 'event', record_bytes: JSON.stringify(startedJudge()) });
    store.acceptProjection({ sequence: 10, record_type: 'event', record_bytes: judgeEventVector.canonical_json });
    expect(store.getState().errors).toEqual([]);
    const judged = store.getState().events.filter((candidate) => candidate.assignment_id === 'assign-1');
    expect(judged.map((event) => event.kind).sort()).toEqual(['judge_completed', 'judge_started']);
    const history = [...store.getState().assignmentEvents.values()].flat().filter((event) => event.assignment_id === 'assign-1');
    expect(history.map((event) => event.judge?.state).sort()).toEqual(['judging', 'met']);
  });

  it('records an error and no event when the wire event omits its progress', () => {
    const store = new EvalStore();
    const body = wire();
    delete body.completed;
    store.acceptProjection({ sequence: 8, record_type: 'event', record_bytes: JSON.stringify(body) });
    expect(store.getState().events).toHaveLength(0);
    expect(store.getState().errors.join(' ')).toContain('completed');
  });
});

describe('decodeLiveEventWire judge events', () => {
  it('decodes the Go-produced judge vector into view vocabulary and numbers', () => {
    expect(judgeEventVector.message_type).toBe('PublicLiveEvent');
    const event = decodeLiveEventWire(judgeWire());
    expect(event.kind).toBe('judge_completed');
    expect(event.variant_id).toBe('gemma4-e2b');
    expect(event.judge).toMatchObject({
      served_model_tag: 'qwen3.5:4b', subject: 'criterion', criterion_id: 'refusal', state: 'met',
      index: 1, count: 2, usage_reported: true, prompt_tokens: 812, completion_tokens: 40, thinking_tokens: 0,
      load_duration_ns: 0, total_duration_ns: 1_200_000_000, executed_at_unix_ms: 1_793_361_605_000, dispatch_elapsed_ns: 1_300_000_000,
    });
    expect(() => decodeViewRecord(event.kind, event)).not.toThrow();
  });

  it('decodes an in-flight judgment', () => {
    const event = decodeLiveEventWire(startedJudge());
    expect(event.judge?.state).toBe('judging');
    expect(event.judge?.usage_reported).toBe(false);
  });

  it('decodes a scored quality judgment and a failed one', () => {
    const scored = decodeLiveEventWire(withJudge({ subject: 'JUDGE_SUBJECT_FINAL_REPLY', criterion_id: undefined, state: 'JUDGE_ACTIVITY_STATE_SCORED', score: 4 }));
    expect(scored.judge).toMatchObject({ subject: 'final_reply', state: 'scored', score: 4 });
    const failed = decodeLiveEventWire(withJudge({ state: 'JUDGE_ACTIVITY_STATE_FAILED', failure: 'JUDGE_FAILURE_VERDICT_INVALID', rationale: undefined }));
    expect(failed.judge).toMatchObject({ state: 'failed', failure: 'verdict_invalid' });
  });

  it.each<[string, Record<string, unknown>]>([
    ['an unknown judge field', withJudge({ prompt: 'secret' })],
    ['a view-form enum on the wire', withJudge({ state: 'met' })],
    ['a 64-bit duration that is not a decimal string', withJudge({ total_duration_ns: 1200 })],
    ['a 64-bit duration past the browser-safe range', withJudge({ total_duration_ns: '9007199254740993' })],
    ['judge on a non-judge event', { ...judgeWire(), kind: 'stage_updated' }],
    ['a judge event without judge', { ...judgeWire(), judge: undefined }],
    ['a metric on a judge event', { ...judgeWire(), metric_delta: { latency_ms: { value: 1 } } }],
    ['a stage label on a judge event', { ...judgeWire(), stage_label: 'judge' }],
    ['a judge event without the subject variant', { ...judgeWire(), variant_id: undefined }],
    ['a scored criterion', withJudge({ state: 'JUDGE_ACTIVITY_STATE_SCORED', score: 4 })],
    ['a met quality subject', withJudge({ subject: 'JUDGE_SUBJECT_MEMORY', criterion_id: undefined })],
    ['a score outside 1..5', withJudge({ subject: 'JUDGE_SUBJECT_MEMORY', criterion_id: undefined, state: 'JUDGE_ACTIVITY_STATE_SCORED', score: 6 })],
    ['a failure without its code', withJudge({ state: 'JUDGE_ACTIVITY_STATE_FAILED', rationale: undefined })],
    ['a verdict with a failure code', withJudge({ failure: 'JUDGE_FAILURE_DISPATCH_FAILED' })],
    ['a verdict without its transaction', withJudge({ transaction_id: undefined })],
    ['tokens without usage_reported', withJudge({ usage_reported: false })],
    ['an index past count', withJudge({ index: 3 })],
    ['a started judgment with usage', { ...startedJudge(), judge: { ...(startedJudge().judge as Record<string, unknown>), total_duration_ns: '1' } }],
  ])('rejects %s', (_, body) => {
    expect(() => decodeLiveEventWire(JSON.parse(JSON.stringify(body)) as Record<string, unknown>)).toThrow(ValidationError);
  });
});
