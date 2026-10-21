// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Evaluation detail view. Per the plan: status, quality, suite, execution
// arm, model-role mapping, start/end/elapsed, assignment progress, a link to
// the live page, terminal outcome counts, metric cards with denominators,
// per-model results, task/assignment table, verification summary, resource
// summary, and public-safe methodology links.

import { useMemo, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import { useActiveDatasetId, useSelectedRelease } from '../state/dataset';
import { releaseLabel } from '../content/release';
import { recordKey, useStoreState } from '../state/store';
import { DataTable, type DataTableColumn } from '../components/DataTable';
import { RunFailuresSection } from '../components/RunFailuresSection';
import { EvidenceChain, NON_VERDICT_NOTE, OutcomeBreakdown, PageHero, PanelSection, TonePill } from '../components/evidence';
import { UnifiedGradeBadges } from '../components/UnifiedGradeBadges';
import {
  DetailRow,
  EmptyState,
  ErrorState,
  MetricCard,
  ProgressBar,
  QualityBadge,
  StatTile,
  SafeSourceLink,
  UnavailableValue,
  formatTimestamp,
  formatDuration,
  formatPercent,
  formatLatency,
  formatThroughput,
  formatNumber,
} from '../components/shared';
import type { AssignmentResult, VerifierState } from '../contract/types';
import { isCapabilityQualificationScenario } from '../content/scenario-task';
import { assignmentVerdictTone, campaignTerminalProgress, isNonVerdictOutcome, lifecycleStateTone, nativeResultTone, nativeScenarioTone, roleLabel, verifierStateTone } from './derived';

const verifierStateLabels: Record<VerifierState, string> = {
  not_run: 'Not yet verified',
  passed: 'Passed',
  failed: 'Failed',
  not_applicable: 'Not applicable',
};

export function EvaluationDetailView() {
  const { runId: routeRun, datasetId: routeDataset } = useParams();
  const [params] = useSearchParams();
  // Single-segment URLs (/evaluations/<run>) resolve the run against the
  // active dataset rather than erroring.
  const runId = routeRun ?? routeDataset;
  const datasetParam = routeRun ? routeDataset : undefined;
  const activeDatasetId = useActiveDatasetId(datasetParam ?? params.get('dataset') ?? undefined, useSelectedRelease());

  const run = useStoreState((state) =>
    runId ? state.evaluations.get(recordKey(activeDatasetId, runId)) : undefined,
  );
  const assignments = useStoreState((state) =>
    runId
      ? Array.from(state.assignments.values()).filter(
          (a) => a.run_id === runId && a.dataset_id === activeDatasetId,
        )
      : [],
  );
  const connection = useStoreState((state) => state.connection);
  const [assignmentSearch, setAssignmentSearch] = useState('');
  const [statusFilter, setStatusFilter] = useState('all');

  const filteredAssignments = useMemo(() => {
    const needle = assignmentSearch.toLowerCase();
    return assignments.filter(
      (a) => {
        const verdict = a.terminal_status === 'completed' || a.terminal_status === 'model_failed';
        if (statusFilter === 'verdict' && !verdict) return false;
        if (statusFilter === 'non-verdict' && (verdict || a.terminal_status === 'running' || a.terminal_status === 'queued')) return false;
        return a.assignment_id.toLowerCase().includes(needle) ||
        a.task_id.toLowerCase().includes(needle) ||
        a.variant_id.toLowerCase().includes(needle);
      },
    );
  }, [assignments, assignmentSearch, statusFilter]);

  const assignmentColumns = useMemo<DataTableColumn<AssignmentResult>[]>(
    () => [
      {
        id: 'assignment',
        header: 'Assignment',
        accessorKey: 'assignment_id',
        cell: ({ row }) => (
          <Link to={`/evaluations/${row.original.dataset_id}/${row.original.run_id}/assignments/${row.original.assignment_id}`}>
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
      { id: 'rep', header: 'Rep', accessorKey: 'repetition' },
      { id: 'status', header: 'Status', accessorKey: 'terminal_status', cell: ({ row }) => <TonePill tone={assignmentVerdictTone(row.original.terminal_status, row.original.verification_disposition)} title={isNonVerdictOutcome(row.original.terminal_status) ? NON_VERDICT_NOTE : undefined}>{row.original.terminal_status.replace(/_/g, ' ')}</TonePill> },
      {
        id: 'verification',
        header: 'Verification',
        accessorFn: (row: AssignmentResult) => row.verification_disposition ?? '',
        cell: ({ row }) =>
          row.original.verification_disposition ? <TonePill tone={verifierStateTone(row.original.verification_disposition)}>{verifierStateLabels[row.original.verification_disposition]}</TonePill> : <UnavailableValue reason="verification not published" />,
      },
      { id: 'criteria', header: 'Criteria', cell: ({ row }) => <UnifiedGradeBadges assignment={row.original} />, enableSorting: false },
      { id: 'duration', header: 'Duration', cell: ({ row }) => row.original.resource_summary?.latency_ms?.value !== undefined ? formatLatency(row.original.resource_summary.latency_ms.value) : <UnavailableValue reason="duration not published" />, enableSorting: false },
    ],
    [],
  );

  if (!runId) return <ErrorState message="No run selected." />;
  if (!run) return <EmptyState hasRecords={false} hasFilters={false} connection={connection} />;

  const headlineLimitations = new Set(run.benchmark_unavailable_reasons ?? []);
  for (const [label, metric] of [
    ['Pass rate', run.headline_metrics.pass_rate],
    ['Latency p50', run.headline_metrics.latency_p50_ms],
    ['Output throughput p50', run.headline_metrics.output_throughput_p50_tokens_per_second],
  ] as const) {
    if (metric?.unavailable_reason) headlineLimitations.add(`${label}: ${metric.unavailable_reason}`);
    if (metric && 'unavailable_count' in metric && metric.value !== undefined && metric.unavailable_count > 0) {
      headlineLimitations.add(`${label}: ${metric.observed_count} of ${metric.eligible_count} eligible assignments contributed evidence.`);
    }
  }

  return (
    <div className="evaluation-detail">
      <nav className="breadcrumb" aria-label="Breadcrumb">
        <Link to="/evaluations">Evaluations</Link>
        <span aria-hidden="true">/</span>
        <span>{run.run_id}</span>
      </nav>

      <PageHero
        kicker={run.native_result ? 'Conformance run' : 'Model campaign'}
        title={run.run_id}
        tone={run.native_result ? nativeResultTone(run.native_result.summary_status) : lifecycleStateTone(run.lifecycle_state)}
        meta={<><span>{run.suite_id}</span><span>{releaseLabel(run)}</span><code>{run.dataset_id}</code>{run.started_at ? <span>{formatTimestamp(run.started_at)}</span> : null}{run.elapsed_seconds !== undefined ? <span>{formatDuration(run.elapsed_seconds)}</span> : null}</>}
        verdict={<><TonePill tone={run.native_result ? nativeResultTone(run.native_result.summary_status) : lifecycleStateTone(run.lifecycle_state)}>{run.native_result?.summary_status ?? run.lifecycle_state}</TonePill>{!run.native_result ? <TonePill tone={verifierStateTone(run.verifier_state)} title={run.verifier_failure_summary}>{verifierStateLabels[run.verifier_state]}</TonePill> : null}</>}
      >
      <div className="quality-banner">
        <QualityBadge state={run.quality_state} />
        {run.verifier_failure_summary ? <span className="verifier-failure">{run.verifier_failure_summary}</span> : null}
      </div>
      {headlineLimitations.size > 0 ? <ul className="catalog-limitations">{Array.from(headlineLimitations).map((reason) => <li key={reason}>{reason}</li>)}</ul> : null}
      <EvidenceChain steps={[
        { key: 'run', label: 'Run', value: run.lifecycle_state, tone: lifecycleStateTone(run.lifecycle_state) },
        { key: 'results', label: run.native_result ? 'Scenarios' : 'Assignments', value: run.native_result ? `${run.native_result.passed_verdict_count}/${run.native_result.required_verdict_count} verdicts passed` : `${campaignTerminalProgress(run).done}/${run.assignment_total} terminal`, tone: run.native_result ? nativeResultTone(run.native_result.summary_status) : run.assignment_failed > 0 ? 'warn' : 'info' },
        { key: 'verification', label: 'Verification', value: run.native_result ? run.native_result.verification_valid ? 'Valid' : 'Invalid' : run.verifier_state === 'not_run' ? 'Pending verification' : verifierStateLabels[run.verifier_state], tone: run.native_result ? run.native_result.verification_valid ? 'ok' : 'critical' : verifierStateTone(run.verifier_state) },
        { key: 'evidence', label: 'Evidence bundle', value: run.evidence_link ? 'Published' : 'Not published', tone: run.evidence_link ? 'info' : 'neutral' },
      ]} targets={{ run: 'run-identity', results: 'run-results', verification: 'run-verification', evidence: 'run-evidence' }} />
      {run.lifecycle_state === 'running' || run.lifecycle_state === 'queued' ? <Link to="/" className="campaign-primary-action">Watch it Live</Link> : null}
      </PageHero>
      <div className="evaluation-sections">

      <PanelSection title={run.native_result ? 'Invariant summary' : 'Assignment progress'}>
        <ProgressBar
          completed={
            run.native_result?.passed_verdict_count ??
            campaignTerminalProgress(run).done
          }
          total={
            run.native_result?.required_verdict_count ??
            campaignTerminalProgress(run).total
          }
          label={run.native_result ? 'Required invariants' : 'Assignments'}
        />
        {run.native_result ? <><p>{run.native_result.summary}</p><TonePill tone="info">{run.native_result.active_posture} · {run.native_result.lane}</TonePill></> : <OutcomeBreakdown outcomes={run.terminal_outcomes} />}
      </PanelSection>

      {!run.native_result ? <PanelSection title="Headline metrics">
        <div className="stat-tile-grid">
          <MetricCard variant="tile" label="Pass rate" metric={run.headline_metrics.pass_rate} formatter={formatPercent} />
          <MetricCard variant="tile" label="Latency p50" metric={run.headline_metrics.latency_p50_ms} formatter={formatLatency} />
          <MetricCard variant="tile" label="Output throughput p50" metric={run.headline_metrics.output_throughput_p50_tokens_per_second ?? run.headline_metrics.throughput} formatter={formatThroughput} />
        </div>
      </PanelSection> : null}

      {run.native_result ? (
        <PanelSection id="run-results" title="Scenario and invariant results">
          <div className="native-scenario-grid">
          {run.native_result.scenarios.map((scenario) => (
            <details key={scenario.scenario_id} className="native-scenario">
              <summary><strong>{scenario.scenario_id}@{scenario.scenario_version}</strong><TonePill tone={nativeScenarioTone(scenario.status)}>{scenario.status}</TonePill></summary>
              <ul>
                {scenario.verdicts.map((verdict) => (
                  <li key={verdict.assertion_id}>
                    <strong>{verdict.assertion_id}@{verdict.assertion_version}</strong>: <TonePill tone={nativeResultTone(verdict.status)}>{verdict.status}</TonePill>
                  </li>
                ))}
              </ul>
            </details>
          ))}
          </div>
        </PanelSection>
      ) : null}

      {run.native_result && run.native_result.metrics.length > 0 ? <PanelSection title="Native metrics"><div className="stat-tile-grid">
        {run.native_result.metrics.map((metric) => <StatTile key={`${metric.metric_id}@${metric.metric_version}`} label={`${metric.metric_id}@${metric.metric_version}`} value={metric.unit === 'ratio' ? formatPercent(metric.value) : formatNumber(metric.value)} hint={`${formatNumber(metric.numerator)} / ${formatNumber(metric.denominator)}`} />)}
      </div></PanelSection> : null}

      {!run.native_result ? <>
        <PanelSection id="run-results" title={`Assignments (${formatNumber(assignments.length)})`}>
          {assignments.length === 0 ? (
            <p>No assignment results for this run.</p>
          ) : (
            <>
              <div className="table-toolbar evaluation-assignment-filters">
                <input
                  type="search"
                  placeholder="Search assignments..."
                  value={assignmentSearch}
                  aria-label="Search assignments"
                  onChange={(e) => setAssignmentSearch(e.target.value)}
                />
                <select aria-label="Filter assignments by verdict" value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}>
                  <option value="all">All statuses</option><option value="verdict">Model verdicts</option><option value="non-verdict">Non-verdict outcomes</option>
                </select>
              </div>
              <DataTable
                data={filteredAssignments}
                columns={assignmentColumns}
                pageSize={50}
                caption={`Assignments for ${run.run_id}`}
                empty={<p>No assignments match the search or status filter.</p>}
              />
            </>
          )}
        </PanelSection>
        <RunFailuresSection assignments={assignments} connection={connection} />
      </> : null}
      <div className="evaluation-context">
      <PanelSection id="run-identity" title="Run identity">
        <dl>
          <DetailRow label="Run ID">{run.run_id}</DetailRow>
          <DetailRow label="Execution arm">{run.arm}</DetailRow>
          <DetailRow label="Evaluation unit">{run.evaluation_unit ? `${run.evaluation_unit.charAt(0).toUpperCase()}${run.evaluation_unit.slice(1)} evaluation` : <UnavailableValue reason="not observed in this dataset" />}</DetailRow>
          <DetailRow label="Campaign">{run.campaign_id ?? <UnavailableValue reason="no campaign recorded" />}</DetailRow>
          <DetailRow label="Platform release">{releaseLabel(run)}</DetailRow>
          {run.source_revision ? <DetailRow label="Source revision">{run.source_revision}</DetailRow> : null}
          <DetailRow label="Started">{run.started_at ? formatTimestamp(run.started_at) : '—'}</DetailRow>
          <DetailRow label="Ended">{run.ended_at ? formatTimestamp(run.ended_at) : '—'}</DetailRow>
          <DetailRow label="Elapsed">{run.elapsed_seconds ? formatDuration(run.elapsed_seconds) : '—'}</DetailRow>
          {run.native_result ? <DetailRow label="Active posture">{run.native_result.active_posture}</DetailRow> : null}
          {run.native_result ? <DetailRow label="Lane">{run.native_result.lane}</DetailRow> : null}
        </dl>
      </PanelSection>

      {run.model_role_mapping && Object.keys(run.model_role_mapping).length > 0 ? (
        <PanelSection title="Model-role mapping">
          <ul className="role-mapping">
            {Object.entries(run.model_role_mapping).map(([role, variantId]) => (
              <li key={role}>
                <span className="role-label">{roleLabel(role)}</span>
                <Link to={`/models/${activeDatasetId}/${variantId}`}>{variantId}</Link>
              </li>
            ))}
          </ul>
        </PanelSection>
      ) : null}

      <PanelSection id="run-verification" title="Verification summary">
        <dl>
          <DetailRow label="Verifier state"><TonePill tone={verifierStateTone(run.verifier_state)}>{verifierStateLabels[run.verifier_state]}</TonePill></DetailRow>
          {run.native_result ? <DetailRow label="Independent verification"><TonePill tone={run.native_result.verification_valid ? 'ok' : 'critical'}>{run.native_result.verification_valid ? 'Valid' : 'Invalid'} ({run.native_result.verification_failure_count} failures)</TonePill></DetailRow> : null}
          {run.verification_metadata?.verifier_release_version ? <DetailRow label="Verifier release">{run.verification_metadata.verifier_release_version}</DetailRow> : null}
          {run.verification_metadata?.verifier_contract_version ? <DetailRow label="Verifier contract">{run.verification_metadata.verifier_contract_version}</DetailRow> : null}
          {run.verification_metadata?.report_digest ? <DetailRow label="Report digest">{run.verification_metadata.report_digest}</DetailRow> : null}
          {run.verification_metadata?.population_digest ? <DetailRow label="Population digest">{run.verification_metadata.population_digest}</DetailRow> : null}
          {run.verification_metadata?.verified_at ? <DetailRow label="Verified at">{formatTimestamp(run.verification_metadata.verified_at)}</DetailRow> : null}
          {run.verifier_failure_summary ? <DetailRow label="Failure summary">{run.verifier_failure_summary}</DetailRow> : null}
        </dl>
      </PanelSection>
      <PanelSection id="run-evidence" title="Evidence bundle">
        {run.evidence_link ? <SafeSourceLink href={run.evidence_link} label="Public-safe evidence" /> : <p className="panel-note">Evidence bundle not published.</p>}
      </PanelSection>
      </div>
      </div>
    </div>
  );
}
