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

function FormationStageNode({
  role,
  stageNumber,
  binding,
  datasetId,
}: {
  role: ModelRole;
  stageNumber: number;
  binding: FormationRoleBinding;
  datasetId?: string;
}) {
  const totalRoleVram = binding.estimated_model_vram_mib + binding.estimated_kv_cache_mib;
  const paramLabel = formatParameterCount(binding.parameter_count);
  const targetUrl = datasetId
    ? `/models/${datasetId}/${binding.variant_id}?role=${role}`
    : `/models/${binding.variant_id}?role=${role}`;

  return (
    <div className={`formation-stage-node formation-stage-${role}`}>
      <div className="formation-stage-header">
        <span className={`role-badge role-badge-${role}`}>
          Stage {stageNumber} · {roleLabel(role)}
        </span>
        <span className="formation-stage-vram" title={`Weights: ${binding.estimated_model_vram_mib} MiB + KV cache: ${binding.estimated_kv_cache_mib} MiB`}>
          {totalRoleVram.toLocaleString('en-US')} MiB
        </span>
      </div>
      <Link to={targetUrl} className="formation-stage-model">
        {roleLabel(role)}: {binding.display_name}
      </Link>
      <code className="served-tag-code">{binding.served_model_tag}</code>
      <div className="formation-stage-specs">
        <span className="spec-pill spec-family">{binding.family}</span>
        {paramLabel ? <span className="spec-pill spec-params">{paramLabel}</span> : null}
        <span className="spec-pill spec-quant">{binding.quantization}</span>
        <span className="spec-pill spec-provider">{binding.provider}</span>
      </div>
    </div>
  );
}

