// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { EvaluationSummary, ModelSummary, SnapshotRecord } from '../src/contract/types';
import { evalStore } from '../src/state/store';
import { RunsPanel } from '../src/components/RunsPanel';

const datasetId = 'ds-formation-settled';
const runId = 'run-formation-settled';

const settledFormation: EvaluationSummary = {
  schema_version: '1.4.0',
  kind: 'evaluation_summary',
  dataset_id: datasetId,
  quality_state: 'exploratory_verified',
  observed_at: '2026-10-01T12:00:00Z',
  run_id: runId,
  campaign_id: 'settled-formation',
  suite_id: 'default-suite',
  arm: 'formation',
  evaluation_unit: 'system',
  lifecycle_state: 'completed',
  run_status: 'completed',
  assignment_total: 10,
  assignment_completed: 9,
  assignment_failed: 1,
  terminal_outcomes: { completed: 5, model_failed: 4, grader_failed: 0, invalid_evidence: 1, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
  started_at: '2026-10-01T10:00:00Z',
  ended_at: '2026-10-01T11:00:00Z',
  verifier_state: 'not_applicable',
  headline_metrics: {},
  model_role_mapping: { primary: 'model-primary', assistant: 'model-assistant', lite: 'model-lite' },
} as EvaluationSummary;

function roleSummary(
  role: 'primary' | 'assistant' | 'lite',
  variantId: string,
  outcomes: { completed: number; model_failed: number; invalid_evidence: number },
  passRate: number,
): ModelSummary {
  return {
    schema_version: '1.4.0',
    kind: 'model_summary',
    dataset_id: datasetId,
    quality_state: 'exploratory_verified',
    observed_at: '2026-10-01T12:00:00Z',
    variant_id: variantId,
    display_name: variantId,
    role,
    inventory_only: false,
    evaluation_coverage: 1,
    terminal_outcomes: { grader_failed: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0, ...outcomes },
    pass_rate: { estimate: passRate, lower: 0, upper: 1, denominator: outcomes.completed + outcomes.model_failed },
  } as ModelSummary;
}

const roleSummaries: ModelSummary[] = [
  roleSummary('primary', 'model-primary', { completed: 3, model_failed: 1, invalid_evidence: 0 }, 0.75),
  roleSummary('assistant', 'model-assistant', { completed: 1, model_failed: 3, invalid_evidence: 0 }, 0.25),
  roleSummary('lite', 'model-lite', { completed: 1, model_failed: 0, invalid_evidence: 1 }, 1),
];

// Assignment records that contradict the summaries: every primary assignment failed.
const contradictingAssignments: SnapshotRecord[] = ['a1', 'a2', 'a3', 'a4'].map((id) => ({
  schema_version: '1.4.0',
  kind: 'assignment_result',
  dataset_id: datasetId,
  quality_state: 'exploratory_verified',
  observed_at: '2026-10-01T12:00:00Z',
  assignment_id: id,
  run_id: runId,
  task_id: 'task-1',
  variant_id: 'model-primary',
  role: 'primary',
  repetition: 1,
  terminal_status: 'model_failed',
  metric_values: { pass: { value: 0 } },
}) as unknown as SnapshotRecord);

function renderRunsPanel() {
  return render(<MemoryRouter><RunsPanel /></MemoryRouter>);
}

describe('RunsPanel run figures from summaries', () => {
  beforeEach(() => {
    evalStore.loadFixtures([], []);
  });

  it('renders the settled formation figures from run and model summaries with no assignments loaded', () => {
    evalStore.loadFixtures([settledFormation, ...roleSummaries] as SnapshotRecord[], []);
    expect(evalStore.getState().assignments.size).toBe(0);

    renderRunsPanel();
    const panel = within(screen.getByRole('region', { name: 'Runs' }));
    expect(panel.getByText(/Formation: 5 passed · 4 failed · 1 no verdict/)).toBeInTheDocument();
    expect(panel.getByText('75%')).toBeInTheDocument();
    expect(panel.getByText('25%')).toBeInTheDocument();
    expect(panel.getByText('100%')).toBeInTheDocument();
    expect(panel.getByRole('progressbar', { name: 'Primary outcomes' })).toHaveAttribute(
      'aria-valuetext', '3 passed, 1 failed, 0 no verdict, 0 remaining',
    );
    expect(panel.getByRole('progressbar', { name: 'Assistant outcomes' })).toHaveAttribute(
      'aria-valuetext', '1 passed, 3 failed, 0 no verdict, 0 remaining',
    );
  });

  it('ignores assignment records held in the store and renders the summary figures', () => {
    evalStore.loadFixtures([settledFormation, ...roleSummaries, ...contradictingAssignments] as SnapshotRecord[], []);

    renderRunsPanel();
    const panel = within(screen.getByRole('region', { name: 'Runs' }));
    expect(panel.getByText('75%')).toBeInTheDocument();
    expect(panel.getByRole('progressbar', { name: 'Primary outcomes' })).toHaveAttribute(
      'aria-valuetext', '3 passed, 1 failed, 0 no verdict, 0 remaining',
    );
  });
});
