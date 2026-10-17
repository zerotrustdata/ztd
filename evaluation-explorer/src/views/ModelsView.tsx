// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Models view — the dedicated Model Catalog & Capabilities directory.
// Focuses strictly on model identity, architecture/hardware specs, 5 governed
// capability probes (Completion, Tool Calling, Structured JSON, Thinking,
// Context Window), role leaders, and benchmark performance per role.
// Clicking any model opens its full Model Profile & Evaluations hub.

import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useSelectedRelease, useUserPref, PREF } from '../state/dataset';
import { modelComparisonId, resolveModelFromComparisonId, useStoreState } from '../state/store';
import { DataTable, type DataTableColumn } from '../components/DataTable';
import { ModelComparisonPanel } from '../components/ModelComparisonPanel';
import { RoleLeadersPanel } from '../components/RoleLeadersPanel';
import { ReleaseSelector } from '../components/ReleaseSelector';
import { matchesRelease } from '../content/release';
import {
  MODEL_CAPABILITY_KEYS,
  MODEL_CAPABILITY_META,
  resolveModelProfile,
  type CapabilityObservation,
  type CapabilityProbeStatus,
  type ModelCapabilityKey,
} from '../content/framework-catalog';
import { OutcomeBreakdown, PassRateBar, TonePill } from '../components/evidence';
import {
  EmptyState,
  ErrorState,
  QualityBadge,
  SectionHeading,
  StatTile,
  UnavailableValue,
  formatPercent,
  formatLatency,
  formatThroughput,
  formatNumber,
} from '../components/shared';
import type { ModelSummary, ModelRole } from '../contract/types';
import {
  availableQualityFilterOptions,
  MODEL_QUALITY_FILTER_STATES,
  normalizeQualityFilter,
} from '../utils/feed-state';
import { comparisonCompatibility, datasetLabel, roleLabel } from './derived';

interface ModelFilters {
  search: string;
  role: ModelRole | 'all';
  evaluated: 'all' | 'evaluated' | 'not_evaluated';
  quality: string | 'all';
  family?: string | 'all';
  capability?: ModelCapabilityKey | 'all';
}

const DEFAULT_FILTERS: ModelFilters = {
  search: '',
  role: 'all',
  evaluated: 'evaluated',
  quality: 'all',
  family: 'all',
  capability: 'all',
};

function filtersActive(filters: ModelFilters, quality: string): boolean {
  return (
    filters.search !== '' ||
    filters.role !== 'all' ||
    filters.evaluated !== 'all' ||
    quality !== 'all' ||
    (filters.family !== undefined && filters.family !== 'all') ||
    (filters.capability !== undefined && filters.capability !== 'all')
  );
}

function capabilityBadgeTone(status: CapabilityProbeStatus): 'ok' | 'critical' | 'warn' | 'neutral' {
  switch (status) {
    case 'pass':
      return 'ok';
    case 'fail':
      return 'critical';
    case 'unsupported':
      return 'warn';
    default:
      return 'neutral';
  }
}

function capabilityStatusSymbol(status: CapabilityProbeStatus): string {
  switch (status) {
    case 'pass':
      return '✓';
    case 'fail':
      return '✕';
    case 'unsupported':
      return '!';
    default:
      return '·';
  }
}

export function CapabilityProbeBadges({
  capabilities,
  compact = true,
}: {
  capabilities: CapabilityObservation[];
  compact?: boolean;
}) {
  return (
    <div className={compact ? 'capability-pill-group compact' : 'capability-pill-group'} aria-label="Model capabilities">
      {capabilities.map((cap) => {
        const tone = capabilityBadgeTone(cap.status);
        return (
          <span
            key={cap.key}
            className={`capability-pill capability-${tone}`}
            data-status={cap.status}
            title={`${cap.label}: ${cap.status.toUpperCase()} — ${cap.detail}`}
          >
            <span className="capability-symbol" aria-hidden="true">
              {capabilityStatusSymbol(cap.status)}
            </span>
            <span>{compact ? cap.shortLabel : cap.label}</span>
          </span>
        );
      })}
    </div>
  );
}

