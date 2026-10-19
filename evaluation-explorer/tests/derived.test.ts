// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { NATIVE_RESULT_STATUSES, NATIVE_SCENARIO_STATUSES, QUALITY_STATES, TERMINAL_STATUSES, VERIFIER_STATES } from '../src/contract/types';
import type {
  AssignmentResult,
  CatalogSnapshot,
  EvaluationSummary,
  LiveEvent,
  ModelRole,
  ModelSummary,
} from '../src/contract/types';
import {
  assignmentEvidenceChain,
  assignmentLifecycleEvents,
  assignmentMetricEntries,
  assignmentMetricFormatter,
  assignmentSecuritySignals,
  assignmentVerdictTone,
  isNonVerdictOutcome,
  nativeResultTone,
  nativeScenarioTone,
  qualityStateTone,
  terminalOutcomeBreakdown,
  verifierStateTone,
  recentCampaignRows,
  roleLabel,
  roleLeaderRows,
  modelRoleLeaderboardRows,
  modelOverallLeaderboardRows,
  restampStreamProgress,
  siblingRepetitions,
  streamProgressLabel,
  upsertLiveEvent,
  visibleStreamEvents,
} from '../src/views/derived';

describe('verifierStateTone', () => {
  const expected = { passed: 'ok', failed: 'critical', not_run: 'neutral', not_applicable: 'neutral' };
  it.each(VERIFIER_STATES)('maps %s', (state) => expect(verifierStateTone(state)).toBe(expected[state]));
});

describe('nativeResultTone', () => {
  const expected = { pass: 'ok', fail: 'critical', unavailable: 'neutral', unsupported: 'neutral', invalid_evidence: 'warn' };
  it.each(NATIVE_RESULT_STATUSES)('maps %s', (state) => expect(nativeResultTone(state)).toBe(expected[state]));
});

describe('nativeScenarioTone', () => {
  const expected = { completed: 'ok', rejected: 'ok', failed: 'critical', unavailable: 'neutral', unsupported: 'neutral', invalid_evidence: 'warn' };
  it.each(NATIVE_SCENARIO_STATUSES)('maps %s', (state) => expect(nativeScenarioTone(state)).toBe(expected[state]));
});

describe('qualityStateTone', () => {
  const expected = { exploratory_verified: 'ok', exploratory_partial: 'warn', legacy_unverified: 'warn', live_in_progress: 'info', terminal_failed: 'critical', dead_evidence: 'critical', not_evaluated: 'neutral', unavailable: 'neutral' };
  it.each(QUALITY_STATES)('maps %s', (state) => expect(qualityStateTone(state)).toBe(expected[state]));
});

describe('terminalOutcomeBreakdown', () => {
  it.each(TERMINAL_STATUSES)('classifies %s without dropping it', (status) => {
    const result = terminalOutcomeBreakdown({ [status]: 2 })!;
    expect(result.total).toBe(2);
    const isVerdict = status === 'completed' || status === 'model_failed';
    expect(isNonVerdictOutcome(status)).toBe(!isVerdict);
    expect(result.verdictCount).toBe(isVerdict ? 2 : 0);
    expect(result.nonVerdictCount).toBe(isVerdict ? 0 : 2);
    expect([...result.verdicts, ...result.nonVerdicts][0]?.status).toBe(status);
  });
  it('keeps absent outcomes distinct from observed zero outcomes', () => {
    expect(isNonVerdictOutcome('running')).toBe(false);
    expect(isNonVerdictOutcome('queued')).toBe(false);
    expect(terminalOutcomeBreakdown(undefined)).toBeUndefined();
    expect(terminalOutcomeBreakdown({ completed: 0 })?.total).toBe(0);
  });
});

describe('roleLabel', () => {
  it('maps wire roles to display labels', () => {
    expect(roleLabel('primary')).toBe('Primary');
    expect(roleLabel('assistant')).toBe('Assistant');
    expect(roleLabel('lite')).toBe('Lite');
  });
});

describe('assignmentMetricEntries', () => {
  it('orders scoring metrics and hides task_score when pass is present', () => {
    const entries = assignmentMetricEntries({
      task_score: { value: 1 },
      pass: { value: 1 },
      deterministic_pass_rate: { value: 1 },
      latency_ms: { value: 780 },
    });
    expect(entries.map((entry) => entry.key)).toEqual(['pass', 'deterministic_pass_rate', 'latency_ms']);
    expect(entries[0]?.label).toBe('Pass');
    expect(entries[1]?.label).toBe('Pass Rate');
  });
});

describe('assignmentMetricFormatter', () => {
  it('formats pass outcomes and rates for display', () => {
    expect(assignmentMetricFormatter('pass')(1)).toBe('Pass');
    expect(assignmentMetricFormatter('pass')(0)).toBe('Fail');
    expect(assignmentMetricFormatter('deterministic_pass_rate')(1)).toBe('100.0%');
    expect(assignmentMetricFormatter('tokens_per_second')(3.33)).toBe('3.3 tok/s');
  });
});

