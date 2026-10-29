// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { AssignmentTable } from '../src/components/AssignmentTable';
import { fixtureEnrichedAssignmentResult } from '../src/fixtures/fixtures';
import type { AssignmentResult } from '../src/contract/types';

const ids = ['a'.repeat(64), 'b'.repeat(64), 'c'.repeat(64)];
function assignment(index: number, overrides: Partial<AssignmentResult> = {}): AssignmentResult {
  return {
    ...fixtureEnrichedAssignmentResult,
    assignment_id: ids[index]!, task_id: `task-${index}`, run_id: 'table-run',
    semantic_grade_summaries: [{ criterion_id: 'answer-content', status: index === 1 ? 'fail' : 'pass', grading_method: 'deterministic', explanation_code: index === 1 ? 'criterion_failed' : 'criterion_passed' }],
    scenario_summary: {
      ...fixtureEnrichedAssignmentResult.scenario_summary!, public_description: 'Diagnose a failed service',
      criteria: [
        { criterion_id: 'answer-content', public_label: 'Answer content', public_description: 'Correct answer', required: true, grading_method: 'deterministic' },
        { criterion_id: 'tool-trajectory', public_label: 'Tool trajectory', public_description: 'Expected tool sequence', required: true, grading_method: 'deterministic' },
      ],
    },
    resource_summary: { latency_ms: { value: index === 0 ? 900 : 12000 }, output_tokens: { value: index === 0 ? 100 : 20 }, retries: { value: 0 } },
    benchmark_observations: { timing: { generation_ms: { value: 2000 } }, unavailable_reasons: [] },
    ...overrides,
  };
}
function renderTable(assignments: AssignmentResult[]) {
  return render(<MemoryRouter><AssignmentTable assignments={assignments} runId="table-run" /></MemoryRouter>);
}
function rowIds() {
  return [...screen.getByRole('table').querySelectorAll('tbody .assignment-short-id')].map((link) => link.getAttribute('aria-label'));
}

