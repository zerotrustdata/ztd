// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { EvaluationSummary, SnapshotRecord } from '../src/contract/types';
import { evalStore } from '../src/state/store';
import { EvaluationDetailView } from '../src/views/EvaluationDetailView';
import { EvaluationsView } from '../src/views/EvaluationsView';
import { FORMATION_CATALOG } from '../src/content/framework-catalog';

function evaluation(
  datasetId: string,
  runId: string,
  startedAt: string,
): EvaluationSummary {
  return {
    schema_version: '1.3.0',
    kind: 'evaluation_summary',
    dataset_id: datasetId,
    quality_state: 'live_in_progress',
    observed_at: startedAt,
    run_id: runId,
    suite_id: 'eval-init-suite',
    arm: 'platform',
    evaluation_unit: 'model',
    model_role_mapping: {},
    lifecycle_state: 'running',
    assignment_total: 75,
    assignment_completed: 10,
    assignment_failed: 0,
    terminal_outcomes: { completed: 10, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
    started_at: startedAt,
    verifier_state: 'not_applicable',
    headline_metrics: {},
  };
}

describe('EvaluationsView', () => {
  afterEach(() => vi.unstubAllGlobals());
  beforeEach(() => {
    evalStore.loadFixtures(
      [
        evaluation('ds-live-init-campaign-1789654273', 'init-campaign-1789654273', '2026-09-17T10:00:00Z'),
        evaluation('ds-live-eval-init-qwen3-4b-1789657337', 'eval-init-qwen3-4b-1789657337', '2026-09-17T12:00:00Z'),
      ] as unknown as SnapshotRecord[],
      [],
    );
  });

  it('shows one section at a time: runs by default, then campaigns and formations tabs', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <EvaluationsView />
      </MemoryRouter>,
    );

    expect(screen.getByRole('tab', { name: 'Runs (2)' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('table', { name: 'Model campaigns' })).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Campaigns' })).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Formations' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: /Campaigns/ }));
    const campaignsPanel = within(screen.getByRole('region', { name: 'Campaigns' }));
    expect(campaignsPanel.getByText('Genesis Homogeneous Model-Role Campaign')).toBeInTheDocument();
    expect(screen.queryByRole('table', { name: 'Model campaigns' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: /Formations/ }));
    const formationsPanel = within(screen.getByRole('region', { name: 'Formations' }));
    expect(formationsPanel.getByText('Qwen Powerhouse')).toBeInTheDocument();
    expect(formationsPanel.getByText('DeepSeek Reasoning Tower')).toBeInTheDocument();
    expect(formationsPanel.getByText('Gemma Cascade')).toBeInTheDocument();
    expect(formationsPanel.getByText('Llama Meta Stack')).toBeInTheDocument();
    expect(formationsPanel.getByText('Ultra-Efficient Speedster')).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Filter by status' })).not.toBeInTheDocument();

    fireEvent.change(screen.getByRole('combobox', { name: 'Filter formations by kind' }), { target: { value: 'curated' } });
    expect(formationsPanel.getByTestId('formation-qwen-powerhouse')).toBeInTheDocument();
    expect(formationsPanel.queryByTestId('formation-heterogeneous-0000')).not.toBeInTheDocument();
  });

  it('sorts campaign progress and numeric pass rates while preserving filtered run links', () => {
    evalStore.loadFixtures([
      { ...evaluation('ds-alpha', 'run-alpha', '2026-10-06T10:00:00Z'), campaign_id: 'Alpha', assignment_total: 100,
        assignment_completed: 90, headline_metrics: { pass_rate: { value: 0.95 } } },
      { ...evaluation('ds-beta', 'run-beta', '2026-10-06T12:00:00Z'), campaign_id: 'Beta', assignment_total: 10,
        assignment_completed: 2, headline_metrics: { pass_rate: { value: 0.8 } } },
    ] as EvaluationSummary[], []);
    render(<MemoryRouter initialEntries={['/?release=all&view=campaigns']}><EvaluationsView /></MemoryRouter>);
    const table = screen.getByRole('table', { name: 'Campaigns' });
    const campaignOrder = () => within(table).getAllByRole('row').slice(1).map((row) => within(row).getByRole('link').textContent);
    fireEvent.click(within(table).getByRole('button', { name: 'Sort by Campaign' }));
    expect(campaignOrder()).toEqual(['Alpha', 'Beta']);
    expect(within(table).getByRole('columnheader', { name: /Campaign/ })).toHaveAttribute('aria-sort', 'ascending');
    fireEvent.click(within(table).getByRole('button', { name: 'Sort by Campaign' }));
    expect(campaignOrder()).toEqual(['Beta', 'Alpha']);
    fireEvent.click(within(table).getByRole('button', { name: 'Sort by Progress' }));
    const progressSort = within(table).getByRole('columnheader', { name: /Progress/ }).getAttribute('aria-sort');
    expect(campaignOrder()).toEqual(progressSort === 'ascending' ? ['Beta', 'Alpha'] : ['Alpha', 'Beta']);
    expect(within(table).getByRole('link', { name: 'Alpha' })).toHaveAttribute('href', '/evaluations/ds-alpha/run-alpha');
    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by status' }), { target: { value: 'completed' } });
    expect(screen.queryByRole('table', { name: 'Campaigns' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by status' }), { target: { value: 'all' } });

    fireEvent.click(screen.getByRole('tab', { name: /Runs/ }));
    const runs = screen.getByRole('table', { name: 'Model campaigns' });
    fireEvent.click(within(runs).getByRole('button', { name: 'Sort by Pass rate' }));
    const passRateSort = within(runs).getByRole('columnheader', { name: /Pass rate/ }).getAttribute('aria-sort');
    const runOrder = within(runs).getAllByRole('row').slice(1).map((row) => within(row).getByRole('link').textContent);
    expect(runOrder).toEqual(passRateSort === 'ascending' ? ['run-beta', 'run-alpha'] : ['run-alpha', 'run-beta']);
  });

  it('sorts formations by numeric VRAM, searches models, and keeps tables on mobile', () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
    render(<MemoryRouter initialEntries={['/?release=all']}><EvaluationsView /></MemoryRouter>);
    expect(screen.getByRole('table', { name: 'Model campaigns' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: /Campaigns/ }));
    expect(screen.getByRole('table', { name: 'Campaigns' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: /Formations/ }));
    const table = screen.getByRole('table', { name: 'Execution formations' });
    fireEvent.click(within(table).getByRole('button', { name: 'Sort by Total VRAM' }));
    const direction = within(table).getByRole('columnheader', { name: /Total VRAM/ }).getAttribute('aria-sort');
    const expected = [...FORMATION_CATALOG].sort((a, b) => direction === 'ascending' ? a.total_vram_mib - b.total_vram_mib : b.total_vram_mib - a.total_vram_mib);
    const ids = within(table).getAllByTestId(/^formation-/).map((cell) => cell.dataset.testid);
    expect(ids).toEqual(expected.map((formation) => `formation-${formation.id}`));
    const qwen = within(table).getByTestId('formation-qwen-powerhouse').closest('tr')!;
    expect(within(qwen).getAllByRole('link')).toHaveLength(3);
    expect(qwen).toHaveTextContent('MiB');
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search formations' }), { target: { value: 'Qwen Powerhouse' } });
    expect(within(table).getAllByRole('row')).toHaveLength(2);
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search formations' }), { target: { value: 'no-such-formation' } });
    expect(screen.getByText('No formations match these filters.')).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Sort cards' })).not.toBeInTheDocument();
  });

  it('counts filtered evidence and separates conformance from campaigns with verifier tones', () => {
    const campaign = { ...evaluation('ds-campaign', 'campaign', '2026-10-06T00:00:00Z'), verifier_state: 'failed', verifier_failure_summary: 'Digest mismatch' } as EvaluationSummary;
    const native: EvaluationSummary = { ...evaluation('ds-native', 'conformance', '2026-10-06T00:00:00Z'), verifier_state: 'passed', assignment_total: 2,
      native_result: { active_posture: 'doctrine', lane: 'platform', summary_status: 'invalid_evidence', summary: 'Invalid evidence', required_verdict_count: 2, passed_verdict_count: 1, verification_valid: false, verification_failure_count: 1, scenarios: [], metrics: [] } };
    evalStore.loadFixtures([campaign, native], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><EvaluationsView /></MemoryRouter>);
    expect(screen.getByRole('tab', { name: 'Runs (2)' })).toBeInTheDocument();
    expect(screen.getByTestId('stat-assignments')).toHaveTextContent('77');
    expect(screen.getByTestId('stat-verifier-passed')).toHaveTextContent('1');
    expect(screen.getByTestId('stat-verifier-failed')).toHaveTextContent('1');
    const conformance = within(screen.getByRole('region', { name: 'Governance conformance' }));
    expect(conformance.getByText('invalid_evidence')).toHaveAttribute('data-tone', 'warn');
    expect(conformance.getByText('1/2 verdicts passed')).toBeInTheDocument();
    expect(conformance.getByText('passed')).toHaveAttribute('data-tone', 'ok');
    const campaigns = within(screen.getByRole('region', { name: 'Model campaigns' }));
    expect(campaigns.getByTitle('Digest mismatch')).toHaveAttribute('data-tone', 'critical');
    fireEvent.change(screen.getByRole('combobox', { name: 'Filter by status' }), { target: { value: 'stopped' } });
    expect(screen.getByRole('tab', { name: 'Runs (0)' })).toBeInTheDocument();
    expect(screen.queryByTestId('stat-assignments')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Reset filters' }));
    expect(screen.getByRole('tab', { name: 'Runs (2)' })).toBeInTheDocument();
  });

  it('only lists quality filter options that exist in the current runs', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <EvaluationsView />
      </MemoryRouter>,
    );

    const qualityFilter = screen.getByRole('combobox', { name: 'Filter by quality' });
    const labels = Array.from(qualityFilter.querySelectorAll('option')).map((option) => option.textContent);
    expect(labels).toEqual([
      'All quality states',
      'In progress · unverified',
    ]);
    expect(labels).not.toContain('Run-scoped verification passed');
  });

  it('shows running and failed runs alongside verified history by default', () => {
    evalStore.loadFixtures([
      evaluation('ds-running', 'run-running', '2026-10-06T12:00:00Z'),
      { ...evaluation('ds-verified', 'run-verified', '2026-10-06T10:00:00Z'),
        quality_state: 'exploratory_verified', lifecycle_state: 'completed', verifier_state: 'passed' },
      { ...evaluation('ds-failed', 'run-failed', '2026-10-06T11:00:00Z'),
        quality_state: 'terminal_failed', lifecycle_state: 'failed' },
    ] as SnapshotRecord[], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><EvaluationsView /></MemoryRouter>);

    expect(screen.getByRole('combobox', { name: 'Filter by quality' })).toHaveValue('all');
    for (const run of ['run-running', 'run-verified', 'run-failed']) {
      expect(screen.getByRole('link', { name: run })).toBeInTheDocument();
    }
  });

  it('honors an explicit verified-only history filter', () => {
    evalStore.loadFixtures([
      evaluation('ds-running', 'run-running', '2026-10-06T12:00:00Z'),
      { ...evaluation('ds-verified', 'run-verified', '2026-10-06T10:00:00Z'),
        quality_state: 'exploratory_verified', lifecycle_state: 'completed', verifier_state: 'passed' },
    ] as SnapshotRecord[], []);
    render(<MemoryRouter initialEntries={['/?release=all&quality=exploratory_verified']}><EvaluationsView /></MemoryRouter>);

    expect(screen.getByRole('link', { name: 'run-verified' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'run-running' })).not.toBeInTheDocument();
  });

  it('lists runs from every dataset without a dataset selector', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <EvaluationsView />
      </MemoryRouter>,
    );

    expect(screen.queryByTestId('dataset-selector')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'init-campaign-1789654273' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'eval-init-qwen3-4b-1789657337' })).toBeInTheDocument();
    expect(screen.getByTitle('ds-live-init-campaign-1789654273')).toBeInTheDocument();
    expect(screen.getByTitle('ds-live-eval-init-qwen3-4b-1789657337')).toBeInTheDocument();
  });

  it('renders typed model headline coverage without system-only cards', () => {
    const summary: EvaluationSummary = {
      ...evaluation('ds-current', 'run-current', '2026-09-22T08:00:00Z'),
      schema_version: '1.5.0',
      lifecycle_state: 'completed',
      assignment_completed: 3,
      assignment_failed: 1,
      terminal_outcomes: { completed: 3, model_failed: 1, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
      verifier_state: 'not_run',
      headline_metrics: {
        pass_rate: { value: 0.75, unit: 'ratio', observed_count: 4, eligible_count: 4, unavailable_count: 0 },
        latency_p50_ms: { value: 640, unit: 'milliseconds', observed_count: 3, eligible_count: 4, unavailable_count: 1 },
        output_throughput_p50_tokens_per_second: { unavailable_reason: 'incomplete_contributor_evidence', unit: 'tokens_per_second', observed_count: 0, eligible_count: 4, unavailable_count: 4 },
      },
    };
    evalStore.loadFixtures([summary], []);

    render(
      <MemoryRouter initialEntries={['/evaluations/ds-current/run-current']}>
        <Routes>
          <Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByTestId('metric-pass-rate')).toHaveTextContent('4 observed / 4 eligible');
    expect(screen.getByTestId('metric-latency-p50')).toHaveTextContent('3 observed / 4 eligible');
    expect(screen.getByTestId('metric-output-throughput-p50')).toHaveTextContent('0 observed / 4 eligible');
    expect(screen.queryByTestId('metric-primary-invocation-share')).not.toBeInTheDocument();
    expect(screen.queryByTestId('metric-correlated-failure-rate')).not.toBeInTheDocument();
    expect(screen.getByText(/heterogeneous routing and correlation metrics appear only for system evaluations/i)).toBeInTheDocument();
    expect(screen.getAllByText('Not yet verified')).toHaveLength(2);
    expect(screen.queryByText('not_run')).not.toBeInTheDocument();
  });
});
