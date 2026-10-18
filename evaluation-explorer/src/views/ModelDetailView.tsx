// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Model detail view — the dedicated Model Profile & Evaluations hub.
// Clicking a model from the Model Catalog opens its architecture & hardware
// specs, 5 governed pre-flight capability probes, role switcher, all
// Evaluation Runs & Campaigns involving this model, overall performance
// metrics, terminal outcomes, suite results, catalog formations, and assignments.

import { useMemo } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { useActiveDatasetId, useSelectedRelease } from '../state/dataset';
import { modelRecordKey, useStoreState } from '../state/store';
import {
  formationsForVariant,
  resolveModelProfile,
  type CapabilityProbeStatus,
} from '../content/framework-catalog';
import {
  DetailRow,
  EmptyState,
  ErrorState,
  IntervalDisplay,
  MetricCard,
  ProgressBar,
  QualityBadge,
  UnavailableValue,
  formatPercent,
  formatLatency,
  formatThroughput,
  formatTokens,
  formatNumber,
  formatDuration,
} from '../components/shared';
import {
  assignmentVerdictTone,
  campaignTerminalProgress,
  datasetLabel,
  isNonVerdictOutcome,
  lifecycleStateTone,
  modelSuiteRows,
  qualityStateTone,
  roleLabel,
  runsForVariant,
  verifierStateTone,
} from './derived';
import {
  DetailLayout,
  NON_VERDICT_NOTE,
  OutcomeBreakdown,
  PageHero,
  PanelSection,
  TonePill,
} from '../components/evidence';
import type { ModelRole } from '../contract/types';

