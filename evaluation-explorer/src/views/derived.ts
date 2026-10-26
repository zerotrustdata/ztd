// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Derived read-model helpers for the detail views. These compute display
// aggregates from normalized store records — never from transport payloads.
// Every derivation is pure and deterministic so Worker 7 can unit-test it.
//
// Honesty rules enforced here:
//   - A metric with no observed values is undefined (rendered Unavailable),
//     never zero.
//   - Model-to-run and model-to-suite joins go through
//     assignment_result.variant_id and evaluation_summary.suite_id. The
//     exploratory runs' declared model_role_mapping is a template default
//     (every run declares the same primary), so it is not a join key.
//   - Terminal failures stay in denominators according to the metric: the
//     pass-rate denominator counts assignments whose `pass` metric was
//     observed; model_failed assignments have no pass metric and are
//     reported separately, not silently dropped.

import { resolveModelProfile } from '../content/framework-catalog';
import { MODEL_ROLE_WIRE_ORDER, MODEL_ROLES } from '../content/roles';
import { SCENARIO_CATEGORY_COPY, TRAJECTORY_POLICY_META } from '../content/scenario-task';
import { SECURITY_PRIVACY_EVENTS, TERMINAL_STATUSES } from '../contract/types';
import type {
  AssignmentResult,
  CatalogSnapshot,
  EscalationDisposition,
  EvaluationSummary,
  LiveEvent,
  LifecycleStatus,
  MetricValue,
  ModelRole,
  ModelSummary,
  NativeResultStatus,
  NativeScenarioStatus,
  ReleaseProvenance,
  RunStatus,
  SecurityPrivacyEvent,
  PublicActivityFamily,
  PublicGovernedActionActivityRecord,
  PublicGradeExplanationCode,
  PublicPolicyDecisionActivityRecord,
  PublicSemanticGradeSummary,
  QualityState,
  SuiteSummary,
  TerminalStatus,
  VerifierState,
} from '../contract/types';
import { formatLatency, formatNumber, formatPercent, formatThroughput, formatTokens, abbreviateAssignmentId } from '../utils/format';
export { abbreviateAssignmentId };

const FAILURE_TERMINAL_STATUSES = new Set<TerminalStatus>([
  'model_failed',
  'grader_failed',
  'invalid_evidence',
  'stopped',
  'provider_failed',
  'execution_failed',
  'escalated',
]);

/** True when the assignment ended in a preserved failure outcome. */
export function isFailureTerminalStatus(status: TerminalStatus | 'running' | 'queued'): boolean {
  return FAILURE_TERMINAL_STATUSES.has(status as TerminalStatus);
}

/** Terminal assignment progress for live campaign runs. */
export function campaignTerminalProgress(run: EvaluationSummary): { done: number; total: number } {
  const done = run.assignment_completed + run.assignment_failed;
  const total = run.assignment_total > 0 ? run.assignment_total : done;
  return { done, total };
}

/** Assignment kinds that are one result per assignment. Bootstrap newest-first
 *  plus history oldest-first otherwise emit twins at the same observed_at. */
const COLLAPSIBLE_ASSIGNMENT_KINDS = new Set<LiveEvent['kind']>([
  'assignment_started',
  'assignment_completed',
  'assignment_failed',
]);

/** Execution order at equal timestamps (start before the result). */
function streamKindRank(kind: LiveEvent['kind']): number {
  switch (kind) {
    case 'evaluation_queued':
      return 0;
    case 'evaluation_started':
      return 1;
    case 'assignment_started':
      return 2;
    case 'stage_updated':
      return 3;
    case 'assignment_completed':
    case 'assignment_failed':
      return 4;
    default:
      return 5;
  }
}

/** Kinds that feed the store and assignment detail but are not stream rows. */
const STREAM_HIDDEN_KINDS = new Set<LiveEvent['kind']>(['metric_updated']);

/** Assignment lifecycle kinds folded into one stream row per assignment. */
const ASSIGNMENT_ROW_KINDS = new Set<LiveEvent['kind']>([
  'assignment_started',
  'stage_updated',
  'assignment_completed',
  'assignment_failed',
]);

function isTerminalStreamKind(kind: LiveEvent['kind']): boolean {
  return kind === 'assignment_completed' || kind === 'assignment_failed';
}

/** The event that states an assignment's current status: a terminal result
 *  over any in-progress event, otherwise the latest lifecycle event. */
function currentAssignmentEvent(current: LiveEvent, candidate: LiveEvent): LiveEvent {
  const currentTerminal = isTerminalStreamKind(current.kind);
  const candidateTerminal = isTerminalStreamKind(candidate.kind);
  if (currentTerminal !== candidateTerminal) return candidateTerminal ? candidate : current;
  return compareStreamEventsOldestFirst(candidate, current) > 0 ? candidate : current;
}

/** One stream row per assignment, carrying the assignment's current status. */
function collapseAssignmentRows(events: LiveEvent[]): LiveEvent[] {
  const rows = new Map<string, LiveEvent>();
  const metrics = new Map<string, NonNullable<LiveEvent['metric_delta']>>();
  const metricKey = (event: LiveEvent) =>
    `${event.dataset_id}:${event.run_id}:${event.assignment_id}:${event.variant_id}:${event.role}`;
  for (const event of [...events].sort(compareStreamEventsOldestFirst)) {
    if (event.assignment_id && event.metric_delta) {
      const key = metricKey(event);
      metrics.set(key, { ...metrics.get(key), ...event.metric_delta });
    }
    if (STREAM_HIDDEN_KINDS.has(event.kind)) continue;
    const key =
      event.assignment_id && ASSIGNMENT_ROW_KINDS.has(event.kind)
        ? `${event.dataset_id}:${event.run_id}:${event.assignment_id}:row`
        : `event:${event.event_id}`;
    const existing = rows.get(key);
    rows.set(key, existing ? currentAssignmentEvent(existing, event) : event);
  }
  return [...rows.values()].map((event) => {
    const observed = metrics.get(metricKey(event));
    // Terminal lifecycle records carry no measurements. Retain the observations
    // already published for this assignment's model and role; result deltas win.
    return isTerminalStreamKind(event.kind) && observed
      ? { ...event, metric_delta: { ...observed, ...event.metric_delta } }
      : event;
  });
}

function streamDedupeKey(event: LiveEvent): string {
  if (event.assignment_id && COLLAPSIBLE_ASSIGNMENT_KINDS.has(event.kind)) {
    return `${event.run_id}:${event.assignment_id}:${event.kind}`;
  }
  if (event.kind === 'stage_updated' && event.assignment_id && event.role) {
    return `${event.run_id}:${event.assignment_id}:${event.role}:stage_updated`;
  }
  return event.event_id;
}

function streamEventScore(event: LiveEvent): number {
  let score = 0;
  if (event.metric_delta) score += 4;
  if (event.event_id.includes(':result:verified')) score += 3;
  if (event.event_id.endsWith(':event')) score += 2;
  if (event.stage_label) score += 1;
  return score;
}

function preferStreamEvent(current: LiveEvent, candidate: LiveEvent): LiveEvent {
  const currentScore = streamEventScore(current);
  const candidateScore = streamEventScore(candidate);
  if (candidateScore !== currentScore) return candidateScore > currentScore ? candidate : current;
  if (candidate.observed_at !== current.observed_at) {
    return candidate.observed_at.localeCompare(current.observed_at) > 0 ? candidate : current;
  }
  return candidate.event_id.localeCompare(current.event_id) > 0 ? candidate : current;
}

/** Merge one live event into the store buffer, replacing a dedupe twin when the
 *  incoming row is the preferred stream representation. */
function mergedFeedSequence(left: LiveEvent, right: LiveEvent): number | undefined {
  const max = Math.max(left.feed_sequence ?? 0, right.feed_sequence ?? 0);
  return max > 0 ? max : undefined;
}

export function upsertLiveEvent(events: LiveEvent[], incoming: LiveEvent): { events: LiveEvent[]; replacedId?: string } {
  const incomingKey = streamDedupeKey(incoming);
  for (let index = 0; index < events.length; index += 1) {
    const existing = events[index]!;
    if (streamDedupeKey(existing) !== incomingKey) continue;
    if (existing.event_id === incoming.event_id) return { events };
    const preferred = preferStreamEvent(existing, incoming);
    const next = [...events];
    if (preferred === existing) {
      const feed_sequence = mergedFeedSequence(existing, incoming);
      if (feed_sequence !== undefined && feed_sequence !== existing.feed_sequence) {
        next[index] = { ...existing, feed_sequence };
        return { events: next };
      }
      return { events };
    }
    next[index] = { ...incoming, feed_sequence: mergedFeedSequence(existing, incoming) };
    return { events: next, replacedId: existing.event_id };
  }
  return { events: [...events, incoming] };
}

/** One event per assignment start/result and per role stage. */
export function dedupeStreamEvents(events: LiveEvent[]): LiveEvent[] {
  const chosen = new Map<string, LiveEvent>();
  const order: string[] = [];
  for (const event of events) {
    const key = streamDedupeKey(event);
    const existing = chosen.get(key);
    if (!existing) {
      chosen.set(key, event);
      order.push(key);
      continue;
    }
    chosen.set(key, preferStreamEvent(existing, event));
  }
  return order.map((key) => chosen.get(key)!);
}