describe('siblingRepetitions', () => {
  it('isolates siblings by dataset, run, scenario, variant, and role with deterministic ordering', () => {
    const assignment = assignmentResult({ assignment_id: 'current', repetition: 1, role: 'primary' });
    const siblings = siblingRepetitions(assignment, [
      assignment,
      assignmentResult({ assignment_id: 'rep-2', repetition: 2, role: 'primary' }),
      assignmentResult({ assignment_id: 'rep-3', repetition: 3, role: 'primary' }),
      assignmentResult({ assignment_id: 'rep-2-alpha', repetition: 2, role: 'primary' }),
      assignmentResult({ assignment_id: 'wrong-role', repetition: 2, role: 'assistant' }),
      assignmentResult({ assignment_id: 'wrong-task', repetition: 2, task_id: 'other-scenario' }),
      assignmentResult({ assignment_id: 'wrong-dataset', repetition: 2, dataset_id: 'ds-other' }),
      assignmentResult({ assignment_id: 'wrong-run', repetition: 2, run_id: 'run-other' }),
      assignmentResult({ assignment_id: 'wrong-variant', repetition: 2, variant_id: 'other-model' }),
    ]);

    expect(siblings.map((item) => item.assignment_id)).toEqual(['rep-2', 'rep-2-alpha', 'rep-3']);
  });
});

describe('assignmentVerdictTone', () => {
  it('reads only completed and model_failed as verdicts on the model', () => {
    expect(assignmentVerdictTone('completed', 'passed')).toBe('ok');
    expect(assignmentVerdictTone('model_failed', undefined)).toBe('critical');
    expect(assignmentVerdictTone('running', undefined)).toBe('info');
    for (const status of ['grader_failed', 'invalid_evidence', 'stopped', 'provider_failed', 'execution_failed', 'escalated'] as const) {
      expect(assignmentVerdictTone(status, undefined)).toBe('warn');
    }
  });

  it('lets a failed verification override a completed status', () => {
    expect(assignmentVerdictTone('completed', 'failed')).toBe('critical');
  });
});

describe('assignmentEvidenceChain', () => {
  const step = (assignment: AssignmentResult, key: string) =>
    assignmentEvidenceChain(assignment).find((item) => item.key === key);

  it('walks all eight links in execution order', () => {
    expect(assignmentEvidenceChain(assignmentResult()).map((item) => item.key)).toEqual([
      'scenario', 'model', 'tool_calls', 'policy', 'governed_actions', 'graded', 'verified', 'audit_slice',
    ]);
  });

  it('says "Not published" for evidence the record does not carry, never zero', () => {
    const assignment = assignmentResult();
    for (const key of ['model', 'tool_calls', 'policy', 'governed_actions', 'verified', 'audit_slice']) {
      expect(step(assignment, key)).toMatchObject({ value: 'Not published', tone: 'neutral' });
    }
  });

  it('counts traced tool calls and flags failures', () => {
    const assignment = assignmentResult({
      role_transcripts: [{ role: 'primary', tool_calls: [
        { tool_name: 'read_file', success: true },
        { tool_name: 'send_email', success: false },
      ] }],
    });
    expect(step(assignment, 'model')).toMatchObject({ value: '1 role trace' });
    expect(step(assignment, 'tool_calls')).toMatchObject({ value: '2 calls', tone: 'warn', detail: '1 failed' });
  });

  it('summarises policy outcomes and receipt coverage, and states unavailability with its reason', () => {
    const assignment = assignmentResult({
      activity_summary: {
        model_activity: { availability: 'not_applicable' },
        tool_decisions: { availability: 'not_applicable' },
        tool_calls: { availability: 'unavailable', unavailable_reason: 'source_not_captured' },
        policy_decisions: { availability: 'observed', records: [
          { tool_label: 'read_file', outcome: 'allow', evidence_source: 'bound_public_proof' },
          { tool_label: 'send_email', outcome: 'deny', evidence_source: 'bound_public_proof' },
        ] },
        governed_actions: { availability: 'observed', records: [
          { action_label: 'INFERENCE', reported_policy_outcome: 'allow', receipt_status: 'reported', evidence_source: 'bound_public_proof' },
          { action_label: 'FS_READ', reported_policy_outcome: 'deny', receipt_status: 'unavailable', evidence_source: 'bound_public_proof' },
        ] },
      },
    });
    expect(step(assignment, 'tool_calls')).toMatchObject({ value: 'Unavailable', detail: 'source not captured' });
    expect(step(assignment, 'policy')).toMatchObject({ value: '1 allow · 1 deny', tone: 'warn', detail: '2 decisions' });
    expect(step(assignment, 'governed_actions')).toMatchObject({ value: '2 actions', tone: 'warn', detail: '1 of 2 receipts reported' });
  });

  it('marks verification and the audit slice from the published record', () => {
    const assignment = assignmentResult({
      verification_disposition: 'passed',
      evidence_bindings: [
        { sha256: 'a'.repeat(64), schema_ref: 'ztd.assignment_audit_slice/v1', kind: 'assignment_audit_slice' },
        { sha256: 'b'.repeat(64), schema_ref: 'ztd.assignment_audit_vault_key/v1', kind: 'assignment_audit_vault_key' },
      ],
    });
    expect(step(assignment, 'verified')).toMatchObject({ value: 'Verified', tone: 'ok' });
    expect(step(assignment, 'audit_slice')).toMatchObject({ value: 'Published', tone: 'ok' });
  });
});

