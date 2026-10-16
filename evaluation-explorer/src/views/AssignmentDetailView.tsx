// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Assignment detail: one model, one scenario, one attempt, and the evidence
// trail behind its grade. The hero states the verdict and walks the evidence
// chain from scenario to signed audit slice; the main column replays what the
// model was asked, every tool call it made, what governance decided, and how
// it was graded; the aside carries proof, security signals, and measurements.

import type { ReactNode } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import type { AssignmentResult, MetricValue } from '../contract/types';
import { TOOL_SCORE_DIMENSIONS } from '../contract/types';
import { AssignmentEvidenceData } from '../components/AssignmentEvidenceData';
import { AssignmentActivitySummary } from '../components/AssignmentActivitySummary';
import { AgentTraceSection, agentTraceBinding } from '../components/AgentTraceSection';
import { ScenarioFixtureSection, scenarioFixtureBindings } from '../components/ScenarioFixtureSection';
import { AssignmentAuditProofRow, filterNonAuditEvidenceBindings } from '../components/AssignmentAuditProofRow';
import { EvidenceBindingsPanel } from '../components/EvidenceBindingsPanel';
import { RoleTranscripts } from '../components/RoleTranscripts';
import { ScenarioContextCard } from '../components/ScenarioContextCard';
import { TaskExpectationSection, TaskPromptSection, TaskProvidedSection } from '../components/TaskPromptSection';
import { UnifiedGradeBadges } from '../components/UnifiedGradeBadges';
import { DetailLayout, EvidenceChain, NON_VERDICT_NOTE, PageHero, PanelSection, TonePill } from '../components/evidence';
import { SCENARIO_CATEGORY_COPY, SCENARIO_TASK_BY_ID } from '../content/scenario-task';
import { useActiveDatasetId, useSelectedRelease } from '../state/dataset';
import { recordKey, useStoreState } from '../state/store';
import {
  CopyButton,
  EmptyState,
  ErrorState,
  StatTile,
  Timeline,
  UnavailableValue,
  formatDuration,
  formatLatency,
  formatThroughput,
  formatTokens,
  formatNumber,
} from '../components/shared';
import { formatRelativeTime } from '../utils/format';
import {
  assignmentEvidenceChain,
  assignmentGradeChips,
  assignmentHasAuditSlice,
  assignmentLifecycleEvents,
  assignmentMetricEntries,
  assignmentMetricFormatter,
  assignmentSecuritySignals,
  assignmentVerdictLabel,
  assignmentVerdictTone,
  isScenarioNotApplicableMetric,
  isNonVerdictOutcome,
  roleLabel,
  siblingRepetitions,
  type EvidenceChainStep,
} from './derived';

const SECTION = {
  task: 'assignment-task',
  trail: 'assignment-trail',
  fixture: 'assignment-scenario-fixture',
  agentTrace: 'assignment-agent-trace',
  governance: 'assignment-governance',
  criteria: 'assignment-criteria',
  scores: 'assignment-scores',
  proof: 'assignment-proof',
} as const;

/** Metric keys the verdict and measured values already show. */
const DECOMPOSED_SCORE_EXCLUDED = new Set(['pass', 'task_score', 'latency_ms']);

function lookupTask(id?: string) {
  if (!id) return undefined;
  const base = id.split('@')[0];
  return SCENARIO_TASK_BY_ID.get(id) ?? (base ? SCENARIO_TASK_BY_ID.get(base) : undefined);
}

function observationLabel(value: string): string {
  return value.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
}

function formatBytes(value: number): string {
  if (value >= 1024 ** 3) return `${formatNumber(value / 1024 ** 3, 1)} GiB`;
  if (value >= 1024 ** 2) return `${formatNumber(value / 1024 ** 2, 1)} MiB`;
  return `${formatNumber(value)} B`;
}

function formatPercent(value: number): string {
  return `${formatNumber(value, 1)}%`;
}

