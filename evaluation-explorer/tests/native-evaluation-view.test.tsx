// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';
import type { SnapshotRecord } from '../src/contract/types';
import { NATIVE_SCENARIO_STATUSES } from '../src/contract/types';
import { fixtureEnrichedAssignmentResult } from '../src/fixtures/fixtures';
import { evalStore } from '../src/state/store';
import { EvaluationDetailView } from '../src/views/EvaluationDetailView';

const catalog = {
  schema_version: '1.3.0', kind: 'catalog_snapshot', dataset_id: 'native-core-execution-boundary', dataset_kind: 'live_run', quality_state: 'exploratory_verified', observed_at: '2026-09-15T23:38:22Z',
  title: 'Native evaluation native-run-1', description: 'Go-native core execution-boundary evaluation.', limitations: [], model_count: 0, evaluated_count: 0,
  suite_count: 1, run_count: 1, assignment_count: 2, provider_request_count: 0, provider_token_count: 0, retry_count: 0, verifier_passed_count: 1,
  verifier_failed_count: 0, generated_at: '2026-09-15T23:38:22Z',
};

const nativeEvaluation = {
  schema_version: '1.3.0', kind: 'evaluation_summary', dataset_id: 'native-core-execution-boundary', quality_state: 'exploratory_verified', observed_at: '2026-09-15T23:38:22Z',
  run_id: 'native-run-1', suite_id: 'core-execution-boundary@1.0.0', arm: 'platform', evaluation_unit: 'system', model_role_mapping: {}, lifecycle_state: 'completed',
  assignment_total: 2, assignment_completed: 2, assignment_failed: 0, terminal_outcomes: { completed: 1, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0 },
  started_at: '2026-09-15T23:38:21Z', ended_at: '2026-09-15T23:38:22Z', elapsed_seconds: 1, verifier_state: 'passed', headline_metrics: { pass_rate: { value: 1 } },
  native_result: {
    active_posture: 'doctrine', lane: 'platform', summary_status: 'pass', summary: '2/2 required invariants passed', required_verdict_count: 2, passed_verdict_count: 2,
    verification_valid: true, verification_failure_count: 0,
    scenarios: [
      { scenario_id: 'allowed-execution-occurs-once', scenario_version: '1.0.0', status: 'completed', verdicts: [{ assertion_id: 'allowed-effect-count', assertion_version: '1.0.0', status: 'pass' }] },
      { scenario_id: 'prohibited-equivalent-causes-no-additional-effect', scenario_version: '1.0.0', status: 'rejected', verdicts: [{ assertion_id: 'prohibited-no-additional-effect', assertion_version: '1.0.0', status: 'pass' }] },
    ],
    metrics: [{ metric_id: 'required-verdict-pass-rate', metric_version: '1.0.0', numerator: 2, denominator: 2, value: 1, unit: 'ratio' }],
  },
};