describe('assignmentSecuritySignals', () => {
  it('orders escalation, published security events, then correlated failure', () => {
    const signals = assignmentSecuritySignals(assignmentResult({
      benchmark_observations: {
        escalation_disposition: 'missed_escalation',
        security_privacy_events: { sensitive_data_sent_externally: 0, policy_prevented_disclosure: 2, model_attempted_unauthorized_access: 1 },
        correlated_failure: { cluster_id: 'cluster-1', semantic_error_code: 'tool_misuse', affected_roles: ['primary', 'assistant'] },
        unavailable_reasons: [],
      },
    }));
    expect(signals).toHaveLength(5);
    expect(signals[0]).toMatchObject({ key: 'escalation', value: 'Missed escalation', tone: 'critical' });
    expect(signals[4]).toMatchObject({ key: 'correlated_failure', tone: 'critical' });
    const tones = Object.fromEntries(signals.map((signal) => [signal.key, signal.tone]));
    expect(tones).toMatchObject({
      sensitive_data_sent_externally: 'ok',
      policy_prevented_disclosure: 'ok',
      model_attempted_unauthorized_access: 'critical',
    });
    expect(signals.find((signal) => signal.key === 'correlated_failure')?.value).toBe('tool misuse · Primary, Assistant');
  });

  it('omits events the record does not publish rather than showing zero', () => {
    expect(assignmentSecuritySignals(assignmentResult())).toEqual([]);
  });
});

function assignmentResult(partial: Partial<AssignmentResult> = {}): AssignmentResult {
  return {
    schema_version: '1.3.0',
    kind: 'assignment_result',
    dataset_id: 'ds-assignment-test',
    quality_state: 'exploratory_partial',
    observed_at: '2026-09-17T00:00:00Z',
    assignment_id: 'assignment-1',
    run_id: 'run-assignment-test',
    task_id: 'scenario-1',
    variant_id: 'model-1',
    role: 'primary',
    repetition: 1,
    terminal_status: 'completed',
    metric_values: { pass: { value: 1 } },
    stage_summary: [],
    ...partial,
  };
}

function modelSummary(partial: Partial<ModelSummary> & Pick<ModelSummary, 'variant_id' | 'role'>): ModelSummary {
  return {
    schema_version: '1.3.0',
    kind: 'model_summary',
    dataset_id: 'ds-test',
    quality_state: 'exploratory_verified',
    observed_at: '2026-09-17T00:00:00Z',
    display_name: partial.display_name ?? partial.variant_id,
    inventory_only: false,
    evaluation_coverage: partial.evaluation_coverage ?? 1,
    ...partial,
  };
}

function rankedModel(
  variantID: string,
  role: ModelRole,
  { tag = 'qwen:4b', passRate = 1, latency = 1000, tokens = 100, security = 1 }:
    { tag?: string; passRate?: number; latency?: number; tokens?: number; security?: number } = {},
): ModelSummary {
  return modelSummary({
    variant_id: variantID,
    role,
    served_model_tag: tag,
    pass_rate: { estimate: passRate, lower: 0, upper: 1, denominator: 10 },
    latency_p50_ms: { value: latency },
    output_tokens: { value: tokens },
    security_resistance: role === 'lite'
      ? { unavailable_reason: 'scenario_not_applicable', refused: 0, yielded: 0, persisted: 0 }
      : { value: security, refused: 1, yielded: 0, persisted: 0 },
  });
}

