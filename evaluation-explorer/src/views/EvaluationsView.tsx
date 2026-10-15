// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Evaluations view — cross-dataset evaluation history. Each row shows dataset,
// run/campaign identity, suite, progress, outcomes, verifier state, and metrics.

import { useEffect, useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useStoreState } from '../state/store';
import { DataTable, type DataTableColumn } from '../components/DataTable';
import { RoleLeadersPanel } from '../components/RoleLeadersPanel';
import { ReleaseSelector } from '../components/ReleaseSelector';
import { matchesRelease, releaseLabel } from '../content/release';
import {
  EmptyState,
  ProgressBar,
  QualityBadge,
  SectionHeading,
  StatTile,
  formatTimestamp,
  formatDuration,
  formatPercent,
} from '../components/shared';
import type { EvaluationSummary } from '../contract/types';
import {
  availableQualityFilterOptions,
  EVALUATION_QUALITY_FILTER_STATES,
  normalizeQualityFilter,
} from '../utils/feed-state';
import { campaignTerminalProgress, datasetLabel, nativeResultTone, verifierStateTone } from './derived';
import { OutcomeBreakdown, PanelSection, TonePill } from '../components/evidence';
import { useSelectedRelease } from '../state/dataset';

export function EvaluationsView() {
  const [params, setParams] = useSearchParams();

  const suiteFilter = params.get('suite') ?? 'all';
  const statusFilter = params.get('status') ?? 'all';
  const qualityFilter = params.get('quality') ?? 'all';
  const releaseFilter = useSelectedRelease();

  const evaluations = useStoreState((state) =>
    Array.from(state.evaluations.values()).sort((a, b) =>
      (b.started_at ?? b.observed_at ?? '').localeCompare(a.started_at ?? a.observed_at ?? ''),
    ),
  );
  const models = useStoreState((state) => Array.from(state.models.values()).filter((record) => matchesRelease(record, releaseFilter)));
  const catalogs = useStoreState((state) => Array.from(state.catalogs.values()).filter((record) => matchesRelease(record, releaseFilter)));
  const connection = useStoreState((state) => state.connection);
  const releaseEvaluations = useMemo(() => evaluations.filter((record) => matchesRelease(record, releaseFilter)), [evaluations, releaseFilter]);

  const qualityOptions = useMemo(
    () => availableQualityFilterOptions(releaseEvaluations, EVALUATION_QUALITY_FILTER_STATES),
    [releaseEvaluations],
  );
  const effectiveQualityFilter = normalizeQualityFilter(qualityFilter, qualityOptions);

  useEffect(() => {
    if (effectiveQualityFilter === qualityFilter) return;
    const next = new URLSearchParams(params);
    next.set('quality', effectiveQualityFilter);
    setParams(next, { replace: true });
  }, [effectiveQualityFilter, qualityFilter, params, setParams]);

  const filtered = useMemo(() => {
    return releaseEvaluations.filter((e) => {
      if (suiteFilter !== 'all' && e.suite_id !== suiteFilter) return false;
      if (statusFilter !== 'all' && e.lifecycle_state !== statusFilter) return false;
      if (effectiveQualityFilter !== 'all' && e.quality_state !== effectiveQualityFilter) return false;
      return true;
    });
  }, [releaseEvaluations, suiteFilter, statusFilter, effectiveQualityFilter]);

  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value === 'all') next.delete(key);
    else next.set(key, value);
    setParams(next);
  };

  const columns = useMemo<DataTableColumn<EvaluationSummary>[]>(
    () => [
      {
        id: 'dataset',
        header: 'Dataset',
        accessorKey: 'dataset_id',
        cell: ({ row }) => (
          <code className="dataset-id" title={row.original.dataset_id}>
            {datasetLabel(row.original.dataset_id)}
          </code>
        ),
      },
      {
        id: 'run',
        header: 'Run',
        accessorKey: 'run_id',
        cell: ({ row }) => (
          <Link to={`/evaluations/${row.original.dataset_id}/${row.original.run_id}`}>
            {row.original.run_id}
          </Link>
        ),
      },
      { id: 'suite', header: 'Suite', accessorKey: 'suite_id' },
      { id: 'release', header: 'Release', accessorFn: releaseLabel },
      { id: 'arm', header: 'Arm', accessorKey: 'arm' },
      {
        id: 'progress',
        header: 'Progress',
        cell: ({ row }) => {
          const terminal = campaignTerminalProgress(row.original);
          return (
            <ProgressBar
              completed={row.original.native_result?.passed_verdict_count ?? terminal.done}
              total={row.original.native_result?.required_verdict_count ?? terminal.total}
              label={`${row.original.run_id} progress`}
            />
          );
        },
        enableSorting: false,
      },
      {
        id: 'outcomes',
        header: 'Outcomes',
        cell: ({ row }) => row.original.native_result ? <>
          <TonePill tone={nativeResultTone(row.original.native_result.summary_status)}>{row.original.native_result.summary_status}</TonePill>
          <p>{row.original.native_result.passed_verdict_count}/{row.original.native_result.required_verdict_count} verdicts passed</p>
        </> : <OutcomeBreakdown outcomes={row.original.terminal_outcomes} />,
        enableSorting: false,
      },
      {
        id: 'verifier',
        header: 'Verifier',
        accessorKey: 'verifier_state',
        cell: ({ row }) => <TonePill tone={verifierStateTone(row.original.verifier_state)} title={row.original.verifier_failure_summary}>{row.original.verifier_state.replace(/_/g, ' ')}</TonePill>,
      },
      {
        id: 'started',
        header: 'Started',
        accessorFn: (row: EvaluationSummary) => row.started_at ?? '',
        cell: ({ row }) => (row.original.started_at ? formatTimestamp(row.original.started_at) : '—'),
      },
      {
        id: 'elapsed',
        header: 'Elapsed',
        accessorFn: (row: EvaluationSummary) => row.elapsed_seconds ?? 0,
        cell: ({ row }) => (row.original.elapsed_seconds !== undefined ? formatDuration(row.original.elapsed_seconds) : '—'),
      },
      {
        id: 'pass_rate',
        header: 'Pass rate',
        accessorFn: (row: EvaluationSummary) => row.headline_metrics.pass_rate?.value ?? -1,
        cell: ({ row }) =>
          row.original.headline_metrics.pass_rate?.value !== undefined
            ? formatPercent(row.original.headline_metrics.pass_rate.value)
            : '—',
      },
      {
        id: 'quality',
        header: 'Quality',
        cell: ({ row }) => <QualityBadge state={row.original.quality_state} />,
        enableSorting: false,
      },
    ],
    [],
  );

  return (
    <div className="evaluations-view">
      <SectionHeading
        kicker="EVALUATIONS"
        title="Evaluation runs"
        description="What has ztd been tested against, and did the evidence hold? Inspect conformance and model campaigns without averaging scores across suites."
      />

      <div className="stat-tile-grid summary-strip" aria-label="Filtered evaluation summary">
        <StatTile label="Runs" value={filtered.length} />
        <StatTile label="Assignments" value={filtered.reduce((sum, run) => sum + run.assignment_total, 0)} />
        <StatTile label="Verifier passed" value={filtered.filter((run) => run.verifier_state === 'passed').length} />
        <StatTile label="Verifier failed" value={filtered.filter((run) => run.verifier_state === 'failed').length} />
        <StatTile label="Verifier not run" value={filtered.filter((run) => run.verifier_state === 'not_run').length} />
        <StatTile label="Conformance runs" value={filtered.filter((run) => run.native_result).length} />
      </div>
      <RoleLeadersPanel models={models} catalogs={catalogs} evaluations={releaseEvaluations} />

      <div className="eval-toolbar">
        <ReleaseSelector />
        <select aria-label="Filter by suite" value={suiteFilter} onChange={(e) => setFilter('suite', e.target.value)}>
          <option value="all">All suites</option>
          {Array.from(new Set(evaluations.map((e) => e.suite_id))).map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
        <select aria-label="Filter by status" value={statusFilter} onChange={(e) => setFilter('status', e.target.value)}>
          <option value="all">All statuses</option>
          <option value="queued">Queued</option>
          <option value="running">Running</option>
          <option value="completed">Completed</option>
          <option value="failed">Failed</option>
          <option value="stopped">Stopped</option>
        </select>
        <select aria-label="Filter by quality" value={effectiveQualityFilter} onChange={(e) => setFilter('quality', e.target.value)}>
          <option value="all">All quality states</option>
          {qualityOptions.map((option) => (
            <option key={option.value} value={option.value}>{option.label}</option>
          ))}
        </select>
      </div>

      {filtered.length === 0 ? (
        <div>
        <EmptyState
          hasRecords={evaluations.length > 0}
          hasFilters={releaseFilter !== 'all' || suiteFilter !== 'all' || statusFilter !== 'all' || effectiveQualityFilter !== 'all'}
          connection={connection}
        />
        <p className="panel-note">Active filters: release {releaseFilter}, suite {suiteFilter}, status {statusFilter}, quality {effectiveQualityFilter}.</p>
        <Link to="/evaluations?release=all">Reset filters</Link>
        </div>
      ) : (
        <div className="program-groups">
          {(['Governance conformance', 'Model campaigns'] as const).map((program) => {
            const runs = filtered.filter((run) => Boolean(run.native_result) === (program === 'Governance conformance'));
            if (runs.length === 0) return null;
            return <PanelSection key={program} title={program}>
              <DataTable data={runs} columns={columns} pageSize={25} caption={program}
                renderCard={(run) => {
                  const progress = campaignTerminalProgress(run);
                  return <article className="run-card">
                    <Link to={`/evaluations/${run.dataset_id}/${run.run_id}`}>{run.run_id}</Link>
                    <p>{run.suite_id} · {releaseLabel(run)}</p>
                    <TonePill tone={verifierStateTone(run.verifier_state)} title={run.verifier_failure_summary}>{run.verifier_state.replace(/_/g, ' ')}</TonePill>
                    {run.native_result ? <TonePill tone={nativeResultTone(run.native_result.summary_status)}>{run.native_result.summary_status}</TonePill> : null}
                    <ProgressBar completed={run.native_result?.passed_verdict_count ?? progress.done} total={run.native_result?.required_verdict_count ?? progress.total} label={`${run.run_id} progress`} />
                    <QualityBadge state={run.quality_state} />
                  </article>;
                }} />
            </PanelSection>;
          })}
        </div>
      )}
    </div>
  );
}