describe('native evaluation detail', () => {
  beforeEach(() => {
    evalStore.loadFixtures([catalog, nativeEvaluation] as unknown as SnapshotRecord[], []);
  });

  it.each(NATIVE_SCENARIO_STATUSES)('renders scenario %s with its distinct conformance tone and expandable verdicts', (status) => {
    evalStore.loadFixtures([catalog, { ...nativeEvaluation, native_result: { ...nativeEvaluation.native_result,
      scenarios: [{ ...nativeEvaluation.native_result.scenarios[0], status }],
    } }] as unknown as SnapshotRecord[], []);
    render(<MemoryRouter initialEntries={['/evaluations/native-core-execution-boundary/native-run-1']}><Routes><Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} /></Routes></MemoryRouter>);
    const scenario = screen.getByText('allowed-execution-occurs-once@1.0.0').closest('details')!;
    const expected = { completed: 'ok', rejected: 'ok', failed: 'critical', unavailable: 'neutral', unsupported: 'neutral', invalid_evidence: 'warn' };
    expect(within(scenario).getByText(status)).toHaveAttribute('data-tone', expected[status]);
    expect(scenario).not.toHaveAttribute('open');
    fireEvent.click(scenario.querySelector('summary')!);
    expect(scenario).toHaveAttribute('open');
    expect(within(scenario).getByText('allowed-effect-count@1.0.0')).toBeVisible();
  });

  it('renders native ratio and count measurements and failed independent verification', () => {
    evalStore.loadFixtures([catalog, { ...nativeEvaluation, native_result: { ...nativeEvaluation.native_result,
      verification_valid: false, verification_failure_count: 2,
      metrics: [...nativeEvaluation.native_result.metrics, { metric_id: 'effects', metric_version: '1.0.0', numerator: 3, denominator: 4, value: 3, unit: 'count' }],
    } }] as unknown as SnapshotRecord[], []);
    render(<MemoryRouter initialEntries={['/evaluations/native-core-execution-boundary/native-run-1']}><Routes><Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} /></Routes></MemoryRouter>);
    expect(screen.getByTestId('stat-required-verdict-pass-rate@1.0.0')).toHaveTextContent('100.0%');
    expect(screen.getByTestId('stat-required-verdict-pass-rate@1.0.0')).toHaveTextContent('2 / 2');
    expect(screen.getByTestId('stat-effects@1.0.0')).toHaveTextContent('3 / 4');
    expect(screen.getByText('Invalid (2 failures)')).toHaveAttribute('data-tone', 'critical');
    expect(screen.queryByRole('link', { name: 'Watch it Live' })).not.toBeInTheDocument();
  });

  it('filters campaign assignments into model verdicts and non-verdict outcomes', () => {
    const run = { ...nativeEvaluation, native_result: undefined, evaluation_unit: 'model' };
    evalStore.loadFixtures([catalog, run, ...(['completed', 'model_failed', 'grader_failed', 'running'] as const).map((status) => ({
      ...fixtureEnrichedAssignmentResult, dataset_id: run.dataset_id, run_id: run.run_id, assignment_id: `assignment-${status}`, terminal_status: status, verification_disposition: 'not_run',
    }))] as unknown as SnapshotRecord[], []);
    render(<MemoryRouter initialEntries={['/evaluations/native-core-execution-boundary/native-run-1']}><Routes><Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} /></Routes></MemoryRouter>);
    const assignments = within(screen.getByRole('region', { name: 'Assignments (4)' }));
    expect(assignments.getByText('grader failed')).toHaveAttribute('data-tone', 'warn');
    expect(assignments.getByText('grader failed')).toHaveAttribute('title', expect.stringContaining('not counted for or against it'));
    fireEvent.change(assignments.getByRole('combobox', { name: 'Filter assignments by verdict' }), { target: { value: 'verdict' } });
    expect(assignments.getByRole('link', { name: 'assignment-completed' })).toBeInTheDocument();
    expect(assignments.getByRole('link', { name: 'assignment-model_failed' })).toBeInTheDocument();
    expect(assignments.queryByRole('link', { name: 'assignment-grader_failed' })).not.toBeInTheDocument();
    fireEvent.change(assignments.getByRole('combobox', { name: 'Filter assignments by verdict' }), { target: { value: 'non-verdict' } });
    expect(assignments.getByRole('link', { name: 'assignment-grader_failed' })).toBeInTheDocument();
    expect(assignments.queryByRole('link', { name: 'assignment-completed' })).not.toBeInTheDocument();
    expect(assignments.queryByRole('link', { name: 'assignment-running' })).not.toBeInTheDocument();
  });

  it('renders the native summary, verification, scenarios, and verdicts without model claims', () => {
    render(
      <MemoryRouter initialEntries={['/evaluations/native-core-execution-boundary/native-run-1']}>
        <Routes>
          <Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText('core-execution-boundary@1.0.0')).toBeInTheDocument();
    expect(screen.getByText('2/2 required invariants passed')).toBeInTheDocument();
    expect(screen.getByText('Valid (0 failures)')).toBeInTheDocument();
    expect(screen.getByText('allowed-execution-occurs-once@1.0.0')).toBeInTheDocument();
    expect(screen.getByText('allowed-effect-count@1.0.0')).toBeInTheDocument();
    expect(screen.getByText('prohibited-equivalent-causes-no-additional-effect@1.0.0')).toBeInTheDocument();
    expect(screen.queryByText('Model-role mapping')).not.toBeInTheDocument();
  });
});