function capabilityTone(status: CapabilityProbeStatus): 'ok' | 'critical' | 'warn' | 'neutral' {
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

export function ModelDetailView() {
  const { variantId: routeVariant, datasetId: routeDataset } = useParams();
  const [params] = useSearchParams();
  const variantId = routeVariant ?? routeDataset;
  const datasetParam = routeVariant ? routeDataset : undefined;
  const activeDatasetId = useActiveDatasetId(
    datasetParam ?? params.get('dataset') ?? undefined,
    useSelectedRelease(),
  );
  const roleFilter = params.get('role');

  const model = useStoreState((state) => {
    if (!variantId) return undefined;
    if (roleFilter === 'primary' || roleFilter === 'assistant' || roleFilter === 'lite') {
      return state.models.get(modelRecordKey(activeDatasetId, variantId, roleFilter));
    }
    for (const candidate of state.models.values()) {
      if (candidate.dataset_id !== activeDatasetId || candidate.variant_id !== variantId) continue;
      if (candidate.pass_rate) return candidate;
    }
    for (const candidate of state.models.values()) {
      if (candidate.dataset_id === activeDatasetId && candidate.variant_id === variantId) return candidate;
    }
    return undefined;
  });

  const roleVariantsInDataset = useStoreState((state) => {
    if (!variantId) return [];
    return Array.from(state.models.values()).filter(
      (candidate) => candidate.dataset_id === activeDatasetId && candidate.variant_id === variantId,
    );
  });

  const suites = useStoreState((state) =>
    Array.from(state.suites.values()).filter((s) => s.dataset_id === activeDatasetId),
  );
  const evaluations = useStoreState((state) =>
    Array.from(state.evaluations.values()).filter((e) => e.dataset_id === activeDatasetId),
  );
  const allEvaluations = useStoreState((state) => Array.from(state.evaluations.values()));
  const assignments = useStoreState((state) =>
    variantId
      ? Array.from(state.assignments.values()).filter(
          (a) => a.variant_id === variantId && a.dataset_id === activeDatasetId,
        )
      : [],
  );
  const allVariantAssignments = useStoreState((state) =>
    variantId ? Array.from(state.assignments.values()).filter((a) => a.variant_id === variantId) : [],
  );
  const connection = useStoreState((state) => state.connection);

  if (!variantId) {
    return <ErrorState message="No model selected." />;
  }
  if (!model) {
    return <EmptyState hasRecords={false} hasFilters={false} connection={connection} />;
  }

  const profile = resolveModelProfile(model);
  const suiteRows = modelSuiteRows(model.variant_id, assignments, evaluations, suites);
  const activeDatasetRuns = runsForVariant(model.variant_id, assignments, evaluations);
  const crossDatasetRuns = useMemo(() => {
    const map = new Map<string, (typeof allEvaluations)[number]>();
    for (const run of activeDatasetRuns) {
      map.set(`${run.dataset_id}:${run.run_id}`, run);
    }
    for (const run of runsForVariant(model.variant_id, allVariantAssignments, allEvaluations)) {
      map.set(`${run.dataset_id}:${run.run_id}`, run);
    }
    for (const run of allEvaluations) {
      if (Object.values(run.model_role_mapping ?? {}).includes(model.variant_id)) {
        map.set(`${run.dataset_id}:${run.run_id}`, run);
      }
    }
    return Array.from(map.values());
  }, [activeDatasetRuns, model.variant_id, allVariantAssignments, allEvaluations]);

  const modelFormations = useMemo(() => formationsForVariant(model.variant_id), [model.variant_id]);

  return (
    <div className="model-detail">
      <nav className="breadcrumb" aria-label="Breadcrumb">
        <Link to={`/models?dataset=${activeDatasetId}`}>Models</Link>
        <span aria-hidden="true">/</span>
        <span>{model.display_name}</span>
      </nav>

      <PageHero
        kicker={`Model · ${profile.family}`}
        title={model.display_name}
        tone={qualityStateTone(model.quality_state)}
        meta={
          <>
            {profile.served_model_tag ? <code>{profile.served_model_tag}</code> : null}
            <span>{profile.provider}</span>
            {profile.parameter_label ? <span>{profile.parameter_label} params</span> : null}
            {profile.quantization ? <span>{profile.quantization}</span> : null}
            {profile.context_label ? <span>{profile.context_label} context</span> : null}
            <span>Role: {roleLabel(model.role)}</span>
          </>
        }
        verdict={
          <TonePill tone={model.pass_rate ? 'info' : 'neutral'}>
            {model.pass_rate
              ? `${formatPercent(model.pass_rate.estimate)} · n=${formatNumber(model.pass_rate.denominator)}`
              : 'Not evaluated'}
          </TonePill>
        }
      >
        <div className="quality-banner">
          <QualityBadge state={model.quality_state} />
          {roleVariantsInDataset.length > 1 ? (
            <div className="role-switcher-pills" role="group" aria-label="Switch model role">
              {(['primary', 'assistant', 'lite'] as const).map((r: ModelRole) => {
                const candidate = roleVariantsInDataset.find((c) => c.role === r);
                if (!candidate) return null;
                const active = candidate.role === model.role;
                return (
                  <Link
                    key={r}
                    to={`/models/${model.dataset_id}/${model.variant_id}?role=${r}`}
                    className={active ? 'role-switch-pill active' : 'role-switch-pill'}
                  >
                    {roleLabel(r)}
                    {candidate.pass_rate ? ` (${formatPercent(candidate.pass_rate.estimate, 0)})` : ''}
                  </Link>
                );
              })}
            </div>
          ) : null}
          {model.unavailable_reasons?.map((reason, i) => (
            <span key={i} className="unavailable-reason">
              {reason}
            </span>
          ))}
        </div>
      </PageHero>

      <DetailLayout
        aside={
          <>
            <PanelSection title="Identity">
              <dl>
                <DetailRow label="Variant ID">{model.variant_id}</DetailRow>
                <DetailRow label="Display name">{model.display_name}</DetailRow>
                <DetailRow label="Served tag">{profile.served_model_tag ?? <UnavailableValue />}</DetailRow>
                <DetailRow label="Model family">{profile.family}</DetailRow>
                <DetailRow label="Parameters">{profile.parameter_label ?? <UnavailableValue />}</DetailRow>
                <DetailRow label="Quantization / weight">
                  {model.quantization_weight_class ?? profile.quantization ?? <UnavailableValue />}
                </DetailRow>
                <DetailRow label="Context window">
                  {profile.context_limit ? `${profile.context_limit.toLocaleString('en-US')} tokens (${profile.context_label})` : <UnavailableValue />}
                </DetailRow>
                <DetailRow label="Role">{roleLabel(model.role)}</DetailRow>
                <DetailRow label="Backend / provider">
                  {model.backend_provider_class ?? profile.provider ?? <UnavailableValue />}
                </DetailRow>
                <DetailRow label="Inventory only">{model.inventory_only ? 'Yes' : 'No'}</DetailRow>
                <DetailRow label="Evaluation coverage">{formatPercent(model.evaluation_coverage)}</DetailRow>
              </dl>
            </PanelSection>
            {model.repeatability ? (
              <PanelSection title="Repeatability">
                <ul className="repeatability-list">
                  <li>
                    <span>Consistently correct</span>
                    <strong>{formatNumber(model.repeatability.consistently_correct)}</strong>
                  </li>
                  <li>
                    <span>Consistently wrong</span>
                    <strong>{formatNumber(model.repeatability.consistently_wrong)}</strong>
                  </li>
                  <li>
                    <span>Inconsistent</span>
                    <strong>{formatNumber(model.repeatability.inconsistent)}</strong>
                  </li>
                  <li>
                    <span>Insufficient</span>
                    <strong>{formatNumber(model.repeatability.insufficient)}</strong>
                  </li>
                </ul>
              </PanelSection>
            ) : null}
            {modelFormations.length > 0 ? (
              <PanelSection title={`Formations (${modelFormations.length})`}>
                <p className="panel-note">
                  Catalog formations that bind this model variant. No formation is scored in v1.1.3.
                </p>
                <ul className="run-card-list">
                  {modelFormations.slice(0, 6).map(({ formation, role }) => (
                    <li key={`${formation.id}:${role}`} className="run-card formation-mini-card">
                      <div className="formation-mini-head">
                        <Link to="/evaluations?view=formations">
                          {formation.display_name}
                        </Link>
                        <span className={`role-badge role-badge-${role}`}>{roleLabel(role)}</span>
                      </div>
                      <p className="formation-mini-desc">{formation.description}</p>
                      <span className="formation-vram-tag">
                        {formation.total_vram_mib.toLocaleString('en-US')} / {formation.max_vram_mib.toLocaleString('en-US')} MiB VRAM
                      </span>
                    </li>
                  ))}
                </ul>
              </PanelSection>
            ) : null}
          </>
        }
      >
        <PanelSection title="Capabilities & pre-flight probes">
          <p className="disclosure">
            Governed pre-flight capability probes verified against the model endpoint before campaign execution ({profile.passed_capability_count} of {profile.capability_list.length} probes passed).
          </p>
          <div className="capability-detail-grid">
            {profile.capability_list.map((cap) => (
              <div key={cap.key} className={`capability-detail-card capability-border-${capabilityTone(cap.status)}`}>
                <div className="capability-detail-head">
                  <strong>{cap.label}</strong>
                  <TonePill tone={capabilityTone(cap.status)}>{cap.status.toUpperCase()}</TonePill>
                </div>
                <p className="capability-detail-copy">{cap.detail}</p>
              </div>
            ))}
          </div>
        </PanelSection>

        <div id="model-evaluations">
          <PanelSection title="Evaluation runs involving this model">
            {crossDatasetRuns.length === 0 ? (
              <p>No runs involve this model in the selected dataset.</p>
            ) : (
              <div className="model-evaluations-grid">
                <ul className="run-card-list model-eval-run-list">
                  {crossDatasetRuns.map((run) => {
                    const progress = campaignTerminalProgress(run);
                    const passRateVal = run.headline_metrics.pass_rate?.value;
                    const latencyVal = run.headline_metrics.latency_p50_ms?.value;
                    return (
                      <li key={`${run.dataset_id}:${run.run_id}`} className="run-card model-eval-run-card">
                        <div className="model-eval-run-head">
                          <div>
                            <Link to={`/evaluations/${run.dataset_id}/${run.run_id}`} className="model-eval-run-link">
                              {run.run_id}
                            </Link>
                            <p className="model-eval-run-sub">
                              <span>{run.suite_id}</span>
                              {run.campaign_id ? <span> · {run.campaign_id}</span> : null}
                              <span> · {datasetLabel(run.dataset_id)}</span>
                            </p>
                          </div>
                          <div className="model-eval-run-pills">
                            <TonePill tone={lifecycleStateTone(run.lifecycle_state)}>{run.lifecycle_state}</TonePill>
                            <TonePill
                              tone={verifierStateTone(run.verifier_state)}
                              title={run.verifier_failure_summary}
                            >
                              {run.verifier_state.replace(/_/g, ' ')}
                            </TonePill>
                            <QualityBadge state={run.quality_state} />
                          </div>
                        </div>
                        <div className="model-eval-run-metrics">
                          <ProgressBar
                            completed={run.native_result?.passed_verdict_count ?? progress.done}
                            total={run.native_result?.required_verdict_count ?? progress.total}
                            label={`${run.run_id} progress`}
                          />
                          <div className="model-eval-run-stats">
                            <span>
                              Pass rate:{' '}
                              <strong>{passRateVal !== undefined ? formatPercent(passRateVal) : '—'}</strong>
                            </span>
                            <span>
                              Latency p50:{' '}
                              <strong>{latencyVal !== undefined ? formatLatency(latencyVal) : '—'}</strong>
                            </span>
                            <span>
                              Elapsed:{' '}
                              <strong>
                                {run.elapsed_seconds !== undefined ? formatDuration(run.elapsed_seconds) : '—'}
                              </strong>
                            </span>
                          </div>
                        </div>
                      </li>
                    );
                  })}
                </ul>
              </div>
            )}
          </PanelSection>
        </div>

        <PanelSection title="Overall metrics">
          {model.pass_rate ? (
            <div className="metric-block">
              <div className="metric-label">Pass rate</div>
              <IntervalDisplay interval={model.pass_rate} label="pass rate" />
            </div>
          ) : (
            <p className="not-evaluated">
              Not evaluated. This model has no eligible observations in the selected dataset.
            </p>
          )}
          <div className="metric-grid">
            <MetricCard label="Agreement pairwise" metric={model.agreement_pairwise} formatter={formatPercent} />
            <MetricCard label="Agreement all-five" metric={model.agreement_all_five} formatter={formatPercent} />
            <MetricCard label="Latency p50" metric={model.latency_p50_ms} formatter={formatLatency} />
            <MetricCard label="Latency p95" metric={model.latency_p95_ms} formatter={formatLatency} />
            <MetricCard
              label="Throughput p50"
              metric={model.output_throughput_p50}
              formatter={formatThroughput}
            />
            <MetricCard
              label="Throughput p95"
              metric={model.output_throughput_p95}
              formatter={formatThroughput}
            />
            <MetricCard label="Input tokens" metric={model.input_tokens} formatter={formatTokens} />
            <MetricCard label="Output tokens" metric={model.output_tokens} formatter={formatTokens} />
            <MetricCard label="Thinking tokens" metric={model.thinking_tokens} formatter={formatTokens} />
            <MetricCard label="Cache tokens" metric={model.cache_tokens} formatter={formatTokens} />
          </div>
        </PanelSection>

        <PanelSection title="Terminal outcomes">
          <OutcomeBreakdown outcomes={model.terminal_outcomes} />
        </PanelSection>

        <PanelSection title="Suite results">
          <p className="disclosure">
            Rows include only assignments produced by this model. Failures and missing measurements remain visible beside the eligible pass-rate denominator.
          </p>
          {suiteRows.length === 0 ? (
            <p>No suite results for this model in the selected dataset.</p>
          ) : (
            <div className="data-table-wrap">
              <table className="suite-table">
                <thead>
                  <tr>
                    <th scope="col">Suite</th>
                    <th scope="col">Total</th>
                    <th scope="col">Eligible</th>
                    <th scope="col">Passed</th>
                    <th scope="col">Pass rate</th>
                    <th scope="col">Model failures</th>
                    <th scope="col">Missing</th>
                    <th scope="col">Latency p50</th>
                    <th scope="col">Verifier</th>
                    <th scope="col">Quality</th>
                  </tr>
                </thead>
                <tbody>
                  {suiteRows.map((suite) => (
                    <tr key={suite.suite_id}>
                      <td>{suite.display_name}</td>
                      <td>{formatNumber(suite.total)}</td>
                      <td>{formatNumber(suite.eligible)}</td>
                      <td>{formatNumber(suite.passed)}</td>
                      <td>
                        {suite.pass_rate !== undefined ? formatPercent(suite.pass_rate) : <UnavailableValue />}
                      </td>
                      <td>{formatNumber(suite.model_failed)}</td>
                      <td>{formatNumber(suite.missing)}</td>
                      <td>
                        {suite.latency_p50_ms !== undefined ? (
                          formatLatency(suite.latency_p50_ms)
                        ) : (
                          <UnavailableValue />
                        )}
                      </td>
                      <td>
                        {suite.verifier_state ? (
                          <TonePill tone={verifierStateTone(suite.verifier_state)}>
                            {suite.verifier_state.replace(/_/g, ' ')}
                          </TonePill>
                        ) : (
                          <UnavailableValue />
                        )}
                      </td>
                      <td>
                        {suite.quality_state ? <QualityBadge state={suite.quality_state} /> : <UnavailableValue />}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </PanelSection>

        {assignments.length > 0 ? (
          <PanelSection title={`Assignments (${formatNumber(assignments.length)})`}>
            {Array.from(new Set(assignments.map((assignment) => assignment.run_id))).map((runId) => (
              <div key={runId}>
                <h3>
                  <Link to={`/evaluations/${activeDatasetId}/${runId}`}>{runId}</Link>
                </h3>
                <ul className="sibling-list">
                  {assignments
                    .filter((assignment) => assignment.run_id === runId)
                    .map((asg) => (
                      <li key={asg.assignment_id}>
                        <Link
                          to={`/evaluations/${asg.dataset_id}/${asg.run_id}/assignments/${asg.assignment_id}`}
                        >
                          {asg.assignment_id} · {asg.task_id}
                        </Link>
                        <span className="sibling-rep">rep {asg.repetition}</span>
                        <span
                          className={`sibling-status sibling-status-${assignmentVerdictTone(
                            asg.terminal_status,
                            asg.verification_disposition,
                          )}`}
                          title={isNonVerdictOutcome(asg.terminal_status) ? NON_VERDICT_NOTE : undefined}
                        >
                          {asg.terminal_status.replace(/_/g, ' ')}
                        </span>
                      </li>
                    ))}
                </ul>
              </div>
            ))}
          </PanelSection>
        ) : null}
      </DetailLayout>
    </div>
  );
}