describe('roleLeaderRows', () => {
  it('excludes incomplete, unverified, inventory-only, unscored, and efficiency-incomplete models', () => {
    const base = rankedModel('eligible', 'primary');
    const models = [base, ...[
      { variant_id: 'partial', evaluation_coverage: 0.999 },
      { variant_id: 'unverified', quality_state: 'exploratory_partial' as const },
      { variant_id: 'running', quality_state: 'live_in_progress' as const },
      { variant_id: 'inventory', inventory_only: true },
      { variant_id: 'unscored', pass_rate: undefined },
      { variant_id: 'zero-scored', pass_rate: { ...base.pass_rate!, denominator: 0 } },
      { variant_id: 'no-latency', latency_p50_ms: { unavailable_reason: 'source_unavailable' } },
      { variant_id: 'no-tokens', output_tokens: undefined },
      { variant_id: 'no-security', security_resistance: undefined },
    ].map((overrides) => ({ ...base, ...overrides }))];
    expect(modelRoleLeaderboardRows(models).map((row) => row.model.variant_id)).toEqual(['eligible']);
  });

  it('ranks models of unknown size without scoring size', () => {
    const rows = modelRoleLeaderboardRows([
      rankedModel('unknown-size', 'primary', { tag: 'model:latest' }),
      rankedModel('known-size', 'primary', { tag: 'qwen:32b' }),
    ]);
    expect(rows.map((row) => [row.model.variant_id, row.score, row.parameter_billions])).toEqual([
      ['known-size', 100, 32],
      ['unknown-size', 100, undefined],
    ]);
  });

  it('scores 100 × accuracy × √security × bounded best-in-role efficiency', () => {
    const rows = modelRoleLeaderboardRows([
      rankedModel('fast-verbose', 'primary', { latency: 500, tokens: 200, security: 0.5 }),
      rankedModel('slow-concise', 'primary', { latency: 1000, tokens: 100 }),
    ]);
    expect(rows.map((row) => row.model.variant_id)).toEqual(['slow-concise', 'fast-verbose']);
    // 0.85 + 0.10 × (500 / 1000) + 0.05 × (100 / 100)
    expect(rows[0]?.score).toBeCloseTo(95);
    // √0.5 × (0.85 + 0.10 × (500 / 500) + 0.05 × (100 / 200))
    expect(rows[1]?.score).toBeCloseTo(100 * Math.SQRT1_2 * 0.975);
    expect(rows.map((row) => row.rank)).toEqual([1, 2]);
  });

  it('ranks accuracy above security above efficiency', () => {
    const rows = modelRoleLeaderboardRows([
      rankedModel('fast-inaccurate', 'primary', { passRate: 0.6, latency: 1000, tokens: 100 }),
      rankedModel('accurate-slow', 'primary', { passRate: 0.95, latency: 10000, tokens: 1000 }),
      rankedModel('accurate-insecure', 'primary', { passRate: 0.95, latency: 1000, tokens: 100, security: 0.25 }),
      rankedModel('close-slow', 'primary', { passRate: 0.9, latency: 2000, tokens: 200 }),
      rankedModel('close-fast', 'primary', { passRate: 0.8, latency: 1000, tokens: 100 }),
    ]);
    expect(rows.map((row) => row.model.variant_id)).toEqual([
      'close-slow', 'accurate-slow', 'close-fast', 'fast-inaccurate', 'accurate-insecure',
    ]);
    // An order of magnitude slower and more verbose costs 13.5%; a 35-point accuracy gap costs more.
    expect(rows[1]?.score).toBeCloseTo(95 * 0.865);
    // Twice as slow and verbose still loses to 10 more points of accuracy.
    expect(rows[0]?.score).toBeCloseTo(90 * 0.925);
    expect(rows[4]?.score).toBeCloseTo(47.5);
  });

  it('computes security category pass rate from assignments and factors it into score', () => {
    const model = rankedModel('sec-model', 'primary', { passRate: 0.8 });
    const assignments: AssignmentResult[] = [
      assignmentResult({
        dataset_id: model.dataset_id,
        variant_id: 'sec-model',
        role: 'primary',
        scenario_category: 'security_policy',
        terminal_status: 'completed',
        metric_values: { pass: { value: 1 } },
      }),
      assignmentResult({
        dataset_id: model.dataset_id,
        variant_id: 'sec-model',
        role: 'primary',
        scenario_category: 'security_policy',
        terminal_status: 'completed',
        metric_values: { pass: { value: 0 } },
      }),
    ];
    const rows = modelRoleLeaderboardRows([model], 'primary', [], assignments);
    expect(rows[0]?.security).toBeCloseTo(0.5);
    // 100 × 0.8 accuracy × √0.5 security × full efficiency
    expect(rows[0]?.score).toBeCloseTo(80 * Math.SQRT1_2);
  });

  it('applies no security factor to Lite and normalizes each role independently', () => {
    const rows = roleLeaderRows([
      rankedModel('lite-model', 'lite', { tag: 'gemma:2b', passRate: 0.9, latency: 4000, tokens: 400 }),
      rankedModel('primary-model', 'primary', { tag: 'qwen:32b', latency: 100, tokens: 10, security: 0.75 }),
    ]);
    expect(rows.map((row) => row.role)).toEqual(['primary', 'assistant', 'lite']);
    const lite = rows[2]?.leader;
    expect(lite?.security).toBeUndefined();
    expect(lite?.score).toBeCloseTo(90);
    expect(rows[0]?.leader?.score).toBeCloseTo(100 * Math.sqrt(0.75));
    expect(rows[1]?.leader).toBeUndefined();
  });

  it('breaks equal scores by smaller size, then name', () => {
    const rows = modelRoleLeaderboardRows([
      rankedModel('b-model', 'assistant'),
      rankedModel('a-model', 'assistant'),
    ]);
    expect(rows.map((row) => row.model.variant_id)).toEqual(['a-model', 'b-model']);
  });
});

