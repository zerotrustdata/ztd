// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type { AssignmentResult, EvaluationSummary, LiveEvent } from '../src/contract/types';
import { LiveEventStream } from '../src/components/LiveEventStream';
import { evalStore, recordKey } from '../src/state/store';
import { EvaluationDetailView } from '../src/views/EvaluationDetailView';

function liveEvent(partial: Partial<LiveEvent> = {}): LiveEvent {
  return {
    schema_version: '1.3.0', dataset_id: 'ds-live-a', quality_state: 'live_in_progress',
    observed_at: '2026-09-17T08:00:00Z', run_id: 'run-a', lifecycle_status: 'completed',
    event_id: 'evt-1', kind: 'assignment_completed', completed: 1, total: 75,
    assignment_id: 'assignment-1', task_id: 'tool-select-file-read', role: 'primary', variant_id: 'model-a',
    ...partial,
  };
}
function assignment(partial: Partial<AssignmentResult> = {}): AssignmentResult {
  return {
    schema_version: '1.5.0', kind: 'assignment_result', dataset_id: 'ds-live-a', quality_state: 'live_in_progress',
    observed_at: '2026-09-17T08:00:00Z', assignment_id: 'assignment-1', run_id: 'run-a',
    task_id: 'tool-select-file-read', variant_id: 'model-a', role: 'primary', repetition: 1,
    terminal_status: 'completed', metric_values: { pass: { value: 1 } }, stage_summary: [], ...partial,
  };
}
function stream(events: LiveEvent[], isReconciling = false) {
  return <MemoryRouter><LiveEventStream events={events} connection="live" streamConnection="connected" isReconciling={isReconciling} /></MemoryRouter>;
}
const rows = () => screen.getAllByRole('row').slice(1);

beforeEach(() => evalStore.loadFixtures([], []));

