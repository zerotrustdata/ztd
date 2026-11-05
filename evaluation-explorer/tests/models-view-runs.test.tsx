// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { EvaluationSummary, ModelSummary, SnapshotRecord } from '../src/contract/types';
import { evalStore } from '../src/state/store';
import { PREF } from '../src/state/dataset';
import { ModelsView } from '../src/views/ModelsView';

const observedAt = '2026-10-01T12:00:00Z';

function evaluation(datasetId: string, runId: string, modelRoleMapping?: EvaluationSummary['model_role_mapping']): EvaluationSummary {
  return {
    schema_version: '1.4.0',
    kind: 'evaluation_summary',
    dataset_id: datasetId,
    quality_state: 'exploratory_verified',
    observed_at: observedAt,
    run_id: runId,
    campaign_id: runId,
    suite_id: 'default-suite',
    arm: 'homogeneous-model-role',
    lifecycle_state: 'completed',
    run_status: 'completed',
    assignment_total: 2,
    assignment_completed: 2,
    assignment_failed: 0,
    terminal_outcomes: { completed: 2, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
    started_at: observedAt,
    ended_at: observedAt,
    verifier_state: 'not_applicable',
    headline_metrics: {},
    model_role_mapping: modelRoleMapping,
  } as EvaluationSummary;
}

function modelSummary(datasetId: string, variantId: string, passRate?: number): ModelSummary {
  return {
    schema_version: '1.4.0',
    kind: 'model_summary',
    dataset_id: datasetId,
    quality_state: 'exploratory_verified',
    observed_at: observedAt,
    variant_id: variantId,
    display_name: variantId,
    role: 'primary',
    inventory_only: false,
    evaluation_coverage: 1,
    terminal_outcomes: { completed: 2, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
    pass_rate: passRate === undefined ? undefined : { estimate: passRate, lower: 0, upper: 1, denominator: 2 },
  } as ModelSummary;
}

function renderModels() {
  return render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);
}

describe('ModelsView runs per variant', () => {
  beforeEach(() => {
    localStorage.removeItem(PREF.filters);
    evalStore.loadFixtures([], []);
  });

  it('counts runs from evaluation role mappings and from the dataset of model summaries with no assignments loaded', () => {
    evalStore.loadFixtures([
      evaluation('ds-mapped', 'run-mapped', { primary: 'variant-mapped' }),
      // Runs that name no variant are attributed to the model summaries of their dataset.
      evaluation('ds-linked', 'run-linked-one'),
      evaluation('ds-linked', 'run-linked-two'),
      modelSummary('ds-mapped', 'variant-mapped', 0.5),
      modelSummary('ds-linked', 'variant-linked'),
    ] as SnapshotRecord[], []);
    expect(evalStore.getState().assignments.size).toBe(0);

    renderModels();
    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by evaluation status' }), { target: { value: 'all' } });
    const catalog = within(screen.getByRole('table', { name: 'Model catalog' }));
    const mappedRow = catalog.getByRole('link', { name: 'variant-mapped' }).closest('tr')!;
    const linkedRow = catalog.getByRole('link', { name: 'variant-linked' }).closest('tr')!;
    expect(within(mappedRow).getByRole('link', { name: /eval/ })).toHaveTextContent('1 eval →');
    expect(within(linkedRow).getByRole('link', { name: /eval/ })).toHaveTextContent('2 evals →');
    expect(screen.getByTestId('stat-runs-covered')).toHaveTextContent('3');
  });
});