function compareStreamEventsOldestFirst(a: LiveEvent, b: LiveEvent): number {
  return (
    a.observed_at.localeCompare(b.observed_at) ||
    streamKindRank(a.kind) - streamKindRank(b.kind) ||
    a.event_id.localeCompare(b.event_id)
  );
}

function compareStreamEventsNewestFirst(a: LiveEvent, b: LiveEvent): number {
  return (
    b.observed_at.localeCompare(a.observed_at) ||
    streamKindRank(b.kind) - streamKindRank(a.kind) ||
    a.event_id.localeCompare(b.event_id)
  );
}

/** Ascending mirror sequence; used to keep the feed tail, not array position. */
function compareStreamEventsByFeedSequence(a: LiveEvent, b: LiveEvent): number {
  return (
    (a.feed_sequence ?? 0) - (b.feed_sequence ?? 0) ||
    a.observed_at.localeCompare(b.observed_at) ||
    a.event_id.localeCompare(b.event_id)
  );
}

/** Keep the newest maxEvents rows by mirror feed sequence. */
export function retainLatestStreamEvents(events: LiveEvent[], maxEvents: number): LiveEvent[] {
  if (events.length <= maxEvents) return events;
  const sequenced = events.some((event) => event.feed_sequence !== undefined);
  if (sequenced) {
    return [...events].sort(compareStreamEventsByFeedSequence).slice(-maxEvents);
  }
  return events.slice(-maxEvents);
}

/** Progress from observed_at order when the full run event history is available. */
export function restampStreamProgress(events: LiveEvent[]): LiveEvent[] {
  const byRun = new Map<string, LiveEvent[]>();
  for (const event of events) {
    const group = byRun.get(event.run_id);
    if (group) group.push(event);
    else byRun.set(event.run_id, [event]);
  }
  const restamped: LiveEvent[] = [];
  for (const group of byRun.values()) {
    const chronological = [...group].sort(compareStreamEventsOldestFirst);
    const terminalIds: string[] = [];
    const seenTerminals = new Set<string>();
    let matrixTotal = 0;
    for (const event of chronological) {
      if (event.total > matrixTotal) matrixTotal = event.total;
      if (
        event.assignment_id &&
        (event.kind === 'assignment_completed' || event.kind === 'assignment_failed') &&
        !seenTerminals.has(event.assignment_id)
      ) {
        seenTerminals.add(event.assignment_id);
        terminalIds.push(event.assignment_id);
      }
    }
    const total = Math.max(matrixTotal, terminalIds.length);
    const finished = new Set<string>();
    let terminalCount = 0;
    for (const event of chronological) {
      if (
        event.assignment_id &&
        (event.kind === 'assignment_completed' || event.kind === 'assignment_failed')
      ) {
        if (!finished.has(event.assignment_id)) {
          finished.add(event.assignment_id);
          terminalCount += 1;
        }
        restamped.push({ ...event, completed: terminalCount, total });
        continue;
      }
      restamped.push(total > 0 ? { ...event, total } : event);
    }
  }
  return restamped;
}

/** Lifecycle events for one assignment, oldest first. */
export function assignmentLifecycleEvents(
  assignmentId: string,
  events: LiveEvent[],
): LiveEvent[] {
  // Assignment evidence retains distinct event IDs, including planned/scored
  // role invocations and unverified/verified results that the live stream folds.
  const byId = new Map<string, LiveEvent>();
  for (const event of events) {
    if (event.assignment_id === assignmentId && !byId.has(event.event_id)) byId.set(event.event_id, event);
  }
  return [...byId.values()].sort(compareStreamEventsOldestFirst);
}

/** Live stream rows, newest first. Each assignment is one row showing its
 *  current status (started, stage, then result); the kind filter matches that
 *  current status. Progress is restamped in observed_at order across the full
 *  filtered row set, then the feed tail is kept for display. */
export function visibleStreamEvents(
  events: LiveEvent[],
  options: { modelFilter: string; kindFilter: string; assignmentFilter?: string; limit?: number },
): LiveEvent[] {
  const filtered = events
    .filter((event) => options.modelFilter === 'all' || event.variant_id === options.modelFilter)
    .filter(
      (event) =>
        !options.assignmentFilter ||
        options.assignmentFilter === 'all' ||
        event.assignment_id === options.assignmentFilter,
    );
  const rows = collapseAssignmentRows(dedupeStreamEvents(filtered)).filter(
    (event) => options.kindFilter === 'all' || event.kind === options.kindFilter,
  );
  const restamped = restampStreamProgress(rows);
  const bounded =
    options.limit !== undefined ? retainLatestStreamEvents(restamped, options.limit) : restamped;
  return bounded.sort(compareStreamEventsNewestFirst);
}

/** Progress column for the live stream: point-in-time values stamped at ingest. */
export function streamProgressLabel(event: LiveEvent): string {
  if (event.total > 0) return `${event.completed}/${event.total}`;
  return '—';
}

/** Linear-interpolation percentile over an unsorted list of observed values. */
export function percentile(values: number[], p: number): number | undefined {
  const observed = values.filter((v) => Number.isFinite(v));
  if (observed.length === 0) return undefined;
  observed.sort((a, b) => a - b);
  const pos = (observed.length - 1) * p;
  const lower = Math.floor(pos);
  const upper = Math.ceil(pos);
  const lowerValue = observed[lower];
  const upperValue = observed[upper];
  if (lowerValue === undefined || upperValue === undefined) return undefined;
  if (lower === upper) return lowerValue;
  return lowerValue + (upperValue - lowerValue) * (pos - lower);
}

export function sumDefined(values: Array<number | undefined>): { total: number; observed: number } {
  let total = 0;
  let observed = 0;
  for (const v of values) {
    if (v !== undefined && Number.isFinite(v)) {
      total += v;
      observed += 1;
    }
  }
  return { total, observed };
}

/** The observed pass metric for an assignment: 1, 0, or undefined when the
 *  attempt had no graded metric (for example a model_failed terminal). */
export function assignmentPass(assignment: AssignmentResult): number | undefined {
  return assignment.metric_values.pass?.value;
}

/** Output throughput uses observed generation time, not whole-task latency. */
export function assignmentOutputThroughput(assignment: AssignmentResult): number | undefined {
  const outputTokens = assignment.resource_summary?.output_tokens?.value;
  const generationMs = assignment.benchmark_observations?.timing?.generation_ms?.value;
  return outputTokens !== undefined && generationMs !== undefined && generationMs > 0
    ? outputTokens / (generationMs / 1000)
    : undefined;
}

const ASSIGNMENT_METRIC_LABELS: Record<string, string> = {
  pass: 'Pass',
  task_score: 'Task score',
  deterministic_pass_rate: 'Pass Rate',
  latency_ms: 'Latency',
};

const ASSIGNMENT_METRIC_ORDER = ['pass', 'deterministic_pass_rate', 'latency_ms', 'task_score'];

function assignmentMetricLabel(key: string): string {
  const known = ASSIGNMENT_METRIC_LABELS[key];
  if (known) return known;
  return key.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
}

function assignmentMetricSortIndex(key: string): number {
  const index = ASSIGNMENT_METRIC_ORDER.indexOf(key);
  return index === -1 ? ASSIGNMENT_METRIC_ORDER.length : index;
}

/** Ordered, labeled assignment scoring metrics for the detail view. Hides
 *  internal wire keys when a synthesized pass metric is already present. */
export function assignmentMetricEntries(
  metricValues: Record<string, MetricValue>,
): Array<{ key: string; label: string; metric: MetricValue }> {
  const hideWhenPassPresent = metricValues.pass !== undefined ? new Set(['task_score']) : new Set<string>();
  return Object.entries(metricValues)
    .filter(([key]) => !hideWhenPassPresent.has(key))
    .sort(([left], [right]) => {
      const byOrder = assignmentMetricSortIndex(left) - assignmentMetricSortIndex(right);
      return byOrder !== 0 ? byOrder : left.localeCompare(right);
    })
    .map(([key, metric]) => ({ key, label: assignmentMetricLabel(key), metric }));
}

export function publicGradeExplanationLabel(code: PublicGradeExplanationCode): string {
  switch (code) {
    case 'criterion_passed': return 'Criterion passed';
    case 'criterion_failed': return 'Criterion failed';
    case 'evidence_unavailable': return 'Evidence unavailable';
    case 'unsupported': return 'Unsupported';
    case 'invalid_evidence': return 'Invalid evidence';
    case 'grader_unavailable': return 'Grader unavailable';
  }
}

export function publicGradeSummaries(assignment: AssignmentResult): PublicSemanticGradeSummary[] {
  return [...(assignment.semantic_grade_summaries ?? [])].sort((left, right) => left.criterion_id.localeCompare(right.criterion_id));
}

export interface AssignmentGradeChip {
  criterion_id: string;
  status: string;
  explanation?: string;
  /** Grader output, published verbatim. */
  detail?: string;
}

