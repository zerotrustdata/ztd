// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TasksView } from '../src/views/TasksView';
import { evalStore } from '../src/state/store';
import { taskResultView } from '../src/state/campaign-adapter';
import { ValidationError } from '../src/contract/validators';
import type { FeedTaskResult } from '../src/contract/types';

const taskId = 'tool-select-grep';

function result(assignmentId: string, overrides: Partial<FeedTaskResult> = {}): FeedTaskResult {
  return {
    run_id: 'run-one',
    assignment_id: assignmentId,
    lifecycle_status: 'EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED',
    summary_status: 'EVALUATION_VERDICT_STATUS_FAIL',
    verification_status: 'unverified',
    completed_at: '2026-10-01T12:00:00Z',
    ...overrides,
  };
}

function renderTask() {
  return render(
    <MemoryRouter initialEntries={[`/tasks/${taskId}`]}>
      <Routes>
        <Route path="/tasks/:taskId" element={<TasksView />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('TasksView recent results from the task index', () => {
  beforeEach(() => {
    evalStore.loadFixtures([], []);
  });

  it('renders the task index rows with their links and verdict tones without any loaded assignments', () => {
    evalStore.acceptTaskIndex([{
      task_id: taskId,
      results: [
        result('recent-one'),
        result('recent-two', {
          run_id: 'run-two',
          lifecycle_status: 'EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED',
          summary_status: 'EVALUATION_VERDICT_STATUS_UNAVAILABLE',
        }),
      ],
    }]);
    expect(evalStore.getState().assignments.size).toBe(0);

    renderTask();
    const recent = within(screen.getByRole('region', { name: 'Recent assignments of this task' }));
    expect(recent.getByRole('link', { name: 'recent-one' })).toHaveAttribute('href', '/evaluations/ds-live-run-one/run-one/assignments/recent-one');
    expect(recent.getByRole('link', { name: 'recent-two' })).toHaveAttribute('href', '/evaluations/ds-live-run-two/run-two/assignments/recent-two');
    expect(recent.getByText('model failed')).toHaveAttribute('data-tone', 'critical');
    expect(recent.getByText('grader failed')).toHaveAttribute('data-tone', 'warn');
  });

  it('renders at most ten rows in the order the index gives them', () => {
    const results = Array.from({ length: 12 }, (_, index) => result(`asg-${String(index + 1).padStart(2, '0')}`));
    evalStore.acceptTaskIndex([{ task_id: taskId, results }]);

    renderTask();
    const recent = within(screen.getByRole('region', { name: 'Recent assignments of this task' }));
    const links = recent.getAllByRole('link');
    expect(links.map((link) => link.textContent)).toEqual(results.slice(0, 10).map((entry) => entry.assignment_id));
    expect(recent.queryByRole('link', { name: 'asg-11' })).not.toBeInTheDocument();
  });

  it('skips a row whose lifecycle status is unknown and keeps the valid rows', () => {
    evalStore.acceptTaskIndex([{
      task_id: taskId,
      results: [
        result('bad-row', { lifecycle_status: 'NOT_A_LIFECYCLE' }),
        result('good-row'),
      ],
    }]);

    renderTask();
    const recent = within(screen.getByRole('region', { name: 'Recent assignments of this task' }));
    expect(recent.queryByRole('link', { name: 'bad-row' })).not.toBeInTheDocument();
    expect(recent.getByRole('link', { name: 'good-row' })).toBeInTheDocument();
  });
});

describe('taskResultView', () => {
  it('maps a valid index result to its dataset, outcome, and verification disposition', () => {
    expect(taskResultView(result('asg-1'))).toEqual({
      dataset_id: 'ds-live-run-one',
      run_id: 'run-one',
      assignment_id: 'asg-1',
      terminal_status: 'model_failed',
      verification_disposition: 'not_run',
    });
  });

  it('throws a ValidationError naming an unknown lifecycle_status', () => {
    try {
      taskResultView(result('asg-1', { lifecycle_status: 'NOT_A_LIFECYCLE' }));
      expect.unreachable('taskResultView accepted an unknown lifecycle_status');
    } catch (error) {
      expect(error).toBeInstanceOf(ValidationError);
      expect((error as ValidationError).path).toBe('lifecycle_status');
    }
  });

  it('throws a ValidationError naming an unknown summary_status', () => {
    try {
      taskResultView(result('asg-1', { summary_status: 'NOT_A_VERDICT' }));
      expect.unreachable('taskResultView accepted an unknown summary_status');
    } catch (error) {
      expect(error).toBeInstanceOf(ValidationError);
      expect((error as ValidationError).path).toBe('summary_status');
    }
  });
});
