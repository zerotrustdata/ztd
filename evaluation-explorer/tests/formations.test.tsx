// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { EvaluationSummary } from '../src/contract/types';
import { evalStore } from '../src/state/store';
import { formationRun } from './formation-fixture';
import { formationGroups, formationLeaderboardRows } from '../src/views/formations';
import { isEvaluationSummary } from '../src/contract/validators';
import { LeaderboardView } from '../src/views/LeaderboardView';
import { EvaluationsView } from '../src/views/EvaluationsView';
import { ModelsView } from '../src/views/ModelsView';
import { ModelDetailView } from '../src/views/ModelDetailView';
import { Route, Routes } from 'react-router-dom';
import { fixtureModelSummaries } from '../src/fixtures/fixtures';
import { modelRoleLeaderboardRows } from '../src/views/derived';
import { toReleaseSeries } from '../src/content/release';

function withSummary(patch: Partial<NonNullable<EvaluationSummary['formations']>[number]>, runPatch: Partial<EvaluationSummary> = {}): EvaluationSummary {
  return { ...formationRun, ...runPatch, formations: [{ ...formationRun.formations![0]!, ...patch }] };
}

describe('published formations', () => {
  it('lists current frozen formations and their whole-run results instead of static reference entries', () => {
    evalStore.loadFixtures([formationRun], []);
    render(<MemoryRouter initialEntries={['/evaluations?view=formations&release=all']}><EvaluationsView /></MemoryRouter>);
    expect(screen.getByRole('tab', { name: 'Formations (1)' })).toBeInTheDocument();
    const table = screen.getByRole('table', { name: 'Formation catalog' });
    expect(within(table).getByRole('link', { name: 'current-project-formation' })).toHaveAttribute('href', '/evaluations/ds-formation/run-formation');
    expect(table).toHaveTextContent('current-primary');
    expect(table).toHaveTextContent('75.0%');
    expect(screen.queryByText('formation-01')).not.toBeInTheDocument();
  });

  it('ranks formation aggregates even when no individual models qualify', () => {
    evalStore.loadFixtures([formationRun], []);
    render(<MemoryRouter initialEntries={['/leaderboard?release=all']}><LeaderboardView /></MemoryRouter>);
    fireEvent.click(screen.getByRole('button', { name: 'Formations 1' }));
    const row = screen.getByRole('link', { name: 'current-project-formation' }).closest('tr')!;
    expect(row).toHaveTextContent('75.0%');
    expect(row).toHaveTextContent('56.3');
    expect(row).toHaveTextContent('400');
    expect(row).toHaveTextContent('10B');
    expect(row).toHaveTextContent('4 judged assignments');
  });

  it('opens the newest rankable formation release when there are no individual model results', () => {
    evalStore.loadFixtures([formationRun], []);
    render(<MemoryRouter initialEntries={['/leaderboard']}><LeaderboardView /></MemoryRouter>);
    expect(screen.getByRole('combobox', { name: 'Filter by release' })).toHaveValue(toReleaseSeries(formationRun.release!));
    expect(screen.getByRole('button', { name: 'Formations 1' })).toBeInTheDocument();
  });

  it('does not score formation outcomes as individual-model leaderboard results', () => {
    const model = {
      ...fixtureModelSummaries[0]!, dataset_id: formationRun.dataset_id,
      quality_state: 'exploratory_verified' as const, inventory_only: false, evaluation_coverage: 1,
      parameter_count: 4_000_000_000, latency_p50_ms: { value: 1000 }, output_tokens: { value: 100 },
      pass_rate: { estimate: 1, lower: 1, upper: 1, denominator: 4 },
      security_pass_rate: { estimate: 1, lower: 1, upper: 1, denominator: 4 },
      security_resistance: { value: 1, refused: 4, yielded: 0, persisted: 0 },
    };
    expect(modelRoleLeaderboardRows([model], 'all', [])).toHaveLength(1);
    expect(modelRoleLeaderboardRows([model], 'all', [formationRun])).toHaveLength(0);
  });

  it('pools repeated runs by judged count and separates suites and frozen stack versions', () => {
    const repeated = withSummary({
      assignment_total: 12, terminal_outcomes: { ...formationRun.terminal_outcomes, completed: 12, model_failed: 0 },
      pass_rate: { estimate: 1, lower: 1, upper: 1, denominator: 12 },
    }, { dataset_id: 'ds-repeat', run_id: 'repeat' });
    const otherSuite = withSummary({}, { suite_id: 'different-suite' });
    const differentStack = withSummary({ stack_digest: 'd'.repeat(64) });
    const groups = formationGroups([formationRun, repeated, otherSuite, differentStack]);
    expect(groups).toHaveLength(3);
    const pooled = groups.find((group) => group.sources.length === 2)!;
    expect(pooled.summary.pass_rate).toMatchObject({ estimate: 15 / 16, denominator: 16 });
    expect(pooled.summary.run_seconds).toBe(120);
    expect(formationLeaderboardRows([formationRun, otherSuite]).map((row) => row.rank)).toEqual([1, 1]);
  });

  it('excludes unverified, unfinished and incomplete summaries and respects the whole-formation artifact budget', () => {
    const runs = [formationRun,
      withSummary({}, { verifier_state: 'not_run' }),
      withSummary({}, { lifecycle_state: 'running' }),
      withSummary({ assignment_total: 5 }),
      withSummary({ parameter_count: undefined }),
      withSummary({ output_tokens: { unavailable_reason: 'source_unavailable' } }),
    ];
    expect(formationLeaderboardRows(runs)).toHaveLength(1);
    expect(formationLeaderboardRows([formationRun], 6)).toHaveLength(1);
    expect(formationLeaderboardRows([formationRun], 5.99)).toHaveLength(0);
    expect(formationLeaderboardRows([withSummary({ artifact_size_bytes: undefined })], 6)).toHaveLength(0);
    expect(formationLeaderboardRows([withSummary({ security_pass_rate: undefined })])[0]!.score).toBe(0);
  });

  it('validates the typed formation boundary and rejects corrupt counts, identity, and nested fields', () => {
    expect(() => isEvaluationSummary(formationRun)).not.toThrow();
    for (const invalid of [
      withSummary({ stack_digest: 'not-a-digest' }),
      withSummary({ assignment_total: 1 }),
      withSummary({ model_role_mapping: { primary: 'one' } as never }),
      withSummary({ pass_rate: { estimate: 1, lower: 1, upper: 1, denominator: 4 } }),
      withSummary({ output_tokens: { value: -1 } }),
      withSummary({ artifact_size_bytes: -1 }),
      withSummary({ artifact_size_bytes: 1.5 }),
      withSummary({ parameter_count: Number.MAX_SAFE_INTEGER + 1 }),
      withSummary({ parameter_count: 0 }),
      { ...formationRun, formations: [formationRun.formations![0]!, formationRun.formations![0]!] },
      { ...formationRun, schema_version: '1.12.0' },
      { ...formationRun, evaluation_unit: 'model' },
      withSummary({ stack_id: 'valid', private_field: true } as never),
    ]) expect(() => isEvaluationSummary(invalid)).toThrow();
  });

  it('applies suite and search filters to the published formation list', () => {
    evalStore.loadFixtures([formationRun, withSummary({ stack_id: 'another' }, { dataset_id: 'other', run_id: 'other', suite_id: 'another-suite' })], []);
    render(<MemoryRouter initialEntries={['/evaluations?view=formations&release=all']}><EvaluationsView /></MemoryRouter>);
    expect(screen.getByRole('tab', { name: 'Formations (2)' })).toBeInTheDocument();
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search formations' }), { target: { value: 'current-primary' } });
    expect(screen.getByRole('link', { name: 'current-project-formation' })).toBeInTheDocument();
    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by suite' }), { target: { value: 'another-suite' } });
    expect(screen.queryByRole('link', { name: 'current-project-formation' })).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Formations (1)' })).toBeInTheDocument();
  });

  it('updates formations when new validated run projections arrive', () => {
    evalStore.loadFixtures([], []);
    render(<MemoryRouter initialEntries={['/evaluations?view=formations&release=all']}><EvaluationsView /></MemoryRouter>);
    expect(screen.getByRole('tab', { name: 'Formations (0)' })).toBeInTheDocument();
    act(() => evalStore.acceptProjection({ sequence: 1, record_type: 'projection', record_bytes: JSON.stringify(formationRun) }));
    expect(screen.getByRole('tab', { name: 'Formations (1)' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'current-project-formation' })).toBeInTheDocument();
    expect(evalStore.getState().errors).toHaveLength(0);
  });

  it('shows published formation membership in Models and Model Details', () => {
    const model = { ...fixtureModelSummaries[0]!, dataset_id: formationRun.dataset_id, variant_id: 'current-primary', role: 'primary' as const };
    evalStore.loadFixtures([formationRun, model], []);
    const view = render(<MemoryRouter initialEntries={['/models?release=all']}><ModelsView /></MemoryRouter>);
    expect(screen.getByRole('columnheader', { name: /Formations/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '1' })).toHaveAttribute('href', '/models/ds-formation/current-primary?role=primary#model-formations');
    view.unmount();
    render(<MemoryRouter initialEntries={['/models/ds-formation/current-primary?role=primary']}><Routes>
      <Route path="/models/:datasetId/:variantId" element={<ModelDetailView />} />
    </Routes></MemoryRouter>);
    expect(screen.getByRole('link', { name: 'current-project-formation' })).toHaveAttribute('href', '/evaluations/ds-formation/run-formation');
    expect(screen.queryByText('formation-01')).not.toBeInTheDocument();
  });
});
