// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import type { AssignmentResult } from '../contract/types';
import { DataTable, type DataTableColumn } from './DataTable';
import { NON_VERDICT_NOTE } from './evidence';
import { abbreviateAssignmentId, formatLatency, formatNumber, formatThroughput } from '../utils/format';
import { assignmentGradeChips, assignmentOutputThroughput, assignmentVerdictTone, isFailureTerminalStatus, isNonVerdictOutcome, roleLabel } from '../views/derived';

type TableView = 'results' | 'grading' | 'performance';
type AssignmentRow = ReturnType<typeof assignmentRow>;
const GRADE_ORDER: Record<string, number> = { fail: 0, invalid_evidence: 1, unsupported: 2, unavailable: 3, pass: 4 };
const VIEW_COLUMNS: Record<TableView, readonly string[]> = {
  results: ['task', 'status', 'role', 'checks', 'duration', 'output', 'model', 'assignment'],
  grading: ['task', 'status', 'role', 'checks', 'verification', 'assignment'],
  performance: ['task', 'status', 'role', 'duration', 'generation', 'throughput', 'input', 'output', 'calls', 'retries', 'guidedRetries', 'model', 'assignment'],
};

function assignmentRow(assignment: AssignmentResult) {
  const grades = new Map(assignmentGradeChips(assignment).map((grade) => [grade.criterion_id, grade]));
  const passed = [...grades.values()].filter((grade) => grade.status === 'pass').length;
  const failed = [...grades.values()].filter((grade) => grade.status === 'fail').length;
  const calls = assignment.activity_summary?.tool_calls;
  return {
    assignment, grades, passed, failed,
    ungraded: new Set([...(assignment.scenario_summary?.criteria ?? []).map((criterion) => criterion.criterion_id), ...grades.keys()]).size - passed - failed,
    passRate: passed + failed > 0 ? passed / (passed + failed) : undefined,
    toolCalls: calls?.availability === 'observed' ? calls.records.length : undefined,
    description: assignment.scenario_summary?.public_description,
  };
}

function assignmentHref(assignment: AssignmentResult) {
  return `/evaluations/${assignment.dataset_id}/${assignment.run_id}/assignments/${assignment.assignment_id}`;
}

function missing(reason = 'Not published') {
  return <span className="assignment-missing" title={reason} aria-label={reason}>—</span>;
}

function numericColumn(id: string, header: string, value: (row: AssignmentRow) => number | undefined, format = formatNumber): DataTableColumn<AssignmentRow> {
  return {
    id, header, accessorFn: value, sortUndefined: 'last', sortDescFirst: true,
    cell: ({ row }) => {
      const observed = value(row.original);
      return observed === undefined ? missing() : <span className="assignment-number">{format(observed)}</span>;
    },
  };
}