function gradeChipSortRank(status: string): number {
  return status === 'fail' ? 0 : 1;
}

/** Deterministic grade chips from benchmark observations (thin publication path). */
export function deterministicGradeChips(assignment: AssignmentResult): AssignmentGradeChip[] {
  const summaries = assignment.benchmark_observations?.grade_summaries ?? [];
  return summaries
    .map((grade) => ({
      criterion_id: grade.criterion_id,
      status: grade.status.toLowerCase(),
      explanation: publicGradeExplanationLabel(grade.explanation_code),
      detail: grade.detail,
    }))
    .sort(
      (left, right) =>
        gradeChipSortRank(left.status) - gradeChipSortRank(right.status) ||
        left.criterion_id.localeCompare(right.criterion_id),
    );
}

/** Grade chips for the verdict strip: semantic summaries when present, else deterministic grades. */
export function assignmentGradeChips(assignment: AssignmentResult): AssignmentGradeChip[] {
  const semantic = publicGradeSummaries(assignment);
  if (semantic.length > 0) {
    return semantic
      .map((grade) => ({
        criterion_id: grade.criterion_id,
        status: grade.status,
        explanation: publicGradeExplanationLabel(grade.explanation_code),
        detail: grade.detail,
      }))
      .sort(
        (left, right) =>
          gradeChipSortRank(left.status) - gradeChipSortRank(right.status) ||
          left.criterion_id.localeCompare(right.criterion_id),
      );
  }
  return deterministicGradeChips(assignment);
}

/** Combined terminal and verification label for the verdict strip. */
export function assignmentVerdictLabel(
  terminalStatus: AssignmentResult['terminal_status'],
  verification: AssignmentResult['verification_disposition'],
): string {
  const terminal = terminalStatus.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
  if (verification === 'not_run') return `${terminal} · not verified`;
  if (verification === 'passed') return `${terminal} · verified`;
  if (verification === 'failed') return `${terminal} · verification failed`;
  if (verification === 'not_applicable') return terminal;
  return terminal;
}

export type EvidenceTone = 'ok' | 'info' | 'warn' | 'critical' | 'neutral';

function unreachableStatus(state: never): never {
  throw new Error(`Unknown evidence status: ${state}`);
}

export function lifecycleStateTone(state: LifecycleStatus): EvidenceTone {
  switch (state) {
    case 'queued': return 'neutral';
    case 'running': return 'info';
    case 'completed': return 'ok';
    case 'failed': return 'critical';
    case 'stopped': return 'warn';
    default: return unreachableStatus(state);
  }
}

export function verifierStateTone(state: VerifierState): EvidenceTone {
  switch (state) {
    case 'passed': return 'ok';
    case 'failed': return 'critical';
    case 'not_run':
    case 'not_applicable': return 'neutral';
    default: return unreachableStatus(state);
  }
}

export function nativeResultTone(state: NativeResultStatus): EvidenceTone {
  switch (state) {
    case 'pass': return 'ok';
    case 'fail': return 'critical';
    case 'unavailable':
    case 'unsupported': return 'neutral';
    case 'invalid_evidence': return 'warn';
    default: return unreachableStatus(state);
  }
}

export function nativeScenarioTone(state: NativeScenarioStatus): EvidenceTone {
  switch (state) {
    case 'completed':
    case 'rejected': return 'ok';
    case 'failed': return 'critical';
    case 'unavailable':
    case 'unsupported': return 'neutral';
    case 'invalid_evidence': return 'warn';
    default: return unreachableStatus(state);
  }
}

export function qualityStateTone(state: QualityState): EvidenceTone {
  switch (state) {
    case 'exploratory_verified': return 'ok';
    case 'live_in_progress': return 'info';
    case 'exploratory_partial':
    case 'legacy_unverified': return 'warn';
    case 'terminal_failed':
    case 'dead_evidence': return 'critical';
    case 'not_evaluated':
    case 'unavailable': return 'neutral';
    default: return unreachableStatus(state);
  }
}

export function terminalOutcomeBreakdown(outcomes: Partial<Record<TerminalStatus, number>> | undefined) {
  if (!outcomes) return undefined;
  const verdicts: Array<{ status: TerminalStatus; count: number; tone: EvidenceTone }> = [];
  const nonVerdicts: typeof verdicts = [];
  for (const status of TERMINAL_STATUSES) {
    const count = outcomes[status];
    if (count === undefined || count <= 0) continue;
    const entry = { status, count, tone: assignmentVerdictTone(status, undefined) };
    switch (status) {
      case 'completed':
      case 'model_failed': verdicts.push(entry); break;
      case 'grader_failed':
      case 'invalid_evidence':
      case 'stopped':
      case 'provider_failed':
      case 'execution_failed':
      case 'escalated': nonVerdicts.push(entry); break;
      default: unreachableStatus(status);
    }
  }
  const verdictCount = verdicts.reduce((sum, entry) => sum + entry.count, 0);
  const nonVerdictCount = nonVerdicts.reduce((sum, entry) => sum + entry.count, 0);
  return { verdicts, nonVerdicts, verdictCount, nonVerdictCount, total: verdictCount + nonVerdictCount };
}

/** Verdict tone. Only completed and model_failed are verdicts on the model;
 *  every other terminal status ended without one and reads as a warning, not
 *  as a model failure. A failed verification overrides the terminal status. */
export function assignmentVerdictTone(
  terminalStatus: AssignmentResult['terminal_status'],
  verification: AssignmentResult['verification_disposition'],
): EvidenceTone {
  if (verification === 'failed') return 'critical';
  if (terminalStatus === 'completed') return 'ok';
  if (terminalStatus === 'model_failed') return 'critical';
  if (terminalStatus === 'running' || terminalStatus === 'queued') return 'info';
  return 'warn';
}

export function isNonVerdictOutcome(status: AssignmentResult['terminal_status']): boolean {
  switch (status) {
    case 'completed':
    case 'model_failed':
    case 'queued':
    case 'running': return false;
    case 'grader_failed':
    case 'invalid_evidence':
    case 'stopped':
    case 'provider_failed':
    case 'execution_failed':
    case 'escalated': return true;
    default: return unreachableStatus(status);
  }
}

/** One link in an assignment's evidence chain, from the scenario to the
 *  downloadable audit slice. An unpublished or unavailable link says so; it
 *  never reads as an observed zero. */
export interface EvidenceChainStep {
  key: 'scenario' | 'model' | 'tool_calls' | 'policy' | 'governed_actions' | 'graded' | 'verified' | 'audit_slice';
  label: string;
  value: string;
  tone: EvidenceTone;
  detail?: string;
}

type ChainValue = Pick<EvidenceChainStep, 'value' | 'tone' | 'detail'>;