export function ModelsView() {
  const [params, setParams] = useSearchParams();
  const [filters, setFilters] = useUserPref<ModelFilters>(PREF.filters, DEFAULT_FILTERS);
  const [comparison, setComparison] = useUserPref<string[]>(PREF.comparison, []);
  const [viewMode, setViewMode] = useState<'table' | 'cards'>('table');

  useEffect(() => {
    const modelsParam = params.get('models');
    if (!modelsParam) return;
    const ids = modelsParam.split(',').filter(Boolean).slice(0, 4);
    if (ids.length > 0) {
      setComparison(ids);
      const next = new URLSearchParams(params);
      next.delete('models');
      setParams(next, { replace: true });
    }
  }, [params, setComparison, setParams]);

  const release = useSelectedRelease();
  const models = useStoreState((state) =>
    Array.from(state.models.values()).filter((record) => matchesRelease(record, release)),
  );
  const connection = useStoreState((state) => state.connection);
  const evaluatedFilter =
    filters.evaluated === 'evaluated' &&
    models.length > 0 &&
    models.every((model) => !model.pass_rate)
      ? 'all'
      : filters.evaluated;

  const comparedModels = useStoreState((state) =>
    comparison
      .map((id) => resolveModelFromComparisonId(state.models, id))
      .filter((m): m is NonNullable<typeof m> => m !== undefined),
  );

  const catalogs = useStoreState((state) =>
    Array.from(state.catalogs.values()).filter((record) => matchesRelease(record, release)),
  );
  const evaluations = useStoreState((state) =>
    Array.from(state.evaluations.values()).filter((record) => matchesRelease(record, release)),
  );
  const assignments = useStoreState((state) => Array.from(state.assignments.values()));

  const compatibility = useMemo(
    () => comparisonCompatibility(comparedModels, catalogs, evaluations),
    [comparedModels, catalogs, evaluations],
  );

  const profilesByKey = useMemo(() => {
    const map = new Map<string, ReturnType<typeof resolveModelProfile>>();
    for (const model of models) {
      map.set(`${model.dataset_id}:${model.variant_id}:${model.role}`, resolveModelProfile(model));
    }
    return map;
  }, [models]);

  const runsByVariant = useMemo(() => {
    const map = new Map<string, Set<string>>();
    for (const asg of assignments) {
      let set = map.get(asg.variant_id);
      if (!set) {
        set = new Set<string>();
        map.set(asg.variant_id, set);
      }
      set.add(`${asg.dataset_id}:${asg.run_id}`);
    }
    for (const run of evaluations) {
      for (const variantId of Object.values(run.model_role_mapping ?? {})) {
        if (!variantId) continue;
        let set = map.get(variantId);
        if (!set) {
          set = new Set<string>();
          map.set(variantId, set);
        }
        set.add(`${run.dataset_id}:${run.run_id}`);
      }
    }
    return map;
  }, [assignments, evaluations]);

  const availableFamilies = useMemo(() => {
    const set = new Set<string>();
    for (const profile of profilesByKey.values()) {
      if (profile.family) set.add(profile.family);
    }
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }, [profilesByKey]);

  const capabilityCounts = useMemo(() => {
    const counts: Record<ModelCapabilityKey, number> = {
      completion: 0,
      tool_calling: 0,
      structured_output: 0,
      thinking: 0,
      context_limit: 0,
    };
    const seenVariants = new Set<string>();
    for (const model of models) {
      if (seenVariants.has(model.variant_id)) continue;
      seenVariants.add(model.variant_id);
      const profile = profilesByKey.get(`${model.dataset_id}:${model.variant_id}:${model.role}`) ?? resolveModelProfile(model);
      for (const key of MODEL_CAPABILITY_KEYS) {
        if (profile.capabilities[key].status === 'pass') {
          counts[key] += 1;
        }
      }
    }
    return { counts, distinctVariants: seenVariants.size };
  }, [models, profilesByKey]);

  const qualityOptions = useMemo(
    () => availableQualityFilterOptions(models, MODEL_QUALITY_FILTER_STATES),
    [models],
  );
  const effectiveQuality = normalizeQualityFilter(filters.quality, qualityOptions);
  const activeFamily = filters.family ?? 'all';
  const activeCapability = filters.capability ?? 'all';

  useEffect(() => {
    if (effectiveQuality !== filters.quality) {
      setFilters({ ...filters, quality: effectiveQuality });
    }
  }, [effectiveQuality, filters, setFilters]);

  const filtered = useMemo(() => {
    const q = filters.search.trim().toLowerCase();
    return models
      .filter((m) => {
        const profile = profilesByKey.get(`${m.dataset_id}:${m.variant_id}:${m.role}`) ?? resolveModelProfile(m);
        if (q) {
          const hay = `${m.display_name} ${m.variant_id} ${profile.served_model_tag ?? ''} ${profile.family} ${profile.provider}`.toLowerCase();
          if (!hay.includes(q)) return false;
        }
        if (filters.role !== 'all' && m.role !== filters.role) return false;
        if (evaluatedFilter === 'evaluated' && (!m.pass_rate || m.evaluation_coverage < 1)) return false;
        if (evaluatedFilter === 'not_evaluated' && m.pass_rate) return false;
        if (effectiveQuality !== 'all' && m.quality_state !== effectiveQuality) return false;
        if (activeFamily !== 'all' && profile.family !== activeFamily) return false;
        if (activeCapability !== 'all' && profile.capabilities[activeCapability].status !== 'pass') return false;
        return true;
      })
      .sort((a, b) => {
        const measured = Number(Boolean(b.pass_rate)) - Number(Boolean(a.pass_rate));
        if (measured !== 0) return measured;
        const passRate = (b.pass_rate?.estimate ?? -1) - (a.pass_rate?.estimate ?? -1);
        return passRate || a.display_name.localeCompare(b.display_name);
      });
  }, [models, filters.search, filters.role, evaluatedFilter, effectiveQuality, activeFamily, activeCapability, profilesByKey]);

  const columns = useMemo<DataTableColumn<ModelSummary>[]>(
    () => [
      {
        id: 'select',
        header: 'Compare',
        cell: ({ row }) => (
          <input
            type="checkbox"
            aria-label={`Select ${row.original.display_name} for comparison`}
            checked={comparison.includes(modelComparisonId(row.original))}
            disabled={!comparison.includes(modelComparisonId(row.original)) && comparison.length >= 4}
            onChange={(e) => {
              const comparisonId = modelComparisonId(row.original);
              const next = e.target.checked
                ? [...comparison, comparisonId].slice(0, 4)
                : comparison.filter((id) => id !== comparisonId);
              setComparison(next);
            }}
          />
        ),
        enableSorting: false,
      },
      {
        id: 'name',
        header: 'Model',
        accessorKey: 'display_name',
        cell: ({ row }) => {
          const profile =
            profilesByKey.get(`${row.original.dataset_id}:${row.original.variant_id}:${row.original.role}`) ??
            resolveModelProfile(row.original);
          return (
            <div className="model-identity-cell">
              <div className="model-identity-top">
                <Link
                  to={`/models/${row.original.dataset_id}/${row.original.variant_id}?role=${row.original.role}`}
                  className="model-link"
                >
                  {row.original.display_name}
                  {row.original.inventory_only ? <TonePill tone="neutral">inventory</TonePill> : null}
                </Link>
              </div>
              <div className="model-identity-sub">
                {profile.served_model_tag ? (
                  <code className="served-tag-code">{profile.served_model_tag}</code>
                ) : (
                  <UnavailableValue />
                )}
                <span className="model-scope-tag" title={row.original.dataset_id}>
                  {datasetLabel(row.original.dataset_id)}
                </span>
              </div>
            </div>
          );
        },
      },
      {
        id: 'specs',
        header: 'Architecture & specs',
        accessorFn: (row: ModelSummary) => {
          const profile =
            profilesByKey.get(`${row.dataset_id}:${row.variant_id}:${row.role}`) ?? resolveModelProfile(row);
          return profile.parameter_count ?? 0;
        },
        cell: ({ row }) => {
          const profile =
            profilesByKey.get(`${row.original.dataset_id}:${row.original.variant_id}:${row.original.role}`) ??
            resolveModelProfile(row.original);
          return (
            <div className="model-specs-cell">
              <span className="spec-pill spec-family">{profile.family}</span>
              {profile.parameter_label ? <span className="spec-pill spec-params">{profile.parameter_label}</span> : null}
              {profile.quantization ? <span className="spec-pill spec-quant">{profile.quantization}</span> : null}
              {profile.context_label ? <span className="spec-pill spec-ctx" title={`Context limit: ${profile.context_limit?.toLocaleString('en-US')} tokens`}>{profile.context_label} ctx</span> : null}
            </div>
          );
        },
      },
      {
        id: 'capabilities',
        header: 'Capabilities',
        accessorFn: (row: ModelSummary) => {
          const profile =
            profilesByKey.get(`${row.dataset_id}:${row.variant_id}:${row.role}`) ?? resolveModelProfile(row);
          return profile.passed_capability_count;
        },
        cell: ({ row }) => {
          const profile =
            profilesByKey.get(`${row.original.dataset_id}:${row.original.variant_id}:${row.original.role}`) ??
            resolveModelProfile(row.original);
          return <CapabilityProbeBadges capabilities={profile.capability_list} compact />;
        },
      },
      {
        id: 'role',
        header: 'Role',
        accessorKey: 'role',
        cell: ({ row }) => (
          <span className={`role-badge role-badge-${row.original.role}`}>
            {roleLabel(row.original.role)}
          </span>
        ),
      },
      {
        id: 'coverage',
        header: 'Coverage',
        accessorFn: (row: ModelSummary) => row.evaluation_coverage,
        cell: ({ row }) => formatPercent(row.original.evaluation_coverage),
      },
      {
        id: 'pass_rate',
        header: 'Pass rate',
        accessorFn: (row: ModelSummary) => row.pass_rate?.estimate ?? -1,
        cell: ({ row }) => <PassRateBar interval={row.original.pass_rate} />,
      },
      {
        id: 'outcomes',
        header: 'Outcomes',
        cell: ({ row }) => <OutcomeBreakdown outcomes={row.original.terminal_outcomes} />,
        enableSorting: false,
      },
      {
        id: 'latency_p50',
        header: 'Latency p50',
        accessorFn: (row: ModelSummary) => row.latency_p50_ms?.value ?? Number.POSITIVE_INFINITY,
        cell: ({ row }) =>
          row.original.latency_p50_ms?.value !== undefined ? (
            formatLatency(row.original.latency_p50_ms.value)
          ) : (
            <UnavailableValue />
          ),
      },
      {
        id: 'throughput_p50',
        header: 'Throughput p50',
        accessorFn: (row: ModelSummary) => row.output_throughput_p50?.value ?? -1,
        cell: ({ row }) =>
          row.original.output_throughput_p50?.value !== undefined ? (
            formatThroughput(row.original.output_throughput_p50.value)
          ) : (
            <UnavailableValue />
          ),
      },
      {
        id: 'evaluations',
        header: 'Evaluations',
        accessorFn: (row: ModelSummary) => runsByVariant.get(row.variant_id)?.size ?? 0,
        cell: ({ row }) => {
          const runCount = runsByVariant.get(row.original.variant_id)?.size ?? (row.original.pass_rate ? 1 : 0);
          return (
            <Link
              to={`/models/${row.original.dataset_id}/${row.original.variant_id}?role=${row.original.role}#model-evaluations`}
              className="model-evals-link"
              title="Inspect evaluations and runs for this model"
            >
              {runCount > 0 ? `${formatNumber(runCount)} ${runCount === 1 ? 'eval' : 'evals'} →` : 'Profile →'}
            </Link>
          );
        },
      },
      {
        id: 'quality',
        header: 'Quality',
        accessorKey: 'quality_state',
        cell: ({ row }) => <QualityBadge state={row.original.quality_state} />,
        enableSorting: false,
      },
    ],
    [comparison, setComparison, profilesByKey, runsByVariant],
  );

  const updateFilter = (patch: Partial<ModelFilters>) => setFilters({ ...filters, ...patch });

  const measuredCount = models.filter((model) => Boolean(model.pass_rate) && model.evaluation_coverage >= 1).length;
  const modelKeys = new Set(models.map((model) => `${model.dataset_id}:${model.variant_id}:${model.role}`));
  const coveredRuns = new Set(
    assignments
      .filter((assignment) => modelKeys.has(`${assignment.dataset_id}:${assignment.variant_id}:${assignment.role}`))
      .map((assignment) => `${assignment.dataset_id}:${assignment.run_id}`),
  );

  return (
    <div className="models-view">
      <SectionHeading
        kicker="MODEL CATALOG & CAPABILITIES"
        title="Models, capabilities & role performance"
        description="Inspect model architecture, 5-probe pre-flight capabilities, and verified role performance across Primary, Assistant, and Lite roles. Select any model to view all evaluations for that model."
      />

      <div className="stat-tile-grid summary-strip" aria-label="Model summary">
        <StatTile label="Measured models" value={models.filter((model) => model.pass_rate).length} />
        <StatTile label="Inventory-only models" value={models.filter((model) => model.inventory_only).length} />
        <StatTile label="Model families" value={availableFamilies.length} />
        <StatTile label="Tool-capable variants" value={capabilityCounts.counts.tool_calling} />
        <StatTile
          label="Runs covered"
          value={evaluations.filter((run) => coveredRuns.has(`${run.dataset_id}:${run.run_id}`)).length}
        />
      </div>

      <RoleLeadersPanel models={models} catalogs={catalogs} evaluations={evaluations} />

      <div className="capability-filter-bar" role="group" aria-label="Filter by probed capability">
        <button
          type="button"
          className={activeCapability === 'all' ? 'capability-filter-chip active' : 'capability-filter-chip'}
          onClick={() => updateFilter({ capability: 'all' })}
        >
          <span>All capabilities</span>
          <span className="chip-count">{capabilityCounts.distinctVariants}</span>
        </button>
        {MODEL_CAPABILITY_KEYS.map((key) => {
          const meta = MODEL_CAPABILITY_META[key];
          const isActive = activeCapability === key;
          return (
            <button
              key={key}
              type="button"
              className={isActive ? 'capability-filter-chip active' : 'capability-filter-chip'}
              onClick={() => updateFilter({ capability: isActive ? 'all' : key })}
              title={meta.description}
            >
              <span>✓ {meta.label}</span>
              <span className="chip-count">{capabilityCounts.counts[key]}</span>
            </button>
          );
        })}
      </div>

      <div className="models-toolbar">
        <ReleaseSelector />
        <input
          type="search"
          placeholder="Search models, tags, or families..."
          value={filters.search}
          aria-label="Search models"
          onChange={(e) => updateFilter({ search: e.target.value })}
        />
        <select
          aria-label="Filter by family"
          value={activeFamily}
          onChange={(e) => updateFilter({ family: e.target.value })}
        >
          <option value="all">All families</option>
          {availableFamilies.map((fam) => (
            <option key={fam} value={fam}>
              {fam}
            </option>
          ))}
        </select>
        <select
          aria-label="Filter by role"
          value={filters.role}
          onChange={(e) => updateFilter({ role: e.target.value as ModelFilters['role'] })}
        >
          <option value="all">All roles</option>
          <option value="primary">Primary</option>
          <option value="assistant">Assistant</option>
          <option value="lite">Lite</option>
        </select>
        <select
          aria-label="Filter by evaluation status"
          value={filters.evaluated}
          onChange={(e) => updateFilter({ evaluated: e.target.value as ModelFilters['evaluated'] })}
        >
          <option value="all">All models</option>
          <option value="evaluated">Evaluated</option>
          <option value="not_evaluated">Not evaluated</option>
        </select>
        <select
          aria-label="Filter by quality state"
          value={effectiveQuality}
          onChange={(e) => updateFilter({ quality: e.target.value })}
        >
          <option value="all">All quality states</option>
          {qualityOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
        <div className="view-mode-toggle" role="group" aria-label="Model view mode">
          <button
            type="button"
            className={viewMode === 'table' ? 'view-mode-btn active' : 'view-mode-btn'}
            onClick={() => setViewMode('table')}
          >
            Table
          </button>
          <button
            type="button"
            className={viewMode === 'cards' ? 'view-mode-btn active' : 'view-mode-btn'}
            onClick={() => setViewMode('cards')}
          >
            Cards
          </button>
        </div>
        <span className="result-count">
          {formatNumber(measuredCount)} measured · {formatNumber(models.length)} total
        </span>
        {comparison.length > 0 ? (
          <button type="button" className="comparison-clear-btn" onClick={() => setComparison([])}>
            Clear {comparison.length} selected
          </button>
        ) : null}
      </div>

      {comparison.length > 0 && comparison.length < 2 ? (
        <p className="comparison-hint">Select at least one more model to compare side by side (up to four).</p>
      ) : null}

      {comparison.length >= 2 && comparedModels.length < 2 ? (
        <ErrorState message="Some selected models were not found in the catalog." />
      ) : null}

      {comparedModels.length >= 2 && !compatibility.compatible ? (
        <ErrorState
          message={`Selected models come from different datasets that cannot be compared: ${compatibility.reason}. Comparison never combines incompatible datasets.`}
        />
      ) : null}

      {comparedModels.length >= 2 && compatibility.compatible ? (
        <ModelComparisonPanel
          models={comparedModels}
          environmentSource={compatibility.crossDataset ? compatibility.environmentSource : undefined}
          onClear={() => setComparison([])}
        />
      ) : null}

      {filtered.length === 0 ? (
        <EmptyState
          hasRecords={models.length > 0}
          hasFilters={filtersActive(filters, effectiveQuality)}
          connection={connection}
        />
      ) : viewMode === 'cards' ? (
        <div className="model-catalog-grid" aria-label="Model cards">
          {filtered.map((model) => {
            const profile =
              profilesByKey.get(`${model.dataset_id}:${model.variant_id}:${model.role}`) ??
              resolveModelProfile(model);
            const runCount = runsByVariant.get(model.variant_id)?.size ?? (model.pass_rate ? 1 : 0);
            const compId = modelComparisonId(model);
            const isSelected = comparison.includes(compId);
            return (
              <article
                key={`${model.dataset_id}:${model.variant_id}:${model.role}`}
                className={isSelected ? 'model-catalog-card selected' : 'model-catalog-card'}
              >
                <div className="model-card-header">
                  <div>
                    <Link
                      to={`/models/${model.dataset_id}/${model.variant_id}?role=${model.role}`}
                      className="model-card-title"
                    >
                      {model.display_name}
                    </Link>
                    {profile.served_model_tag ? (
                      <code className="served-tag-code">{profile.served_model_tag}</code>
                    ) : null}
                  </div>
                  <span className={`role-badge role-badge-${model.role}`}>{roleLabel(model.role)}</span>
                </div>
                <div className="model-specs-cell">
                  <span className="spec-pill spec-family">{profile.family}</span>
                  {profile.parameter_label ? <span className="spec-pill spec-params">{profile.parameter_label}</span> : null}
                  {profile.quantization ? <span className="spec-pill spec-quant">{profile.quantization}</span> : null}
                  {profile.context_label ? <span className="spec-pill spec-ctx">{profile.context_label} ctx</span> : null}
                  {model.inventory_only ? <TonePill tone="neutral">inventory</TonePill> : null}
                </div>
                <CapabilityProbeBadges capabilities={profile.capability_list} compact={false} />
                <div className="model-card-metrics">
                  <div>
                    <span className="metric-mini-label">Pass rate</span>
                    <PassRateBar interval={model.pass_rate} />
                  </div>
                  <div className="model-card-perf-row">
                    <div>
                      <span className="metric-mini-label">Latency p50</span>
                      <strong>
                        {model.latency_p50_ms?.value !== undefined ? (
                          formatLatency(model.latency_p50_ms.value)
                        ) : (
                          <UnavailableValue />
                        )}
                      </strong>
                    </div>
                    <div>
                      <span className="metric-mini-label">Throughput p50</span>
                      <strong>
                        {model.output_throughput_p50?.value !== undefined ? (
                          formatThroughput(model.output_throughput_p50.value)
                        ) : (
                          <UnavailableValue />
                        )}
                      </strong>
                    </div>
                    <div>
                      <span className="metric-mini-label">Coverage</span>
                      <strong>{formatPercent(model.evaluation_coverage)}</strong>
                    </div>
                  </div>
                </div>
                <OutcomeBreakdown outcomes={model.terminal_outcomes} />
                <div className="model-card-footer">
                  <QualityBadge state={model.quality_state} />
                  <div className="model-card-actions">
                    <label className="compare-check-label">
                      <input
                        type="checkbox"
                        aria-label={`Select ${model.display_name} for comparison`}
                        checked={isSelected}
                        disabled={!isSelected && comparison.length >= 4}
                        onChange={(event) =>
                          setComparison(
                            event.target.checked
                              ? [...comparison, compId].slice(0, 4)
                              : comparison.filter((id) => id !== compId),
                          )
                        }
                      />{' '}
                      Compare
                    </label>
                    <Link
                      to={`/models/${model.dataset_id}/${model.variant_id}?role=${model.role}`}
                      className="model-evals-link"
                      title={model.dataset_id}
                    >
                      {runCount > 0 ? `View ${runCount} ${runCount === 1 ? 'evaluation' : 'evaluations'} →` : 'Model profile →'}
                    </Link>
                  </div>
                </div>
              </article>
            );
          })}
        </div>
      ) : (
        <DataTable
          data={filtered}
          columns={columns}
          pageSize={25}
          caption="Model catalog"
          isRowSelected={(model) => comparison.includes(modelComparisonId(model))}
          renderCard={(model) => {
            const profile =
              profilesByKey.get(`${model.dataset_id}:${model.variant_id}:${model.role}`) ??
              resolveModelProfile(model);
            return (
              <article className="run-card">
                <label>
                  <input
                    type="checkbox"
                    aria-label={`Select ${model.display_name} for comparison`}
                    checked={comparison.includes(modelComparisonId(model))}
                    disabled={!comparison.includes(modelComparisonId(model)) && comparison.length >= 4}
                    onChange={(event) =>
                      setComparison(
                        event.target.checked
                          ? [...comparison, modelComparisonId(model)].slice(0, 4)
                          : comparison.filter((id) => id !== modelComparisonId(model)),
                      )
                    }
                  />{' '}
                  Compare
                </label>
                <Link to={`/models/${model.dataset_id}/${model.variant_id}?role=${model.role}`}>
                  {model.display_name}
                </Link>
                <p>
                  {profile.served_model_tag} · {profile.family} · {roleLabel(model.role)}
                </p>
                <CapabilityProbeBadges capabilities={profile.capability_list} compact />
                {model.inventory_only ? <TonePill tone="neutral">inventory</TonePill> : null}
                <PassRateBar interval={model.pass_rate} />
                <OutcomeBreakdown outcomes={model.terminal_outcomes} />
                <QualityBadge state={model.quality_state} />
              </article>
            );
          }}
        />
      )}
    </div>
  );
}
