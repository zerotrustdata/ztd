// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Evaluations view — one tab at a time: evaluation Runs, Campaigns that group
// them, and published frozen formations, scored as complete stacks.
// Scores are never averaged across suites.

import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useStoreState } from '../state/store';
import { DataTable, type DataTableColumn } from '../components/DataTable';
import { ReleaseSelector } from '../components/ReleaseSelector';
import { matchesRelease } from '../content/release';
import {
  deriveCampaignSummaries,
  type CampaignSummaryGroup,
} from '../content/framework-catalog';
import {
  EmptyState,
  ProgressBar,
  QualityBadge,
  SectionHeading,
  formatTimestamp,
  formatPercent,
  formatNumber,
} from '../components/shared';
import type { EvaluationSummary } from '../contract/types';
import {
  availableQualityFilterOptions,
  EVALUATION_QUALITY_FILTER_STATES,
  normalizeQualityFilter,
} from '../utils/feed-state';
import {
  campaignTerminalProgress,
  nativeResultTone,
  verifierStateTone,
} from './derived';
import { OutcomeBreakdown, PanelSection, TonePill } from '../components/evidence';
import { useSelectedRelease } from '../state/dataset';
import { evaluationPassRate } from '../utils/run-metrics';
import { formationGroups } from './formations';
import { FormationTable } from '../components/FormationTable';

type EvaluationsTab = 'runs' | 'campaigns' | 'formations';

const TABS: readonly EvaluationsTab[] = ['runs', 'campaigns', 'formations'];

function runProgress(run: EvaluationSummary) {
  const terminal = campaignTerminalProgress(run);
  return {
    done: run.native_result?.passed_verdict_count ?? terminal.done,
    total: run.native_result?.required_verdict_count ?? terminal.total,
  };
}

