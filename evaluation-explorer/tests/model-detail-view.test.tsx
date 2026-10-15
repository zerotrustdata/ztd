// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';
import { fixtureModelSummaries, fixtureEvaluationSummaries, fixtureEnrichedAssignmentResult } from '../src/fixtures/fixtures';
import { evalStore } from '../src/state/store';
import { ModelDetailView } from '../src/views/ModelDetailView';

const model = { ...fixtureModelSummaries[0]!, terminal_outcomes: { ...fixtureModelSummaries[0]!.terminal_outcomes!, grader_failed: 1 } };
const run = { ...fixtureEvaluationSummaries[0]!, dataset_id: model.dataset_id };
function renderModel() {
  return render(<MemoryRouter initialEntries={[`/models/${model.dataset_id}/${model.variant_id}?role=${model.role}`]}><Routes><Route path="/models/:datasetId/:variantId" element={<ModelDetailView />} /></Routes></MemoryRouter>);
}

describe('ModelDetailView', () => {
  beforeEach(() => evalStore.loadFixtures([model, run,
    { ...fixtureEnrichedAssignmentResult, dataset_id: model.dataset_id, variant_id: model.variant_id, role: model.role, run_id: run.run_id, assignment_id: 'model-assignment', terminal_status: 'grader_failed', verification_disposition: 'not_run' },
  ], []));

  it('shows measured identity, verdict denominators, outcome separation, runs and assignment tones', () => {
    renderModel();
    const hero = screen.getByRole('heading', { level: 1, name: model.display_name }).closest('header')!;
    expect(hero).toHaveTextContent(`n=${model.pass_rate!.denominator}`);
    const outcomes = within(screen.getByRole('region', { name: 'Terminal outcomes' }));
    expect(outcomes.getByTestId('outcome-breakdown')).toBeInTheDocument();
    expect(outcomes.getByText(/not counted for or against it/)).toBeInTheDocument();
    const assignment = screen.getByRole('link', { name: /model-assignment/ });
    expect(assignment).toHaveAttribute('href', `/evaluations/${model.dataset_id}/${run.run_id}/assignments/model-assignment`);
    expect(assignment.closest('li')?.querySelector('.sibling-status')).toHaveClass('sibling-status-warn');
    const runs = within(screen.getByRole('region', { name: 'Evaluation runs involving this model' }));
    expect(runs.getByRole('link', { name: run.run_id })).toBeInTheDocument();
    expect(runs.getByText(run.lifecycle_state)).toHaveAttribute('data-tone');
  });

  it('renders an unmeasured model as Not evaluated without inventing a zero pass rate', () => {
    evalStore.loadFixtures([{ ...model, pass_rate: undefined, inventory_only: true, terminal_outcomes: undefined, quality_state: 'not_evaluated' }, run], []);
    renderModel();
    const hero = screen.getByRole('heading', { level: 1 }).closest('header')!;
    expect(within(hero.querySelector('.evidence-verdict')!).getByText('Not evaluated', { exact: true })).toHaveAttribute('data-tone', 'neutral');
    expect(hero).not.toHaveTextContent('0.0%');
    expect(screen.queryByRole('region', { name: /Assignments/ })).not.toBeInTheDocument();
  });
});