describe('AssignmentTable', () => {
  it('opens the assignment reply and tool calls in the same table', async () => {
    const user = userEvent.setup();
    renderTable([assignment(0, {
      model_response: undefined,
      failure_reason: 'The model never called the expected tool.',
      trajectory_outcome: 'no_tool_call',
      role_transcripts: [{ role: 'primary', response: 'I looked and stopped.', tool_calls: [{ tool_name: 'read_file', arguments_json: '{"path":"/tmp/a"}', success: false }] }],
    })]);
    expect(screen.queryByText('I looked and stopped.')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Show task-0 detail' }));
    expect(screen.getByText('I looked and stopped.')).toBeInTheDocument();
    expect(screen.getByText(/read_file failed/)).toBeInTheDocument();
    expect(screen.getByText('no tool call')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open assignment' })).toHaveAttribute('href', expect.stringContaining(`/assignments/${ids[0]}`));
  });

  it('prioritizes tasks and outcomes, abbreviates IDs, and keeps full detail links', () => {
    renderTable([assignment(0)]);
    const table = screen.getByRole('table');
    expect(within(table).getAllByRole('columnheader')[0]).toHaveTextContent('Task');
    expect(within(table).queryByRole('columnheader', { name: /Scope|Criteria/ })).not.toBeInTheDocument();
    const link = within(table).getByRole('link', { name: ids[0] });
    expect(link).toHaveTextContent('aaaaaa…');
    expect(link).toHaveAttribute('title', ids[0]);
    expect(link).toHaveAttribute('href', expect.stringContaining(`/assignments/${ids[0]}`));
    expect(within(table).getByText('1 ungraded')).toBeInTheDocument();
  });

  it('sorts numeric duration and token values with absent measurements last in both directions', async () => {
    const user = userEvent.setup();
    renderTable([assignment(2, { resource_summary: undefined }), assignment(0), assignment(1)]);
    await user.click(screen.getByRole('button', { name: 'Sort by Duration' }));
    expect(rowIds()).toEqual([ids[1], ids[0], ids[2]]);
    await user.click(screen.getByRole('button', { name: 'Sort by Duration' }));
    expect(rowIds()).toEqual([ids[0], ids[1], ids[2]]);
    await user.click(screen.getByRole('button', { name: 'Sort by Output tokens' }));
    expect(rowIds()).toEqual([ids[0], ids[1], ids[2]]);
    await user.click(screen.getByRole('button', { name: 'Performance' }));
    const table = screen.getByRole('table');
    expect(within(table).getByText('50 tok/s')).toBeInTheDocument();
    expect(within(table).getByText('10 tok/s')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Sort by Output rate' }));
    expect(rowIds()).toEqual([ids[0], ids[1], ids[2]]);
    const measuredRow = within(table).getByRole('link', { name: ids[0] }).closest('tr')!;
    expect(measuredRow.querySelector('[data-column="retries"]')).toHaveTextContent('0');
    const absentRow = within(table).getByRole('link', { name: ids[2] }).closest('tr')!;
    expect(absentRow.querySelector('[data-column="retries"]')).toHaveTextContent('—');
  });

  it('sorts each criterion, preserves missing grades, and keeps the matrix stable while filtering', async () => {
    const user = userEvent.setup();
    renderTable([assignment(0), assignment(1), assignment(2, { semantic_grade_summaries: [], benchmark_observations: undefined })]);
    await user.click(screen.getByRole('button', { name: 'Grading' }));
    await user.click(screen.getByRole('button', { name: 'Sort by Answer content' }));
    expect(rowIds()).toEqual([ids[1], ids[0], ids[2]]);
    await user.click(screen.getByRole('button', { name: 'Sort by Answer content' }));
    expect(rowIds()).toEqual([ids[0], ids[1], ids[2]]);
    const failedRow = screen.getByRole('link', { name: ids[1] }).closest('tr')!;
    expect(within(failedRow).getByRole('link', { name: 'Fail' })).toHaveAttribute('href', expect.stringContaining('#assignment-grades'));
    expect(within(failedRow).getByLabelText('No grade published for this criterion')).toBeInTheDocument();
    await user.type(screen.getByRole('searchbox'), 'task-2');
    expect(rowIds()).toEqual([ids[2]]);
    expect(screen.getByRole('button', { name: 'Sort by Answer content' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sort by Tool trajectory' })).toBeInTheDocument();
  });

  it('allows individual criterion columns in Results and resets columns with presets', async () => {
    const user = userEvent.setup();
    renderTable([assignment(0)]);
    await user.click(screen.getByRole('button', { name: /Columns/ }));
    await user.click(screen.getByRole('checkbox', { name: 'Answer content' }));
    expect(screen.getByRole('button', { name: 'Sort by Answer content' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Performance' }));
    expect(screen.queryByRole('button', { name: 'Sort by Answer content' })).not.toBeInTheDocument();
  });

  it('searches full IDs, task descriptions and failures and combines outcome and role filters', async () => {
    const user = userEvent.setup();
    renderTable([
      assignment(0, { role: 'primary' }),
      assignment(1, { role: 'assistant', terminal_status: 'model_failed', failure_reason: 'Expected grep but received file_read.' }),
      assignment(2, { role: 'assistant', terminal_status: 'provider_failed' }),
    ]);
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter assignments by verdict' }), 'failures');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter assignments by role' }), 'assistant');
    expect(rowIds()).toEqual([ids[1], ids[2]]);
    await user.type(screen.getByRole('searchbox'), 'grep');
    expect(rowIds()).toEqual([ids[1]]);
    await user.click(screen.getByRole('button', { name: 'Grading' }));
    expect(rowIds()).toEqual([ids[1]]);
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    await user.type(screen.getByRole('searchbox'), ids[0]!);
    expect(rowIds()).toEqual([ids[0]]);
    await user.clear(screen.getByRole('searchbox'));
    await user.type(screen.getByRole('searchbox'), 'Diagnose a failed service');
    expect(rowIds()).toHaveLength(3);
    await user.type(screen.getByRole('searchbox'), 'not present');
    expect(screen.getByText('No assignments match these filters.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    expect(rowIds()).toHaveLength(3);
  });

  it('uses thin-publication deterministic grades and handles zero generation time', async () => {
    const user = userEvent.setup();
    renderTable([assignment(0, { semantic_grade_summaries: undefined, benchmark_observations: {
      grade_summaries: [{ criterion_id: 'answer-content', status: 'PASS', explanation_code: 'criterion_passed' }],
      timing: { generation_ms: { value: 0 } }, unavailable_reasons: [],
    } })]);
    await user.click(screen.getByRole('button', { name: 'Grading' }));
    expect(screen.getByRole('link', { name: 'Pass' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Performance' }));
    expect(screen.getByRole('table').querySelector('tbody [data-column="throughput"]')).toHaveTextContent('—');
  });
});