export function EvaluationsView() {
  const [params, setParams] = useSearchParams();
  const [formationSearch, setFormationSearch] = useState('');

  const requestedTab = params.get('view');
  const activeTab: EvaluationsTab = TABS.includes(requestedTab as EvaluationsTab)
    ? (requestedTab as EvaluationsTab)
    : 'runs';
  const suiteFilter = params.get('suite') ?? 'all';
  const statusFilter = params.get('status') ?? 'all';
  const qualityFilter = params.get('quality') ?? 'all';
  const releaseFilter = useSelectedRelease('all');

  const evaluations = useStoreState((state) =>
    Array.from(state.evaluations.values()).sort((a, b) =>
      (b.started_at ?? b.observed_at ?? '').localeCompare(a.started_at ?? a.observed_at ?? ''),
    ),
  );
  const connection = useStoreState((state) => state.connection);
  const releaseEvaluations = useMemo(
    () => evaluations.filter((record) => matchesRelease(record, releaseFilter)),
    [evaluations, releaseFilter],
  );

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

  const campaignGroups = useMemo(() => deriveCampaignSummaries(filtered), [filtered]);

  const formations = useMemo(() => formationGroups(filtered), [filtered]);
  const models = useStoreState((state) => Array.from(state.models.values()));
  const filteredFormations = useMemo(() => {
    const q = formationSearch.trim().toLowerCase();
    return formations.filter((formation) => {
      const variants = Object.values(formation.summary.model_role_mapping);
      const memberNames = models.filter((m) => variants.includes(m.variant_id) && formation.sources.some(({ run }) => run.dataset_id === m.dataset_id))
        .map((m) => `${m.display_name} ${m.served_model_tag ?? ''} ${m.model_family ?? ''}`).join(' ');
      return `${formation.summary.stack_id} ${formation.suite_id} ${variants.join(' ')} ${memberNames}`.toLowerCase().includes(q);
    });
  }, [formations, formationSearch, models]);

  const setFilter = (key: string, value: string, fallback = 'all') => {
    const next = new URLSearchParams(params);
    if (value === fallback) next.delete(key);
    else next.set(key, value);
    setParams(next);
  };

  const hasFilters =
    releaseFilter !== 'all' || suiteFilter !== 'all' || statusFilter !== 'all' || effectiveQualityFilter !== 'all';
  const resetFiltersUrl = `/evaluations?release=all${activeTab === 'runs' ? '' : `&view=${activeTab}`}`;

  const columns = useMemo<DataTableColumn<EvaluationSummary>[]>(
    () => [
      {
        id: 'run',
        header: 'Run',
        accessorKey: 'run_id',
        cell: ({ row }) => (
          <Link to={`/evaluations/${row.original.dataset_id}/${row.original.run_id}`} title={row.original.dataset_id}>
            {row.original.run_id}
          </Link>
        ),
      },
      { id: 'suite', header: 'Suite', accessorKey: 'suite_id' },
      { id: 'status', header: 'Status', accessorKey: 'lifecycle_state' },
      {
        id: 'progress',
        header: 'Progress',
        accessorFn: (run: EvaluationSummary) => {
          const { done, total } = runProgress(run);
          return total > 0 ? done / total : -1;
        },
        cell: ({ row }) => {
          const progress = runProgress(row.original);
          return (
            <div className="eval-progress-cell">
              <ProgressBar completed={progress.done} total={progress.total} label={`${row.original.run_id} progress`} />
            </div>
          );
        },
      },
      {
        id: 'outcomes',
        header: 'Outcomes',
        accessorFn: (run: EvaluationSummary) => run.native_result?.summary_status ?? run.terminal_outcomes.completed,
        cell: ({ row }) =>
          row.original.native_result ? (
            <>
              <TonePill tone={nativeResultTone(row.original.native_result.summary_status)}>
                {row.original.native_result.summary_status}
              </TonePill>
              <p>
                {row.original.native_result.passed_verdict_count}/
                {row.original.native_result.required_verdict_count} verdicts passed
              </p>
            </>
          ) : (
            <OutcomeBreakdown outcomes={row.original.terminal_outcomes} scope="run" />
          ),
      },
      {
        id: 'verifier',
        header: 'Verifier',
        accessorKey: 'verifier_state',
        cell: ({ row }) => (
          <TonePill
            tone={verifierStateTone(row.original.verifier_state)}
            title={row.original.verifier_failure_summary}
          >
            {row.original.verifier_state.replace(/_/g, ' ')}
          </TonePill>
        ),
      },
      {
        id: 'pass_rate',
        header: 'Pass rate',
        accessorFn: (row: EvaluationSummary) => evaluationPassRate(row)?.value ?? -1,
        cell: ({ row }) => {
          const rate = evaluationPassRate(row.original)?.value;
          return rate !== undefined ? formatPercent(rate) : '—';
        },
      },
      {
        id: 'quality',
        header: 'Quality',
        accessorKey: 'quality_state',
        cell: ({ row }) => <QualityBadge state={row.original.quality_state} />,
      },
      {
        id: 'started',
        header: 'Started',
        accessorFn: (row: EvaluationSummary) => row.started_at ?? '',
        cell: ({ row }) => (row.original.started_at ? formatTimestamp(row.original.started_at) : '—'),
      },
    ],
    [],
  );

  const campaignColumns = useMemo<DataTableColumn<CampaignSummaryGroup>[]>(() => [
    {
      id: 'campaign', header: 'Campaign', accessorKey: 'title',
      cell: ({ row: { original: group } }) => (
        <Link to={`/evaluations/${group.runs[0]!.dataset_id}/${group.runs[0]!.run_id}`} title={group.campaign_id}>
          {group.title}
        </Link>
      ),
    },
    { id: 'suites', header: 'Suites', accessorFn: (group) => group.suites.join(', ') },
    { id: 'runs', header: 'Runs', accessorKey: 'total_runs' },
    {
      id: 'progress', header: 'Progress',
      accessorFn: (group) => group.total_assignments > 0 ? (group.completed_assignments + group.failed_assignments) / group.total_assignments : -1,
      cell: ({ row: { original: group } }) => (
        <div className="eval-progress-cell">
          <ProgressBar completed={group.completed_assignments + group.failed_assignments}
            total={group.total_assignments} label={`${group.campaign_id} assignments`} />
        </div>
      ),
    },
    {
      id: 'verification', header: 'Verified', accessorKey: 'verified_runs',
      cell: ({ row: { original: group } }) => (
        <TonePill tone={group.verified_runs === group.total_runs ? 'ok' : group.verifier_failed_runs > 0 ? 'critical' : 'neutral'}>
          {group.verified_runs}/{group.total_runs}
        </TonePill>
      ),
    },
    {
      id: 'pass_rate', header: 'Median pass rate', accessorFn: (group) => group.pass_rate_median ?? -1,
      cell: ({ row: { original: group } }) => group.pass_rate_median === undefined ? '—' : formatPercent(group.pass_rate_median),
    },
    {
      id: 'latest', header: 'Latest run', accessorFn: (group) => group.latest_activity ?? '',
      cell: ({ row: { original: group } }) => group.latest_activity ? formatTimestamp(group.latest_activity) : '—',
    },
  ], []);

  const tabLabel: Record<EvaluationsTab, string> = {
    runs: `Runs (${formatNumber(filtered.length)})`,
    campaigns: `Campaigns (${formatNumber(campaignGroups.length)})`,
    formations: `Formations (${formatNumber(formations.length)})`,
  };

  return (
    <div className="evaluations-view">
      <SectionHeading
        title="Evaluations"
        description="Runs, the campaigns that group them, and the formation catalog. Scores stay with their suite."
      />

      <div className="eval-tabs-row">
        <div className="eval-tabs" role="tablist" aria-label="Evaluation sections">
          {TABS.map((tab) => (
            <button
              key={tab}
              type="button"
              role="tab"
              aria-selected={activeTab === tab}
              className={activeTab === tab ? 'eval-tab active' : 'eval-tab'}
              onClick={() => setFilter('view', tab, 'runs')}
            >
              {tabLabel[tab]}
            </button>
          ))}
        </div>

        <div className="eval-toolbar">
            {activeTab === 'formations' ? <input type="search" placeholder="Search formations" aria-label="Search formations"
              value={formationSearch} onChange={(e) => setFormationSearch(e.target.value)} /> : null}
            <ReleaseSelector defaultRelease="all" />
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
            <select
              aria-label="Filter by quality"
              value={effectiveQualityFilter}
              onChange={(e) => setFilter('quality', e.target.value)}
            >
              <option value="all">All quality states</option>
              {qualityOptions.map((option) => (
                <option key={option.value} value={option.value}>{option.label}</option>
              ))}
            </select>
        </div>
      </div>

      {activeTab === 'formations' ? (
        <PanelSection title="Formations">
          <p className="panel-note">Frozen formations from published runs. Each stack is scored as one unit; suites and frozen stack versions remain separate.</p>
          <FormationTable formations={filteredFormations} />
        </PanelSection>
      ) : filtered.length === 0 ? (
        <div>
          <EmptyState hasRecords={evaluations.length > 0} hasFilters={hasFilters} connection={connection} />
          {hasFilters && evaluations.length > 0 ? <Link to={resetFiltersUrl}>Reset filters</Link> : null}
        </div>
      ) : (
        <>
          {activeTab === 'campaigns' ? (
            <PanelSection title="Campaigns">
              <DataTable data={campaignGroups} columns={campaignColumns} pageSize={25} enableColumnVisibility={false}
                caption="Campaigns" />
            </PanelSection>
          ) : (
            <div className="program-groups">
              {(['Governance conformance', 'Model campaigns'] as const).map((program) => {
                const runs = filtered.filter(
                  (run) => Boolean(run.native_result) === (program === 'Governance conformance'),
                );
                if (runs.length === 0) return null;
                return (
                  <PanelSection key={program} title={program}>
                    <DataTable data={runs} columns={columns} pageSize={25} enableColumnVisibility={false}
                      caption={program} />
                  </PanelSection>
                );
              })}
            </div>
          )}
        </>
      )}
    </div>
  );
}