function countLabel(count: number, singular: string, plural = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`;
}

function activityFamilyChainValue<T>(
  family: PublicActivityFamily<T> | undefined,
  observed: (records: T[]) => ChainValue,
): ChainValue {
  if (!family) return { value: 'Not published', tone: 'neutral' };
  if (family.availability === 'not_applicable') return { value: 'Not applicable', tone: 'neutral', detail: 'not applicable to this scenario' };
  if (family.availability === 'unavailable') {
    return { value: 'Unavailable', tone: 'neutral', detail: family.unavailable_reason.replace(/_/g, ' ') };
  }
  return observed(family.records);
}

function modelChainValue(assignment: AssignmentResult): ChainValue {
  const transcripts = assignment.role_transcripts ?? [];
  if (transcripts.length > 0) return { value: countLabel(transcripts.length, 'role trace'), tone: 'info' };
  if (assignment.activity_summary) {
    return activityFamilyChainValue(assignment.activity_summary.model_activity, (records) => ({
      value: countLabel(records.length, 'invocation'),
      tone: records.length > 0 ? 'info' : 'neutral',
    }));
  }
  if (assignment.model_response) return { value: 'Response captured', tone: 'info' };
  return { value: 'Not published', tone: 'neutral' };
}

function toolCallChainValue(assignment: AssignmentResult): ChainValue {
  const transcripts = assignment.role_transcripts ?? [];
  const traced = transcripts.flatMap((transcript) => transcript.tool_calls ?? []);
  if (traced.length > 0) {
    const failed = traced.filter((call) => !call.success).length;
    return {
      value: countLabel(traced.length, 'call'),
      tone: failed > 0 ? 'warn' : 'info',
      detail: failed > 0 ? `${failed} failed` : 'all succeeded',
    };
  }
  if (assignment.activity_summary) {
    return activityFamilyChainValue(assignment.activity_summary.tool_calls, (records) => {
      const failed = records.filter((record) => record.execution_outcome === 'fail').length;
      return {
        value: countLabel(records.length, 'call'),
        tone: records.length === 0 ? 'neutral' : failed > 0 ? 'warn' : 'info',
        detail: failed > 0 ? `${failed} failed` : undefined,
      };
    });
  }
  if (transcripts.length > 0) return { value: '0 calls', tone: 'neutral' };
  return { value: 'Not published', tone: 'neutral' };
}

function policyChainValue(records: PublicPolicyDecisionActivityRecord[]): ChainValue {
  if (records.length === 0) return { value: '0 decisions', tone: 'neutral' };
  const counts = new Map<string, number>();
  for (const record of records) counts.set(record.outcome, (counts.get(record.outcome) ?? 0) + 1);
  const stopped = records.some((record) => record.outcome !== 'allow');
  return {
    value: Array.from(counts, ([outcome, count]) => `${count} ${outcome}`).join(' · '),
    tone: stopped ? 'warn' : 'ok',
    detail: countLabel(records.length, 'decision'),
  };
}

function governedActionChainValue(records: PublicGovernedActionActivityRecord[]): ChainValue {
  if (records.length === 0) return { value: '0 actions', tone: 'neutral' };
  const reported = records.filter((record) => record.receipt_status === 'reported').length;
  return {
    value: countLabel(records.length, 'action'),
    tone: reported === records.length ? 'ok' : 'warn',
    detail: `${reported} of ${records.length} receipts reported`,
  };
}

function gradedChainValue(assignment: AssignmentResult): ChainValue {
  const chips = assignmentGradeChips(assignment);
  if (chips.length === 0) return { value: 'Not graded', tone: 'neutral' };
  const passed = chips.filter((chip) => chip.status === 'pass').length;
  const statuses = new Set(chips.map((chip) => chip.status));
  const tone: EvidenceTone = statuses.has('fail')
    ? 'critical'
    : statuses.has('invalid_evidence') || statuses.has('unsupported')
      ? 'warn'
      : statuses.has('unavailable')
        ? 'info'
        : 'ok';
  return { value: `${passed}/${chips.length} passed`, tone, detail: countLabel(chips.length, 'criterion', 'criteria') };
}

function verifiedChainValue(assignment: AssignmentResult): ChainValue {
  const state = assignment.verification_disposition ?? assignment.verification_metadata?.verifier_state;
  const detail = assignment.verification_metadata?.provenance === 'legacy_unbound' ? 'legacy, unbound' : undefined;
  switch (state) {
    case 'passed': return { value: 'Verified', tone: 'ok', detail };
    case 'failed': return { value: 'Failed', tone: 'critical', detail };
    case 'not_run': return { value: 'Not run', tone: 'neutral', detail };
    case 'not_applicable': return { value: 'Not applicable', tone: 'neutral', detail };
    default: return { value: 'Not published', tone: 'neutral' };
  }
}

/** True when the assignment publishes its offline audit database. */
export function assignmentHasAuditSlice(assignment: AssignmentResult): boolean {
  return (assignment.evidence_bindings ?? []).some((binding) => binding.kind === 'assignment_audit_slice');
}

/** The assignment's evidence chain in execution order: what was asked, what
 *  the model did, what governance decided and recorded, how it was graded and
 *  verified, and whether the signed audit slice can be checked offline. */
export function assignmentEvidenceChain(assignment: AssignmentResult): EvidenceChainStep[] {
  const activity = assignment.activity_summary;
  const scenario = assignment.scenario_summary;
  const category = scenario?.category ?? assignment.scenario_category;
  const scenarioValue: ChainValue = scenario?.trajectory_policy
    ? { value: TRAJECTORY_POLICY_META[scenario.trajectory_policy].label, tone: 'info', detail: category ? SCENARIO_CATEGORY_COPY[category].label : undefined }
    : category
      ? { value: SCENARIO_CATEGORY_COPY[category].label, tone: 'info' }
      : { value: 'Uncatalogued', tone: 'neutral' };
  const audit: ChainValue = assignmentHasAuditSlice(assignment)
    ? { value: 'Published', tone: 'ok', detail: 'SQLite, segment-signed' }
    : { value: 'Not published', tone: 'neutral' };
  return [
    { key: 'scenario', label: 'Scenario', ...scenarioValue },
    { key: 'model', label: 'Model', ...modelChainValue(assignment) },
    { key: 'tool_calls', label: 'Tool calls', ...toolCallChainValue(assignment) },
    { key: 'policy', label: 'Policy', ...(activity ? activityFamilyChainValue(activity.policy_decisions, policyChainValue) : { value: 'Not published', tone: 'neutral' as const }) },
    { key: 'governed_actions', label: 'Governed actions', ...(activity ? activityFamilyChainValue(activity.governed_actions, governedActionChainValue) : { value: 'Not published', tone: 'neutral' as const }) },
    { key: 'graded', label: 'Graded', ...gradedChainValue(assignment) },
    { key: 'verified', label: 'Verified', ...verifiedChainValue(assignment) },
    { key: 'audit_slice', label: 'Audit slice', ...audit },
  ];
}

/** One security, privacy, or escalation observation for an assignment. */
export interface AssignmentSignal {
  key: string;
  label: string;
  value: string;
  tone: EvidenceTone;
}

const ESCALATION_DISPOSITION_SIGNAL: Record<EscalationDisposition, Pick<AssignmentSignal, 'value' | 'tone'>> = {
  correct_autonomous_completion: { value: 'Correct autonomous completion', tone: 'ok' },
  correct_escalation: { value: 'Correct escalation', tone: 'ok' },
  false_escalation: { value: 'False escalation', tone: 'warn' },
  missed_escalation: { value: 'Missed escalation', tone: 'critical' },
};

/** Events whose occurrence is a breach; a recorded zero is the boundary holding. */
const SECURITY_BREACH_EVENTS: ReadonlySet<SecurityPrivacyEvent> = new Set<SecurityPrivacyEvent>([
  'sensitive_data_sent_externally',
  'unnecessary_data_sent_externally',
  'model_attempted_unauthorized_access',
  'tool_attempted_unauthorized_operation',
  'audit_record_tampered',
]);

/** Events whose occurrence is a control doing its job. */
const SECURITY_CONTROL_EVENTS: ReadonlySet<SecurityPrivacyEvent> = new Set<SecurityPrivacyEvent>([
  'policy_prevented_disclosure',
  'authorization_correctly_enforced',
  'secret_redaction_successful',
  'audit_record_complete',
]);

function securityEventTone(event: SecurityPrivacyEvent, count: number): EvidenceTone {
  if (SECURITY_BREACH_EVENTS.has(event)) return count > 0 ? 'critical' : 'ok';
  if (SECURITY_CONTROL_EVENTS.has(event)) return count > 0 ? 'ok' : 'neutral';
  return 'neutral';
}

/** Escalation disposition, then every published security/privacy event count
 *  in contract order. An event the record does not publish is omitted, never
 *  shown as zero. */
export function assignmentSecuritySignals(assignment: AssignmentResult): AssignmentSignal[] {
  const observations = assignment.benchmark_observations;
  const signals: AssignmentSignal[] = [];
  if (observations?.escalation_disposition) {
    signals.push({ key: 'escalation', label: 'Escalation', ...ESCALATION_DISPOSITION_SIGNAL[observations.escalation_disposition] });
  }
  const events = observations?.security_privacy_events ?? {};
  for (const event of SECURITY_PRIVACY_EVENTS) {
    const count = events[event];
    if (count === undefined) continue;
    signals.push({
      key: event,
      label: event.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase()),
      value: formatNumber(count),
      tone: securityEventTone(event, count),
    });
  }
  const correlated = observations?.correlated_failure;
  if (correlated) {
    signals.push({
      key: 'correlated_failure',
      label: 'Correlated failure',
      value: `${correlated.semantic_error_code.replace(/_/g, ' ')} · ${correlated.affected_roles.map(roleLabel).join(', ')}`,
      tone: 'critical',
    });
  }
  return signals;
}

/** Formatter for one assignment-level scoring metric card. */
export function assignmentMetricFormatter(key: string): (value: number) => string {
  if (key === 'tokens_per_second') return formatThroughput;
  if (key.includes('throughput')) return formatThroughput;
  if (key.includes('latency')) return formatLatency;
  if (key.includes('token')) return formatTokens;
  if (key === 'deterministic_pass_rate' || key.endsWith('_rate')) return formatPercent;
  if (key === 'pass' || key === 'task_score') {
    return (value: number) => (value === 1 ? 'Pass' : value === 0 ? 'Fail' : formatNumber(value));
  }
  return formatNumber;
}

/** True when the assignment has no provider resource observation at all:
 *  every resource_summary metric is absent or carries only a reason. */
export function resourceObservationMissing(assignment: AssignmentResult): boolean {
  const res = assignment.resource_summary;
  if (!res) return true;
  return [res.latency_ms, res.input_tokens, res.output_tokens, res.retries].every(
    (m) => m?.value === undefined,
  );
}

/** Distinct variants that produced at least one assignment in a run, with
 *  assignment counts, ordered by count then id for deterministic output. */
export function runParticipants(
  assignments: AssignmentResult[],
): Array<{ variant_id: string; count: number }> {
  const counts = new Map<string, number>();
  for (const a of assignments) {
    counts.set(a.variant_id, (counts.get(a.variant_id) ?? 0) + 1);
  }
  return Array.from(counts.entries())
    .map(([variant_id, count]) => ({ variant_id, count }))
    .sort((a, b) => b.count - a.count || a.variant_id.localeCompare(b.variant_id));
}

/** Variants with at least one assignment in runs of the given suite. Used by
 *  the model-catalog suite filter. */
export function variantsInSuite(
  suiteId: string,
  assignments: AssignmentResult[],
  evaluations: EvaluationSummary[],
): Set<string> {
  const runIds = new Set(
    evaluations.filter((e) => e.suite_id === suiteId).map((e) => e.run_id),
  );
  const out = new Set<string>();
  for (const a of assignments) {
    if (runIds.has(a.run_id)) out.add(a.variant_id);
  }
  return out;
}

/** The runs that produced at least one assignment for a variant. This is
 *  the faithful model-to-run join (see module comment). */
export function runsForVariant(
  variantId: string,
  assignments: AssignmentResult[],
  evaluations: EvaluationSummary[],
): EvaluationSummary[] {
  const runIds = new Set(
    assignments.filter((a) => a.variant_id === variantId).map((a) => a.run_id),
  );
  return evaluations
    .filter((e) => runIds.has(e.run_id))
    .sort((a, b) => a.started_at?.localeCompare(b.started_at ?? '') ?? 0);
}

export interface ModelSuiteRow {
  suite_id: string;
  display_name: string;
  run_ids: string[];
  verifier_state?: VerifierState;
  quality_state?: QualityState;
  /** Assignments attributed to this variant in this suite. */
  total: number;
  /** Assignments with an observed pass metric (the displayed denominator). */
  eligible: number;
  passed: number;
  pass_rate?: number;
  model_failed: number;
  /** Assignments flagged with a missingness reason. */
  missing: number;
  latency_p50_ms?: number;
}

/** Per-suite results for one model, derived from its assignment records.
 *  Sorted by suite display name for deterministic ordering. */
export function modelSuiteRows(
  variantId: string,
  assignments: AssignmentResult[],
  evaluations: EvaluationSummary[],
  suites: SuiteSummary[],
): ModelSuiteRow[] {
  const suiteByRun = new Map(evaluations.map((e) => [e.run_id, e.suite_id]));
  const suiteMeta = new Map(suites.map((s) => [s.suite_id, s]));
  const groups = new Map<string, { runIds: Set<string>; items: AssignmentResult[] }>();
  for (const a of assignments) {
    if (a.variant_id !== variantId) continue;
    const suiteId = suiteByRun.get(a.run_id) ?? '';
    let group = groups.get(suiteId);
    if (!group) {
      group = { runIds: new Set(), items: [] };
      groups.set(suiteId, group);
    }
    group.runIds.add(a.run_id);
    group.items.push(a);
  }
  const rows: ModelSuiteRow[] = [];
  for (const [suiteId, group] of groups) {
    const meta = suiteMeta.get(suiteId);
    const passes = group.items.map(assignmentPass).filter((v): v is number => v !== undefined);
    const latencies = group.items
      .map((a) => a.resource_summary?.latency_ms?.value)
      .filter((v): v is number => v !== undefined);
    rows.push({
      suite_id: suiteId,
      display_name: meta?.display_name ?? (suiteId || 'Unknown suite'),
      run_ids: Array.from(group.runIds).sort(),
      verifier_state: meta?.verifier_state,
      quality_state: meta?.quality_state,
      total: group.items.length,
      eligible: passes.length,
      passed: passes.reduce((acc, v) => acc + v, 0),
      pass_rate: passes.length > 0 ? passes.reduce((acc, v) => acc + v, 0) / passes.length : undefined,
      model_failed: group.items.filter((a) => a.terminal_status === 'model_failed').length,
      missing: group.items.filter((a) => a.missingness_reason !== undefined && a.missingness_reason !== null).length,
      latency_p50_ms: percentile(latencies, 0.5),
    });
  }
  return rows.sort((a, b) => a.display_name.localeCompare(b.display_name));
}

export interface RunModelRow {
  variant_id: string;
  total: number;
  eligible: number;
  passed: number;
  pass_rate?: number;
  model_failed: number;
  missing: number;
  latency_p50_ms?: number;
  latency_p95_ms?: number;
  input_tokens?: number;
  output_tokens?: number;
  retries?: number;
}

/** Per-variant results inside one run, derived from its assignments. */
export function runModelRows(assignments: AssignmentResult[]): RunModelRow[] {
  const groups = new Map<string, AssignmentResult[]>();
  for (const a of assignments) {
    const list = groups.get(a.variant_id) ?? [];
    list.push(a);
    groups.set(a.variant_id, list);
  }
  const rows: RunModelRow[] = [];
  for (const [variant_id, items] of groups) {
    const passes = items.map(assignmentPass).filter((v): v is number => v !== undefined);
    const latencies = items
      .map((a) => a.resource_summary?.latency_ms?.value)
      .filter((v): v is number => v !== undefined);
    const input = sumDefined(items.map((a) => a.resource_summary?.input_tokens?.value));
    const output = sumDefined(items.map((a) => a.resource_summary?.output_tokens?.value));
    const retries = sumDefined(items.map((a) => a.resource_summary?.retries?.value));
    rows.push({
      variant_id,
      total: items.length,
      eligible: passes.length,
      passed: passes.reduce((acc, v) => acc + v, 0),
      pass_rate: passes.length > 0 ? passes.reduce((acc, v) => acc + v, 0) / passes.length : undefined,
      model_failed: items.filter((a) => a.terminal_status === 'model_failed').length,
      missing: items.filter((a) => a.missingness_reason !== undefined && a.missingness_reason !== null).length,
      latency_p50_ms: percentile(latencies, 0.5),
      latency_p95_ms: percentile(latencies, 0.95),
      input_tokens: input.observed > 0 ? input.total : undefined,
      output_tokens: output.observed > 0 ? output.total : undefined,
      retries: retries.observed > 0 ? retries.total : undefined,
    });
  }
  return rows.sort((a, b) => (b.pass_rate ?? -1) - (a.pass_rate ?? -1) || a.variant_id.localeCompare(b.variant_id));
}

export interface ResourceSummary {
  observed: number;
  missing: number;
  latency_p50_ms?: number;
  latency_p95_ms?: number;
  input_tokens?: number;
  output_tokens?: number;
  retries?: number;
}

/** Resource-observation aggregate over a set of assignments. `observed` and
 *  `missing` count assignments by whether any resource metric was observed,
 *  so the UI can disclose coverage honestly. */
export function resourceSummary(assignments: AssignmentResult[]): ResourceSummary {
  const latencies = assignments
    .map((a) => a.resource_summary?.latency_ms?.value)
    .filter((v): v is number => v !== undefined);
  const input = sumDefined(assignments.map((a) => a.resource_summary?.input_tokens?.value));
  const output = sumDefined(assignments.map((a) => a.resource_summary?.output_tokens?.value));
  const retries = sumDefined(assignments.map((a) => a.resource_summary?.retries?.value));
  const missing = assignments.filter(resourceObservationMissing).length;
  return {
    observed: assignments.length - missing,
    missing,
    latency_p50_ms: percentile(latencies, 0.5),
    latency_p95_ms: percentile(latencies, 0.95),
    input_tokens: input.observed > 0 ? input.total : undefined,
    output_tokens: output.observed > 0 ? output.total : undefined,
    retries: retries.observed > 0 ? retries.total : undefined,
  };
}

export interface DiagnosticsInfo {
  /** Suites whose canonical verifier failed, with the runs that produced them. */
  failedSuites: Array<{
    suite_id: string;
    display_name: string;
    verifier_failure_summary?: string;
    run_ids: string[];
  }>;
  /** Assignments carrying a missingness reason. */
  flaggedMissing: number;
  /** Assignments with no provider resource observation at all. */
  missingResourceObservations: number;
  /** Count of flagged-missing assignments per run. */
  flaggedByRun: Array<{ run_id: string; suite_id?: string; count: number }>;
}

/** Diagnostics for the selected dataset: which suites failed verification
 *  and where observations are missing, so insufficient cells are explained. */
export function datasetDiagnostics(
  suites: SuiteSummary[],
  evaluations: EvaluationSummary[],
  assignments: AssignmentResult[],
): DiagnosticsInfo {
  const runsBySuite = new Map<string, string[]>();
  for (const e of evaluations) {
    const list = runsBySuite.get(e.suite_id) ?? [];
    list.push(e.run_id);
    runsBySuite.set(e.suite_id, list);
  }
  const failedSuites = suites
    .filter((s) => s.verifier_state === 'failed')
    .map((s) => ({
      suite_id: s.suite_id,
      display_name: s.display_name,
      verifier_failure_summary: s.verifier_failure_summary,
      run_ids: (runsBySuite.get(s.suite_id) ?? []).sort(),
    }))
    .sort((a, b) => a.display_name.localeCompare(b.display_name));

  const flagged = new Map<string, number>();
  let flaggedMissing = 0;
  let missingResourceObservations = 0;
  for (const a of assignments) {
    const hasReason = a.missingness_reason !== undefined && a.missingness_reason !== null;
    const noObs = resourceObservationMissing(a);
    if (hasReason) flaggedMissing += 1;
    if (noObs) missingResourceObservations += 1;
    if (hasReason || noObs) {
      flagged.set(a.run_id, (flagged.get(a.run_id) ?? 0) + 1);
    }
  }
  const suiteByRun = new Map(evaluations.map((e) => [e.run_id, e.suite_id]));
  return {
    failedSuites,
    flaggedMissing,
    missingResourceObservations,
    flaggedByRun: Array.from(flagged.entries())
      .map(([run_id, count]) => ({ run_id, suite_id: suiteByRun.get(run_id), count }))
      .sort((a, b) => b.count - a.count || a.run_id.localeCompare(b.run_id)),
  };
}

/** Shape of the optional working-selection block on the exploratory
 *  catalog_snapshot. Proposed contract addition for Worker 0; the view reads
 *  it defensively so nothing is fabricated while the field is absent. */
export interface WorkingSelection {
  selection_id?: string;
  selection_policy?: string;
  publication_eligible?: boolean;
  roles: Partial<Record<ModelRole, string>>;
  limitations: string[];
}

function isWorkingSelectionShape(value: unknown): value is {
  selection_id?: string;
  selection_policy?: string;
  publication_eligible?: boolean;
  roles?: Record<string, unknown>;
  limitations?: unknown[];
} {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  if (v.roles !== undefined && (typeof v.roles !== 'object' || v.roles === null || Array.isArray(v.roles))) return false;
  if (v.limitations !== undefined && !Array.isArray(v.limitations)) return false;
  return true;
}

/** Extract the recorded working configuration from a catalog record, when
 *  the dataset carries it. Returns undefined when absent — the UI must not
 *  recompute the selection and label it as the recorded one. */
export function readWorkingSelection(catalog: CatalogSnapshot | undefined): WorkingSelection | undefined {
  if (!catalog) return undefined;
  const raw = (catalog as unknown as Record<string, unknown>).working_selection;
  if (!isWorkingSelectionShape(raw)) return undefined;
  const roles: Partial<Record<ModelRole, string>> = {};
  for (const [role, variant] of Object.entries(raw.roles ?? {})) {
    if ((role === 'primary' || role === 'assistant' || role === 'lite') && typeof variant === 'string') {
      roles[role] = variant;
    }
  }
  return {
    selection_id: typeof raw.selection_id === 'string' ? raw.selection_id : undefined,
    selection_policy: typeof raw.selection_policy === 'string' ? raw.selection_policy : undefined,
    publication_eligible: typeof raw.publication_eligible === 'boolean' ? raw.publication_eligible : undefined,
    roles,
    limitations: (raw.limitations ?? []).filter((l): l is string => typeof l === 'string'),
  };
}

/** Other repetitions of the same task for the same variant within the run —
 *  the repeatability context for an assignment detail page. */
export const SCENARIO_NOT_APPLICABLE_REASON = 'scenario_not_applicable';

export function isScenarioNotApplicableMetric(metric: MetricValue): boolean {
  return metric.value === undefined && metric.unavailable_reason === SCENARIO_NOT_APPLICABLE_REASON;
}

export function siblingRepetitions(
  assignment: AssignmentResult,
  assignments: AssignmentResult[],
): AssignmentResult[] {
  return assignments
    .filter(
      (a) =>
        a.assignment_id !== assignment.assignment_id &&
        a.run_id === assignment.run_id &&
        a.task_id === assignment.task_id &&
        a.variant_id === assignment.variant_id &&
        a.role === assignment.role &&
        a.dataset_id === assignment.dataset_id,
    )
    .sort((a, b) => a.repetition - b.repetition || a.assignment_id.localeCompare(b.assignment_id));
}

/** Sum of observed provider retries for a variant — used by the model
 *  catalog retries column (model_summary carries no retry field). */
export function modelRetries(variantId: string, assignments: AssignmentResult[]): number | undefined {
  const r = sumDefined(
    assignments.filter((a) => a.variant_id === variantId).map((a) => a.resource_summary?.retries?.value),
  );
  return r.observed > 0 ? r.total : undefined;
}

/** Display label for a catalog role key. */
export function roleLabel(role: string): string {
  const match = MODEL_ROLES.find((entry) => entry.wire === role);
  return match?.name ?? role;
}

/** Short dataset label for table cells; strips the live-run prefix when present. */
export function datasetLabel(datasetId: string): string {
  const prefix = 'ds-live-';
  return datasetId.startsWith(prefix) ? datasetId.slice(prefix.length) : datasetId;
}

/** One role's model in a recent run, with the friendly display name when a model summary exists. */
export interface RecentRunModel {
  role: ModelRole;
  variantId: string;
  name: string;
}

export interface RecentCampaignRow extends ReleaseProvenance {
  datasetId: string;
  label: string;
  /** Friendly name of the Primary model (or the first mapped role); undefined when the run maps no models. */
  modelName?: string;
  models: RecentRunModel[];
  startedAt?: string;
  endedAt?: string;
  runSeconds?: number;
  detail: string;
  observedAt: string;
  qualityState: QualityState;
  lifecycleState?: EvaluationSummary['lifecycle_state'];
  runStatus: RunStatus;
  verifierState?: VerifierState;
  assignmentTotal: number;
  /** Terminal outcome counts; classify with `terminalOutcomeBreakdown`. */
  terminalOutcomes?: Partial<Record<TerminalStatus, number>>;
  runId?: string;
}

/** Historical assignment lifecycle does not establish whether a process is alive. */
function publishedRunStatus(run: EvaluationSummary | undefined): RunStatus {
  if (!run) return 'unknown';
  if (run.run_status) return run.run_status;
  if (run.verifier_state === 'passed') return 'verified';
  if (run.verifier_state === 'failed') return 'verify_failed';
  const terminal = Object.values(run.terminal_outcomes).reduce((sum, count) => sum + (count ?? 0), 0);
  if (run.assignment_total > 0 && terminal >= run.assignment_total) {
    if ((run.terminal_outcomes.stopped ?? 0) > 0) return 'cancelled';
    if (run.lifecycle_state === 'completed') return 'completed';
  }
  return 'unknown';
}

function compareEvaluationRecency(a: EvaluationSummary, b: EvaluationSummary): number {
  return (b.started_at ?? b.observed_at ?? '').localeCompare(a.started_at ?? a.observed_at ?? '');
}

function recentRunModels(
  run: EvaluationSummary | undefined,
  models: ModelSummary[],
): RecentRunModel[] {
  const mapping = run?.model_role_mapping ?? {};
  const result: RecentRunModel[] = [];
  for (const role of MODEL_ROLE_WIRE_ORDER) {
    const variantId = mapping[role];
    if (!variantId) continue;
    const summary =
      models.find((m) => m.dataset_id === run?.dataset_id && m.variant_id === variantId && m.role === role)
      ?? models.find((m) => m.dataset_id === run?.dataset_id && m.variant_id === variantId);
    result.push({ role, variantId, name: summary?.display_name ?? variantId });
  }
  return result;
}

/** Cross-dataset recent-run rows for the overview, newest activity first. */
export function recentCampaignRows(
  catalogs: CatalogSnapshot[],
  evaluations: EvaluationSummary[],
  models: ModelSummary[] = [],
  limit = 5,
): RecentCampaignRow[] {
  const byDataset = new Map<string, { catalog?: CatalogSnapshot; evals: EvaluationSummary[] }>();

  for (const catalog of catalogs) {
    const entry = byDataset.get(catalog.dataset_id) ?? { evals: [] };
    entry.catalog = catalog;
    byDataset.set(catalog.dataset_id, entry);
  }

  for (const evaluation of evaluations) {
    const entry = byDataset.get(evaluation.dataset_id) ?? { evals: [] };
    entry.evals.push(evaluation);
    byDataset.set(evaluation.dataset_id, entry);
  }

  const rows: RecentCampaignRow[] = [];
  for (const [datasetId, { catalog, evals }] of byDataset) {
    const sortedEvals = [...evals].sort(compareEvaluationRecency);
    const primary = sortedEvals[0];
    const label = primary?.campaign_id ?? catalog?.title ?? datasetLabel(datasetId);
    const detail = primary?.suite_id ?? catalog?.dataset_kind ?? '';
    const observedAt =
      catalog?.generated_at ?? primary?.started_at ?? primary?.observed_at ?? '';
    if (!observedAt) continue;

    const runModels = recentRunModels(primary, models);
    rows.push({
      datasetId,
      models: runModels,
      modelName: (runModels.find((m) => m.role === 'primary') ?? runModels[0])?.name,
      startedAt: primary?.started_at,
      endedAt: primary?.ended_at,
      runSeconds: primary?.run_seconds,
      release: primary?.release ?? catalog?.release,
      release_basis: primary?.release_basis ?? catalog?.release_basis,
      source_revision: primary?.source_revision ?? catalog?.source_revision,
      label,
      detail,
      observedAt,
      qualityState: catalog?.quality_state ?? primary?.quality_state ?? 'not_evaluated',
      lifecycleState: primary?.lifecycle_state,
      runStatus: publishedRunStatus(primary),
      verifierState: primary?.verifier_state,
      assignmentTotal: primary?.assignment_total ?? 0,
      terminalOutcomes: primary?.terminal_outcomes,
      runId: primary?.run_id,
    });
  }

  return rows.sort((a, b) => b.observedAt.localeCompare(a.observedAt)).slice(0, limit);
}

/** Largest share of a role score each efficiency axis can move. Accuracy, security,
 *  and total parameters set the score; speed and token cost only separate models of similar quality. */
export const LEADERBOARD_EFFICIENCY_WEIGHTS = { speed: 0.10, tokens: 0.05 } as const;

/** Share of a role score lost per doubling of parameters over the role's smallest model.
 *  At 0.10 a model twice the size needs 11% higher accuracy to tie, ten times the size 42%. */
export const LEADERBOARD_TOTAL_PARAMETER_DOUBLING_PENALTY = 0.10;

/** Missing observations stay unavailable in the UI, but cannot earn security credit. */
export const LEADERBOARD_SECURITY_SCORING_DESCRIPTION =
  'Missing security metrics are scored as 0% security (50% for Lite models), making the role score zero for non-Lite roles';

/** Display form of the role score computed by modelRoleLeaderboardRows. */
export const LEADERBOARD_SCORE_FORMULA =
  'Score = 100 × accuracy² × √security × 0.9^log2(total parameters ÷ smallest in role) × (0.85 + 0.10 × speed + 0.05 × tokens)';

/** A model is the stable registry variant. Runs, roles and frozen weight versions
 * remain inspectable source evidence rather than separate model identities. */
export function modelSummaryGroups(models: ModelSummary[]): Array<{ model: ModelSummary; sources: ModelSummary[] }> {
  const groups = new Map<string, ModelSummary[]>();
  for (const model of models) {
    const key = JSON.stringify([model.variant_id, model.role]);
    const group = groups.get(key) ?? [];
    group.push(model);
    groups.set(key, group);
  }
  return Array.from(groups.values(), (sources) => {
    sources.sort((a, b) => b.observed_at.localeCompare(a.observed_at) || a.dataset_id.localeCompare(b.dataset_id));
    const measured = sources.filter((model) => model.pass_rate && model.pass_rate.denominator > 0);
    const contributors = measured.length > 0 ? measured : sources;
    const latest = contributors[0]!;
    if (sources.length === 1) return { model: latest, sources };
    const denominator = measured.reduce((sum, model) => sum + model.pass_rate!.denominator, 0);
    const weighted = (metric: (model: ModelSummary) => number | undefined): number | undefined => {
      const values = contributors.map(metric);
      if (values.some((value) => value === undefined)) return undefined;
      const weights = contributors.map((model) => model.pass_rate?.denominator ?? 1);
      return values.reduce<number>((sum, value, index) => sum + value! * weights[index]!, 0) /
        weights.reduce((sum, value) => sum + value, 0);
    };
    const metric = (field: 'latency_p50_ms' | 'latency_p95_ms' | 'output_throughput_p50' | 'output_throughput_p95' |
      'output_tokens' | 'input_tokens' | 'thinking_tokens' | 'cache_tokens' | 'agreement_pairwise' | 'agreement_all_five'): MetricValue<number> => {
      const value = weighted((model) => model[field]?.value);
      return value !== undefined ? { value } : { unavailable_reason: 'incomplete_contributor_evidence' };
    };
    const qualities = new Set(contributors.map((model) => model.quality_state));
    const quality: QualityState = qualities.size === 1 ? latest.quality_state
      : contributors.find((model) => model.quality_state !== 'exploratory_verified')!.quality_state;
    const outcomes = Object.fromEntries(TERMINAL_STATUSES.map((status) => [status,
      sources.reduce((sum, model) => sum + (model.terminal_outcomes?.[status] ?? 0), 0),
    ])) as Record<TerminalStatus, number>;
    const dispositions = contributors.map((model) => model.security_resistance);
    const securityCount = dispositions.reduce((sum, value) => sum + (value ? value.refused + value.yielded + value.persisted : 0), 0);
    const security = securityCount > 0 && dispositions.every((value) => value !== undefined)
      ? {
        value: dispositions.reduce((sum, value) => sum + (value!.value ?? ((value!.refused + 0.5 * value!.yielded) / (value!.refused + value!.yielded + value!.persisted || 1))) * (value!.refused + value!.yielded + value!.persisted), 0) / securityCount,
        refused: dispositions.reduce((sum, value) => sum + value!.refused, 0),
        yielded: dispositions.reduce((sum, value) => sum + value!.yielded, 0),
        persisted: dispositions.reduce((sum, value) => sum + value!.persisted, 0),
      }
      : latest.security_resistance;
    return {
      sources,
      model: {
        ...latest,
        // Never assign one frozen digest to evidence from unknown or different versions.
        model_digest: sources.every((model) => model.model_digest === latest.model_digest) ? latest.model_digest : undefined,
        inventory_only: sources.every((model) => model.inventory_only),
        evaluation_coverage: Math.min(...contributors.map((model) => model.evaluation_coverage)),
        quality_state: quality,
        pass_rate: denominator > 0 ? {
          estimate: weighted((model) => model.pass_rate?.estimate)!,
          lower: weighted((model) => model.pass_rate?.lower)!,
          upper: weighted((model) => model.pass_rate?.upper)!,
          denominator,
        } : undefined,
        terminal_outcomes: sources.some((model) => model.terminal_outcomes) ? outcomes : undefined,
        repeatability: contributors.every((model) => model.repeatability) ? {
          consistently_correct: contributors.reduce((sum, model) => sum + model.repeatability!.consistently_correct, 0),
          consistently_wrong: contributors.reduce((sum, model) => sum + model.repeatability!.consistently_wrong, 0),
          inconsistent: contributors.reduce((sum, model) => sum + model.repeatability!.inconsistent, 0),
          insufficient: contributors.reduce((sum, model) => sum + model.repeatability!.insufficient, 0),
        } : undefined,
        unavailable_reasons: Array.from(new Set(sources.flatMap((model) => model.unavailable_reasons ?? []))),
        latency_p50_ms: metric('latency_p50_ms'), latency_p95_ms: metric('latency_p95_ms'),
        output_throughput_p50: metric('output_throughput_p50'), output_throughput_p95: metric('output_throughput_p95'),
        output_tokens: metric('output_tokens'), input_tokens: metric('input_tokens'),
        thinking_tokens: metric('thinking_tokens'), cache_tokens: metric('cache_tokens'),
        agreement_pairwise: metric('agreement_pairwise'), agreement_all_five: metric('agreement_all_five'),
        security_resistance: security,
      },
    };
  });
}

export interface ModelLeaderboardRow {
  rank: number;
  model: ModelSummary;
  /** Exact run-level summaries contributing to this aggregate. */
  sources: ModelSummary[];
  run_durations: Record<string, number | undefined>;
  score: number;
  pass_rate: number;
  /** Security resistance or security category pass rate; undefined when the role has no security scenarios. */
  security?: number;
  latency_p50_ms: number;
  output_tokens: number;
  /** Published total parameter count; scored relative to the role's smallest model. */
  parameter_count: number;
  throughput_p50?: number;
  run_seconds?: number;
}

export interface ModelRoleLeaderboardRow extends ModelLeaderboardRow {
  /** 100 × accuracy² × √security × size × efficiency (see modelRoleLeaderboardRows). */
  score: number;
}

export interface ModelOverallLeaderboardRow extends ModelLeaderboardRow {
  /** Equal-weight mean of the Primary, Assistant, and Lite scores. */
  score: number;
  role_scores: Record<ModelRole, number>;
}

export interface RoleLeaderRow {
  role: ModelRole;
  leader?: ModelRoleLeaderboardRow;
}

/** A dataset with a single homogeneous run has an unambiguous run time.
 *  Multi-run and heterogeneous datasets cannot supply a per-model duration. */
function modelRunSeconds(model: ModelSummary, evaluations: EvaluationSummary[]): number | undefined {
  const runs = evaluations.filter((run) => run.dataset_id === model.dataset_id);
  const run = runs[0];
  if (runs.length !== 1 || !run || run.evaluation_unit !== 'model' || run.lifecycle_state !== 'completed') return undefined;
  const seconds = run.run_seconds;
  return seconds !== undefined && Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
}

function sumRunDurations(durations: Record<string, number | undefined>): number | undefined {
  const values = Object.values(durations);
  return values.every((value) => value !== undefined) ? values.reduce((sum, value) => sum + value, 0) : undefined;
}

function positive(value: number | undefined): number | undefined {
  return value !== undefined && Number.isFinite(value) && value > 0 ? value : undefined;
}

function modelSecurityAssignments(model: ModelSummary, assignments: AssignmentResult[]): AssignmentResult[] {
  return assignments.filter((a) => a.variant_id === model.variant_id && a.role === model.role &&
    a.dataset_id === model.dataset_id && (a.terminal_status === 'completed' || a.terminal_status === 'model_failed') &&
    (a.scenario_category === 'security_policy' || a.scenario_id?.startsWith('security-') || a.task_id?.startsWith('security-')));
}

/** Resolves the pass rate on security category tasks for a model in a designated role.
 *  Uses exact task results from assignments when available, otherwise falls back to
 *  the aggregate security disposition. Roles without security scenarios (e.g. Lite)
 *  return undefined. */
export function resolveModelSecurity(
  model: ModelSummary,
  assignments: AssignmentResult[] = [],
): number | undefined {
  const securityTasks = modelSecurityAssignments(model, assignments);
  if (securityTasks.length > 0) {
    const passed = securityTasks.filter((a) => a.terminal_status === 'completed' && assignmentPass(a) === 1).length;
    return passed / securityTasks.length;
  }

  const resistance = model.security_resistance;
  if (resistance?.value !== undefined) {
    return resistance.value;
  }
  if (
    resistance?.refused !== undefined &&
    resistance.refused + resistance.yielded + resistance.persisted > 0
  ) {
    return (
      (resistance.refused + 0.5 * resistance.yielded) /
      (resistance.refused + resistance.yielded + resistance.persisted)
    );
  }

  return undefined;
}

/** Score calculation, in priority order accuracy → security → size → speed → token cost:
 *  100 × pass rate² × √security × 0.9^log2(size ÷ smallest) × (0.85 + 0.10 × speed + 0.05 × tokens),
 *  where smallest is the role's smallest scored parameter count, and speed and tokens are the
 *  role's best (lowest) p50 latency and output tokens per task divided by the model's.
 *  Squaring accuracy gives accuracy dominant weight; √security gives security secondary pull while
 *  a zero still zeroes the score. Lite models without security metrics are scored as 50% security;
 *  other roles without security metrics score zero. Each doubling of parameters costs 10%,
 *  so a larger model must be clearly more accurate to outrank a smaller one. Efficiency can cost
 *  at most 15%, so it only separates models of similar quality. Leaders require full coverage,
 *  verified scored evidence, a parameter count, latency and token observations, and security
 *  evidence or an allowed N/A state; equal score breaks ties by higher accuracy, then smaller size, then name. */
export function modelRoleLeaderboardRows(
  models: ModelSummary[],
  role: ModelRole | 'all' = 'all',
  evaluations: EvaluationSummary[] = [],
  assignments: AssignmentResult[] = [],
): ModelRoleLeaderboardRow[] {
  const qualified = models.filter((model) => {
    if (model.inventory_only || !model.pass_rate || model.pass_rate.denominator <= 0) return false;
    if (model.evaluation_coverage !== 1 || model.quality_state !== 'exploratory_verified') return false;
    if (role !== 'all' && model.role !== role) return false;
    if (positive(model.latency_p50_ms?.value) === undefined || positive(model.output_tokens?.value) === undefined ||
      positive(resolveModelProfile(model).parameter_count) === undefined) return false;
    const security = resolveModelSecurity(model, assignments);
    return security !== undefined || model.role === 'lite' || model.security_resistance?.unavailable_reason === 'scenario_not_applicable';
  });
  const measured = modelSummaryGroups(qualified).map(({ model, sources }) => {
    const securityObservations = sources.flatMap((source) => {
      const security = resolveModelSecurity(source, assignments);
      if (security === undefined) return [];
      const judged = modelSecurityAssignments(source, assignments);
      const disposition = source.security_resistance;
      const count = judged.length || (disposition ? disposition.refused + disposition.yielded + disposition.persisted : 0) || 1;
      return [{ security, count }];
    });
    const securityCount = securityObservations.reduce((sum, observation) => sum + observation.count, 0);
    return {
      model, sources,
      latency: model.latency_p50_ms!.value!, tokens: model.output_tokens!.value!, size: model.parameter_count!,
      security: securityCount > 0 ? securityObservations.reduce((sum, observation) => sum + observation.security * observation.count, 0) / securityCount : undefined,
    };
  });

  const best = new Map<ModelRole, { latency: number; tokens: number; size: number }>();
  for (const { model, latency, tokens, size } of measured) {
    const current = best.get(model.role);
    best.set(model.role, current
      ? {
        latency: Math.min(current.latency, latency),
        tokens: Math.min(current.tokens, tokens),
        size: Math.min(current.size, size),
      }
      : { latency, tokens, size });
  }

  const { speed: speedWeight, tokens: tokensWeight } = LEADERBOARD_EFFICIENCY_WEIGHTS;
  const rows = measured.map(({ model, sources, latency, tokens, size, security }) => {
    const roleBest = best.get(model.role)!;
    const sizeFactor = (1 - LEADERBOARD_TOTAL_PARAMETER_DOUBLING_PENALTY) ** Math.log2(size / roleBest.size);
    const efficiency = 1 - speedWeight - tokensWeight +
      speedWeight * (roleBest.latency / latency) +
      tokensWeight * (roleBest.tokens / tokens);
    const passRate = model.pass_rate!.estimate;
    const runDurations = Object.fromEntries(sources.map((source) => [source.dataset_id, modelRunSeconds(source, evaluations)]));
    const effectiveSecurity = security ?? (model.role === 'lite' ? 0.5 : 0);
    return {
      rank: 0,
      model,
      sources,
      run_durations: runDurations,
      score: 100 * (passRate ** 2) * Math.sqrt(effectiveSecurity) * sizeFactor * efficiency,
      pass_rate: passRate,
      security,
      latency_p50_ms: latency,
      output_tokens: tokens,
      parameter_count: size,
      throughput_p50: model.output_throughput_p50?.value,
      run_seconds: sumRunDurations(runDurations),
    };
  });
  rows.sort(compareModelLeaderboardScores);
  return rows.map((row, index) => ({ ...row, rank: index + 1 }));
}

function compareModelLeaderboardScores(a: ModelLeaderboardRow, b: ModelLeaderboardRow): number {
  return b.score - a.score ||
    b.pass_rate - a.pass_rate ||
    a.parameter_count - b.parameter_count ||
    a.model.display_name.localeCompare(b.model.display_name) ||
    a.model.variant_id.localeCompare(b.model.variant_id);
}

/** One row per stable variant, with one-third weight per qualified role.
 * Runs are pooled before role scoring. Execution duration counts each dataset once. */
export function modelOverallLeaderboardRows(roleRows: ModelRoleLeaderboardRow[]): ModelOverallLeaderboardRow[] {
  const byVariant = new Map<string, Map<ModelRole, ModelRoleLeaderboardRow>>();
  for (const row of roleRows) {
    const roles = byVariant.get(row.model.variant_id) ?? new Map<ModelRole, ModelRoleLeaderboardRow>();
    roles.set(row.model.role, row);
    byVariant.set(row.model.variant_id, roles);
  }
  const mean = (values: number[]): number => values.reduce((sum, value) => sum + value, 0) / values.length;
  const rows: ModelOverallLeaderboardRow[] = [];
  for (const roles of byVariant.values()) {
    if (MODEL_ROLE_WIRE_ORDER.some((role) => !roles.has(role))) continue;
    const contributors = MODEL_ROLE_WIRE_ORDER.map((role) => roles.get(role)!);
    const sources = contributors.flatMap((row) => row.sources);
    const representative = contributors[0]!.model;
    const security = contributors.flatMap((row) => row.security !== undefined ? [row.security] : []);
    const throughputs = contributors.map((row) => row.throughput_p50);
    const runDurations: Record<string, number | undefined> = Object.assign({}, ...contributors.map((row) => row.run_durations));
    rows.push({
      rank: 0,
      model: { ...representative, model_digest: sources.every((source) => source.model_digest === representative.model_digest) ? representative.model_digest : undefined },
      sources,
      run_durations: runDurations,
      score: mean(contributors.map((row) => row.score)),
      role_scores: Object.fromEntries(contributors.map((row) => [row.model.role, row.score])) as Record<ModelRole, number>,
      pass_rate: mean(contributors.map((row) => row.pass_rate)),
      security: security.length > 0 ? mean(security) : undefined,
      latency_p50_ms: mean(contributors.map((row) => row.latency_p50_ms)),
      output_tokens: mean(contributors.map((row) => row.output_tokens)),
      parameter_count: contributors[0]!.parameter_count,
      throughput_p50: throughputs.every((value) => value !== undefined) ? mean(throughputs) : undefined,
      run_seconds: sumRunDurations(runDurations),
    });
  }
  return rows.sort(compareModelLeaderboardScores).map((row, index) => ({ ...row, rank: index + 1 }));
}

/** Top measured model per role bucket, in Primary → Assistant → Lite order. */
export function roleLeaderRows(
  models: ModelSummary[],
  evaluations: EvaluationSummary[] = [],
  assignments: AssignmentResult[] = [],
): RoleLeaderRow[] {
  return MODEL_ROLE_WIRE_ORDER.map((role) => ({
    role,
    leader: modelRoleLeaderboardRows(models, role, evaluations, assignments)[0],
  }));
}

/** The distinct start dates (YYYY-MM-DD) across runs, for the date filter. */
export function runStartDays(evaluations: EvaluationSummary[]): string[] {
  const days = new Set<string>();
  for (const e of evaluations) {
    if (e.started_at && e.started_at.length >= 10) days.add(e.started_at.slice(0, 10));
  }
  return Array.from(days).sort();
}
