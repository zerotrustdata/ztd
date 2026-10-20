// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Run-scoped terminal failures for evaluation detail pages.

import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { DataTable, type DataTableColumn } from './DataTable';
import { EmptyState, QualityBadge, formatTimestamp } from './shared';
import { NON_VERDICT_NOTE, PanelSection, TonePill } from './evidence';
import type { AssignmentResult, TerminalStatus } from '../contract/types';
import type { FeedConnectionState } from '../utils/feed-state';
import { assignmentVerdictTone, isFailureTerminalStatus, isNonVerdictOutcome, roleLabel } from '../views/derived';
import { isCapabilityQualificationScenario } from '../content/scenario-task';

function observationLabel(value: string): string {
  return value.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
}

type RunFailuresSectionProps = {
  assignments: AssignmentResult[];
  connection: FeedConnectionState;
};

export function RunFailuresSection({ assignments, connection }: RunFailuresSectionProps) {
  const [statusFilter, setStatusFilter] = useState<TerminalStatus | 'all'>('all');

  const failures = useMemo(() => {
    return assignments
      .filter((a) => isFailureTerminalStatus(a.terminal_status))
      .filter((a) => statusFilter === 'all' || a.terminal_status === statusFilter)
      .sort((a, b) => (b.observed_at ?? '').localeCompare(a.observed_at ?? ''));
  }, [assignments, statusFilter]);

  const columns = useMemo<DataTableColumn<AssignmentResult>[]>(
    () => [
      {
        id: 'assignment',
        header: 'Assignment',
        accessorKey: 'assignment_id',
        cell: ({ row }) => (
          <Link
            to={`/evaluations/${row.original.dataset_id}/${row.original.run_id}/assignments/${row.original.assignment_id}`}
          >
            {row.original.assignment_id}
          </Link>
        ),
      },
      { id: 'task', header: 'Task', accessorKey: 'task_id' },
      {
        id: 'scope',
        header: 'Scope',
        accessorFn: (row: AssignmentResult) => isCapabilityQualificationScenario(row.scenario_summary?.expected_tools) ? 'capability qualification' : '',
        cell: ({ row }) => isCapabilityQualificationScenario(row.original.scenario_summary?.expected_tools)
          ? <TonePill tone="warn" title="This result measures direct command handling, not investigation context retrieval or continuity.">Capability qualification</TonePill>
          : '—',
      },
      { id: 'variant', header: 'Variant', accessorKey: 'variant_id' },
      {
        id: 'role',
        header: 'Role',
        accessorKey: 'role',
        cell: ({ row }) => roleLabel(row.original.role),
      },
      {
        id: 'status',
        header: 'Failure class',
        accessorKey: 'terminal_status',
        cell: ({ row }) => <TonePill tone={assignmentVerdictTone(row.original.terminal_status, row.original.verification_disposition)} title={isNonVerdictOutcome(row.original.terminal_status) ? NON_VERDICT_NOTE : undefined}>{observationLabel(row.original.terminal_status)}</TonePill>,
      },
      {
        id: 'category',
        header: 'Scenario category',
        accessorFn: (row: AssignmentResult) => row.scenario_category ?? '',
        cell: ({ row }) =>
          row.original.scenario_category ? observationLabel(row.original.scenario_category) : '—',
      },
      {
        id: 'observed',
        header: 'Observed',
        accessorFn: (row: AssignmentResult) => row.observed_at ?? '',
        cell: ({ row }) =>
          row.original.observed_at ? formatTimestamp(row.original.observed_at) : '—',
      },
      {
        id: 'quality',
        header: 'Quality',
        accessorKey: 'quality_state',
        cell: ({ row }) => (
          <QualityBadge state={row.original.quality_state} />
        ),
      },
    ],
    [],
  );

  return (
    <PanelSection className="run-failures" title="Terminal failures" note="All terminal failures remain in the dataset. Infrastructure and evidence failures are not counted for or against the model.">

      <div className="view-toolbar">
        <label className="filter-control">
          <span>Failure class</span>
          <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as TerminalStatus | 'all')}>
            <option value="all">All failure classes</option>
            <option value="model_failed">Model failed</option>
            <option value="grader_failed">Grader failed</option>
            <option value="invalid_evidence">Invalid evidence</option>
            <option value="provider_failed">Provider failed</option>
            <option value="execution_failed">Execution failed</option>
            <option value="escalated">Escalated</option>
            <option value="stopped">Stopped</option>
          </select>
        </label>
      </div>

      {failures.length === 0 ? (
        <EmptyState hasRecords={assignments.length > 0} hasFilters={statusFilter !== 'all'} connection={connection} />
      ) : (
        <DataTable columns={columns} data={failures} caption="Terminal failures for this run" />
      )}
    </PanelSection>
  );
}
