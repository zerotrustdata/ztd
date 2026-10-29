// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Evaluations view — one tab at a time: evaluation Runs, Campaigns that group
// them, and the 3-role Formations catalog (Lite, Assistant, Primary). The
// catalog is reference data for system-lane campaigns, which execute and score
// each selected formation as one unit.
// Scores are never averaged across suites.

import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useStoreState } from '../state/store';
import { DataTable, type DataTableColumn } from '../components/DataTable';
import { ReleaseSelector } from '../components/ReleaseSelector';
import { matchesRelease } from '../content/release';
import {
  deriveCampaignSummaries,
  EVALUATION_FRAMEWORK_VALUES,
  FORMATION_CATALOG,
  type CampaignSummaryGroup,
  type FormationRoleBinding,
  type FormationTopologySpec,
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
import type { EvaluationSummary, ModelRole } from '../contract/types';
import {
  availableQualityFilterOptions,
  EVALUATION_QUALITY_FILTER_STATES,
  normalizeQualityFilter,
} from '../utils/feed-state';
import {
  campaignTerminalProgress,
  nativeResultTone,
  roleLabel,
  verifierStateTone,
} from './derived';
import { OutcomeBreakdown, PanelSection, TonePill } from '../components/evidence';
import { useSelectedRelease } from '../state/dataset';
import { evaluationPassRate } from '../utils/run-metrics';

type EvaluationsTab = 'runs' | 'campaigns' | 'formations';
type FormationKindFilter = 'all' | 'curated' | 'heterogeneous';

const TABS: readonly EvaluationsTab[] = ['runs', 'campaigns', 'formations'];

function FormationModelCell({ binding, role, datasetId }: {
  binding: FormationRoleBinding;
  role: ModelRole;
  datasetId?: string;
}) {
  const targetUrl = datasetId
    ? `/models/${datasetId}/${binding.variant_id}?role=${role}`
    : `/models/${binding.variant_id}?role=${role}`;
  return (
    <div className="eval-cell-stack">
      <Link to={targetUrl} title={binding.served_model_tag}>{binding.display_name}</Link>
      <span className="eval-cell-detail">
        {binding.quantization} · {formatNumber(binding.estimated_model_vram_mib + binding.estimated_kv_cache_mib)} MiB
      </span>
    </div>
  );
}

function runProgress(run: EvaluationSummary) {
  const terminal = campaignTerminalProgress(run);
  return {
    done: run.native_result?.passed_verdict_count ?? terminal.done,
    total: run.native_result?.required_verdict_count ?? terminal.total,
  };
}

export function EvaluationsView() {
  const [params, setParams] = useSearchParams();
  const [formationKind, setFormationKind] = useState<FormationKindFilter>('all');
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

  const filteredFormations = useMemo(() => {
    const q = formationSearch.trim().toLowerCase();
    return FORMATION_CATALOG.filter((f) => {
      if (formationKind !== 'all' && f.kind !== formationKind) return false;
      if (q) {
        const hay = `${f.display_name} ${f.id} ${f.description} ${f.primary.display_name} ${f.assistant.display_name} ${f.lite.display_name} ${f.primary.family} ${f.assistant.family} ${f.lite.family}`.toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }, [formationKind, formationSearch]);

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

  const defaultDatasetId = filtered[0]?.dataset_id ?? evaluations[0]?.dataset_id;
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

  const formationColumns = useMemo<DataTableColumn<FormationTopologySpec>[]>(() => [
    {
      id: 'formation', header: 'Formation', accessorKey: 'display_name',
      cell: ({ row: { original: formation } }) => (
        <div className="eval-cell-stack" data-testid={`formation-${formation.id}`} title={formation.description}>
          <strong>{formation.display_name}</strong>
          <span className="eval-cell-detail">{formation.kind}</span>
        </div>
      ),
    },
    ...(['lite', 'assistant', 'primary'] as const).map((role): DataTableColumn<FormationTopologySpec> => ({
      id: role, header: roleLabel(role), accessorFn: (formation) => formation[role].display_name,
      cell: ({ row }) => <FormationModelCell binding={row.original[role]} role={role} datasetId={defaultDatasetId} />,
    })),
    {
      id: 'vram', header: 'Total VRAM', accessorKey: 'total_vram_mib',
      cell: ({ row: { original: formation } }) => (
        <div className="eval-cell-stack eval-progress-cell">
          <span>{formatNumber(formation.total_vram_mib)} MiB</span>
          <div className="formation-vram-track" aria-hidden="true">
            <div className="formation-vram-fill" style={{ width: `${Math.min(100, formation.vram_utilization_pct * 100)}%` }} />
          </div>
        </div>
      ),
    },
  ], [defaultDatasetId]);

  const tabLabel: Record<EvaluationsTab, string> = {
    runs: `Runs (${formatNumber(filtered.length)})`,
    campaigns: `Campaigns (${formatNumber(campaignGroups.length)})`,
    formations: `Formations (${formatNumber(FORMATION_CATALOG.length)})`,
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

        {activeTab === 'formations' ? (
          <div className="eval-toolbar">
            <select
              aria-label="Filter formations by kind"
              value={formationKind}
              onChange={(e) => setFormationKind(e.target.value as FormationKindFilter)}
            >
              <option value="all">All kinds</option>
              <option value="curated">Curated</option>
              <option value="heterogeneous">Heterogeneous</option>
            </select>
            <input
              type="search"
              placeholder="Search formations"
              aria-label="Search formations"
              value={formationSearch}
              onChange={(e) => setFormationSearch(e.target.value)}
            />
          </div>
        ) : (
          <div className="eval-toolbar">
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
        )}
      </div>

      {activeTab === 'formations' ? (
        <PanelSection title="Formations">
          <details className="signal-fold signal-fold-inline">
            <summary className="signal-fold-summary">
              <span className="signal-fold-title">Catalog</span>
              <span className="signal-fold-fact">
                {formatNumber(filteredFormations.length)} shown · {formatNumber(EVALUATION_FRAMEWORK_VALUES.maxVramBudgetMib)} MiB budget
              </span>
            </summary>
            <p className="panel-note">
              Lite, Assistant, and Primary stacks. A system-lane campaign scores one stack as a unit.
              {formatNumber(FORMATION_CATALOG.length)} of {formatNumber(EVALUATION_FRAMEWORK_VALUES.totalFormationsCount)} catalogued stacks are listed.
            </p>
          </details>
          <DataTable data={filteredFormations} columns={formationColumns} pageSize={25} enableColumnVisibility={false}
            caption="Formation catalog" empty={<p className="table-empty">No formations match these filters.</p>} />
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
