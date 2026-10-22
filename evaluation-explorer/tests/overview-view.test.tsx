// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { CatalogSnapshot, EvaluationSummary, LiveEvent, ModelSummary, SnapshotRecord, SuiteSummary } from '../src/contract/types';
import { evalStore } from '../src/state/store';
import { OverviewView } from '../src/views/OverviewView';

const datasetId = 'ds-live-smoke-run';

const catalog: CatalogSnapshot = {
  schema_version: '1.4.0',
  kind: 'catalog_snapshot',
  dataset_id: datasetId,
  dataset_kind: 'live_run',
  quality_state: 'live_in_progress',
  observed_at: '2026-09-22T12:05:00Z',
  title: 'Live smoke run',
  description: 'Evaluating model reliability across governed operational scenarios.',
  limitations: [],
  model_count: 1,
  evaluated_count: 1,
  suite_count: 1,
  run_count: 1,
  assignment_count: 75,
  provider_request_count: 6,
  provider_token_count: 1200,
  retry_count: 0,
  verifier_passed_count: 0,
  verifier_failed_count: 0,
  generated_at: '2026-09-22T12:05:00Z',
};

const archivedCatalog: CatalogSnapshot = {
  ...catalog,
  dataset_id: 'ds-verified-archive',
  dataset_kind: 'verified_public_snapshot',
  quality_state: 'legacy_unverified',
  title: 'Archived campaign',
};

const suite: SuiteSummary = {
  schema_version: '1.4.0',
  kind: 'suite_summary',
  dataset_id: datasetId,
  quality_state: 'live_in_progress',
  observed_at: '2026-09-22T12:05:00Z',
  suite_id: 'default-suite',
  display_name: 'Default Suite',
  task_count: 25,
  assignment_count: 75,
  status: 'running',
  verifier_state: 'not_applicable',
  model_coverage: ['ministral-3-8b'],
  metric_summaries: {},
  limitations: [],
};

