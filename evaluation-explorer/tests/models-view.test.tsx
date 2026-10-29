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
    localStorage.removeItem(PREF.filters);
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

  it('aggregates repeated runs per role in the catalog and does not render the leaderboard panel', () => {
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
    expect(screen.queryByRole('region', { name: 'Leaderboard' })).not.toBeInTheDocument();
    expect(screen.queryByRole('math')).not.toBeInTheDocument();
    expect(screen.getByTestId('stat-measured-models')).toHaveTextContent('1');
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

  it('shows scored coverage beside a perfect score with provider failures', () => {
    const source = allFixtureSnapshotRecords.find((record): record is ModelSummary => record.kind === 'model_summary' && Boolean(record.pass_rate))!;
    evalStore.loadFixtures([{
      ...source, variant_id: 'one-scored', display_name: 'One scored task', role: 'lite',
      pass_rate: { estimate: 1, lower: 1, upper: 1, denominator: 1 },
      evaluation_coverage: 1,
      terminal_outcomes: { completed: 1, model_failed: 0, provider_failed: 14, invalid_evidence: 0, grader_failed: 0, stopped: 0, execution_failed: 0, escalated: 0 },
    }], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><ModelsView /></MemoryRouter>);
    const row = within(screen.getByRole('link', { name: 'One scored task' }).closest('tr')!);
    const coverage = row.getByLabelText('Scored coverage');
    expect(coverage).toHaveTextContent('1/15 scored');
    expect(coverage).toHaveTextContent('14 provider failures');
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

  it('filters deployment storage budgets using reported artifact bytes', () => {
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
