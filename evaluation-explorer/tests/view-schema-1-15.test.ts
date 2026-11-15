// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { decodeLiveEventWire } from '../src/contract/live-event-wire';
import { decodeViewRecord, ValidationError } from '../src/contract/validators';
import { fixtureEnrichedAssignmentResult, fixtureModelSummaries } from '../src/fixtures/fixtures';

// View schema 1.15.0 names a generation the Inference Operator stopped because
// its output degenerated into a loop, in three fields, and an episode that
// ended with no answer. A record below 1.15.0 cannot carry either.

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '../..');
const judgeVector = JSON.parse(readFileSync(join(repoRoot, 'protocol/vectors/eval/public_live_event_judge.json'), 'utf8')) as { canonical_json: string };

function assignmentWith(schemaVersion: string, patch: { trajectory_outcome?: string; finish_state?: string }): Record<string, unknown> {
  const activity = fixtureEnrichedAssignmentResult.activity_summary!;
  const models = activity.model_activity as unknown as { records: Record<string, unknown>[] };
  return {
    ...fixtureEnrichedAssignmentResult,
    schema_version: schemaVersion,
    ...(patch.trajectory_outcome ? { trajectory_outcome: patch.trajectory_outcome } : {}),
    activity_summary: {
      ...activity,
      model_activity: { ...models, records: models.records.map((record) => ({ ...record, ...(patch.finish_state ? { finish_state: patch.finish_state } : {}) })) },
    },
  };
}

function failedJudgeEvent(schemaVersion: string): Record<string, unknown> {
  const body = JSON.parse(judgeVector.canonical_json) as Record<string, unknown>;
  const event = decodeLiveEventWire({
    ...body,
    schema_version: schemaVersion,
    judge: { ...(body.judge as Record<string, unknown>), state: 'JUDGE_ACTIVITY_STATE_FAILED', failure: 'JUDGE_FAILURE_REPETITION', rationale: undefined },
  });
  return event as unknown as Record<string, unknown>;
}

describe('view schema 1.15.0: repetition', () => {
  it.each([
    ['repetition trajectory outcome', { trajectory_outcome: 'repetition' }],
    ['no_answer trajectory outcome', { trajectory_outcome: 'no_answer' }],
    ['repetition model activity finish state', { finish_state: 'repetition' }],
  ])('accepts a %s at 1.15.0', (_field, patch) => {
    expect(() => decodeViewRecord('assignment_result', assignmentWith('1.15.0', patch))).not.toThrow();
  });

  it.each([
    ['repetition trajectory outcome', { trajectory_outcome: 'repetition' }],
    ['no_answer trajectory outcome', { trajectory_outcome: 'no_answer' }],
    ['repetition model activity finish state', { finish_state: 'repetition' }],
  ])('rejects a %s below 1.15.0', (_field, patch) => {
    expect(() => decodeViewRecord('assignment_result', assignmentWith('1.14.0', patch))).toThrow(ValidationError);
  });

  it('decodes a judge stopped for repetition as its own failure', () => {
    const event = failedJudgeEvent('1.15.0');
    expect(event.judge).toMatchObject({ state: 'failed', failure: 'repetition' });
    expect(() => decodeViewRecord('judge_completed', event)).not.toThrow();
  });

  it('rejects a repetition judge failure below 1.15.0', () => {
    expect(() => decodeViewRecord('judge_completed', failedJudgeEvent('1.14.0'))).toThrow(ValidationError);
  });
});

describe('view schema 1.15.0: model_summary.tool_calling_exclusions', () => {
  const summary = (schemaVersion: string, count: unknown) => ({ ...fixtureModelSummaries[0]!, schema_version: schemaVersion, tool_calling_exclusions: count });

  it.each([0, 3])('accepts %d exclusions at 1.15.0', (count) => {
    expect(() => decodeViewRecord('model_summary', summary('1.15.0', count))).not.toThrow();
  });

  it.each([-1, 1.5, '0'])('rejects %p at 1.15.0', (count) => {
    expect(() => decodeViewRecord('model_summary', summary('1.15.0', count))).toThrow(ValidationError);
  });

  it('rejects the count below 1.15.0', () => {
    expect(() => decodeViewRecord('model_summary', summary('1.14.0', 0))).toThrow(ValidationError);
  });
});