describe('LiveEventStream', () => {
  it('renders bootstrap activity while history is reconciling', () => {
    render(stream([liveEvent()], true));
    expect(screen.getByText('Syncing feed history…')).toBeInTheDocument();
    expect(screen.getByRole('table')).toBeInTheDocument();
  });

  it('keeps seven columns and links the task and result directly to evidence', () => {
    render(stream([liveEvent({ metric_delta: { pass: { value: 1 }, latency_ms: { value: 780 } } })]));
    expect(screen.getAllByRole('columnheader').map((header) => header.textContent?.replace(/[▲▼↕]/g, '').trim()))
      .toEqual(['Result', 'Task', 'Model', 'Latency', 'Output', 'Tokens/s', 'Time']);
    const href = '/evaluations/ds-live-a/run-a/assignments/assignment-1';
    expect(screen.getByRole('link', { name: 'tool-select-file-read — View details' })).toHaveAttribute('href', href);
    expect(screen.getByRole('link', { name: /Passed:.*View details/ })).toHaveAttribute('href', href);
    expect(screen.getByRole('link', { name: 'model-a' })).toHaveAttribute('href', '/models/ds-live-a/model-a?role=primary');
    expect(screen.getByText('780 ms')).toBeInTheDocument();
    expect(screen.getByText('Primary')).toHaveClass('stream-role');
  });

  it('caps the unfiltered feed at 100 rows and paginates 25 at a time', async () => {
    const user = userEvent.setup();
    const events = Array.from({ length: 120 }, (_, index) => liveEvent({
      event_id: `evt-${index}`, assignment_id: `assignment-${index}`, task_id: `task-${index}`,
      observed_at: new Date(Date.UTC(2026, 8, 17, 8, index)).toISOString(),
    }));
    render(stream(events));
    expect(screen.getByText('Page 1 of 4 (100 events)')).toBeInTheDocument();
    expect(rows()).toHaveLength(25);
    expect(rows()[0]).toHaveTextContent('task-119');
    expect(screen.queryByText('task-0')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(screen.getByText('Page 2 of 4 (100 events)')).toBeInTheDocument();
    expect(rows()[0]).toHaveTextContent('task-94');
  });

  it('collapses assignment lifecycle updates into one result', () => {
    render(stream([
      liveEvent({ event_id: 'start', kind: 'assignment_started', lifecycle_status: 'running' }),
      liveEvent({ event_id: 'stage', kind: 'stage_updated', lifecycle_status: 'running' }),
      liveEvent({ event_id: 'metric', kind: 'metric_updated' }),
      liveEvent(),
    ]));
    expect(rows()).toHaveLength(1);
    expect(screen.getByRole('link', { name: /Completed:.*View details/ })).toHaveTextContent('✓');
  });

  it.each(['model_failed', 'grader_failed', 'invalid_evidence', 'provider_failed', 'execution_failed'] as const)(
    'keeps %s diagnostic prose off the landing page while linking to details', (status) => {
      const reason = 'Expected the model to decline the forbidden operation.';
      evalStore.loadFixtures([assignment({ terminal_status: status, failure_reason: reason })], []);
      render(stream([liveEvent({ kind: 'assignment_failed', terminal_status: status })]));
      const result = rows()[0]!.querySelector('.stream-result');
      expect(result).toHaveAttribute('href', '/evaluations/ds-live-a/run-a/assignments/assignment-1');
      expect(result).toHaveAttribute('data-tone', status === 'model_failed' ? 'critical' : 'neutral');
      expect(result).toHaveTextContent(status === 'model_failed' ? '×' : '!');
      expect(screen.queryByText(reason)).not.toBeInTheDocument();
      expect(screen.queryByTitle(reason)).not.toBeInTheDocument();
    },
  );

  it('shows a failed observation as an X even when execution completed', () => {
    render(stream([liveEvent({ metric_delta: { pass: { value: 0 } } })]));
    expect(screen.getByRole('link', { name: /Model failed:/ })).toHaveTextContent('×');
    expect(screen.queryByText('0.0%')).not.toBeInTheDocument();
  });

  it('uses final resource observations and derives throughput only from generation time', () => {
    evalStore.loadFixtures([assignment({
      resource_summary: { latency_ms: { value: 1200 }, output_tokens: { value: 180 } },
      benchmark_observations: { timing: { generation_ms: { value: 780 } }, unavailable_reasons: [] },
    })], []);
    render(stream([liveEvent()]));
    expect(screen.getByText('180 tok')).toBeInTheDocument();
    expect(screen.getByText('230.8 tok/s')).toBeInTheDocument();
  });

  it('distinguishes missing metrics from zero and does not infer speed from latency', () => {
    render(stream([liveEvent({ metric_delta: { output_tokens: { value: 0 }, latency_ms: { value: 1000 } } })]));
    expect(screen.getByText('0 tok')).toBeInTheDocument();
    expect(screen.getByText('--')).toHaveAttribute('title', 'not observed');
  });

  it('does not borrow outcome or metrics from an assignment in another run', () => {
    evalStore.loadFixtures([assignment({ run_id: 'other-run', terminal_status: 'provider_failed', resource_summary: { output_tokens: { value: 999 } } })], []);
    render(stream([liveEvent()]));
    expect(screen.getByRole('link', { name: /Completed:/ })).toHaveTextContent('✓');
    expect(screen.queryByText('999 tok')).not.toBeInTheDocument();
  });

  it('keeps model choices available after filtering and clears the model filter', async () => {
    const user = userEvent.setup();
    render(stream([liveEvent(), liveEvent({ assignment_id: 'second', event_id: 'second', variant_id: 'model-b' })]));
    const select = screen.getByRole('combobox', { name: 'Filter by model' });
    await user.selectOptions(select, 'model-a');
    expect(within(select).getByRole('option', { name: 'model-b' })).toBeInTheDocument();
    expect(rows()).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(rows()).toHaveLength(2);
  });

  it('shows successful results, model failures, and non-verdict errors together', () => {
    render(stream([
      liveEvent(),
      liveEvent({ event_id: 'failed', assignment_id: 'failed', kind: 'assignment_failed', terminal_status: 'model_failed' }),
      liveEvent({ event_id: 'provider', assignment_id: 'provider', kind: 'assignment_failed', terminal_status: 'provider_failed' }),
    ]));
    expect(rows()).toHaveLength(3);
    expect(screen.getByRole('link', { name: /Completed:/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Model failed:/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Provider Failed:/ })).toBeInTheDocument();
  });

  it('reveals category filters on demand and resolves categories before assignment results arrive', async () => {
    const user = userEvent.setup();
    render(stream([
      liveEvent({ role: 'lite', stage_label: 'model role invoked · lite · model-a · tool-select-file-read' }),
      liveEvent({ event_id: 'format', assignment_id: 'format', task_id: 'instruction-exact-format' }),
    ]));
    expect(screen.getByText('Filters', { selector: 'summary' }).closest('details')).not.toHaveAttribute('open');
    await user.click(screen.getByText('Filters', { selector: 'summary' }));
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by category' }), 'Tool Selection');
    expect(rows()).toHaveLength(1);
    expect(rows()[0]).toHaveTextContent('Lite');
    expect(rows()[0]).toHaveTextContent('Tool Selection');
  });

  it('filters by latest run and assignment without losing older run activity by default', async () => {
    const user = userEvent.setup();
    render(stream([
      liveEvent({ event_id: 'old', assignment_id: 'old', run_id: 'old-run' }),
      liveEvent({ event_id: 'new', assignment_id: 'new', run_id: 'new-run', observed_at: '2026-09-17T09:00:00Z' }),
    ]));
    expect(rows()).toHaveLength(2);
    await user.click(screen.getByText('Filters', { selector: 'summary' }));
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by assignment' }), 'old');
    expect(rows()[0]?.querySelector('.stream-task-link')).toHaveAttribute('href', '/evaluations/ds-live-a/old-run/assignments/old');
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by run scope' }), 'latest');
    expect(rows()).toHaveLength(1);
    expect(rows()[0]?.querySelector('.stream-task-link')).toHaveAttribute('href', '/evaluations/ds-live-a/new-run/assignments/new');
  });

  it('waits for a classified result before attributing an unclassified failure to the model', () => {
    render(stream([liveEvent({ kind: 'assignment_failed' })]));
    expect(screen.getByRole('link', { name: /Failed:/ })).toHaveAttribute('data-tone', 'neutral');
  });

  it('sorts metrics numerically with unavailable observations last in either direction', async () => {
    const user = userEvent.setup();
    render(stream([
      liveEvent({ event_id: 'ten', assignment_id: 'ten', metric_delta: { output_tokens: { value: 10 } } }),
      liveEvent({ event_id: 'two', assignment_id: 'two', metric_delta: { output_tokens: { value: 2 } } }),
      liveEvent({ event_id: 'missing', assignment_id: 'missing' }),
    ]));
    const output = () => rows().map((row) => row.querySelector('[data-label="Output"]')?.textContent);
    await user.click(screen.getByRole('button', { name: 'Sort by Output' }));
    expect(output()).toEqual(['2 tok', '10 tok', '--']);
    await user.click(screen.getByRole('button', { name: 'Sort by Output' }));
    expect(output()).toEqual(['10 tok', '2 tok', '--']);
  });

  it('updates rows and metrics as new activity arrives', () => {
    evalStore.loadFixtures([assignment({ resource_summary: { output_tokens: { value: 10 } } })], []);
    const { rerender } = render(stream([liveEvent()]));
    expect(screen.getByText('10 tok')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Needs attention|Pause|Resume/i })).not.toBeInTheDocument();
    act(() => evalStore.loadFixtures([assignment({ resource_summary: { output_tokens: { value: 20 } } })], []));
    rerender(stream([liveEvent(), liveEvent({ event_id: 'new', assignment_id: 'new', task_id: 'new-task' })]));
    expect(screen.getByText('Live', { exact: true })).toBeInTheDocument();
    expect(screen.queryByText('10 tok')).not.toBeInTheDocument();
    expect(rows()).toHaveLength(2);
    expect(screen.getByText('20 tok')).toBeInTheDocument();
    expect(screen.getByText('new-task')).toBeInTheDocument();
  });

  it('links run lifecycle activity to the run without claiming a model passed', () => {
    render(stream([liveEvent({ kind: 'evaluation_completed', assignment_id: undefined, task_id: undefined, variant_id: undefined, role: undefined })]));
    const result = screen.getByRole('link', { name: /Evaluation Completed:.*View details/ });
    expect(result).toHaveAttribute('href', '/evaluations/ds-live-a/run-a');
    expect(result).toHaveAttribute('data-tone', 'neutral');
    expect(result).not.toHaveTextContent('✓');
  });

  it('links active evaluation details to the live page without duplicating the event stream', () => {
    const evaluation: EvaluationSummary = {
      schema_version: '1.3.0',
      kind: 'evaluation_summary',
      dataset_id: 'ds-live-a',
      quality_state: 'live_in_progress',
      observed_at: '2026-09-17T08:00:00Z',
      run_id: 'run-a',
      suite_id: 'suite-a',
      arm: 'platform',
      evaluation_unit: 'model',
      model_role_mapping: {},
      lifecycle_state: 'running',
      assignment_total: 30,
      assignment_completed: 0,
      assignment_failed: 0,
      terminal_outcomes: { completed: 0, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
      started_at: '2026-09-17T08:00:00Z',
      verifier_state: 'not_applicable',
      headline_metrics: {},
    };
    const events = Array.from({ length: 30 }, (_, index) =>
      liveEvent({
        event_id: `evt-detail-${index}`,
        kind: 'stage_updated',
        observed_at: `2026-09-17T08:${String(index).padStart(2, '0')}:00Z`,
      }),
    );
    evalStore.loadFixtures([], events);
    evalStore.getState().evaluations.set(recordKey(evaluation.dataset_id, evaluation.run_id), evaluation);

    render(
      <MemoryRouter initialEntries={['/evaluations/ds-live-a/run-a']}>
        <Routes>
          <Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} />
        </Routes>
      </MemoryRouter>,
    );

    const liveLink = screen.getByRole('link', { name: 'Watch it Live' });
    expect(liveLink).toHaveAttribute('href', '/');
    expect(screen.queryByRole('region', { name: 'Live timeline' })).not.toBeInTheDocument();
    expect(screen.queryByText('Page 1 of 2 (30 events)')).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

});
