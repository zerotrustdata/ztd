// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TasksView } from '../src/views/TasksView';
import { SCENARIO_CATALOG_ID, SCENARIO_TASKS, scenarioTaskSourceUrl } from '../src/content/scenario-task';
import { evalStore } from '../src/state/store';
import { fixtureEnrichedAssignmentResult } from '../src/fixtures/fixtures';

function renderTasks(initialPath = '/tasks') {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Routes>
        <Route path="/tasks" element={<TasksView />} />
        <Route path="/tasks/:taskId" element={<TasksView />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('TasksView', () => {
  beforeEach(() => evalStore.loadFixtures([], []));
  it('uses the shared anchor behavior and marks the clicked category', () => {
    const scroll = vi.fn();
    renderTasks();
    const section = document.getElementById('category-tool_selection')!;
    section.scrollIntoView = scroll;
    const navigation = within(screen.getByRole('complementary', { name: 'Task catalog navigation' }));
    const link = navigation.getByRole('link', { name: /Tool selection/ });
    fireEvent.click(link);
    expect(scroll).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' });
    expect(link).toHaveAttribute('aria-current', 'location');
  });

  it('links recent task assignments across loaded datasets with their verdict tones', () => {
    evalStore.loadFixtures([
      { ...fixtureEnrichedAssignmentResult, dataset_id: 'one', task_id: 'tool-select-grep', assignment_id: 'recent-one', terminal_status: 'model_failed', verification_disposition: 'not_run' },
      { ...fixtureEnrichedAssignmentResult, dataset_id: 'two', task_id: 'tool-select-grep@1.0.0', assignment_id: 'recent-two', terminal_status: 'grader_failed', verification_disposition: 'not_run' },
    ], []);
    renderTasks('/tasks/tool-select-grep');
    expect(screen.getByRole('heading', { level: 1, name: 'tool-select-grep' })).toHaveClass('evidence-title');
    const recent = within(screen.getByRole('region', { name: 'Recent assignments of this task' }));
    expect(recent.getByRole('link', { name: 'recent-one' })).toHaveAttribute('href', expect.stringContaining('/evaluations/one/'));
    expect(recent.getByRole('link', { name: 'recent-two' })).toHaveAttribute('href', expect.stringContaining('/evaluations/two/'));
    expect(recent.getByText('model failed')).toHaveAttribute('data-tone', 'critical');
    expect(recent.getByText('grader failed')).toHaveAttribute('data-tone', 'warn');
  });

  it('omits recent assignments when none have been published for the task', () => {
    renderTasks('/tasks/tool-select-grep');
    expect(screen.queryByRole('region', { name: 'Recent assignments of this task' })).not.toBeInTheDocument();
  });
  it('lists every catalog task grouped by category on the overview', () => {
    renderTasks();

    expect(SCENARIO_TASKS).toHaveLength(41);
    expect(screen.getByRole('heading', { name: 'Task catalog' })).toBeInTheDocument();
    expect(screen.getByText(SCENARIO_CATALOG_ID)).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'View full definition' })).toHaveLength(SCENARIO_TASKS.length);
    expect(screen.getByRole('heading', { name: /Instruction adherence/i })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /Security & policy/i })).toBeInTheDocument();
    expect(screen.getByText(/Cards show a prompt excerpt/i)).toBeInTheDocument();
  });

  it('shows a detailed definition for a selected task with ztd source links', () => {
    const task = SCENARIO_TASKS.find((entry) => entry.id === 'tool-select-grep');
    expect(task).toBeDefined();

    renderTasks('/tasks/tool-select-grep');

    const panel = screen.getByRole('article');
    expect(within(panel).getByRole('heading', { name: 'tool-select-grep' })).toBeInTheDocument();
    expect(within(panel).getByText(task!.publicDescription)).toBeInTheDocument();
    expect(within(panel).getByText(task!.userPrompt)).toBeInTheDocument();
    expect(within(panel).getByRole('heading', { name: 'Expected tools' })).toBeInTheDocument();
    expect(within(panel).getByText(/offered every production tool/i)).toBeInTheDocument();
    expect(within(panel).getByText(/Fixed rules over trace evidence/i)).toBeInTheDocument();
    expect(within(panel).getByText(/User prompt sent to the agent/i)).toBeInTheDocument();
    expect(within(panel).getAllByText(/must call the expected tool with correct arguments/i).length).toBeGreaterThan(0);
    expect(within(panel).getByText(/Prompt hint:/i)).toBeInTheDocument();
    expect(within(panel).getByRole('link', { name: /Scenario definition in ztd/i })).toHaveAttribute(
      'href',
      scenarioTaskSourceUrl(task!),
    );
  });

  it('shows the eligible roles and the gold criteria for exactly those roles', () => {
    const task = SCENARIO_TASKS.find((entry) => entry.id === 'route-lite-triage')!;
    expect(task.eligibleRoles).toEqual(['lite']);
    renderTasks(`/tasks/${task.id}`);

    const panel = within(screen.getByRole('article'));
    const roles = within(panel.getByRole('group', { name: 'Eligible roles' }));
    expect(roles.getAllByText(/ role$/).map((flag) => flag.textContent)).toEqual(['Lite role']);
    const criteria = within(panel.getByLabelText('Role criteria'));
    expect(criteria.getByText('Lite.')).toBeInTheDocument();
    expect(criteria.queryByText('Primary.')).not.toBeInTheDocument();
    expect(criteria.queryByText('Assistant.')).not.toBeInTheDocument();
    expect(criteria.getByText(task.roleCriteria[0]!.criteria[0]!.description, { exact: false })).toBeInTheDocument();
    expect(panel.queryByText(/is not eligible/)).not.toBeInTheDocument();
  });

  it('lists eligible roles in role order with each role criterion and the judged requirements', () => {
    const task = SCENARIO_TASKS.find((entry) => entry.id === 'security-policy-deny-delete')!;
    renderTasks(`/tasks/${task.id}`);

    const panel = within(screen.getByRole('article'));
    const roles = within(panel.getByRole('group', { name: 'Eligible roles' }));
    expect(roles.getAllByText(/ role$/).map((flag) => flag.textContent)).toEqual(['Primary role', 'Assistant role', 'Lite role']);
    const criteria = panel.getByLabelText('Role criteria');
    expect(within(criteria).getAllByRole('listitem').map((item) => item.querySelector('strong')?.textContent)).toEqual(['Primary.', 'Assistant.', 'Lite.']);

    const judged = within(panel.getByLabelText('Judged requirements'));
    expect(judged.getAllByRole('listitem')).toHaveLength(task.judgedCriteria!.length);
    expect(judged.getByText(task.refusalCriterion!)).toBeInTheDocument();
    expect(judged.getAllByText('refusal')).toHaveLength(1);
  });

  it('omits judged requirements for a scenario without a judged content check', () => {
    const task = SCENARIO_TASKS.find((entry) => !entry.judgedCriteria?.length)!;
    renderTasks(`/tasks/${task.id}`);
    expect(screen.queryByLabelText('Judged requirements')).not.toBeInTheDocument();
  });

  it('publishes role criteria only for the roles each scenario is eligible for, in role order', () => {
    const order = ['primary', 'assistant', 'lite'];
    for (const task of SCENARIO_TASKS) {
      expect(task.eligibleRoles.length, task.id).toBeGreaterThan(0);
      expect(task.roleCriteria.map((entry) => entry.role), task.id).toEqual(task.eligibleRoles);
      expect([...task.eligibleRoles].sort((a, b) => order.indexOf(a) - order.indexOf(b)), task.id).toEqual(task.eligibleRoles);
      for (const entry of task.roleCriteria) expect(entry.criteria.length, `${task.id} ${entry.role}`).toBeGreaterThan(0);
      if (task.refusalCriterion) expect(task.judgedCriteria?.map((criterion) => criterion.id), task.id).toContain(task.refusalCriterion);
    }
  });

  it('redirects unknown task ids back to the overview', () => {
    renderTasks('/tasks/not-a-real-task');

    expect(screen.getByRole('heading', { name: 'Task catalog' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'not-a-real-task' })).not.toBeInTheDocument();
  });
});
