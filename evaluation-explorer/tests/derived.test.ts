// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { NATIVE_RESULT_STATUSES, NATIVE_SCENARIO_STATUSES, QUALITY_STATES, TERMINAL_STATUSES, VERIFIER_STATES } from '../src/contract/types';
import type {
  AssignmentResult,
  CatalogSnapshot,
  EvaluationSummary,
  LiveEvent,
  ModelSummary,
  ProviderEnvironment,
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
  comparisonCompatibility,
  recentCampaignRows,
  roleLabel,
  roleLeaderRows,
  modelRoleLeaderboardRows,
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
  const expected = { verified_public: 'ok', exploratory_verified: 'info', exploratory_partial: 'warn', legacy_unverified: 'warn', live_in_progress: 'info', terminal_failed: 'critical', dead_evidence: 'critical', not_evaluated: 'neutral', unavailable: 'neutral' };
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
          { action_label: 'governed action', reported_policy_outcome: 'allow', receipt_status: 'reported', evidence_source: 'bound_public_proof' },
          { action_label: 'governed action', reported_policy_outcome: 'deny', receipt_status: 'unavailable', evidence_source: 'bound_public_proof' },
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

describe('roleLeaderRows', () => {
  it('excludes incomplete, unverified, inventory-only, and unscored models', () => {
    const base = modelSummary({ variant_id: 'eligible', role: 'primary',
      pass_rate: { estimate: 0.8, lower: 0.8, upper: 0.8, denominator: 10 } });
    const models = [base, ...[
      { variant_id: 'partial', evaluation_coverage: 0.999 },
      { variant_id: 'unverified', quality_state: 'exploratory_partial' as const },
      { variant_id: 'running', quality_state: 'live_in_progress' as const },
      { variant_id: 'inventory', inventory_only: true },
      { variant_id: 'unscored', pass_rate: undefined },
      { variant_id: 'zero-scored', pass_rate: { ...base.pass_rate!, denominator: 0 } },
    ].map((overrides) => ({ ...base, ...overrides }))];
    expect(modelRoleLeaderboardRows(models).map((row) => row.model.variant_id)).toEqual(['eligible']);
  });

  it('breaks score ties by smaller parameter size, then faster completed run', () => {
    const models = [
      ['large', 'qwen:8b', 10, 0.8],
      ['small-slow', 'gemma:e4b', 120, 0.8],
      ['small-fast', 'qwen:4b', 60, 0.8],
      ['best-score', 'qwen:32b', 300, 0.9],
      ['unknown-size', 'model:latest', 1, 0.8],
      ['unknown-time', 'qwen:4b', undefined, 0.8],
      ['tiny', 'model:500m', 200, 0.8],
    ].map(([id, tag, , score]) => modelSummary({
      variant_id: id as string, dataset_id: id as string, role: 'primary', served_model_tag: tag as string,
      pass_rate: { estimate: score as number, lower: 0, upper: 1, denominator: 10 },
    }));
    const evaluations = models.map((model) => ({
      ...evaluationSummary({ dataset_id: model.dataset_id, run_id: model.dataset_id }), lifecycle_state: 'completed' as const,
      elapsed_seconds: ({ large: 10, 'small-slow': 120, 'small-fast': 60, 'best-score': 300,
        'unknown-size': 1, tiny: 200 } as Record<string, number>)[model.variant_id],
    }));
    expect(modelRoleLeaderboardRows(models, 'all', evaluations).map((row) => row.model.variant_id))
      .toEqual(['best-score', 'tiny', 'small-fast', 'small-slow', 'unknown-time', 'large', 'unknown-size']);
  });

  it('returns one leader per role ordered Primary, Assistant, Lite', () => {
    const models = [
      modelSummary({
        variant_id: 'qwen3-4b',
        role: 'lite',
        pass_rate: { estimate: 0.82, lower: 0.82, upper: 0.82, denominator: 18 },
        evaluation_coverage: 1,
      }),
      modelSummary({
        variant_id: 'qwen3-4b',
        role: 'assistant',
        pass_rate: { estimate: 0.77, lower: 0.77, upper: 0.77, denominator: 18 },
        evaluation_coverage: 1,
      }),
      modelSummary({
        variant_id: 'qwen3-4b',
        role: 'primary',
        pass_rate: { estimate: 0.78, lower: 0.78, upper: 0.78, denominator: 18 },
        evaluation_coverage: 1,
      }),
    ];
    const rows = roleLeaderRows(models);
    expect(rows.map((row) => row.role)).toEqual(['primary', 'assistant', 'lite']);
    expect(rows.every((row) => row.leader?.model.variant_id === 'qwen3-4b')).toBe(true);
    expect(rows[0]?.leader?.pass_rate).toBe(0.78);
  });

  it('picks the highest pass-rate model per role', () => {
    const models = [
      modelSummary({
        variant_id: 'model-a',
        role: 'primary',
        pass_rate: { estimate: 0.6, lower: 0.6, upper: 0.6, denominator: 10 },
      }),
      modelSummary({
        variant_id: 'model-b',
        role: 'primary',
        pass_rate: { estimate: 0.9, lower: 0.9, upper: 0.9, denominator: 10 },
      }),
    ];
    const primary = roleLeaderRows(models).find((row) => row.role === 'primary');
    expect(primary?.leader?.model.variant_id).toBe('model-b');
  });
});

const TEST_ENVIRONMENT: ProviderEnvironment = {
  source: 'declared',
  processor: 'test-cpu',
  memory: '64 GiB',
  graphics: 'test-gpu',
};

function datasetCatalog(datasetId: string, environment?: ProviderEnvironment): CatalogSnapshot {
  return {
    schema_version: '1.5.0',
    kind: 'catalog_snapshot',
    dataset_id: datasetId,
    dataset_kind: 'live_run',
    quality_state: 'live_in_progress',
    observed_at: '2026-09-17T00:00:00Z',
    title: datasetId,
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
    verifier_passed_count: 0,
    verifier_failed_count: 0,
    generated_at: '2026-09-17T00:00:00Z',
    ...(environment ? { provider_environment: environment } : {}),
  };
}

describe('comparisonCompatibility', () => {
  const alpha = modelSummary({ variant_id: 'model-a', role: 'primary', dataset_id: 'ds-a' });
  const beta = modelSummary({ variant_id: 'model-b', role: 'primary', dataset_id: 'ds-b' });
  const catalogs = [datasetCatalog('ds-a', TEST_ENVIRONMENT), datasetCatalog('ds-b', { ...TEST_ENVIRONMENT })];
  const runs = [
    evaluationSummary({ dataset_id: 'ds-a', run_id: 'run-a' }),
    evaluationSummary({ dataset_id: 'ds-b', run_id: 'run-b' }),
  ];

  it('accepts models from one dataset without consulting environment or suites', () => {
    const sibling = modelSummary({ variant_id: 'model-c', role: 'lite', dataset_id: 'ds-a' });
    expect(comparisonCompatibility([alpha, sibling], [], [])).toEqual({ compatible: true, crossDataset: false });
  });

  it('accepts separate datasets that share a provider environment and suites', () => {
    expect(comparisonCompatibility([alpha, beta], catalogs, runs)).toEqual({
      compatible: true,
      crossDataset: true,
      environmentSource: 'declared',
    });
  });

  it('reports an observed environment as observed', () => {
    const observed = { ...TEST_ENVIRONMENT, source: 'observed' as const };
    const result = comparisonCompatibility(
      [alpha, beta],
      [datasetCatalog('ds-a', observed), datasetCatalog('ds-b', { ...observed })],
      runs,
    );
    expect(result).toEqual({ compatible: true, crossDataset: true, environmentSource: 'observed' });
  });

  it('treats a suite set that differs only in run count as the same suites', () => {
    const extra = [...runs, evaluationSummary({ dataset_id: 'ds-b', run_id: 'run-b-2' })];
    expect(comparisonCompatibility([alpha, beta], catalogs, extra)).toMatchObject({
      compatible: true,
      crossDataset: true,
    });
  });

  it('fails closed when a dataset does not declare its provider environment', () => {
    const result = comparisonCompatibility([alpha, beta], [catalogs[0]!, datasetCatalog('ds-b')], runs);
    expect(result).toEqual({
      compatible: false,
      reason: 'a selected dataset does not declare its provider environment',
    });
  });

  it('rejects datasets whose provider environments differ in any field', () => {
    for (const changed of [
      { source: 'observed' },
      { processor: 'other-cpu' },
      { memory: '32 GiB' },
      { graphics: 'other-gpu' },
      { storage: 'nvme' },
      { system_type: 'workstation' },
    ] as Partial<ProviderEnvironment>[]) {
      const result = comparisonCompatibility(
        [alpha, beta],
        [catalogs[0]!, datasetCatalog('ds-b', { ...TEST_ENVIRONMENT, ...changed })],
        runs,
      );
      expect(result, JSON.stringify(changed)).toEqual({
        compatible: false,
        reason: 'the datasets ran on different provider environments',
      });
    }
  });

  it('fails closed when a dataset has no evaluation suite', () => {
    const result = comparisonCompatibility([alpha, beta], catalogs, [runs[0]!]);
    expect(result).toEqual({ compatible: false, reason: 'a selected dataset has no evaluation suite' });
  });

  it('rejects datasets that evaluated different suites', () => {
    const result = comparisonCompatibility(
      [alpha, beta],
      catalogs,
      [runs[0]!, evaluationSummary({ dataset_id: 'ds-b', run_id: 'run-b', suite_id: 'other-suite' })],
    );
    expect(result).toEqual({ compatible: false, reason: 'the datasets evaluated different suites' });
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
        quality_state: 'verified_public',
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