export function AssignmentTable({ assignments, runId }: { assignments: AssignmentResult[]; runId: string }) {
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('all');
  const [role, setRole] = useState('all');
  const [view, setView] = useState<TableView>('results');
  const [visibilityOverrides, setVisibilityOverrides] = useState<Record<string, boolean>>({});
  const rows = useMemo(() => assignments.map(assignmentRow), [assignments]);
  // Build the column catalog from the whole run, never from the filtered rows.
  const criteria = useMemo(() => {
    const labels = new Map<string, string>();
    for (const { assignment, grades } of rows) {
      for (const grade of grades.values()) labels.set(grade.criterion_id, grade.label);
      for (const criterion of assignment.scenario_summary?.criteria ?? []) if (!labels.has(criterion.criterion_id)) labels.set(criterion.criterion_id, criterion.public_label);
    }
    return [...labels].sort((a, b) => a[1].localeCompare(b[1]));
  }, [rows]);
  const roles = useMemo(() => [...new Set(assignments.map((assignment) => assignment.role))].sort(), [assignments]);
  const statuses = useMemo(() => [...new Set(assignments.map((assignment) => assignment.terminal_status))].sort(), [assignments]);
  const columns = useMemo<DataTableColumn<AssignmentRow>[]>(() => [
    {
      id: 'task', header: 'Task', accessorFn: (row) => row.assignment.task_id, enableHiding: false,
      cell: ({ row: { original: row } }) => <div className="assignment-task-cell">
        <Link to={assignmentHref(row.assignment)} title={row.description}>{row.assignment.task_id}</Link>
        {row.assignment.failure_reason ? <span className="assignment-failure-reason" title={row.assignment.failure_reason}>{row.assignment.failure_reason}</span> : null}
      </div>,
    },
    {
      id: 'status', header: 'Outcome', accessorFn: (row) => row.assignment.terminal_status,
      cell: ({ row: { original: { assignment } } }) => <span className="assignment-result" data-tone={assignmentVerdictTone(assignment.terminal_status, assignment.verification_disposition)} title={isNonVerdictOutcome(assignment.terminal_status) ? NON_VERDICT_NOTE : undefined}>{assignment.terminal_status.replace(/_/g, ' ')}</span>,
    },
    { id: 'role', header: 'Role', accessorFn: (row) => row.assignment.role, cell: ({ row }) => roleLabel(row.original.assignment.role) },
    {
      id: 'checks', header: 'Checks passed', accessorFn: (row) => row.passRate, sortUndefined: 'last', sortDescFirst: false,
      cell: ({ row: { original: row } }) => row.grades.size === 0 ? missing('No grades published') : <span className="assignment-checks" title={`${row.passed} passed, ${row.failed} failed, ${row.ungraded} without a pass/fail verdict`}>
        <span className="assignment-number">{row.passed + row.failed > 0 ? `${row.passed} / ${row.passed + row.failed}` : '—'}</span>
        {row.ungraded > 0 ? <small>{row.ungraded} ungraded</small> : null}
      </span>,
    },
    ...criteria.map(([id, label]): DataTableColumn<AssignmentRow> => ({
      id: `criterion:${id}`, header: label, sortDescFirst: false, sortUndefined: 'last',
      accessorFn: (row) => { const grade = row.grades.get(id); return grade ? GRADE_ORDER[grade.status] : undefined; },
      cell: ({ row }) => {
        const grade = row.original.grades.get(id);
        if (!grade) return missing('No grade published for this criterion');
        return <Link className="assignment-grade" data-grade={grade.status} to={`${assignmentHref(row.original.assignment)}#assignment-grades`} title={[grade.explanation, grade.detail].filter(Boolean).join(': ')}>
          {grade.status.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase())}
        </Link>;
      },
    })),
    numericColumn('duration', 'Duration', (row) => row.assignment.resource_summary?.latency_ms?.value, formatLatency),
    numericColumn('generation', 'Generation', (row) => row.assignment.benchmark_observations?.timing?.generation_ms?.value, formatLatency),
    numericColumn('throughput', 'Output rate', (row) => assignmentOutputThroughput(row.assignment), formatThroughput),
    numericColumn('input', 'Input tokens', (row) => row.assignment.resource_summary?.input_tokens?.value),
    numericColumn('output', 'Output tokens', (row) => row.assignment.resource_summary?.output_tokens?.value),
    numericColumn('thinking', 'Thinking tokens', (row) => row.assignment.resource_summary?.thinking_tokens?.value),
    numericColumn('calls', 'Tool calls', (row) => row.toolCalls),
    numericColumn('retries', 'Retries', (row) => row.assignment.resource_summary?.retries?.value),
    numericColumn('guidedRetries', 'Guided retries', (row) => row.assignment.guided_retry_count),
    numericColumn('rep', 'Repetition', (row) => row.assignment.repetition),
    { id: 'model', header: 'Model', accessorFn: (row) => row.assignment.variant_id, cell: ({ row }) => <span className="assignment-model" title={row.original.assignment.variant_id}>{row.original.assignment.variant_id}</span> },
    {
      id: 'verification', header: 'Verification', accessorFn: (row) => row.assignment.verification_disposition, sortUndefined: 'last',
      cell: ({ row }) => row.original.assignment.verification_disposition?.replace(/_/g, ' ') ?? missing(),
    },
    { id: 'failure', header: 'Failure reason', accessorFn: (row) => row.assignment.failure_reason, sortUndefined: 'last', cell: ({ row }) => row.original.assignment.failure_reason ?? missing('No failure reason published') },
    { id: 'description', header: 'Task description', accessorFn: (row) => row.description, sortUndefined: 'last', cell: ({ row }) => row.original.description ?? missing() },
    { id: 'trajectory', header: 'Trajectory', accessorFn: (row) => row.assignment.trajectory_outcome, sortUndefined: 'last', cell: ({ row }) => row.original.assignment.trajectory_outcome?.replace(/_/g, ' ') ?? missing() },
    {
      id: 'assignment', header: 'Assignment', accessorFn: (row) => row.assignment.assignment_id,
      cell: ({ row }) => <Link className="assignment-short-id" to={assignmentHref(row.original.assignment)} title={row.original.assignment.assignment_id} aria-label={row.original.assignment.assignment_id}>{abbreviateAssignmentId(row.original.assignment.assignment_id)}</Link>,
    },
  ], [criteria]);
  const visibility = Object.fromEntries(columns.map((column) => [column.id!,
    visibilityOverrides[column.id!] ?? (VIEW_COLUMNS[view].includes(column.id!) || (view === 'grading' && column.id!.startsWith('criterion:'))),
  ]));
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return rows.filter(({ assignment, description, grades }) => {
      if (role !== 'all' && assignment.role !== role) return false;
      const verdict = assignment.terminal_status === 'completed' || assignment.terminal_status === 'model_failed';
      if (status === 'verdict' && !verdict) return false;
      if (status === 'non-verdict' && !isNonVerdictOutcome(assignment.terminal_status)) return false;
      if (status === 'failures' && !isFailureTerminalStatus(assignment.terminal_status)) return false;
      if (!['all', 'verdict', 'non-verdict', 'failures'].includes(status) && assignment.terminal_status !== status) return false;
      return [assignment.assignment_id, assignment.task_id, assignment.variant_id, assignment.role, description, assignment.failure_reason,
        ...[...grades.values()].flatMap((grade) => [grade.criterion_id, grade.explanation, grade.detail]),
      ].some((value) => value?.toLowerCase().includes(needle));
    });
  }, [rows, search, status, role]);

  if (assignments.length === 0) return <p>No assignment results for this run.</p>;
  return <div className={`assignment-table assignment-table-${view}`}>
    <div className="assignment-table-controls">
      <div className="assignment-table-views" role="group" aria-label="Assignment table view">
        {(['results', 'grading', 'performance'] as const).map((mode) => <button key={mode} type="button" aria-pressed={view === mode} onClick={() => { setView(mode); setVisibilityOverrides({}); }}>{mode.charAt(0).toUpperCase() + mode.slice(1)}</button>)}
      </div>
      <span className="assignment-result-count" role="status">{formatNumber(filtered.length)} of {formatNumber(assignments.length)} assignments</span>
    </div>
    <div className="table-toolbar evaluation-assignment-filters">
      <input type="search" placeholder="Search tasks, models, IDs, or failure details…" aria-label="Search assignments" value={search} onChange={(event) => setSearch(event.target.value)} />
      <select aria-label="Filter assignments by verdict" value={status} onChange={(event) => setStatus(event.target.value)}>
        <option value="all">All outcomes</option><option value="failures">All failures</option><option value="verdict">Model verdicts</option><option value="non-verdict">Non-verdict outcomes</option>
        {statuses.map((value) => <option key={value} value={value}>{value.replace(/_/g, ' ')}</option>)}
      </select>
      <select aria-label="Filter assignments by role" value={role} onChange={(event) => setRole(event.target.value)}>
        <option value="all">All roles</option>{roles.map((value) => <option key={value} value={value}>{roleLabel(value)}</option>)}
      </select>
      {search || status !== 'all' || role !== 'all' ? <button className="assignment-reset" type="button" onClick={() => { setSearch(''); setStatus('all'); setRole('all'); }}>Clear filters</button> : null}
    </div>
    {view === 'grading' ? <p className="assignment-table-note">Each criterion is sortable. Select a result for its grading evidence. — means no grade was published.</p> : null}
    {view === 'performance' ? <p className="assignment-table-note">Measured values only. Output rate uses generation time. — means not published; zero means an observed zero.</p> : null}
    <DataTable key={view} data={filtered} columns={columns} pageSize={50} caption={`Assignments for ${runId}`}
      columnVisibility={visibility} onColumnVisibilityChange={(update) => setVisibilityOverrides(typeof update === 'function' ? update(visibility) : update)}
      empty={<p>No assignments match these filters.</p>} />
  </div>;
}