const evaluation: EvaluationSummary = {
  schema_version: '1.4.0',
  kind: 'evaluation_summary',
  dataset_id: datasetId,
  quality_state: 'live_in_progress',
  observed_at: '2026-09-22T12:05:00Z',
  run_id: 'eval-init-ministral-3-8b-1790084488',
  campaign_id: 'smoke-run',
  suite_id: 'default-suite',
  arm: 'homogeneous-model-role',
  lifecycle_state: 'running',
  run_status: 'running',
  assignment_total: 75,
  assignment_completed: 5,
  assignment_failed: 1,
  terminal_outcomes: { completed: 5, model_failed: 1, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
  started_at: '2026-09-22T12:00:00Z',
  verifier_state: 'not_applicable',
  headline_metrics: {},
};

const events: LiveEvent[] = [
  {
    schema_version: '1.4.0',
    kind: 'assignment_started',
    dataset_id: datasetId,
    quality_state: 'live_in_progress',
    observed_at: '2026-09-22T12:04:55Z',
    event_id: 'event-1',
    run_id: evaluation.run_id,
    assignment_id: 'assignment-7',
    task_id: 'tool-selection-04',
    variant_id: 'ministral-3-8b',
    role: 'primary',
    lifecycle_status: 'running',
    completed: 6,
    total: 75,
  },
  {
    schema_version: '1.4.0',
    kind: 'stage_updated',
    dataset_id: datasetId,
    quality_state: 'live_in_progress',
    observed_at: '2026-09-22T12:05:00Z',
    event_id: 'event-2',
    run_id: evaluation.run_id,
    assignment_id: 'assignment-7',
    lifecycle_status: 'running',
    completed: 6,
    total: 75,
    stage_label: 'semantic grading',
  },
];

describe('OverviewView', () => {
  beforeEach(() => {
    evalStore.loadFixtures([catalog, archivedCatalog, suite, evaluation] as SnapshotRecord[], events);
    evalStore.setConnection('live');
    evalStore.setStreamConnection('connected');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each(['running', 'completed', 'cancelled', 'interrupted', 'verified', 'verify_failed', 'scheduled', 'in_progress', 'initialized', 'unknown'])(
    'shows the published CLI run status %s instead of assignment lifecycle', (status) => {
      evalStore.loadFixtures([catalog, Object.assign({}, evaluation, { run_status: status })], []);
      render(<MemoryRouter initialEntries={['/?release=all']}><OverviewView /></MemoryRouter>);
      const row = within(screen.getByRole('list', { name: 'Recent runs' }));
      expect(row.getByText(status)).toHaveClass(`status-${status}`);
      if (status !== 'running') expect(row.queryByText(/Running for/)).not.toBeInTheDocument();
    },
  );

  it('does not claim running from a historical partial summary without process evidence', () => {
    evalStore.loadFixtures([catalog, { ...evaluation, run_status: undefined }], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><OverviewView /></MemoryRouter>);
    const row = within(screen.getByRole('list', { name: 'Recent runs' }));
    expect(row.getByText('unknown')).toBeInTheDocument();
    expect(row.queryByText(/Running for/)).not.toBeInTheDocument();
  });

  it('receives cancellation through a pushed summary and counts stopped assignments as terminal', () => {
    evalStore.loadFixtures([catalog, Object.assign({}, evaluation, { run_status: 'running' })], []);
    render(<MemoryRouter initialEntries={['/?release=all']}><OverviewView /></MemoryRouter>);
    act(() => evalStore.acceptProjection({
      sequence: 1,
      record_type: 'projection',
      record_bytes: JSON.stringify({
        ...evaluation, run_status: 'cancelled', lifecycle_state: 'stopped',
        terminal_outcomes: { ...evaluation.terminal_outcomes, stopped: 69 },
      }),
    }));
    const row = within(screen.getByRole('list', { name: 'Recent runs' }));
    expect(row.getByText('cancelled')).toBeInTheDocument();
    expect(row.getByText('5 passed · 1 failed · 69 no verdict')).toBeInTheDocument();
    expect(row.getByRole('progressbar')).toHaveAttribute('aria-valuetext', '5 passed, 1 model failed, 69 no verdict, 0 remaining');
  });

  it.each(['assignment_started', 'assignment_completed', 'assignment_failed', 'stage_updated'] as const)(
    'does not let %s overwrite the run lifecycle', (kind) => {
      evalStore.loadFixtures([catalog, { ...evaluation, lifecycle_state: 'stopped' }], []);
      evalStore.acceptProjection({
        sequence: 1, record_type: 'event', record_bytes: JSON.stringify({
          ...events[0], kind, lifecycle_status: kind === 'assignment_completed' ? 'completed' : 'running',
        }),
      });
      expect(evalStore.getEvaluation(datasetId, evaluation.run_id)?.lifecycle_state).toBe('stopped');
    },
  );

  it('renders the live event stream without the system overview panel', () => {
    render(
      <MemoryRouter initialEntries={['/?dataset=ds-verified-archive&release=all']}>
        <OverviewView />
      </MemoryRouter>,
    );

    expect(screen.getByRole('region', { name: 'Live activity' })).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Filter by release' })).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'System overview' })).not.toBeInTheDocument();
    expect(screen.queryByText('Active campaign')).not.toBeInTheDocument();
    expect(screen.queryByText(/live deployment of the ztd AI governance suite/i)).not.toBeInTheDocument();
  });

  it('shows activity from every run, including events arriving before summaries', () => {
    const secondEvent: LiveEvent = {
      ...events[0]!,
      dataset_id: 'ds-live-second-run',
      run_id: 'second-run',
      assignment_id: 'tiny-model-assignment',
      event_id: 'tiny-model-event',
      variant_id: 'smollm2-135m',
      kind: 'assignment_failed',
      terminal_status: 'provider_failed',
      lifecycle_status: 'failed',
      completed: 1,
      total: 41,
    };
    evalStore.loadFixtures([catalog, evaluation], [...events, secondEvent]);

    render(<MemoryRouter initialEntries={['/?release=all']}><OverviewView /></MemoryRouter>);

    const stream = within(screen.getByRole('region', { name: 'Live activity' }));
    expect(stream.getByRole('link', { name: /Provider Failed:.*View details/ })).toHaveAttribute('data-tone', 'neutral');
    expect(stream.getByRole('link', { name: 'smollm2-135m' })).toBeInTheDocument();
    expect(stream.getByRole('link', { name: /Stage Updated:.*View details/ })).toBeInTheDocument();
  });

  it.each(['/', '/?release=v1.1.2'])('shows live activity across releases at %s before summaries arrive', (route) => {
    evalStore.loadFixtures([], [
      { ...events[0]!, release: 'v1.1.2', release_basis: 'recorded', variant_id: 'current-model' },
      { ...events[0]!, event_id: 'older-event', run_id: 'older-run', assignment_id: 'older-assignment', release: 'v1.1.1', release_basis: 'recorded', variant_id: 'older-model' },
      { ...events[0]!, event_id: 'unknown-event', run_id: 'unknown-run', assignment_id: 'unknown-assignment', variant_id: 'unknown-model' },
    ]);

    render(<MemoryRouter initialEntries={[route]}><OverviewView /></MemoryRouter>);

    const stream = within(screen.getByRole('region', { name: 'Live activity' }));
    expect(stream.getByRole('link', { name: 'current-model' })).toBeInTheDocument();
    expect(stream.getByRole('link', { name: 'older-model' })).toBeInTheDocument();
    expect(stream.getByRole('link', { name: 'unknown-model' })).toBeInTheDocument();
  });

  it('shows recent runs across releases by recency', () => {
    evalStore.loadFixtures([
      { ...evaluation, release: 'v1.3.0', release_basis: 'recorded' },
      { ...evaluation, dataset_id: 'ds-live-older-release', run_id: 'recent-older-release',
        release: 'v1.2.0', release_basis: 'recorded', started_at: '2026-09-23T12:00:00Z' },
      { ...evaluation, dataset_id: 'ds-live-unknown', run_id: 'recent-unknown', started_at: '2026-09-24T12:00:00Z' },
    ], []);
    render(<MemoryRouter><OverviewView /></MemoryRouter>);
    const recent = within(screen.getByRole('list', { name: 'Recent runs' }));
    expect(recent.getAllByRole('link').map((link) => link.textContent)).toEqual([
      'recent-unknown', 'recent-older-release', evaluation.run_id,
    ]);
  });

  it('removes the landing page explainer while retaining recent runs', () => {
    render(<MemoryRouter initialEntries={['/?release=all']}><OverviewView /></MemoryRouter>);
    expect(screen.queryByRole('region', { name: 'What am I looking at?' })).not.toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Runs' })).toBeInTheDocument();
  });

  it('presents recent runs with coverage for engineers without duplicate public data section', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <OverviewView />
      </MemoryRouter>,
    );

    const evidence = within(screen.getByRole('region', { name: 'Runs' }));
    expect(evidence.getByRole('heading', { name: 'Runs' })).toBeInTheDocument();
    expect(evidence.getByText('Passed')).toBeInTheDocument();
    expect(evidence.getByText('Model failed')).toBeInTheDocument();
    expect(evidence.getByText('No verdict')).toBeInTheDocument();
    expect(evidence.getByText('Remaining')).toBeInTheDocument();
    const coverage = evidence.getByRole('progressbar', { name: 'smoke-run assignment outcomes' });
    expect(coverage).toHaveAttribute('aria-valuenow', '6');
    expect(coverage).toHaveAttribute('aria-valuemax', '75');
    expect(evidence.getByText('5 passed · 1 failed · 0 no verdict · 69 remaining')).toBeInTheDocument();
    expect(evidence.getByText('Verification pending')).toBeInTheDocument();

    expect(screen.queryByRole('region', { name: 'Public data and APIs' })).not.toBeInTheDocument();
    expect(screen.queryByText('Public data & APIs')).not.toBeInTheDocument();
  });

  it.each(['primary', 'assistant'] as const)('links the %s model to its definition and the run ID to evaluation details', (role) => {
    const model: ModelSummary = {
      schema_version: '1.4.0',
      kind: 'model_summary',
      dataset_id: datasetId,
      quality_state: 'live_in_progress',
      observed_at: evaluation.observed_at,
      variant_id: 'ministral-3-8b',
      display_name: 'Ministral 3 8B',
      role,
      inventory_only: false,
      evaluation_coverage: 1,
    };
    evalStore.loadFixtures([
      catalog, model,
      { ...evaluation, model_role_mapping: { [role]: model.variant_id } },
    ], []);

    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <OverviewView />
      </MemoryRouter>,
    );

    const recent = within(screen.getByRole('region', { name: 'Runs' }));
    expect(recent.getByRole('link', { name: model.display_name })).toHaveAttribute(
      'href', `/models/${datasetId}/${model.variant_id}?role=${role}`,
    );
    expect(recent.getByRole('link', { name: evaluation.run_id })).toHaveAttribute(
      'href', `/evaluations/${datasetId}/${evaluation.run_id}`,
    );
  });
});