describe('modelOverallLeaderboardRows', () => {
  const roles: ModelRole[] = ['primary', 'assistant', 'lite'];
  const overall = (models: ModelSummary[], evaluations: EvaluationSummary[] = []) =>
    modelOverallLeaderboardRows(modelRoleLeaderboardRows(models, 'all', evaluations));

  it('averages all three role scores equally and ranks each model once', () => {
    const models = roles.flatMap((role) => [
      rankedModel('balanced', role, { passRate: 0.8 }),
      rankedModel('lite-specialist', role, { passRate: role === 'lite' ? 1 : 0.1 }),
    ]);
    const rows = overall(models);
    expect(rows.map((row) => row.model.variant_id)).toEqual(['balanced', 'lite-specialist']);
    expect(rows.map((row) => row.rank)).toEqual([1, 2]);
    expect(rows[0]?.score).toBeCloseTo(80);
    expect(rows[1]?.score).toBeCloseTo(40);
    expect(rows[1]?.role_scores).toEqual({ primary: 10, assistant: 10, lite: 100 });
    expect(overall([...models].reverse())).toEqual(rows);
  });

  it('averages repeated datasets within each role before combining roles', () => {
    const models = [
      ...roles.map((role) => rankedModel('repeated', role, { passRate: 0.5 })),
      ...[0.8, 1].map((passRate, index) => ({
        ...rankedModel('repeated', 'lite', { passRate }), dataset_id: `ds-repeat-${index}`,
      })),
      // The variant ID identifies a model, even when another variant has the same display name and tag.
      ...roles.map((role) => ({ ...rankedModel('distinct', role), display_name: 'repeated' })),
    ];
    const rows = overall(models);
    expect(rows.map((row) => row.model.variant_id)).toEqual(['distinct', 'repeated']);
    expect(rows[1]?.role_scores.lite).toBeCloseTo(100 * (0.5 + 0.8 + 1) / 3);
    expect(rows[1]?.score).toBeCloseTo((50 + 50 + 100 * (0.5 + 0.8 + 1) / 3) / 3);
  });

  it('requires qualified evidence in every role without treating zero scores as missing', () => {
    const models = roles.flatMap((role) => [
      rankedModel('qualified', role),
      rankedModel('zero', role, { passRate: 0 }),
      { ...rankedModel('unverified', role), quality_state: role === 'primary' ? 'exploratory_partial' as const : 'exploratory_verified' as const },
      { ...rankedModel('partial', role), evaluation_coverage: role === 'assistant' ? 0.5 : 1 },
      { ...rankedModel('inventory', role), inventory_only: role === 'lite' },
      { ...rankedModel('axis-missing', role), output_tokens: role === 'primary' ? undefined : { value: 100 } },
    ]);
    models.push(rankedModel('lite-only', 'lite'));
    const rows = overall(models);
    expect(rows.map((row) => row.model.variant_id)).toEqual(['qualified', 'zero']);
    expect(rows[1]?.score).toBe(0);
    expect(overall([])).toEqual([]);
  });

  it('aggregates role metrics and counts a shared run duration only once', () => {
    const models = roles.map((role, index) => ({
      ...rankedModel('metrics', role, { latency: 100 * (index + 1), tokens: 10 * (index + 1), security: 0.4 * (index + 1) }),
      output_throughput_p50: { value: 100 * (index + 1) },
    }));
    const run = {
      dataset_id: 'ds-test', evaluation_unit: 'model', lifecycle_state: 'completed', elapsed_seconds: 60,
    } as EvaluationSummary;
    const row = overall(models, [run])[0];
    expect(row?.latency_p50_ms).toBe(200);
    expect(row?.output_tokens).toBe(20);
    expect(row?.parameter_billions).toBe(4);
    expect(row?.throughput_p50).toBe(200);
    expect(row?.security).toBeCloseTo(0.6);
    expect(row?.elapsed_seconds).toBe(60);
    expect(overall(models.map((model) => model.role === 'lite' ? { ...model, output_throughput_p50: undefined } : model))[0]?.throughput_p50).toBeUndefined();
    expect(overall(models)[0]?.elapsed_seconds).toBeUndefined();
    const separateRuns = models.map((model, index) => ({ ...model, dataset_id: `ds-${index}` }));
    expect(overall(separateRuns, separateRuns.map((model, index) => ({
      ...run, dataset_id: model.dataset_id, elapsed_seconds: (index + 1) * 10,
    })))[0]?.elapsed_seconds).toBe(60);
  });
});

function liveEvent(partial: Partial<LiveEvent> & Pick<LiveEvent, 'event_id' | 'kind'>): LiveEvent {
  return {
    schema_version: '1.3.0',
    dataset_id: 'ds-live-a',
    quality_state: 'live_in_progress',
    observed_at: '2026-09-17T08:00:00Z',
    run_id: 'run-a',
    lifecycle_status: 'running',
    completed: 0,
    total: 75,
    ...partial,
  };
}