function formatTemperature(value: number): string {
  return `${formatNumber(value, 1)}°C`;
}

function formatPower(value: number): string {
  return `${formatNumber(value, 1)}W`;
}

function formatClockSpeed(value: number): string {
  if (value >= 1000) return `${formatNumber(value / 1000, 1)}GHz`;
  return `${formatNumber(value)}MHz`;
}

function terminalLabel(status: AssignmentResult['terminal_status']): string {
  return status.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
}

function toolActionSummaries(activity: AssignmentResult['activity_summary']): string[] {
  if (!activity) return [];
  const lines: string[] = [];

  if (activity.tool_calls.availability === 'observed') {
    for (const record of activity.tool_calls.records) {
      lines.push(`Called ${record.tool_label || 'an unlabeled tool'} — ${observationLabel(record.semantic_outcome)}`);
    }
  }
  if (activity.tool_decisions.availability === 'observed') {
    for (const record of activity.tool_decisions.records) {
      lines.push(
        `${record.selected ? 'Selected' : 'Considered but did not select'} ${record.tool_label || 'an unlabeled tool'} — ${observationLabel(record.outcome)}`,
      );
    }
  }
  if (lines.length === 0 && activity.policy_decisions.availability === 'observed') {
    for (const record of activity.policy_decisions.records) {
      lines.push(`Policy ${observationLabel(record.outcome)} for ${record.tool_label || 'an unlabeled tool'}`);
    }
  }
  return lines;
}

function trajectoryOutcomeLabel(outcome: string): string {
  return outcome.replace(/_/g, ' ').toUpperCase();
}

/** Trajectory outcome badge, guided retries, and the tools declared to the model. */
function FailureTrajectoryMeta({ assignment }: { assignment: AssignmentResult }) {
  const { trajectory_outcome: outcome, guided_retry_count: retries, tools_declared: declared } = assignment;
  if (!outcome && !retries && declared === undefined) return null;
  return (
    <div className="failure-trajectory-meta">
      {outcome ? <span className="task-flag failure-trajectory-badge">{trajectoryOutcomeLabel(outcome)}</span> : null}
      {retries ? <span className="failure-trajectory-retries">Guided retries: {retries}</span> : null}
      {declared !== undefined ? (
        <p className="failure-tools-declared">
          <strong>Tools declared:</strong>{' '}
          {declared.length > 0 ? declared.join(', ') : 'none'}
        </p>
      ) : null}
    </div>
  );
}

/** Why a non-completed assignment ended where it did. Rendered once, directly
 *  under the verdict, so the reason is read before the trail. */
