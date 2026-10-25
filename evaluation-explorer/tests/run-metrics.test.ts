// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { TERMINAL_STATUSES, type EvaluationSummary } from '../src/contract/types';
import { allFixtureSnapshotRecords } from '../src/fixtures/fixtures';
import { evaluationPassRate } from '../src/utils/run-metrics';

const source = allFixtureSnapshotRecords.find((record): record is EvaluationSummary =>
  record.kind === 'evaluation_summary' && !record.native_result)!;
const emptyOutcomes = { completed: 0, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 };

describe('evaluationPassRate', () => {
  it.each(TERMINAL_STATUSES.filter((status) => status !== 'completed'))('counts %s in the total', (status) => {
    const run = { ...source, assignment_total: 100, terminal_outcomes: { ...emptyOutcomes, completed: 1, [status]: 3 }, headline_metrics: { pass_rate: { value: 1 } } };
    expect(evaluationPassRate(run)).toEqual({ value: 0.25, unit: 'ratio', observed_count: 4, eligible_count: 4, unavailable_count: 0 });
  });

  it('reports zero when all terminal outcomes are provider failures', () => {
    expect(evaluationPassRate({ ...source, terminal_outcomes: { ...emptyOutcomes, provider_failed: 24 } })?.value).toBe(0);
  });

  it('remains unavailable until the first terminal outcome', () => {
    expect(evaluationPassRate({ ...source, terminal_outcomes: emptyOutcomes })).toEqual({ unavailable_reason: 'source_unavailable', unit: 'ratio', observed_count: 0, eligible_count: 0, unavailable_count: 0 });
  });

  it('preserves conformance verdict metrics', () => {
    const native: EvaluationSummary = {
      ...source,
      native_result: { active_posture: 'doctrine', lane: 'platform', summary_status: 'pass', summary: 'Passed', required_verdict_count: 2, passed_verdict_count: 2, verification_valid: true, verification_failure_count: 0, scenarios: [], metrics: [] },
      headline_metrics: { pass_rate: { value: 1 } },
    };
    expect(evaluationPassRate(native)).toBe(native.headline_metrics.pass_rate);
  });
});
