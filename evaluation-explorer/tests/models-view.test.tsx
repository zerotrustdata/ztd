// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { allFixtureSnapshotRecords, fixtureLiveEvents, FIXTURE_DATASET_IDS } from '../src/fixtures/fixtures';
import type { ModelSummary } from '../src/contract/types';
import { VIEW_SCHEMA_VERSION } from '../src/contract/types';
import { MODEL_ROLE_WIRE_ORDER } from '../src/content/roles';
import { evalStore } from '../src/state/store';
import { PREF } from '../src/state/dataset';
import { ModelsView } from '../src/views/ModelsView';

describe('ModelsView', () => {
  beforeEach(() => {
    evalStore.loadFixtures(allFixtureSnapshotRecords, fixtureLiveEvents);
  });

  it('retains the selected evidence quality while the feed reloads', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate))!;
    localStorage.setItem(PREF.filters, JSON.stringify({ search: '', role: 'all', evaluated: 'evaluated', quality: 'exploratory_verified' }));
    evalStore.loadFixtures([], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);
    act(() => evalStore.loadFixtures([
      { ...source, dataset_id: 'ds-verified', quality_state: 'exploratory_verified' },
      { ...source, dataset_id: 'ds-partial', quality_state: 'exploratory_partial' },
    ], []));
    expect(screen.getByRole('combobox', { name: 'Filter by quality state' })).toHaveValue('exploratory_verified');
    const catalog = within(screen.getByRole('table', { name: 'Model catalog' }));
    expect(catalog.getByRole('link', { name: source.display_name })).toHaveAttribute('href', `/models/ds-verified/${source.variant_id}?role=${source.role}`);
  });

  it('aggregates repeated runs and roles into one leaderboard row and counts the model once', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate))!;
    evalStore.loadFixtures(['ds-reps-one', 'ds-reps-three'].flatMap((dataset_id, index) => MODEL_ROLE_WIRE_ORDER.map((role) => ({
      ...source, variant_id: 'aggregate', display_name: 'Aggregate model', dataset_id, role,
      model_digest: index === 0 ? undefined : 'a'.repeat(64),
      quality_state: 'exploratory_verified', inventory_only: false, evaluation_coverage: 1,
      parameter_count: 4_000_000_000, latency_p50_ms: { value: 1000 }, output_tokens: { value: 100 },
      pass_rate: { estimate: index === 0 ? 0.5 : 1, lower: index === 0 ? 0.5 : 1, upper: index === 0 ? 0.5 : 1, denominator: index === 0 ? 10 : 30 },
      security_resistance: { value: 1, refused: index === 0 ? 1 : 3, yielded: 0, persisted: 0 },
    }))), []);
    render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);
    const panel = within(screen.getByRole('region', { name: 'Leaderboard' }));
    expect(panel.getAllByRole('link', { name: 'Aggregate model' })).toHaveLength(1);
    expect(panel.getByText('2 runs · 120 judged assignments')).toBeInTheDocument();
    expect(panel.getByText('87.5%')).toBeInTheDocument();
    expect(screen.getByTestId('stat-measured-models')).toHaveTextContent('1');
    fireEvent.click(panel.getByRole('button', { name: /^Primary/ }));
    expect(panel.getAllByRole('link', { name: 'Aggregate model' })).toHaveLength(1);
    expect(panel.getByText('2 runs · 40 judged assignments')).toBeInTheDocument();
    const catalog = within(screen.getByRole('table', { name: 'Model catalog' }));
    expect(catalog.getAllByRole('link', { name: 'Aggregate model' })).toHaveLength(3);
    expect(catalog.getAllByText('87.5% · n=40')).toHaveLength(3);
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

  it('combines datasets into one catalog entry per model-role without a dataset selector', () => {
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
    expect(gemmaLinks).toHaveLength(1);
    expect(gemmaLinks[0]).toHaveAttribute('href', `/models/${FIXTURE_DATASET_IDS.exploratory}/gemma4-e4b?role=primary`);
    expect(gemmaLinks[0]!.closest('tr')).toHaveTextContent('2 datasets');
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
      parameter_count: 4_000_000_000,
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
          run_seconds: 60,
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
    expect(panel.getByRole('button', { name: 'Overall 0' })).toBeInTheDocument();
    expect(panel.getByText('No models have qualified results in all three roles yet.')).toBeInTheDocument();
    fireEvent.click(panel.getByRole('button', { name: /^Assistant/ }));
    expect(panel.getByRole('link', { name: 'Qualified model' })).toBeInTheDocument();
    expect(panel.queryByRole('link', { name: 'Unverified model' })).not.toBeInTheDocument();
    expect(panel.queryByRole('link', { name: 'Partial model' })).not.toBeInTheDocument();
    expect(panel.getByText(/1 of 3 roles qualified/)).toBeInTheDocument();
    // Sole model in its role: full efficiency, so 100 × 0.8 × √0.5 ≈ 56.6.
    expect(panel.getByText('56.6')).toBeInTheDocument();
    expect(panel.getByText('80.0%')).toBeInTheDocument();
    expect(panel.getByText('50%')).toBeInTheDocument();
    expect(panel.getByText('4B')).toBeInTheDocument();
    expect(panel.getByText('1.20 s')).toBeInTheDocument();
    for (const header of ['Score', 'Accuracy', 'Security', 'Speed', 'Tokens', 'Total parameters', 'Run time']) {
      expect(panel.getByRole('columnheader', { name: header })).toBeInTheDocument();
    }
    expect(panel.queryByRole('columnheader', { name: 'Status' })).not.toBeInTheDocument();
    expect(panel.getByRole('columnheader', { name: 'Rank' })).toBeInTheDocument();
    expect(panel.getByText('1st')).toBeInTheDocument();
    expect(panel.queryByText(/Task owner · plans · delegates · synthesizes/i)).not.toBeInTheDocument();
    expect(panel.queryByText(/Bounded technical work for Primary/i)).not.toBeInTheDocument();
    expect(panel.queryByRole('columnheader', { name: 'Points' })).not.toBeInTheDocument();
    expect(panel.queryByRole('columnheader', { name: 'Pass rate' })).not.toBeInTheDocument();
    expect(panel.queryByRole('columnheader', { name: 'Coverage' })).not.toBeInTheDocument();
    expect(panel.queryByRole('link', { name: /View all models/i })).not.toBeInTheDocument();
  });

  it('scores absent security as zero in role and Overall tables while showing N/A', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate))!;
    evalStore.loadFixtures(MODEL_ROLE_WIRE_ORDER.flatMap((role): ModelSummary[] => [
      {
        ...source, role, variant_id: 'no-security', display_name: 'No security evidence',
        quality_state: 'exploratory_verified', inventory_only: false, evaluation_coverage: 1,
        parameter_count: 4_000_000_000, latency_p50_ms: { value: 1000 }, output_tokens: { value: 100 },
        pass_rate: { estimate: 1, lower: 1, upper: 1, denominator: 10 },
        security_resistance: { unavailable_reason: 'scenario_not_applicable', refused: 0, yielded: 0, persisted: 0 },
      },
      {
        ...source, role, variant_id: 'measured-security', display_name: 'Measured security',
        quality_state: 'exploratory_verified', inventory_only: false, evaluation_coverage: 1,
        parameter_count: 4_000_000_000, latency_p50_ms: { value: 1000 }, output_tokens: { value: 100 },
        pass_rate: { estimate: 0.75, lower: 0.75, upper: 0.75, denominator: 10 },
        security_resistance: { value: 1, refused: 4, yielded: 0, persisted: 0 },
      },
    ]), []);
    render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);
    const panel = within(screen.getByRole('region', { name: 'Leaderboard' }));
    const checkRows = () => {
      const missing = within(panel.getByRole('link', { name: 'No security evidence' }).closest('tr')!);
      expect(missing.getByText('0.0')).toBeInTheDocument();
      expect(missing.getByText('N/A')).toHaveAttribute('title', expect.stringContaining('scored as 0% security'));
      expect(missing.getByText('2nd')).toBeInTheDocument();
      const measured = within(panel.getByRole('link', { name: 'Measured security' }).closest('tr')!);
      expect(measured.getByText('75.0')).toBeInTheDocument();
      expect(measured.getByText('100%')).toBeInTheDocument();
      expect(measured.getByText('1st')).toBeInTheDocument();
      expect(panel.getByRole('columnheader', { name: 'Score' })).toHaveAttribute('title', expect.stringContaining('scored as 0% security'));
    };
    checkRows();
    for (const role of ['Primary', 'Assistant', 'Lite']) {
      fireEvent.click(panel.getByRole('button', { name: new RegExp(`^${role}`) }));
      checkRows();
    }
  });

  it('supports browsing leaderboard categories and sorting statistical columns', () => {
    const base: ModelSummary = {
      schema_version: '1.7.0',
      kind: 'model_summary',
      dataset_id: 'ds-leader',
      observed_at: '2026-10-06T00:00:00Z',
      quality_state: 'exploratory_verified',
      variant_id: 'model-a',
      served_model_tag: 'qwen:4b',
      parameter_count: 4_000_000_000,
      display_name: 'Model Alpha',
      role: 'primary',
      inventory_only: false,
      evaluation_coverage: 1,
      pass_rate: { estimate: 0.9, lower: 0.9, upper: 0.9, denominator: 10 },
      latency_p50_ms: { value: 500 },
      output_tokens: { value: 100 },
      security_resistance: { value: 0.8, refused: 4, yielded: 0, persisted: 1 },
    };
    const modelB: ModelSummary = {
      ...base,
      variant_id: 'model-b',
      display_name: 'Model Beta',
      role: 'primary',
      served_model_tag: 'qwen:2b',
      parameter_count: 2_000_000_000,
      pass_rate: { estimate: 0.8, lower: 0.8, upper: 0.8, denominator: 10 },
      latency_p50_ms: { value: 1000 },
      output_tokens: { value: 50 },
      security_resistance: { value: 0.9, refused: 9, yielded: 0, persisted: 1 },
    };
    const modelC: ModelSummary = {
      ...base,
      variant_id: 'model-c',
      display_name: 'Model Gamma',
      role: 'assistant',
      served_model_tag: 'qwen:1b',
      parameter_count: 1_000_000_000,
      pass_rate: { estimate: 0.95, lower: 0.95, upper: 0.95, denominator: 10 },
      latency_p50_ms: { value: 200 },
      output_tokens: { value: 80 },
      security_resistance: { value: 0.7, refused: 7, yielded: 0, persisted: 3 },
    };
    evalStore.loadFixtures(
      [base, modelB, modelC].flatMap((model) => MODEL_ROLE_WIRE_ORDER.map((role) => ({ ...model, role }))),
      [],
    );

    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    const panel = within(screen.getByRole('region', { name: 'Leaderboard' }));

    // 1. All 3 models visible under Overall category
    expect(panel.getByRole('link', { name: 'Model Alpha' })).toBeInTheDocument();
    expect(panel.getByRole('link', { name: 'Model Beta' })).toBeInTheDocument();
    expect(panel.getByRole('link', { name: 'Model Gamma' })).toBeInTheDocument();
    expect(panel.getByText('1st')).toBeInTheDocument();
    expect(panel.getByText('2nd')).toBeInTheDocument();
    expect(panel.getByText('3rd')).toBeInTheDocument();

    // 2. Switch to Primary category
    fireEvent.click(panel.getByRole('button', { name: /^Primary/ }));
    expect(panel.getByRole('link', { name: 'Model Alpha' })).toBeInTheDocument();
    expect(panel.getByRole('link', { name: 'Model Beta' })).toBeInTheDocument();
    expect(panel.getByRole('link', { name: 'Model Gamma' })).toHaveAttribute('href', '/models/ds-leader/model-c?role=primary');

    // 3. Switch to Assistant category
    fireEvent.click(panel.getByRole('button', { name: /^Assistant/ }));
    expect(panel.getByRole('link', { name: 'Model Gamma' })).toBeInTheDocument();
    expect(panel.getByRole('link', { name: 'Model Alpha' })).toHaveAttribute('href', '/models/ds-leader/model-a?role=assistant');
    expect(panel.getByRole('link', { name: 'Model Beta' })).toHaveAttribute('href', '/models/ds-leader/model-b?role=assistant');

    // 4. Return to Overall and sort by Accuracy
    fireEvent.click(panel.getByRole('button', { name: /^Overall/ }));
    const accuracyBtn = panel.getByRole('button', { name: 'Sort by Accuracy' });
    fireEvent.click(accuracyBtn);
    const tableRows = panel.getAllByRole('row');
    // First data row (index 1) should be Model Gamma with highest accuracy (95.0%)
    expect(within(tableRows[1]!).getByRole('link', { name: 'Model Gamma' })).toBeInTheDocument();

    // Toggle Accuracy sort direction to ascending
    fireEvent.click(accuracyBtn);
    const rowsAsc = panel.getAllByRole('row');
    // Lowest accuracy is Model Beta (80.0%)
    expect(within(rowsAsc[1]!).getByRole('link', { name: 'Model Beta' })).toBeInTheDocument();

    // 5. Sort by Speed (faster is better -> lowest latency first, ranked 1st)
    const speedBtn = panel.getByRole('button', { name: 'Sort by Speed' });
    fireEvent.click(speedBtn);
    const rowsSpeed = panel.getAllByRole('row');
    // Fastest is Model Gamma (200 ms) and gets 1st rank
    expect(within(rowsSpeed[1]!).getByRole('link', { name: 'Model Gamma' })).toBeInTheDocument();
    expect(within(rowsSpeed[1]!).getByText('1st')).toBeInTheDocument();

    // 6. Sort by Total parameters (smaller is better -> smallest model first, ranked 1st)
    const sizeBtn = panel.getByRole('button', { name: 'Sort by Total parameters' });
    fireEvent.click(sizeBtn);
    const rowsSize = panel.getAllByRole('row');
    // Smallest is Model Gamma (1B) and gets 1st rank, then Beta (2B), then Alpha (4B)
    expect(within(rowsSize[1]!).getByRole('link', { name: 'Model Gamma' })).toBeInTheDocument();
    expect(within(rowsSize[1]!).getByText('1st')).toBeInTheDocument();
    expect(within(rowsSize[2]!).getByRole('link', { name: 'Model Beta' })).toBeInTheDocument();
    expect(within(rowsSize[2]!).getByText('2nd')).toBeInTheDocument();
    expect(within(rowsSize[3]!).getByRole('link', { name: 'Model Alpha' })).toBeInTheDocument();
    expect(within(rowsSize[3]!).getByText('3rd')).toBeInTheDocument();

    // 7. Sort by Tokens (smaller is better -> fewest output tokens first, ranked 1st)
    const tokensBtn = panel.getByRole('button', { name: 'Sort by Tokens' });
    fireEvent.click(tokensBtn);
    const rowsTokens = panel.getAllByRole('row');
    // Fewest tokens is Model Beta (50 tok) and gets 1st rank, then Gamma (80 tok), then Alpha (100 tok)
    expect(within(rowsTokens[1]!).getByRole('link', { name: 'Model Beta' })).toBeInTheDocument();
    expect(within(rowsTokens[1]!).getByText('1st')).toBeInTheDocument();
    expect(within(rowsTokens[2]!).getByRole('link', { name: 'Model Gamma' })).toBeInTheDocument();
    expect(within(rowsTokens[2]!).getByText('2nd')).toBeInTheDocument();
    expect(within(rowsTokens[3]!).getByRole('link', { name: 'Model Alpha' })).toBeInTheDocument();
    expect(within(rowsTokens[3]!).getByText('3rd')).toBeInTheDocument();

    // 8. Verify Status column is absent and role descriptions are not rendered
    expect(panel.queryByRole('columnheader', { name: 'Status' })).not.toBeInTheDocument();
    expect(panel.queryByText(/Task owner · plans · delegates · synthesizes/i)).not.toBeInTheDocument();
    fireEvent.click(panel.getByRole('button', { name: 'Sort by Rank' }));
    expect(within(panel.getAllByRole('row')[1]!).getByText('1st')).toBeInTheDocument();
  });

  it('ranks one row per model by the mean of all three role scores instead of its Lite score', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate))!;
    const records = MODEL_ROLE_WIRE_ORDER.flatMap((role) => [
      {
        ...source,
        variant_id: 'balanced', display_name: 'Balanced model', served_model_tag: 'qwen:4b', role,
        quality_state: 'exploratory_verified' as const, evaluation_coverage: 1,
        pass_rate: { estimate: 0.8, lower: 0.8, upper: 0.8, denominator: role === 'lite' ? 10 : 100 },
        latency_p50_ms: { value: 100 }, output_tokens: { value: 100 },
        security_resistance: { value: 1, refused: 10, yielded: 0, persisted: 0 },
      },
      {
        ...source,
        variant_id: 'lite-specialist', display_name: 'Lite specialist', served_model_tag: 'qwen:4b', role,
        quality_state: 'exploratory_verified' as const, evaluation_coverage: 1,
        pass_rate: { estimate: role === 'lite' ? 1 : 0.1, lower: 0, upper: 1, denominator: role === 'lite' ? 1000 : 10 },
        latency_p50_ms: { value: 100 }, output_tokens: { value: 100 },
        security_resistance: { value: 1, refused: 10, yielded: 0, persisted: 0 },
      },
    ]);
    evalStore.loadFixtures(records, []);
    render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);

    const panel = within(screen.getByRole('region', { name: 'Leaderboard' }));
    expect(panel.getByRole('button', { name: 'Overall 2' })).toBeInTheDocument();
    expect(panel.getAllByRole('row')).toHaveLength(3);
    expect(panel.getAllByRole('link', { name: 'Balanced model' })).toHaveLength(1);
    expect(panel.queryByRole('columnheader', { name: 'Role' })).not.toBeInTheDocument();
    const [balanced, specialist] = panel.getAllByRole('row').slice(1).map((row) => within(row));
    expect(balanced!.getByRole('link', { name: 'Balanced model' })).toHaveAttribute(
      'href', `/models/${source.dataset_id}/balanced`,
    );
    expect(balanced!.getByText('80.0')).toBeInTheDocument();
    expect(specialist!.getByRole('link', { name: 'Lite specialist' })).toBeInTheDocument();
    expect(specialist!.getByText('40.0')).toBeInTheDocument();

    fireEvent.click(panel.getByRole('button', { name: /^Lite/ }));
    expect(panel.getByRole('columnheader', { name: 'Role' })).toBeInTheDocument();
    expect(within(panel.getAllByRole('row')[1]!).getByRole('link', { name: 'Lite specialist' })).toBeInTheDocument();
    fireEvent.click(panel.getByRole('button', { name: 'Sort by Role' }));
    fireEvent.click(panel.getByRole('button', { name: /^Overall/ }));
    expect(panel.getByRole('columnheader', { name: 'Score' })).toHaveAttribute('aria-sort', 'descending');
    expect(within(panel.getAllByRole('row')[1]!).getByRole('link', { name: 'Balanced model' })).toBeInTheDocument();
  });

  it('displays model capabilities, architecture specs, and supports capability filtering', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    const catalogTable = within(screen.getByRole('table', { name: 'Model catalog' }));
    expect(catalogTable.getByRole('columnheader', { name: /Model specifications/i })).toBeInTheDocument();
    expect(catalogTable.getByRole('columnheader', { name: /Capabilities/i })).toBeInTheDocument();
    expect(catalogTable.getByRole('columnheader', { name: /Evaluations/i })).toBeInTheDocument();

    const capabilityBar = within(screen.getByRole('group', { name: 'Filter by probed capability' }));
    expect(capabilityBar.getByRole('button', { name: /Tool Calling/i })).toBeInTheDocument();
    expect(capabilityBar.getByRole('button', { name: /Structured JSON/i })).toBeInTheDocument();
  });

  it('filters deployment storage budgets using reported artifact bytes, including the leaderboard', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate))!;
    const common = { ...source, quality_state: 'exploratory_verified' as const, evaluation_coverage: 1 };
    evalStore.loadFixtures([
      { ...common, variant_id: 'fits', display_name: 'Fits storage budget', artifact_size_bytes: 4 * 1024 ** 3, quantization_weight_class: 'Q8_0' },
      { ...common, variant_id: 'too-large', display_name: 'Exceeds storage budget', artifact_size_bytes: 8 * 1024 ** 3 },
      { ...common, variant_id: 'unknown', display_name: 'Unknown artifact size' },
    ], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);
    const catalog = within(screen.getByRole('table', { name: 'Model catalog' }));
    expect(catalog.getByRole('link', { name: 'Unknown artifact size' })).toBeInTheDocument();
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Maximum artifact size in GiB' }), { target: { value: '4' } });
    expect(catalog.getByRole('link', { name: 'Fits storage budget' }).closest('tr')).toHaveTextContent('4.00 GiB');
    expect(catalog.getByRole('link', { name: 'Fits storage budget' }).closest('tr')).toHaveTextContent('Q8_0');
    expect(screen.queryByRole('link', { name: 'Exceeds storage budget' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Unknown artifact size' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Maximum artifact size in GiB' }), { target: { value: '' } });
    expect(catalog.getByRole('link', { name: 'Unknown artifact size' })).toBeInTheDocument();
  });
});