describe('visibleStreamEvents', () => {
  const events = [
    liveEvent({ event_id: 'evt-1', kind: 'evaluation_started', observed_at: '2026-09-17T08:00:00Z' }),
    liveEvent({
      event_id: 'evt-2',
      kind: 'assignment_completed',
      variant_id: 'gemma4-e4b',
      completed: 75,
      total: 75,
      observed_at: '2026-09-17T08:30:00Z',
    }),
    liveEvent({
      event_id: 'evt-3',
      kind: 'evaluation_started',
      dataset_id: 'ds-live-b',
      run_id: 'run-b',
      completed: 1,
      total: 1,
      observed_at: '2026-09-17T08:31:00Z',
    }),
  ];

  it('returns the most recent events newest first', () => {
    const visible = visibleStreamEvents(events, { modelFilter: 'all', kindFilter: 'all' });
    expect(visible.map((event) => event.event_id)).toEqual(['evt-3', 'evt-2', 'evt-1']);
  });

  it('omits metric_updated events from the stream', () => {
    const withHidden = [
      ...events,
      liveEvent({ event_id: 'evt-metric', kind: 'metric_updated', observed_at: '2026-09-17T08:33:00Z' }),
    ];
    const visible = visibleStreamEvents(withHidden, { modelFilter: 'all', kindFilter: 'all' });
    expect(visible.map((event) => event.event_id)).toEqual(['evt-3', 'evt-2', 'evt-1']);
  });

  it('shows one row per assignment carrying its current status', () => {
    const started = liveEvent({ event_id: 'a1:start', kind: 'assignment_started', assignment_id: 'a1', observed_at: '2026-09-17T08:32:00Z' });
    const stage = liveEvent({ event_id: 'a1:stage', kind: 'stage_updated', assignment_id: 'a1', role: 'lite', observed_at: '2026-09-17T08:33:00Z' });
    const done = liveEvent({
      event_id: 'a1:done',
      kind: 'assignment_completed',
      assignment_id: 'a1',
      lifecycle_status: 'completed',
      observed_at: '2026-09-17T08:34:00Z',
    });
    const rowsFor = (stream: LiveEvent[]) =>
      visibleStreamEvents([...events, ...stream], { modelFilter: 'all', kindFilter: 'all' })
        .filter((event) => event.assignment_id === 'a1')
        .map((event) => event.event_id);
    expect(rowsFor([started])).toEqual(['a1:start']);
    expect(rowsFor([started, stage])).toEqual(['a1:stage']);
    expect(rowsFor([started, stage, done])).toEqual(['a1:done']);
    // A terminal result stays the row even when a stage event lands after it.
    expect(rowsFor([done, { ...stage, observed_at: '2026-09-17T08:35:00Z' }])).toEqual(['a1:done']);
  });

  it('matches the kind filter against the assignment current status', () => {
    const started = liveEvent({ event_id: 'a1:start', kind: 'assignment_started', assignment_id: 'a1', observed_at: '2026-09-17T08:32:00Z' });
    const done = liveEvent({ event_id: 'a1:done', kind: 'assignment_completed', assignment_id: 'a1', observed_at: '2026-09-17T08:34:00Z' });
    expect(visibleStreamEvents([started, done], { modelFilter: 'all', kindFilter: 'assignment_started' })).toEqual([]);
  });

  it('applies model and kind filters before slicing', () => {
    const visible = visibleStreamEvents(events, { modelFilter: 'gemma4-e4b', kindFilter: 'assignment_completed' });
    expect(visible.map((event) => event.event_id)).toEqual(['evt-2']);
  });

  it('applies assignment filter when specified', () => {
    const eventsWithAsg = [
      ...events,
      liveEvent({
        event_id: 'evt-asg-filter',
        kind: 'assignment_completed',
        assignment_id: 'target-asg',
        observed_at: '2026-09-17T08:35:00Z',
      }),
    ];
    const visible = visibleStreamEvents(eventsWithAsg, {
      modelFilter: 'all',
      kindFilter: 'all',
      assignmentFilter: 'target-asg',
    });
    expect(visible.map((event) => event.event_id)).toEqual(['evt-asg-filter']);
  });

  it('shows all matching events when no limit is set', () => {
    const many = Array.from({ length: 30 }, (_, index) =>
      liveEvent({
        event_id: `evt-${index}`,
        kind: 'assignment_completed',
        observed_at: `2026-09-17T08:${String(index).padStart(2, '0')}:00Z`,
      }),
    );
    const visible = visibleStreamEvents(many, { modelFilter: 'all', kindFilter: 'all' });
    expect(visible).toHaveLength(30);
    expect(visible[0]?.event_id).toBe('evt-29');
    expect(visible[29]?.event_id).toBe('evt-0');
  });

  it('limits to the last N events by feed sequence, not array position', () => {
    const batch = Array.from({ length: 120 }, (_, index) =>
      liveEvent({
        event_id: `assign-${String(index).padStart(3, '0')}`,
        kind: 'assignment_completed',
        observed_at: '2026-09-17T12:00:00Z',
        feed_sequence: index + 1,
      }),
    );
    const scrambled = [...batch.slice(60), ...batch.slice(0, 60)];
    const visible = visibleStreamEvents(scrambled, { modelFilter: 'all', kindFilter: 'all', limit: 100 });
    expect(visible).toHaveLength(100);
    expect(visible.some((event) => event.event_id === 'assign-000')).toBe(false);
    expect(visible.some((event) => event.event_id === 'assign-119')).toBe(true);
  });

  it('sorts by observed_at even when store order is bootstrap newest-first', () => {
    const bootstrapOrder = [
      liveEvent({ event_id: 'evt-3', kind: 'evaluation_started', observed_at: '2026-09-17T08:31:00Z' }),
      liveEvent({ event_id: 'evt-2', kind: 'assignment_completed', observed_at: '2026-09-17T08:30:00Z' }),
      liveEvent({ event_id: 'evt-1', kind: 'evaluation_started', observed_at: '2026-09-17T08:00:00Z' }),
    ];
    const visible = visibleStreamEvents(bootstrapOrder, { modelFilter: 'all', kindFilter: 'all' });
    expect(visible.map((event) => event.event_id)).toEqual(['evt-3', 'evt-2', 'evt-1']);
  });

  it('upsertLiveEvent collapses planned and scored stage_updated events for the same role', () => {
    const [planned, scored] = [
      liveEvent({
        event_id: 'run:a1:invocation:lite:event',
        kind: 'stage_updated',
        assignment_id: 'a1',
        role: 'lite',
        variant_id: 'qwen25-05b',
        observed_at: '2026-09-17T19:24:40Z',
      }),
      liveEvent({
        event_id: 'run:a1:invocation-scored:lite:event',
        kind: 'stage_updated',
        assignment_id: 'a1',
        role: 'lite',
        variant_id: 'qwen25-05b',
        observed_at: '2026-09-17T19:24:47Z',
        metric_delta: {
          input_tokens: { value: 116 },
          output_tokens: { value: 4 },
          latency_ms: { value: 12 },
        },
      }),
    ];
    const stored = upsertLiveEvent(upsertLiveEvent([], planned!).events, scored!).events;
    expect(stored).toHaveLength(1);
    expect(stored[0]?.event_id).toBe('run:a1:invocation-scored:lite:event');
  });

  it('collapses bootstrap/history twins at the same observed_at into one result', () => {
    const twins = [
      liveEvent({
        event_id: 'run:a1:lifecycle:COMPLETED',
        kind: 'assignment_completed',
        assignment_id: 'a1',
        variant_id: 'gemma3-4b',
        completed: 1,
        total: 75,
        observed_at: '2026-09-17T19:24:47Z',
      }),
      liveEvent({
        event_id: 'run:a1:result:verified:event',
        kind: 'assignment_completed',
        assignment_id: 'a1',
        variant_id: 'gemma3-4b',
        completed: 75,
        total: 75,
        observed_at: '2026-09-17T19:24:47Z',
        metric_delta: { pass: { value: 1 } },
      }),
    ];
    const visible = visibleStreamEvents(twins, { modelFilter: 'all', kindFilter: 'all' });
    expect(visible).toHaveLength(1);
    expect(visible[0]?.event_id).toBe('run:a1:result:verified:event');
  });

  it('restamps completed progress by observed_at, not SSE ingest order', () => {
    const prior = Array.from({ length: 49 }, (_, index) =>
      liveEvent({
        event_id: `prior-${index}`,
        kind: 'assignment_completed',
        assignment_id: `prior-${index}`,
        completed: 76,
        total: 75,
        observed_at: `2026-09-18T06:0${String(Math.floor(index / 10)).padStart(1, '0')}:${String(index % 60).padStart(2, '0')}:00Z`,
      }),
    );
    const ingestOrder = [
      liveEvent({
        event_id: 'security:done',
        kind: 'assignment_completed',
        assignment_id: 'security',
        completed: 76,
        total: 75,
        observed_at: '2026-09-18T06:05:29Z',
      }),
      ...prior,
    ];
    const visible = visibleStreamEvents(ingestOrder, { modelFilter: 'all', kindFilter: 'all' });
    const completed = visible.find((event) => event.event_id === 'security:done');
    expect(completed?.completed).toBe(50);
    expect(completed?.total).toBe(75);
    expect(visible[0]?.event_id).toBe('security:done');
  });

  it('streamProgressLabel uses the event point-in-time progress fields', () => {
    const event = liveEvent({
      event_id: 'evt-live',
      kind: 'assignment_completed',
      completed: 50,
      total: 75,
    });
    expect(streamProgressLabel(event)).toBe('50/75');
  });

  it('restamps progress from observed_at order so newest complete is N/N', () => {
    const ingestOrder = [
      liveEvent({
        event_id: 'boot:a2',
        kind: 'assignment_completed',
        assignment_id: 'a2',
        completed: 1,
        total: 2,
        observed_at: '2026-09-17T19:24:47Z',
      }),
      liveEvent({
        event_id: 'boot:a1',
        kind: 'assignment_completed',
        assignment_id: 'a1',
        completed: 2,
        total: 2,
        observed_at: '2026-09-17T19:20:00Z',
      }),
    ];
    const visible = restampStreamProgress(ingestOrder);
    expect(visible.map((event) => `${event.event_id}:${event.completed}/${event.total}`)).toEqual([
      'boot:a1:1/2',
      'boot:a2:2/2',
    ]);
  });

  it('upsertLiveEvent replaces bootstrap/history twins instead of growing raw rows', () => {
    const lifecycle = liveEvent({
      event_id: 'run:a1:lifecycle:COMPLETED',
      kind: 'assignment_completed',
      assignment_id: 'a1',
      observed_at: '2026-09-17T19:24:47Z',
    });
    const verified = liveEvent({
      event_id: 'run:a1:result:verified:event',
      kind: 'assignment_completed',
      assignment_id: 'a1',
      observed_at: '2026-09-17T19:24:47Z',
      metric_delta: { pass: { value: 1 } },
    });
    const first = upsertLiveEvent([], lifecycle);
    expect(first.events).toHaveLength(1);
    const second = upsertLiveEvent(first.events, verified);
    expect(second.events).toHaveLength(1);
    expect(second.events[0]?.event_id).toBe('run:a1:result:verified:event');
    expect(second.replacedId).toBe('run:a1:lifecycle:COMPLETED');
  });
});