function FailureDiagnosis({ assignment }: { assignment: AssignmentResult }) {
  if (assignment.terminal_status === 'completed') return null;
  const failingChips = assignmentGradeChips(assignment).filter(
    (chip) => chip.status === 'fail' || chip.status === 'invalid_evidence',
  );
  if (!assignment.failure_reason && !assignment.failure_output && failingChips.length === 0 && !assignment.missingness_reason) {
    return null;
  }
  const tone = assignmentVerdictTone(assignment.terminal_status, assignment.verification_disposition);
  return (
    <div className={`assignment-failure-callout assignment-failure-${tone}`} role="alert">
      <h3>Failure Diagnosis ({terminalLabel(assignment.terminal_status)})</h3>
      {assignment.failure_reason ? <p className="failure-reason">{assignment.failure_reason}</p> : null}
      <FailureTrajectoryMeta assignment={assignment} />
      {assignment.failure_output ? <p className="failure-output-detail">{assignment.failure_output}</p> : null}
      {failingChips.length > 0 ? (
        <ul className="failure-criteria-list">
          {failingChips.map((chip) => (
            <li key={chip.criterion_id}>
              <strong>{chip.criterion_id.replace(/-/g, ' ')}:</strong>{' '}
              {chip.explanation ? chip.explanation.replace(/_/g, ' ') : observationLabel(chip.status)}
              {chip.detail ? <span className="grade-detail">{chip.detail}</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
      {assignment.missingness_reason ? <p className="failure-missingness">{assignment.missingness_reason}</p> : null}
    </div>
  );
}

function failureDiagnosisShowsMissingness(assignment: AssignmentResult): boolean {
  if (assignment.terminal_status === 'completed' || !assignment.missingness_reason) return false;
  return true;
}

function ModelResponseSection({ assignment }: { assignment: AssignmentResult }) {
  const isFailure = assignment.terminal_status !== 'completed';
  const transcripts = assignment.role_transcripts ?? [];
  const hasResponse = Boolean(assignment.model_response);
  const toolActions = hasResponse ? [] : toolActionSummaries(assignment.activity_summary);
  const verdict = assignmentVerdictLabel(assignment.terminal_status, assignment.verification_disposition);

  return (
    <PanelSection id={SECTION.trail} className="assignment-model-response" title="What the model did">
      {transcripts.length > 0 ? (
        <p className="task-section-note">
          Lifted verbatim from the digest-bound role trace, in execution order: the exact arguments, the command
          the Operator resolved, and the result the model saw.
        </p>
      ) : null}

      {assignment.terminal_status === 'completed' ? <FailureTrajectoryMeta assignment={assignment} /> : null}
      {transcripts.length > 0 ? <RoleTranscripts transcripts={transcripts} variantId={assignment.variant_id} /> : null}
      {hasResponse && !transcripts.some((transcript) => transcript.response === assignment.model_response) ? (
        <div className="model-response-block">
          <div className="model-response-header">
            <span className="model-response-label">
              {isFailure ? "Model's Response (Resulted in Failure)" : "Model's Response"}
            </span>
            <span className="model-response-variant">{assignment.variant_id}</span>
            <CopyButton text={assignment.model_response!} label="model response" />
          </div>
          <pre className="model-response-text"><code>{assignment.model_response}</code></pre>
        </div>
      ) : transcripts.length > 0 ? null : toolActions.length > 0 ? (
        <div className="model-response-block model-response-tool-only">
          <div className="model-response-header">
            <span className="model-response-label">Tool activity was published; the response transcript is unavailable</span>
            <span className="model-response-variant">{assignment.variant_id}</span>
          </div>
          <ul className="model-response-tool-list">
            {toolActions.map((line, index) => (
              <li key={index}>{line}</li>
            ))}
          </ul>
          <p className="model-response-tool-verdict">Result: {verdict}</p>
        </div>
      ) : (
        <p className="model-response-empty">
          Response and tool transcript were not published for this assignment. This does not establish that
          the model produced no response or made no tool calls. Result: {verdict}.
        </p>
      )}
      {!isFailure && assignment.failure_output ? <div className="model-response-block">
        <h3>Captured failure output</h3>
        <pre className="model-response-text"><code>{assignment.failure_output}</code></pre>
      </div> : null}
    </PanelSection>
  );
}

function visibleToolScoreDimensions(assignment: AssignmentResult) {
  return TOOL_SCORE_DIMENSIONS.filter((dimension) => {
    const metric = assignment.benchmark_observations?.tool_scorecard?.[dimension];
    if (metric && isScenarioNotApplicableMetric(metric)) return false;
    return metric?.value !== undefined || assignment.scenario_summary?.tool_score_dimensions?.some((requirement) => requirement.dimension === dimension);
  });
}

function ToolScoreTile({ assignment, dimension }: { assignment: AssignmentResult; dimension: (typeof TOOL_SCORE_DIMENSIONS)[number] }) {
  const metric = assignment.benchmark_observations?.tool_scorecard?.[dimension];
  if (metric && isScenarioNotApplicableMetric(metric)) return null;
  const label = observationLabel(dimension);
  if (metric?.value !== undefined) return <StatTile label={label} value={formatNumber(metric.value)} />;
  const scenarioDimension = assignment.scenario_summary?.tool_score_dimensions?.find((req) => req.dimension === dimension);
  if (!scenarioDimension) return null;
  if (scenarioDimension.required) {
    return <StatTile label={label} value={<UnavailableValue reason="required but not observed" />} />;
  }
  return <StatTile label={label} value={<span className="score-not-applicable">Not scored for this scenario</span>} />;
}

function decomposedScores(assignment: AssignmentResult): Array<{ key: string; label: string; metric: MetricValue }> {
  const scorecardKeys: ReadonlySet<string> = assignment.benchmark_observations?.tool_scorecard
    ? new Set<string>(TOOL_SCORE_DIMENSIONS)
    : new Set<string>();
  return assignmentMetricEntries(assignment.metric_values).filter(
    ({ key, metric }) => !DECOMPOSED_SCORE_EXCLUDED.has(key) && !scorecardKeys.has(key) && !isScenarioNotApplicableMetric(metric),
  );
}

function ScoresSection({ assignment }: { assignment: AssignmentResult }) {
  const dimensions = visibleToolScoreDimensions(assignment);
  const decomposed = decomposedScores(assignment);
  if (dimensions.length === 0 && decomposed.length === 0) return null;
  return (
    <PanelSection id={SECTION.scores} className="assignment-scores" title="Scores">
      {decomposed.length > 0 ? (
        <>
          <h3 className="resource-group-heading">Decomposed scores</h3>
          <div className="stat-tile-grid">
            {decomposed.map(({ key, label, metric }) => (
              <StatTile
                key={key}
                label={label}
                value={metric.value !== undefined ? assignmentMetricFormatter(key)(metric.value) : <UnavailableValue reason={metric.unavailable_reason} />}
              />
            ))}
          </div>
        </>
      ) : null}
      {dimensions.length > 0 ? (
        <>
          {decomposed.length > 0 ? <h3 className="resource-group-heading">Tool-use scorecard</h3> : null}
          <div className="stat-tile-grid">
            {dimensions.map((dimension) => (
              <ToolScoreTile key={dimension} assignment={assignment} dimension={dimension} />
            ))}
          </div>
        </>
      ) : null}
    </PanelSection>
  );
}

function SignalsSection({ assignment }: { assignment: AssignmentResult }) {
  const signals = assignmentSecuritySignals(assignment);
  if (signals.length === 0) return null;
  return (
    <PanelSection className="assignment-signals" title="Security & escalation">
      <ul className="signal-list">
        {signals.map((signal) => (
          <li key={signal.key} className={`signal-row signal-${signal.tone}`}>
            <span className="signal-label">{signal.label}</span>
            <span className="signal-value">{signal.value}</span>
          </li>
        ))}
      </ul>
    </PanelSection>
  );
}

interface Measure {
  label: string;
  value: number | undefined;
  format: (value: number) => string;
}

function measured(label: string, metric: MetricValue<number> | undefined, format: (value: number) => string): Measure {
  return { label, value: metric?.value, format };
}

function measureGroups(assignment: AssignmentResult): Array<{ title: string; measures: Measure[] }> {
  const resource = assignment.resource_summary;
  const timing = assignment.benchmark_observations?.timing;
  const gpu = assignment.benchmark_observations?.gpu;
  const outputTokens = resource?.output_tokens?.value;
  const generationMs = timing?.generation_ms?.value;
  const tokensPerSecond =
    outputTokens !== undefined && generationMs !== undefined && generationMs > 0
      ? outputTokens / (generationMs / 1000)
      : undefined;
  return [
    {
      title: 'Timing',
      measures: [
        measured('Load', timing?.model_load_ms, formatLatency),
        measured('Generation', timing?.generation_ms, formatLatency),
        measured('Task', timing?.whole_task_ms, formatLatency),
        measured('TTFT', timing?.time_to_first_token_ms, formatLatency),
        measured('Latency', resource?.latency_ms, formatLatency),
      ],
    },
    {
      title: 'Memory & GPU',
      measures: [
        measured('VRAM before', gpu?.vram_before_bytes, formatBytes),
        measured('VRAM peak', gpu?.vram_peak_bytes, formatBytes),
        measured('RAM peak', gpu?.system_ram_peak_bytes, formatBytes),
        measured('GPU utilization', gpu?.utilization_percent, formatPercent),
        measured('GPU temperature', gpu?.temperature_celsius, formatTemperature),
        measured('GPU power', gpu?.power_watts, formatPower),
        measured('GPU clock', gpu?.clock_mhz, formatClockSpeed),
      ],
    },
    {
      title: 'Throughput',
      measures: [
        measured('Input', resource?.input_tokens, formatTokens),
        measured('Output', resource?.output_tokens, formatTokens),
        { label: 'Tokens/s', value: tokensPerSecond, format: formatThroughput },
        measured('Thinking', resource?.thinking_tokens, formatTokens),
        measured('Cache', resource?.cache_tokens, formatTokens),
      ],
    },
    { title: 'Reliability', measures: [measured('Retries', resource?.retries, formatNumber)] },
  ]
    .map((group) => ({ ...group, measures: group.measures.filter((measure) => measure.value !== undefined) }))
    .filter((group) => group.measures.length > 0);
}

function MeasuredValues({ groups }: { groups: ReturnType<typeof measureGroups> }) {
  return (
    <PanelSection className="assignment-resources" title="Measured values">
      {groups.map((group) => (
        <div key={group.title} className="resource-group">
          <h3 className="resource-group-heading">{group.title}</h3>
          <div className="stat-tile-grid stat-tile-grid-compact">
            {group.measures.map((measure) => (
              <StatTile key={measure.label} label={measure.label} value={measure.format(measure.value!)} />
            ))}
          </div>
        </div>
      ))}
    </PanelSection>
  );
}

/** Scroll target for an evidence-chain step; wraps sections owned by shared components. */
function Anchor({ id, children }: { id: string; children: ReactNode }) {
  return (
    <div id={id} className="assignment-anchor">
      {children}
    </div>
  );
}

export function AssignmentDetailView() {
  const { assignmentId, runId, datasetId: routeDataset } = useParams();
  const [params] = useSearchParams();
  const activeDatasetId = useActiveDatasetId(routeDataset ?? params.get('dataset') ?? undefined, useSelectedRelease());

  const assignment = useStoreState((state) =>
    assignmentId ? state.assignments.get(recordKey(activeDatasetId, assignmentId)) : undefined,
  );
  const lifecycleEvents = useStoreState((state) =>
    assignmentId
      ? assignmentLifecycleEvents(
          assignmentId,
          (state.assignmentEvents.get(recordKey(activeDatasetId, assignmentId)) ?? []).filter((event) => event.run_id === runId),
        )
      : [],
  );
  const sourceRecord = useStoreState((state) =>
    assignmentId ? state.assignmentSources.get(recordKey(activeDatasetId, assignmentId)) : undefined,
  );
  const siblingAssignments = useStoreState((state) =>
    assignment ? siblingRepetitions(assignment, Array.from(state.assignments.values())) : [],
  );
  const connection = useStoreState((state) => state.connection);

  if (!assignmentId || !runId) return <ErrorState message="No assignment selected." />;
  if (!assignment) return <EmptyState hasRecords={false} hasFilters={false} connection={connection} />;

  const task =
    lookupTask(assignment.task_id) ??
    lookupTask(assignment.scenario_id) ??
    lookupTask(assignment.scenario_summary?.scenario_id);

  const tone = assignmentVerdictTone(assignment.terminal_status, assignment.verification_disposition);
  const category = assignment.scenario_summary?.category ?? assignment.scenario_category;
  const gradeChips = assignmentGradeChips(assignment);
  const publishedActivity = Boolean(assignment.activity_summary);
  const transcripts = assignment.role_transcripts ?? [];
  const hasAgentTrace = Boolean(agentTraceBinding(assignment.evidence_bindings));
  const fixtureBindings = scenarioFixtureBindings(assignment.evidence_bindings);
  const hasFixture = Boolean(assignment.scenario_id && fixtureBindings.input && fixtureBindings.gold);
  const hasScores = visibleToolScoreDimensions(assignment).length > 0 || decomposedScores(assignment).length > 0;
  const groups = measureGroups(assignment);
  const otherEvidenceBindings = filterNonAuditEvidenceBindings(assignment.evidence_bindings);
  const hasEvidence = Boolean(otherEvidenceBindings?.length || assignment.verification_metadata);
  const hasAuditSlice = assignmentHasAuditSlice(assignment);
  const hasProof = hasEvidence || hasAuditSlice;
  const hasSignals = assignmentSecuritySignals(assignment).length > 0;
  const hasAside =
    hasProof || hasSignals || groups.length > 0 || assignment.stage_summary.length > 0 || lifecycleEvents.length > 0 || siblingAssignments.length > 0;
  const limitation = failureDiagnosisShowsMissingness(assignment)
    ? assignment.benchmark_observations?.unavailable_reasons.join(' · ')
    : assignment.missingness_reason ?? assignment.benchmark_observations?.unavailable_reasons.join(' · ');

  const chainTargets: Partial<Record<EvidenceChainStep['key'], string>> = {
    scenario: hasFixture ? SECTION.fixture : task ? SECTION.task : undefined,
    model: hasAgentTrace ? SECTION.agentTrace : SECTION.trail,
    tool_calls: hasAgentTrace ? SECTION.agentTrace : transcripts.length > 0 ? SECTION.trail : publishedActivity ? SECTION.governance : undefined,
    policy: publishedActivity ? SECTION.governance : undefined,
    governed_actions: publishedActivity ? SECTION.governance : undefined,
    graded: hasScores ? SECTION.scores : task ? SECTION.criteria : undefined,
    verified: hasProof ? SECTION.proof : undefined,
    audit_slice: hasAuditSlice ? SECTION.proof : undefined,
  };

  return (
    <div className="assignment-detail">
      <nav className="breadcrumb" aria-label="Breadcrumb">
        <Link to="/evaluations">Evaluations</Link>
        <span aria-hidden="true">/</span>
        <Link to={`/evaluations/${activeDatasetId}/${runId}`}>{runId}</Link>
        <span aria-hidden="true">/</span>
        <span>{assignment.assignment_id}</span>
      </nav>

      <PageHero
        kicker={`Assignment${category ? ` · ${SCENARIO_CATEGORY_COPY[category].label}` : ''}`}
        title={assignment.task_id}
        tone={tone}
        meta={<>
              <Link to={`/models/${activeDatasetId}/${assignment.variant_id}`}>{assignment.variant_id}</Link>
              <span>{roleLabel(assignment.role)}</span>
              <span>Repetition {assignment.repetition}</span>
              <span>{formatRelativeTime(assignment.observed_at)}</span>
              <span className="assignment-id">
                <code>{assignment.assignment_id}</code>
                <CopyButton text={assignment.assignment_id} label="assignment id" />
              </span>
        </>}
        verdict={<div aria-label="Assignment verdict">
            <TonePill tone={tone} className={`terminal-${assignment.terminal_status}`}>
              {assignmentVerdictLabel(assignment.terminal_status, assignment.verification_disposition)}
            </TonePill>
            {isNonVerdictOutcome(assignment.terminal_status) ? (
              <p className="evidence-verdict-note">{NON_VERDICT_NOTE}</p>
            ) : null}
          </div>}
      >

        <ScenarioContextCard scenario={assignment.scenario_summary} />

        {gradeChips.length > 0 ? (
          <div className="assignment-criteria-strip">
            <span className="assignment-strip-label">Criteria</span>
            <UnifiedGradeBadges assignment={assignment} />
          </div>
        ) : null}

        <EvidenceChain steps={assignmentEvidenceChain(assignment)} targets={chainTargets} />

        {limitation ? <p className="assignment-limitation">{limitation}</p> : null}
      </PageHero>

      <FailureDiagnosis assignment={assignment} />

      <DetailLayout asideLabel="Proof and measurements" aside={hasAside ? <>
            {hasProof ? (
              <div id={SECTION.proof} className="assignment-proof">
                <AssignmentAuditProofRow bindings={assignment.evidence_bindings} />
                {hasEvidence ? (
                  <EvidenceBindingsPanel bindings={otherEvidenceBindings} verification={assignment.verification_metadata} />
                ) : null}
              </div>
            ) : null}

            <SignalsSection assignment={assignment} />

            {groups.length > 0 ? <MeasuredValues groups={groups} /> : null}

            {assignment.stage_summary.length > 0 ? (
              <PanelSection className="assignment-stages" title="Stages">
                <ul className="stage-list">
                  {assignment.stage_summary.map((stage, index) => (
                    <li key={index}>
                      <span className="stage-name">{stage.name}</span>
                      <span className="stage-duration">{formatDuration(stage.duration_seconds)}</span>
                    </li>
                  ))}
                </ul>
              </PanelSection>
            ) : null}

            {lifecycleEvents.length > 0 ? (
              <PanelSection className="assignment-feed-timeline" title="Timeline">
                <Timeline
                  events={lifecycleEvents.map((event) => ({
                    observed_at: event.observed_at,
                    kind: event.kind,
                    stage_label: event.stage_label,
                    completed: event.completed,
                    total: event.total,
                    assignment_id: event.assignment_id,
                    run_id: event.run_id,
                    dataset_id: activeDatasetId,
                  }))}
                />
              </PanelSection>
            ) : null}

            {siblingAssignments.length > 0 ? (
              <PanelSection className="assignment-repetitions" title="Sibling repetitions">
                <ul className="sibling-list">
                  {siblingAssignments.map((sibling) => (
                    <li key={sibling.assignment_id}>
                      <Link to={`/evaluations/${sibling.dataset_id}/${sibling.run_id}/assignments/${sibling.assignment_id}`}>
                        {sibling.assignment_id}
                      </Link>
                      <span className="sibling-rep">rep {sibling.repetition}</span>
                      <span className={`sibling-status sibling-status-${assignmentVerdictTone(sibling.terminal_status, sibling.verification_disposition)}`} title={isNonVerdictOutcome(sibling.terminal_status) ? NON_VERDICT_NOTE : undefined}>
                        {terminalLabel(sibling.terminal_status)}
                      </span>
                    </li>
                  ))}
                </ul>
              </PanelSection>
            ) : null}
          </> : undefined}>
        {task ? <Anchor id={SECTION.task}>
          <TaskPromptSection task={task} className="assignment-task-prompt" />
          <TaskProvidedSection task={task} className="assignment-task-provided" />
        </Anchor> : null}
        <ScenarioFixtureSection id={SECTION.fixture} bindings={assignment.evidence_bindings} scenarioId={assignment.scenario_id} />
        <ModelResponseSection assignment={assignment} />
        <AgentTraceSection id={SECTION.agentTrace} bindings={assignment.evidence_bindings} runId={runId} assignmentId={assignment.assignment_id} />
        <Anchor id={SECTION.governance}><AssignmentActivitySummary activity={assignment.activity_summary} /></Anchor>
        {task ? <Anchor id={SECTION.criteria}><TaskExpectationSection task={task} className="assignment-task-expected" /></Anchor> : null}
        <ScoresSection assignment={assignment} />
        <AssignmentEvidenceData assignment={assignment} events={lifecycleEvents} source={sourceRecord} />
      </DetailLayout>
    </div>
  );
}
