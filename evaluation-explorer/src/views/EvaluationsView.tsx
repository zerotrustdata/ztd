// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Evaluations view — the Evaluation Framework hub.
// Surfaces Evaluation Framework values and architecture across three core
// pillars:
//   1. Campaigns (program rollouts, suites, arms, assignment totals, verifier health)
//   2. Evaluation Runs (cross-dataset Governance Conformance and Model/System runs)
//   3. Formations (execution-topologies.v1 3-role Lite -> Assistant -> Primary stacks & VRAM budgets)

import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useStoreState } from '../state/store';
import { DataTable, type DataTableColumn } from '../components/DataTable';
import { ReleaseSelector } from '../components/ReleaseSelector';
import { matchesRelease, releaseLabel } from '../content/release';
import {
  deriveCampaignSummaries,
  EVALUATION_FRAMEWORK_VALUES,
  FORMATION_CATALOG,
  formatParameterCount,
  type CampaignSummaryGroup,
  type FormationRoleBinding,
  type FormationTopologySpec,
} from '../content/framework-catalog';
import {
  EmptyState,
  ProgressBar,
  QualityBadge,
  SectionHeading,
  StatTile,
  formatTimestamp,
  formatDuration,
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
  datasetLabel,
  nativeResultTone,
  roleLabel,
  verifierStateTone,
} from './derived';
import { OutcomeBreakdown, PanelSection, TonePill } from '../components/evidence';
import { useSelectedRelease } from '../state/dataset';

type FrameworkSectionTab = 'all' | 'campaigns' | 'runs' | 'formations';
type FormationKindFilter = 'all' | 'curated' | 'heterogeneous';