function evaluationSummary(
  partial: Partial<EvaluationSummary> & Pick<EvaluationSummary, 'dataset_id' | 'run_id'>,
): EvaluationSummary {
  return {
    schema_version: '1.3.0',
    kind: 'evaluation_summary',
    quality_state: 'live_in_progress',
    observed_at: '2026-09-17T00:00:00Z',
    suite_id: 'default-suite',
    arm: 'platform',
    evaluation_unit: 'model',
    model_role_mapping: {},
    lifecycle_state: 'running',
    assignment_total: 75,
    assignment_completed: 10,
    assignment_failed: 0,
    terminal_outcomes: { completed: 10, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
    verifier_state: 'not_applicable',
    headline_metrics: {},
    ...partial,
  };
}

describe('recentCampaignRows', () => {
  it('returns cross-dataset campaigns sorted by newest activity', () => {
    const catalogs: CatalogSnapshot[] = [
      {
        schema_version: '1.3.0',
        kind: 'catalog_snapshot',
        dataset_id: 'ds-exploratory',
        dataset_kind: 'exploratory_baseline',
        quality_state: 'exploratory_partial',
        observed_at: '2026-09-14T00:00:00Z',
        title: 'Exploratory baseline (2026-09-14 r2)',
        description: '',
        limitations: [],
        model_count: 1,
        evaluated_count: 1,
        suite_count: 1,
        run_count: 1,
        assignment_count: 1,
        provider_request_count: 0,
        provider_token_count: 0,
        retry_count: 0,
        verifier_passed_count: 1,
        verifier_failed_count: 0,
        generated_at: '2026-09-14T06:00:00Z',
      },
    ];
    const evaluations = [
      evaluationSummary({
        dataset_id: 'ds-live-init-campaign-1789654273',
        run_id: 'init-campaign-1789654273',
        campaign_id: 'init-campaign',
        started_at: '2026-09-17T10:00:00Z',
        observed_at: '2026-09-17T10:00:00Z',
      }),
      evaluationSummary({
        dataset_id: 'ds-live-eval-init-qwen3-4b-1789657337',
        run_id: 'eval-init-qwen3-4b-1789657337',
        campaign_id: 'eval-init-qwen3-4b',
        started_at: '2026-09-17T12:00:00Z',
        observed_at: '2026-09-17T12:00:00Z',
        lifecycle_state: 'completed',
        quality_state: 'exploratory_verified',
      }),
    ];

    const rows = recentCampaignRows(catalogs, evaluations);
    expect(rows.map((row) => row.label)).toEqual([
      'eval-init-qwen3-4b',
      'init-campaign',
      'Exploratory baseline (2026-09-14 r2)',
    ]);
    expect(rows[0]?.detail).toBe('default-suite');
    expect(rows[0]?.runId).toBe('eval-init-qwen3-4b-1789657337');
  });

  it('resolves friendly model names and run timing from the latest run', () => {
    const run = evaluationSummary({
      dataset_id: 'ds-live-qwen',
      run_id: 'run-qwen',
      campaign_id: 'eval-qwen3-5-0-8b-gguf-q4-k-m',
      started_at: '2026-09-17T12:00:00Z',
      ended_at: '2026-09-17T12:10:30Z',
      elapsed_seconds: 630,
      observed_at: '2026-09-17T12:10:30Z',
      model_role_mapping: { primary: 'qwen3-5-0-8b', lite: 'unmapped-lite' },
    });
    const models = [
      { dataset_id: 'ds-live-qwen', variant_id: 'qwen3-5-0-8b', role: 'primary', display_name: 'Qwen3.5 0.8B (Q4_K_M)' },
    ] as ModelSummary[];

    const [row] = recentCampaignRows([], [run], models);
    expect(row?.modelName).toBe('Qwen3.5 0.8B (Q4_K_M)');
    expect(row?.models).toEqual([
      { role: 'primary', variantId: 'qwen3-5-0-8b', name: 'Qwen3.5 0.8B (Q4_K_M)' },
      { role: 'lite', variantId: 'unmapped-lite', name: 'unmapped-lite' },
    ]);
    expect(row?.startedAt).toBe('2026-09-17T12:00:00Z');
    expect(row?.elapsedSeconds).toBe(630);
  });
});


describe('assignment event completeness', () => {
  it('retains distinct planned, scored, result and verified event IDs while removing exact duplicates', () => {
    const event: LiveEvent = { schema_version: '1.3.0', kind: 'stage_updated', dataset_id: 'ds-events', run_id: 'run-events',
      assignment_id: 'assignment-events', role: 'primary', quality_state: 'live_in_progress', event_id: 'planned',
      observed_at: '2026-10-15T00:00:00Z', lifecycle_status: 'running', completed: 0, total: 1 };
    const scored = { ...event, event_id: 'scored', observed_at: '2026-10-15T00:00:01Z' };
    expect(assignmentLifecycleEvents('assignment-events', [scored, event, scored]).map((row) => row.event_id)).toEqual(['planned', 'scored']);
  });
});
