// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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
  quality_state: 'verified_public',
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

  it('renders the live event stream full width without the system overview panel', () => {
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

  it('shows activity from every run in the selected release, including events arriving before summaries', () => {
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

  it('filters live events by release even before a catalog or run summary arrives', () => {
    evalStore.loadFixtures([], [
      { ...events[0]!, release: 'v1.1.2', release_basis: 'recorded', variant_id: 'current-model' },
      { ...events[0]!, event_id: 'older-event', assignment_id: 'older-assignment', release: 'v1.1.1', release_basis: 'recorded', variant_id: 'older-model' },
    ]);

    render(<MemoryRouter initialEntries={['/?release=v1.1.2']}><OverviewView /></MemoryRouter>);

    const stream = within(screen.getByRole('region', { name: 'Live activity' }));
    expect(stream.getByRole('link', { name: 'current-model' })).toBeInTheDocument();
    expect(stream.queryByRole('link', { name: 'older-model' })).not.toBeInTheDocument();
  });

  it('removes the landing page explainer while retaining recent runs', () => {
    render(<MemoryRouter initialEntries={['/?release=all']}><OverviewView /></MemoryRouter>);
    expect(screen.queryByRole('region', { name: 'What am I looking at?' })).not.toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Recent runs' })).toBeInTheDocument();
  });

  it('presents recent runs with coverage for engineers without duplicate public data section', () => {
    render(
      <MemoryRouter initialEntries={['/?release=all']}>
        <OverviewView />
      </MemoryRouter>,
    );

    const evidence = within(screen.getByRole('region', { name: 'Recent runs' }));
    expect(evidence.getByRole('heading', { name: 'Recent Runs' })).toBeInTheDocument();
    expect(evidence.getByText('Completed')).toBeInTheDocument();
    expect(evidence.getByText('Failed')).toBeInTheDocument();
    expect(evidence.getByText('Remaining')).toBeInTheDocument();
    const coverage = evidence.getByRole('progressbar', { name: 'smoke-run assignment outcomes' });
    expect(coverage).toHaveAttribute('aria-valuenow', '6');
    expect(coverage).toHaveAttribute('aria-valuemax', '75');
    expect(evidence.getByText('6 / 75 terminal')).toBeInTheDocument();
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

    const recent = within(screen.getByRole('region', { name: 'Recent runs' }));
    expect(recent.getByRole('link', { name: model.display_name })).toHaveAttribute(
      'href', `/models/${datasetId}/${model.variant_id}?role=${role}`,
    );
    expect(recent.getByRole('link', { name: evaluation.run_id })).toHaveAttribute(
      'href', `/evaluations/${datasetId}/${evaluation.run_id}`,
    );
  });
});