function FormationModelCell({ binding, role, datasetId }: {
  binding: FormationRoleBinding;
  role: ModelRole;
  datasetId?: string;
}) {
  const targetUrl = datasetId
    ? `/models/${datasetId}/${binding.variant_id}?role=${role}`
    : `/models/${binding.variant_id}?role=${role}`;
  return (
    <div className="eval-cell-stack eval-model-cell">
      <Link to={targetUrl}>{binding.display_name}</Link>
      <code>{binding.served_model_tag}</code>
      <span className="eval-cell-detail">
        {binding.family} · {formatParameterCount(binding.parameter_count)} · {binding.quantization}
      </span>
      <span className="eval-cell-detail">{binding.provider} · {binding.trust}</span>
      <span className="eval-cell-detail">
        Weights {formatNumber(binding.estimated_model_vram_mib)} + KV {formatNumber(binding.estimated_kv_cache_mib)} MiB
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
  const [activeTab, setActiveTab] = useState<FrameworkSectionTab>('all');
  const [formationKind, setFormationKind] = useState<FormationKindFilter>('all');
  const [formationSearch, setFormationSearch] = useState('');

  const suiteFilter = params.get('suite') ?? 'all';
  const statusFilter = params.get('status') ?? 'all';
  const qualityFilter = params.get('quality') ?? 'all';
  const releaseFilter = useSelectedRelease();

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
      {
        id: 'campaign',
        header: 'Campaign',
        accessorFn: (row: EvaluationSummary) => row.campaign_id ?? '—',
        cell: ({ row }) =>
          row.original.campaign_id ? (
            <code className="campaign-id-chip">{row.original.campaign_id}</code>
          ) : (
            <span className="muted-dash">—</span>
          ),
      },
      { id: 'suite', header: 'Suite', accessorKey: 'suite_id' },
      { id: 'release', header: 'Release', accessorFn: releaseLabel },
      { id: 'arm', header: 'Arm', accessorKey: 'arm' },
      { id: 'status', header: 'Status', accessorKey: 'lifecycle_state' },
      { id: 'assignments', header: 'Assignments', accessorKey: 'assignment_total' },
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
            <ProgressBar
              completed={progress.done}
              total={progress.total}
              label={`${row.original.run_id} progress`}
            />
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
            <OutcomeBreakdown outcomes={row.original.terminal_outcomes} />
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
        id: 'started',
        header: 'Started',
        accessorFn: (row: EvaluationSummary) => row.started_at ?? '',
        cell: ({ row }) => (row.original.started_at ? formatTimestamp(row.original.started_at) : '—'),
      },
      {
        id: 'elapsed',
        header: 'Elapsed',
        accessorFn: (row: EvaluationSummary) => row.elapsed_seconds ?? -1,
        cell: ({ row }) =>
          row.original.elapsed_seconds !== undefined ? formatDuration(row.original.elapsed_seconds) : '—',
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
        accessorKey: 'quality_state',
        cell: ({ row }) => <QualityBadge state={row.original.quality_state} />,
      },
    ],
    [],
  );

  const defaultDatasetId = filtered[0]?.dataset_id ?? evaluations[0]?.dataset_id;
  const campaignColumns = useMemo<DataTableColumn<CampaignSummaryGroup>[]>(() => [
    {
      id: 'campaign', header: 'Campaign', accessorKey: 'title',
      cell: ({ row: { original: group } }) => (
        <div className="eval-cell-stack eval-identity-cell">
          <Link to={`/evaluations/${group.runs[0]!.dataset_id}/${group.runs[0]!.run_id}`}>{group.title}</Link>
          <code className="campaign-id-chip">{group.campaign_id}</code>
        </div>
      ),
    },
    { id: 'program', header: 'Program', accessorKey: 'program' },
    { id: 'arm', header: 'Arm', accessorKey: 'arm' },
    { id: 'suites', header: 'Suites', accessorFn: (group) => group.suites.join(', ') },
    { id: 'runs', header: 'Runs', accessorKey: 'total_runs' },
    { id: 'completed', header: 'Completed runs', accessorKey: 'completed_runs' },
    { id: 'active', header: 'Active runs', accessorKey: 'running_runs' },
    { id: 'failed', header: 'Failed / stopped runs', accessorKey: 'failed_runs' },
    {
      id: 'assignments', header: 'Assignments', accessorKey: 'total_assignments',
      cell: ({ row: { original: group } }) => (
        <div className="eval-cell-stack eval-progress-cell">
          <strong>{formatNumber(group.total_assignments)}</strong>
          <span className="eval-cell-detail">{formatNumber(group.completed_assignments)} completed · {formatNumber(group.failed_assignments)} failed</span>
        </div>
      ),
    },
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
      id: 'verification', header: 'Verified runs', accessorKey: 'verified_runs',
      cell: ({ row: { original: group } }) => (
        <div className="eval-cell-stack">
          <TonePill tone={group.verified_runs === group.total_runs ? 'ok' : group.verifier_failed_runs > 0 ? 'critical' : 'neutral'}>
            {group.verified_runs === group.total_runs ? '✓ ' : ''}{group.verified_runs}/{group.total_runs} verified
          </TonePill>
          <span className="eval-cell-detail">{group.verifier_failed_runs} verifier failed</span>
        </div>
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
        <div className="eval-cell-stack eval-identity-cell" data-testid={`formation-${formation.id}`}>
          <strong>{formation.display_name}</strong>
          <code>{formation.id}</code>
          <span className="eval-cell-detail">{formation.description}</span>
        </div>
      ),
    },
    { id: 'kind', header: 'Kind', accessorKey: 'kind' },
    ...(['lite', 'assistant', 'primary'] as const).map((role, index): DataTableColumn<FormationTopologySpec> => ({
      id: role, header: `${index + 1}. ${roleLabel(role)}`, accessorFn: (formation) => formation[role].display_name,
      cell: ({ row }) => <FormationModelCell binding={row.original[role]} role={role} datasetId={defaultDatasetId} />,
    })),
    {
      id: 'vram', header: 'Total VRAM', accessorKey: 'total_vram_mib',
      cell: ({ row: { original: formation } }) => (
        <div className="eval-cell-stack eval-progress-cell">
          <strong>{formatNumber(formation.total_vram_mib)} MiB</strong>
          <span className="eval-cell-detail">of {formatNumber(formation.max_vram_mib)} MiB · {formatPercent(formation.vram_utilization_pct)}</span>
          <div className="formation-vram-track" aria-hidden="true">
            <div className="formation-vram-fill" style={{ width: `${Math.min(100, formation.vram_utilization_pct * 100)}%` }} />
          </div>
        </div>
      ),
    },
    { id: 'weights', header: 'Weights (MiB)', accessorFn: (formation) => formation.lite.estimated_model_vram_mib + formation.assistant.estimated_model_vram_mib + formation.primary.estimated_model_vram_mib },
    { id: 'kv', header: 'KV cache (MiB)', accessorFn: (formation) => formation.lite.estimated_kv_cache_mib + formation.assistant.estimated_kv_cache_mib + formation.primary.estimated_kv_cache_mib },
    { id: 'families', header: 'Families', accessorKey: 'family_diversity_count' },
    { id: 'providers', header: 'Providers', accessorKey: 'provider_diversity_count' },
  ], [defaultDatasetId]);

  const showCampaigns = activeTab === 'all' || activeTab === 'campaigns';
  const showRuns = activeTab === 'all' || activeTab === 'runs';
  const showFormations = activeTab === 'all' || activeTab === 'formations';

  return (
    <div className="evaluations-view">
      <SectionHeading
        kicker="EVALUATION FRAMEWORK"
        title="Campaigns, runs & execution formations"
        description="Inspect the ztd evaluation framework values — governed Campaigns, cross-dataset Evaluation Runs, and 3-role Sovereign Execution Formations (Lite → Assistant → Primary) without averaging scores across suites."
      />

      <section className="framework-values-banner" aria-label="Evaluation framework values">
        <details className="framework-reference">
          <summary>Framework reference · campaigns, verification, formations & capability gates</summary>
          <DataTable
            data={[...EVALUATION_FRAMEWORK_VALUES.pillars]}
            columns={[
              { id: 'area', header: 'Area', accessorKey: 'kicker' },
              { id: 'capability', header: 'Capability', accessorKey: 'title' },
              { id: 'specification', header: 'Specification', accessorKey: 'metric' },
              { id: 'details', header: 'Details', accessorKey: 'summary' },
            ]}
            caption="Evaluation framework reference"
            enableColumnVisibility={false}
          />
        </details>
        <div className="framework-spec-strip" aria-label="Framework specification values">
          <span>
            <strong>Schema:</strong> <code>{EVALUATION_FRAMEWORK_VALUES.schemaVersion}</code>
          </span>
          <span>
            <strong>Verifier Contract:</strong> <code>v{EVALUATION_FRAMEWORK_VALUES.verifierContractVersion}</code>
          </span>
          <span>
            <strong>Scenario Matrix:</strong> {EVALUATION_FRAMEWORK_VALUES.scenarioCount} scenarios ·{' '}
            {EVALUATION_FRAMEWORK_VALUES.scenarioCategories} task categories
          </span>
          <span>
            <strong>Homogeneous Matrix:</strong> {EVALUATION_FRAMEWORK_VALUES.homogeneousCellsPerVariant} cells/model (25 × 3 roles)
          </span>
          <span>
            <strong>Formations Catalog:</strong> {EVALUATION_FRAMEWORK_VALUES.totalFormationsCount.toLocaleString('en-US')} stacks ({EVALUATION_FRAMEWORK_VALUES.curatedFormationsCount} curated + {EVALUATION_FRAMEWORK_VALUES.heterogeneousFormationsCount.toLocaleString('en-US')} heterogeneous)
          </span>
          <span>
            <strong>VRAM Ceiling:</strong> {EVALUATION_FRAMEWORK_VALUES.maxVramBudgetMib.toLocaleString('en-US')} MiB (12 GiB)
          </span>
        </div>
      </section>

      <div className="stat-tile-grid summary-strip" aria-label="Filtered evaluation summary">
        <StatTile label="Campaigns" value={campaignGroups.length} />
        <StatTile label="Runs" value={filtered.length} />
        <StatTile label="Assignments" value={filtered.reduce((sum, run) => sum + run.assignment_total, 0)} />
        <StatTile label="Verifier passed" value={filtered.filter((run) => run.verifier_state === 'passed').length} />
        <StatTile label="Verifier failed" value={filtered.filter((run) => run.verifier_state === 'failed').length} />
        <StatTile label="Verifier not run" value={filtered.filter((run) => run.verifier_state === 'not_run').length} />
        <StatTile label="Conformance runs" value={filtered.filter((run) => run.native_result).length} />
        <StatTile label="Formations" value={EVALUATION_FRAMEWORK_VALUES.totalFormationsCount} />
      </div>

      <div className="framework-tabs-bar" role="tablist" aria-label="Evaluation framework sections">
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'all'}
          className={activeTab === 'all' ? 'framework-tab active' : 'framework-tab'}
          onClick={() => setActiveTab('all')}
        >
          All Framework Views
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'campaigns'}
          className={activeTab === 'campaigns' ? 'framework-tab active' : 'framework-tab'}
          onClick={() => setActiveTab('campaigns')}
        >
          Campaigns ({formatNumber(campaignGroups.length)})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'runs'}
          className={activeTab === 'runs' ? 'framework-tab active' : 'framework-tab'}
          onClick={() => setActiveTab('runs')}
        >
          Evaluation Runs ({formatNumber(filtered.length)})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === 'formations'}
          className={activeTab === 'formations' ? 'framework-tab active' : 'framework-tab'}
          onClick={() => setActiveTab('formations')}
        >
          Formations ({formatNumber(FORMATION_CATALOG.length)} of {formatNumber(EVALUATION_FRAMEWORK_VALUES.totalFormationsCount)})
        </button>
      </div>

      <div className="eval-toolbar">
        <ReleaseSelector />
        <select
          aria-label="Filter by suite"
          value={suiteFilter}
          onChange={(e) => setFilter('suite', e.target.value)}
        >
          <option value="all">All suites</option>
          {Array.from(new Set(evaluations.map((e) => e.suite_id))).map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <select
          aria-label="Filter by status"
          value={statusFilter}
          onChange={(e) => setFilter('status', e.target.value)}
        >
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
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </div>

      {filtered.length === 0 ? (
        <div>
          <EmptyState
            hasRecords={evaluations.length > 0}
            hasFilters={
              releaseFilter !== 'all' ||
              suiteFilter !== 'all' ||
              statusFilter !== 'all' ||
              effectiveQualityFilter !== 'all'
            }
            connection={connection}
          />
          <p className="panel-note">
            Active filters: release {releaseFilter}, suite {suiteFilter}, status {statusFilter}, quality{' '}
            {effectiveQualityFilter}.
          </p>
          <Link to="/evaluations?release=all">Reset filters</Link>
        </div>
      ) : (
        <>
          {showCampaigns && campaignGroups.length > 0 ? (
            <PanelSection title="Campaigns">
              <p className="disclosure">
                Campaign programs group evaluation runs by governed campaign binding, execution arm, and scenario suite.
              </p>
              <DataTable data={campaignGroups} columns={campaignColumns} pageSize={25} caption="Campaigns" />
            </PanelSection>
          ) : null}

          {showRuns ? (
            <div className="program-groups">
              {(['Governance conformance', 'Model campaigns'] as const).map((program) => {
                const runs = filtered.filter(
                  (run) => Boolean(run.native_result) === (program === 'Governance conformance'),
                );
                if (runs.length === 0) return null;
                return (
                  <PanelSection key={program} title={program}>
                    <DataTable
                      data={runs}
                      columns={columns}
                      pageSize={25}
                      caption={program}
                    />
                  </PanelSection>
                );
              })}
            </div>
          ) : null}
        </>
      )}

      {showFormations ? (
        <PanelSection title="Formations — 3-role execution topologies">
          <div className="formations-intro">
            <p className="disclosure">
              Formations (<code>execution-topologies.v1</code>) define multi-model agent execution pipelines where{' '}
              <strong>Lite</strong> handles triage and routing, <strong>Assistant</strong> executes bounded technical
              work, and <strong>Primary</strong> owns planning, delegation, and synthesis — all constrained within a
              sovereign local <strong>12,288 MiB (12 GiB)</strong> combined VRAM budget.
            </p>
            <div className="formations-toolbar">
              <div className="formation-filter-pills" role="group" aria-label="Filter formations by kind">
                <button
                  type="button"
                  className={formationKind === 'all' ? 'capability-filter-chip active' : 'capability-filter-chip'}
                  onClick={() => setFormationKind('all')}
                >
                  All showcased ({FORMATION_CATALOG.length})
                </button>
                <button
                  type="button"
                  className={formationKind === 'curated' ? 'capability-filter-chip active' : 'capability-filter-chip'}
                  onClick={() => setFormationKind('curated')}
                >
                  Curated reference stacks (5)
                </button>
                <button
                  type="button"
                  className={
                    formationKind === 'heterogeneous' ? 'capability-filter-chip active' : 'capability-filter-chip'
                  }
                  onClick={() => setFormationKind('heterogeneous')}
                >
                  Heterogeneous stacks (15 of 1,000)
                </button>
              </div>
              <input
                type="search"
                placeholder="Search formations by stack, model, or family..."
                aria-label="Search formations"
                value={formationSearch}
                onChange={(e) => setFormationSearch(e.target.value)}
              />
            </div>
          </div>
          <DataTable data={filteredFormations} columns={formationColumns} pageSize={25}
            caption="Execution formations" empty={<p className="table-empty">No formations match these filters.</p>} />
        </PanelSection>
      ) : null}
    </div>
  );
}
