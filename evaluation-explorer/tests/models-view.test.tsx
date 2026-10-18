// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { allFixtureSnapshotRecords, fixtureLiveEvents, FIXTURE_DATASET_IDS } from '../src/fixtures/fixtures';
import type { ModelSummary } from '../src/contract/types';
import { VIEW_SCHEMA_VERSION } from '../src/contract/types';
import { evalStore } from '../src/state/store';
import { ModelsView } from '../src/views/ModelsView';

describe('ModelsView', () => {
  beforeEach(() => {
    evalStore.loadFixtures(allFixtureSnapshotRecords, fixtureLiveEvents);
  });

  it('shows denominators, neutral inventory pills, and unevaluated results', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate))!;
    evalStore.loadFixtures([
      { ...source, display_name: 'Measured', variant_id: 'measured' },
      { ...source, display_name: 'Inventory', variant_id: 'inventory', inventory_only: true, pass_rate: undefined, quality_state: 'not_evaluated' },
    ], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);
    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by evaluation status' }), { target: { value: 'all' } });
    const inventory = within(screen.getByRole('link', { name: /Inventory/ }).closest('tr')!);
    expect(inventory.getByText('inventory')).toHaveAttribute('data-tone', 'neutral');
    expect(inventory.getAllByText('Not evaluated').length).toBeGreaterThan(0);
    expect(inventory.queryByText('0.0%')).not.toBeInTheDocument();
    const measured = screen.getByRole('link', { name: 'Measured' }).closest('tr')!;
    expect(measured).toHaveTextContent(`n=${source.pass_rate!.denominator}`);
    expect(screen.getByTestId('stat-inventory-only-models')).toHaveTextContent('1');
  });

  it('renders verifier-passed model summaries with a run-scoped verification label', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate));
    expect(source).toBeDefined();
    evalStore.loadFixtures([{ ...source!, quality_state: 'exploratory_verified' }], []);
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    expect(screen.getAllByTestId('quality-exploratory_verified').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Verified').length).toBeGreaterThan(0);
  });

  it('prevents incomplete coverage from rendering as verified', () => {
    const source = allFixtureSnapshotRecords.find(
      (record): record is ModelSummary => record.kind === 'model_summary' && record.variant_id === 'gemma4-12b',
    );
    expect(source).toBeDefined();
    evalStore.loadFixtures([{ ...source!, schema_version: VIEW_SCHEMA_VERSION, quality_state: 'exploratory_verified' }], []);
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by evaluation status' }), { target: { value: 'all' } });
    expect(screen.getByRole('link', { name: 'Gemma 4 12B' }).closest('tr')).toHaveTextContent('Not fully verified');
  });

  it('labels the historical Gemma 4 12B result as legacy unverified', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by evaluation status' }), { target: { value: 'all' } });
    const gemma12b = screen.getByRole('link', { name: 'Gemma 4 12B' });
    expect(gemma12b.closest('tr')).toHaveTextContent('Legacy · unverified');
  });

  it('lists models from every dataset without a dataset selector', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by evaluation status' }), { target: { value: 'all' } });
    expect(screen.queryByTestId('dataset-selector')).not.toBeInTheDocument();
    expect(screen.getAllByTitle(FIXTURE_DATASET_IDS.exploratory).length).toBeGreaterThan(0);
    expect(screen.getAllByTitle(FIXTURE_DATASET_IDS.verified).length).toBeGreaterThan(0);
    const gemmaLinks = screen.getAllByRole('link', { name: 'Gemma 4 E4B' });
    expect(gemmaLinks.map((link) => link.getAttribute('href'))).toEqual(
      expect.arrayContaining([
        `/models/${FIXTURE_DATASET_IDS.exploratory}/gemma4-e4b?role=primary`,
        `/models/${FIXTURE_DATASET_IDS.verified}/gemma4-e4b?role=primary`,
      ]),
    );
  });

  it('excludes measured models with incomplete evaluation coverage', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate));
    expect(source).toBeDefined();
    evalStore.loadFixtures([
      { ...source!, variant_id: 'fully-covered-model', display_name: 'Fully covered model', evaluation_coverage: 1 },
      { ...source!, variant_id: 'partially-covered-model', display_name: 'Partially covered model', evaluation_coverage: 0.8 },
    ], []);

    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by evaluation status' }), { target: { value: 'evaluated' } });
    expect(screen.getByRole('link', { name: 'Fully covered model' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Partially covered model' })).not.toBeInTheDocument();
  });

  it('only lists quality filter options that exist in the current catalog', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate));
    expect(source).toBeDefined();
    evalStore.loadFixtures([
      { ...source!, variant_id: 'verified-model', display_name: 'Verified model', quality_state: 'exploratory_verified' },
      { ...source!, variant_id: 'partial-model', display_name: 'Partial model', quality_state: 'exploratory_partial' },
    ], []);

    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    const qualityFilter = screen.getByRole('combobox', { name: 'Filter by quality state' });
    const labels = Array.from(qualityFilter.querySelectorAll('option')).map((option) => option.textContent);
    expect(labels).toEqual([
      'All quality states',
      'Verified',
      'Not fully verified',
    ]);
    expect(labels).not.toContain('Current-standard verified');
    expect(labels).not.toContain('Not evaluated');
  });

  it('quality filter includes verified exploratory rows without promoting partial rows', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate));
    expect(source).toBeDefined();
    evalStore.loadFixtures([
      { ...source!, variant_id: 'verified-model', display_name: 'Verified model', quality_state: 'exploratory_verified' },
      { ...source!, variant_id: 'partial-model', display_name: 'Partial model', quality_state: 'exploratory_partial' },
      { ...source!, variant_id: 'other-dataset-model', display_name: 'Other dataset model', dataset_id: FIXTURE_DATASET_IDS.verified, quality_state: 'exploratory_partial' },
    ], []);

    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by quality state' }), { target: { value: 'exploratory_verified' } });
    const catalogTable = within(screen.getByRole('table', { name: 'Model catalog' }));
    expect(catalogTable.getByRole('link', { name: 'Verified model' })).toBeInTheDocument();
    expect(catalogTable.queryByRole('link', { name: 'Partial model' })).not.toBeInTheDocument();
    expect(catalogTable.queryByRole('link', { name: 'Other dataset model' })).not.toBeInTheDocument();
    expect(screen.getByTestId('quality-exploratory_verified')).toHaveTextContent('Verified');
  });

  it('shows only fully evaluated, verified role leaders and their ranking evidence on the Models view', () => {
    const model: ModelSummary = {
      schema_version: '1.7.0',
      kind: 'model_summary',
      dataset_id: 'ds-leader',
      observed_at: '2026-10-06T00:00:00Z',
      quality_state: 'exploratory_verified',
      variant_id: 'leader',
      served_model_tag: 'qwen:4b',
      display_name: 'Qualified model',
      role: 'assistant',
      inventory_only: false,
      evaluation_coverage: 1,
      pass_rate: { estimate: 0.8, lower: 0.8, upper: 0.8, denominator: 10 },
      latency_p50_ms: { value: 1200 },
      output_tokens: { value: 150 },
      security_resistance: { value: 0.5, refused: 1, yielded: 0, persisted: 1 },
    };
    evalStore.loadFixtures(
      [
        model,
        { ...model, variant_id: 'unverified', display_name: 'Unverified model', role: 'primary', quality_state: 'exploratory_partial' },
        { ...model, variant_id: 'partial', display_name: 'Partial model', role: 'lite', evaluation_coverage: 0.5 },
        {
          schema_version: '1.3.0',
          kind: 'evaluation_summary',
          dataset_id: 'ds-leader',
          quality_state: 'live_in_progress',
          observed_at: '2026-10-06T00:00:00Z',
          run_id: 'run-leader',
          suite_id: 'eval-init-suite',
          arm: 'platform',
          evaluation_unit: 'model',
          model_role_mapping: {},
          lifecycle_state: 'completed',
          assignment_total: 75,
          assignment_completed: 10,
          assignment_failed: 0,
          terminal_outcomes: { completed: 10, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
          started_at: '2026-10-06T00:00:00Z',
          elapsed_seconds: 60,
          verifier_state: 'not_applicable',
          headline_metrics: {},
        },
      ],
      [],
    );
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );
    const panel = within(screen.getByRole('region', { name: 'Leaderboard' }));
    expect(panel.getByRole('link', { name: 'Qualified model' })).toBeInTheDocument();
    expect(panel.queryByRole('link', { name: 'Unverified model' })).not.toBeInTheDocument();
    expect(panel.queryByRole('link', { name: 'Partial model' })).not.toBeInTheDocument();
    expect(panel.getByText(/1 of 3 roles qualified/)).toBeInTheDocument();
    // Sole model in its role: every axis at full weight, so 100 × 0.8 × 0.5 = 40 points.
    expect(panel.getByText('40.0')).toBeInTheDocument();
    expect(panel.getByText('50%')).toBeInTheDocument();
    expect(panel.getByText('4B')).toBeInTheDocument();
    expect(panel.getByText('1.20 s')).toBeInTheDocument();
    for (const header of ['Points', 'Security', 'Speed', 'Tokens', 'Size', 'Eval time']) {
      expect(panel.getByRole('columnheader', { name: header })).toBeInTheDocument();
    }
    expect(panel.queryByRole('columnheader', { name: 'Pass rate' })).not.toBeInTheDocument();
    expect(panel.queryByRole('columnheader', { name: 'Coverage' })).not.toBeInTheDocument();
  });

  it('displays model capabilities, architecture specs, and supports capability filtering', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    const catalogTable = within(screen.getByRole('table', { name: 'Model catalog' }));
    expect(catalogTable.getByRole('columnheader', { name: /Architecture & specs/i })).toBeInTheDocument();
    expect(catalogTable.getByRole('columnheader', { name: /Capabilities/i })).toBeInTheDocument();
    expect(catalogTable.getByRole('columnheader', { name: /Evaluations/i })).toBeInTheDocument();

    const capabilityBar = within(screen.getByRole('group', { name: 'Filter by probed capability' }));
    expect(capabilityBar.getByRole('button', { name: /Tool Calling/i })).toBeInTheDocument();
    expect(capabilityBar.getByRole('button', { name: /Structured JSON/i })).toBeInTheDocument();
  });
});