function FormationCard({
  formation,
  defaultDatasetId,
}: {
  formation: FormationTopologySpec;
  defaultDatasetId?: string;
}) {
  const vramPct = Math.min(100, Math.round(formation.vram_utilization_pct * 100));
  const modelVram =
    formation.lite.estimated_model_vram_mib +
    formation.assistant.estimated_model_vram_mib +
    formation.primary.estimated_model_vram_mib;
  const kvVram =
    formation.lite.estimated_kv_cache_mib +
    formation.assistant.estimated_kv_cache_mib +
    formation.primary.estimated_kv_cache_mib;

  return (
    <article className="formation-card" data-testid={`formation-${formation.id}`}>
      <div className="formation-card-head">
        <div>
          <div className="formation-title-row">
            <h3 className="formation-title">{formation.display_name}</h3>
            <TonePill tone={formation.kind === 'curated' ? 'info' : 'neutral'}>
              {formation.kind === 'curated' ? 'Curated Stack' : 'Heterogeneous Stack'}
            </TonePill>
          </div>
          <code className="formation-id-code">{formation.id}</code>
          <p className="formation-desc">{formation.description}</p>
        </div>
        <div className="formation-diversity-pills">
          <span className="spec-pill spec-family">{formation.family_diversity_count} Model Families</span>
          <span className="spec-pill spec-provider">{formation.provider_diversity_count} Providers</span>
          <span className="spec-pill spec-trust">Sovereign Local</span>
        </div>
      </div>

      <div className="formation-vram-bar-wrap" aria-label={`${formation.display_name} VRAM budget`}>
        <div className="formation-vram-labels">
          <span>
            Combined VRAM Budget:{' '}
            <strong>
              {formation.total_vram_mib.toLocaleString('en-US')} / {formation.max_vram_mib.toLocaleString('en-US')} MiB
            </strong>{' '}
            ({vramPct}%)
          </span>
          <span className="formation-vram-sub">
            Weights {modelVram.toLocaleString('en-US')} MiB · KV Cache {kvVram.toLocaleString('en-US')} MiB
          </span>
        </div>
        <div className="formation-vram-track">
          <div
            className="formation-vram-fill"
            style={{ width: `${vramPct}%` }}
          />
        </div>
      </div>

      <div className="formation-pipeline" aria-label="Execution order Lite to Assistant to Primary">
        <FormationStageNode
          role="lite"
          stageNumber={1}
          binding={formation.lite}
          datasetId={defaultDatasetId}
        />
        <div className="formation-pipeline-arrow" aria-hidden="true">→</div>
        <FormationStageNode
          role="assistant"
          stageNumber={2}
          binding={formation.assistant}
          datasetId={defaultDatasetId}
        />
        <div className="formation-pipeline-arrow" aria-hidden="true">→</div>
        <FormationStageNode
          role="primary"
          stageNumber={3}
          binding={formation.primary}
          datasetId={defaultDatasetId}
        />
      </div>
    </article>
  );
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
        enableSorting: false,
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
        accessorFn: (row: EvaluationSummary) => row.elapsed_seconds ?? 0,
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
        cell: ({ row }) => <QualityBadge state={row.original.quality_state} />,
        enableSorting: false,
      },
    ],
    [],
  );

  const defaultDatasetId = filtered[0]?.dataset_id ?? evaluations[0]?.dataset_id;
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
        <div className="framework-pillars-grid">
          {EVALUATION_FRAMEWORK_VALUES.pillars.map((pillar) => (
            <article key={pillar.id} className="framework-pillar-card">
              <div className="framework-pillar-top">
                <span className="framework-pillar-kicker">{pillar.kicker}</span>
                <span className="framework-pillar-metric">{pillar.metric}</span>
              </div>
              <h3 className="framework-pillar-title">{pillar.title}</h3>
              <p className="framework-pillar-copy">{pillar.summary}</p>
            </article>
          ))}
        </div>
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
              <div className="campaign-cards-grid">
                {campaignGroups.map((group) => {
                  const doneAssignments = group.completed_assignments + group.failed_assignments;
                  return (
                    <article key={group.campaign_id} className="campaign-summary-card">
                      <div className="campaign-card-head">
                        <div>
                          <span className="campaign-program-tag">{group.program}</span>
                          <h3 className="campaign-card-title">{group.title}</h3>
                          <code className="campaign-id-chip">{group.campaign_id}</code>
                        </div>
                        <div className="campaign-card-badges">
                          <TonePill tone={group.verified_runs > 0 ? 'ok' : group.verifier_failed_runs > 0 ? 'critical' : 'neutral'}>
                            {group.verified_runs}/{group.total_runs} verified
                          </TonePill>
                        </div>
                      </div>
                      <div className="campaign-card-meta">
                        <span>
                          <strong>Arm:</strong> <code>{group.arm}</code>
                        </span>
                        <span>
                          <strong>Suites:</strong> {group.suites.join(', ')}
                        </span>
                        <span>
                          <strong>Runs:</strong> {formatNumber(group.total_runs)} ({group.completed_runs} completed · {group.running_runs} active · {group.failed_runs} failed)
                        </span>
                        {group.pass_rate_median !== undefined ? (
                          <span>
                            <strong>Median pass rate:</strong> {formatPercent(group.pass_rate_median)}
                          </span>
                        ) : null}
                      </div>
                      <ProgressBar
                        completed={doneAssignments}
                        total={group.total_assignments > 0 ? group.total_assignments : doneAssignments}
                        label={`${group.campaign_id} assignments`}
                      />
                    </article>
                  );
                })}
              </div>
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
                      renderCard={(run) => {
                        const progress = campaignTerminalProgress(run);
                        return (
                          <article className="run-card">
                            <Link to={`/evaluations/${run.dataset_id}/${run.run_id}`}>{run.run_id}</Link>
                            <p>
                              {run.suite_id} · {releaseLabel(run)}
                            </p>
                            <TonePill
                              tone={verifierStateTone(run.verifier_state)}
                              title={run.verifier_failure_summary}
                            >
                              {run.verifier_state.replace(/_/g, ' ')}
                            </TonePill>
                            {run.native_result ? (
                              <TonePill tone={nativeResultTone(run.native_result.summary_status)}>
                                {run.native_result.summary_status}
                              </TonePill>
                            ) : null}
                            <ProgressBar
                              completed={run.native_result?.passed_verdict_count ?? progress.done}
                              total={run.native_result?.required_verdict_count ?? progress.total}
                              label={`${run.run_id} progress`}
                            />
                            <QualityBadge state={run.quality_state} />
                          </article>
                        );
                      }}
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
          <div className="formations-grid">
            {filteredFormations.map((formation) => (
              <FormationCard
                key={formation.id}
                formation={formation}
                defaultDatasetId={defaultDatasetId}
              />
            ))}
          </div>
        </PanelSection>
      ) : null}
    </div>
  );
}
