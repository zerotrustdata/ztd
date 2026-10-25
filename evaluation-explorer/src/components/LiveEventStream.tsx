// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Live SSE event feed for the overview page. Newest events at the top inside a
// bounded scroll region. Detailed diagnostics live on the assignment page.

import { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import type { FeedConnectionState, StreamConnectionState } from '../utils/feed-state';
import { recordKey, resolveModelSummary, useStoreState } from '../state/store';
import {
  roleLabel,
  visibleStreamEvents,
} from '../views/derived';
import { EmptyState, ReconcilePlaceholder, StreamStatusIndicator } from './shared';
import { SCENARIO_TASK_BY_ID } from '../content/scenario-task';
import {
  MODEL_ROLES,
  type AssignmentResult,
  type LiveEvent,
  type MetricValue,
  type ModelRole,
} from '../contract/types';
import { formatNumber } from '../utils/format';
import { LIVE_EVENT_RETENTION_LIMIT } from '../constants';
const STREAM_PAGE_SIZE = 25;
const STREAM_EMPTY_METRIC = '--';
const TRIBUNAL_VOTE_PREFIX = 'tribunal vote\n';

/** Canonical Tribunal seats with display titles describing their member stances. */
const TRIBUNAL_SEATS = [
  { id: 'axiom', name: 'Axiom', office: 'Coherent Composer', symbol: '✶' },
  { id: 'concord', name: 'Concord', office: 'Vigilant Guardian', symbol: '⛨' },
  { id: 'variance', name: 'Variance', office: 'Exhaustive Analyst', symbol: '⋮' },
  { id: 'pragma', name: 'Pragma', office: 'Practical Tactician', symbol: '≡' },
  { id: 'nemesis', name: 'Nemesis', office: 'Endemic Adversary', symbol: '⚔' },
] as const;

type TribunalSeatId = (typeof TRIBUNAL_SEATS)[number]['id'];

function isTribunalVoteEvent(event: LiveEvent): boolean {
  return event.stage_label?.startsWith(TRIBUNAL_VOTE_PREFIX) ?? false;
}

/** The audit record carried on a consensus-posture vote event, unchanged. */
function tribunalVoteRecord(event: LiveEvent): Record<string, unknown> | undefined {
  if (!isTribunalVoteEvent(event) || event.stage_label === undefined) return undefined;
  try {
    const parsed: unknown = JSON.parse(event.stage_label.slice(TRIBUNAL_VOTE_PREFIX.length));
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return undefined;
    return parsed as Record<string, unknown>;
  } catch {
    return undefined;
  }
}

function tribunalSeatId(record: Record<string, unknown>): TribunalSeatId | undefined {
  const member = record.member_app_id;
  if (typeof member !== 'string' || !member.startsWith('tribunal-')) return undefined;
  const id = member.slice('tribunal-'.length);
  return TRIBUNAL_SEATS.some((seat) => seat.id === id) ? id as TribunalSeatId : undefined;
}

type SeatVotes = Map<TribunalSeatId, Record<string, unknown>>;

function assignmentRowKey(event: LiveEvent): string {
  return `${event.dataset_id}:${event.run_id}:${event.assignment_id}`;
}

/** Only explicit dataset/run/assignment identities associate votes with rows.
 *  Unassigned governance activity must never attach to a campaign assignment.
 *  Within a row only the latest round shows, one vote per seat. */
function tribunalVotesByAssignment(events: LiveEvent[]): Map<string, SeatVotes> {
  const byTime = (left: LiveEvent, right: LiveEvent) => left.observed_at.localeCompare(right.observed_at);
  const votes = events.filter((event) => event.assignment_id && isTribunalVoteEvent(event)).sort(byTime);
  const rounds = new Map<string, { transaction: string; seats: SeatVotes }>();
  for (const vote of votes) {
    const record = tribunalVoteRecord(vote);
    const seat = record ? tribunalSeatId(record) : undefined;
    if (!record || !seat) continue;
    const key = assignmentRowKey(vote);
    const transaction = typeof record.transaction_id === 'string' ? record.transaction_id : '';
    let round = rounds.get(key);
    if (!round || round.transaction !== transaction) {
      round = { transaction, seats: new Map() };
      rounds.set(key, round);
    }
    round.seats.set(seat, record);
  }
  return new Map([...rounds].map(([key, round]) => [key, round.seats]));
}

function TribunalVotes({ votes }: { votes: SeatVotes }) {
  return (
    <div className="tribunal-seats" role="group" aria-label="Tribunal">
      {TRIBUNAL_SEATS.map((seat) => {
        const vote = votes.get(seat.id);
        const outcome = typeof vote?.outcome === 'string' ? vote.outcome : undefined;
        const body = (
          <>
            <span className="tribunal-symbol" aria-hidden="true">{seat.symbol}</span>
            <span className="tribunal-name">{seat.name}</span>
            <span className="tribunal-office">{seat.office}</span>
            <span className="tribunal-outcome">{outcome ? displayLabel(outcome) : 'Pending'}</span>
          </>
        );
        return vote ? (
          <details key={seat.id} className="tribunal-seat" title={`${seat.name}, ${seat.office}`} data-outcome={outcome ?? 'pending'}>
            <summary>{body}</summary>
            <pre>{JSON.stringify(vote, null, 2)}</pre>
          </details>
        ) : (
          <div key={seat.id} className="tribunal-seat" title={`${seat.name}, ${seat.office}`} data-outcome="pending">{body}</div>
        );
      })}
    </div>
  );
}

function eventTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleTimeString('en-US', { hour12: false });
}

function isAssignmentLevelEvent(event: LiveEvent): boolean {
  return (
    event.kind === 'assignment_started' ||
    event.kind === 'assignment_completed' ||
    event.kind === 'assignment_failed'
  );
}

function isTerminalAssignmentEvent(event: LiveEvent): boolean {
  return event.kind === 'assignment_completed' || event.kind === 'assignment_failed';
}

function applyAssignmentResourceSummary(
  values: Record<string, MetricValue>,
  assignment: AssignmentResult | undefined,
): Record<string, MetricValue> {
  const resources = assignment?.resource_summary;
  if (!resources) return values;
  if (resources.latency_ms) values.latency_ms = resources.latency_ms;
  if (resources.input_tokens) values.input_tokens = resources.input_tokens;
  if (resources.output_tokens) values.output_tokens = resources.output_tokens;
  if (resources.thinking_tokens) values.thinking_tokens = resources.thinking_tokens;
  if (resources.cache_tokens) values.cache_tokens = resources.cache_tokens;
  if (resources.retries) values.retries = resources.retries;
  return values;
}

/** Designated role per assignment, from assignment-level events (they carry the
 *  designated role) so rows resolve a role before the assignment result lands. */
function designatedRolesByAssignment(events: LiveEvent[]): Map<string, ModelRole> {
  const designated = new Map<string, ModelRole>();
  for (const event of events) {
    if (event.assignment_id && event.role && isAssignmentLevelEvent(event)) {
      designated.set(`${event.dataset_id}:${event.run_id}:${event.assignment_id}`, event.role);
    }
  }
  return designated;
}

/** The role a row belongs to: its own, else its assignment's designated role. A
 *  row with an assignment and neither is shown as unresolved, never guessed from
 *  the model's registry role. A row without an assignment is platform-level. */
function streamEventRoleLabel(
  event: LiveEvent,
  assignment: AssignmentResult | undefined,
  designatedRoles: Map<string, ModelRole>,
): string {
  const role =
    event.role ?? (event.assignment_id ? (assignment?.role ?? designatedRoles.get(`${event.dataset_id}:${event.run_id}:${event.assignment_id}`)) : undefined);
  if (role) return roleLabel(role);
  return event.assignment_id ? '—' : 'Platform';
}

/** A terminal row carries the assignment's final values over its event delta. */
function assignmentMetricValues(event: LiveEvent, assignment: AssignmentResult | undefined): Record<string, MetricValue> {
  const delta = event.metric_delta ?? {};
  if (isTerminalAssignmentEvent(event)) {
    return applyAssignmentResourceSummary({ ...delta, ...(assignment?.metric_values ?? {}) }, assignment);
  }
  return { ...delta };
}

function displayLabel(value: string | undefined): string | undefined {
  return value?.replace(/_/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase());
}

type EventParts = {
  category: string;
  task: string;
};

function eventParts(event: LiveEvent, assignment: AssignmentResult | undefined): EventParts {
  const stageParts = event.stage_label?.split(' · ').map((part) => part.trim()) ?? [];
  const isModelRoleInvocation = stageParts[0]?.toLowerCase() === 'model role invoked';

  const secondPart = stageParts[1];
  const task =
    assignment?.task_id ??
    event.task_id ??
    (isModelRoleInvocation
      ? stageParts[3]
      : stageParts.length > 2
        ? stageParts[2]
        : stageParts.length === 2 && secondPart !== undefined && SCENARIO_TASK_BY_ID.has(secondPart)
          ? secondPart
          : undefined) ??
    '—';

  let fallbackCategory: string | undefined;
  if (!isModelRoleInvocation && secondPart !== undefined) {
    const isRole = (MODEL_ROLES as readonly string[]).includes(secondPart.toLowerCase());
    const isTask = secondPart === task || SCENARIO_TASK_BY_ID.has(secondPart);
    if (!isRole && !isTask) {
      fallbackCategory = secondPart;
    }
  }

  const rawCategory =
    assignment?.scenario_category ??
    (task !== '—' ? SCENARIO_TASK_BY_ID.get(task)?.category : undefined) ??
    fallbackCategory;

  return {
    category: displayLabel(rawCategory) ?? '—',
    task,
  };
}

type AssignmentMetricColumn = 'latency_ms' | 'input_tokens' | 'output_tokens' | 'tokens_per_second';
type StreamSortField = 'time' | 'result' | 'task' | 'model' | AssignmentMetricColumn;
type StreamSortDirection = 'asc' | 'desc';

const ASSIGNMENT_METRIC_COLUMNS: Array<{ key: AssignmentMetricColumn; label: string; format: (value: number) => string }> = [
  { key: 'latency_ms', label: 'Latency (ms)', format: (ms) => formatNumber(ms) },
  { key: 'input_tokens', label: 'Input (tok)', format: (value) => formatNumber(value) },
  { key: 'output_tokens', label: 'Output (tok)', format: (value) => formatNumber(value) },
  { key: 'tokens_per_second', label: 'Tokens/s', format: (value) => formatNumber(value, 1) },
];

type StreamResult = { label: string; symbol: string; tone: 'ok' | 'critical' | 'neutral' | 'info' };

/** Model verdicts stay distinct from infrastructure failures and run lifecycle. */
function streamResult(event: LiveEvent, assignment: AssignmentResult | undefined): StreamResult {
  if (isTerminalAssignmentEvent(event)) {
    const status = event.terminal_status ?? assignment?.terminal_status;
    if (status && status !== 'completed' && status !== 'model_failed') {
      return { label: displayLabel(status)!, symbol: '!', tone: 'neutral' };
    }
    const pass = assignmentMetricValues(event, assignment).pass?.value;
    if (status === 'model_failed' || pass === 0) {
      return { label: 'Model failed', symbol: '×', tone: 'critical' };
    }
    if (event.kind === 'assignment_failed') return { label: 'Failed', symbol: '!', tone: 'neutral' };
    return { label: pass === 1 ? 'Passed' : 'Completed', symbol: '✓', tone: 'ok' };
  }
  const label = displayLabel(event.kind) ?? 'Activity';
  if (event.lifecycle_status === 'failed') return { label, symbol: '!', tone: 'neutral' };
  if (event.lifecycle_status === 'stopped') return { label, symbol: 'Ⅱ', tone: 'neutral' };
  if (event.lifecycle_status === 'completed') return { label, symbol: '■', tone: 'neutral' };
  return { label, symbol: '•', tone: 'info' };
}

function matchingAssignment(event: LiveEvent, assignments: Map<string, AssignmentResult>): AssignmentResult | undefined {
  const assignment = event.assignment_id ? assignments.get(recordKey(event.dataset_id, event.assignment_id)) : undefined;
  return assignment?.run_id === event.run_id ? assignment : undefined;
}

function assignmentTokensPerSecond(
  event: LiveEvent,
  assignment: AssignmentResult | undefined,
): MetricValue | undefined {
  const values = assignmentMetricValues(event, assignment);
  const output = values.output_tokens?.value;
  const generationMs = assignment?.benchmark_observations?.timing?.generation_ms?.value;
  if (output === undefined || generationMs === undefined || generationMs <= 0) return undefined;
  return { value: output / (generationMs / 1000) };
}

function assignmentMetric(event: LiveEvent, assignment: AssignmentResult | undefined, key: AssignmentMetricColumn): MetricValue | undefined {
  if (key === 'tokens_per_second') {
    return assignmentTokensPerSecond(event, assignment);
  }
  return assignmentMetricValues(event, assignment)[key];
}

function streamMetricDisplay(
  metric: MetricValue | undefined,
  formatter: (value: number) => string,
): { text: string; unavailable: boolean; reason?: string } {
  if (!metric) {
    return { text: STREAM_EMPTY_METRIC, unavailable: true, reason: 'not observed' };
  }
  if (metric.value === undefined) {
    return {
      text: STREAM_EMPTY_METRIC,
      unavailable: true,
      reason: metric.unavailable_reason,
    };
  }
  return { text: formatter(metric.value), unavailable: false };
}

function streamSortValue(
  event: LiveEvent,
  assignment: AssignmentResult | undefined,
  model: ReturnType<typeof resolveModelSummary>,
  field: StreamSortField,
): string | number {
  if (field === 'time') return event.observed_at;
  if (field === 'result') return streamResult(event, assignment).label;
  if (field === 'task') return eventParts(event, assignment).task;
  if (field === 'model') return model?.served_model_tag ?? event.variant_id ?? '';
  return assignmentMetric(event, assignment, field)?.value ?? '';
}

function SortHeader({
  label,
  field,
  sortField,
  sortDirection,
  onSort,
}: {
  label: string;
  field: StreamSortField;
  sortField: StreamSortField | undefined;
  sortDirection: StreamSortDirection;
  onSort: (field: StreamSortField) => void;
}) {
  const isSorted = sortField === field;
  return (
    <th scope="col" aria-sort={isSorted ? (sortDirection === 'asc' ? 'ascending' : 'descending') : 'none'}>
      <button type="button" className="th-sort stream-sort" onClick={() => onSort(field)} aria-label={`Sort by ${label}`}>
        {label}
        <span className="sort-indicator" aria-hidden="true">
          {isSorted ? (sortDirection === 'asc' ? '▲' : '▼') : '↕'}
        </span>
      </button>
    </th>
  );
}

export function LiveEventStream({
  events,
  connection,
  streamConnection,
  isReconciling = false,
  title = 'Live activity',
  id = 'live-stream',
  className,
}: {
  events: LiveEvent[];
  connection: FeedConnectionState;
  streamConnection: StreamConnectionState;
  isReconciling?: boolean;
  title?: string;
  id?: string;
  className?: string;
}) {
  const [modelFilter, setModelFilter] = useState('all');
  const [kindFilter, setKindFilter] = useState('all');
  const [categoryFilter, setCategoryFilter] = useState('all');
  const [assignmentFilter, setAssignmentFilter] = useState('all');
  const [runScope, setRunScope] = useState<'all' | 'latest'>('all');
  const [sortField, setSortField] = useState<StreamSortField>('time');
  const [sortDirection, setSortDirection] = useState<StreamSortDirection>('desc');
  const [page, setPage] = useState(0);
  const scrollRef = useRef<HTMLDivElement>(null);
  const models = useStoreState((state) => state.models);
  const assignments = useStoreState((state) => state.assignments);
  const activityEvents = useMemo(() => events.filter((event) => !isTribunalVoteEvent(event)), [events]);
  const tribunalVotes = useMemo(() => tribunalVotesByAssignment(events), [events]);

  const latestEvent = useMemo(() => activityEvents.reduce<LiveEvent | undefined>(
    (latest, event) => !latest || event.observed_at > latest.observed_at ? event : latest, undefined,
  ), [activityEvents]);
  const targetEvents = useMemo(() => runScope === 'latest' && latestEvent
    ? activityEvents.filter((event) => event.run_id === latestEvent.run_id && event.dataset_id === latestEvent.dataset_id)
    : activityEvents, [activityEvents, runScope, latestEvent]);
  const baseRows = useMemo(() => visibleStreamEvents(targetEvents, {
    modelFilter: 'all', kindFilter: 'all',
  }), [targetEvents]);
  const visible = useMemo(() => baseRows.filter((event) => {
    const assignment = matchingAssignment(event, assignments);
    return (modelFilter === 'all' || event.variant_id === modelFilter)
      && (kindFilter === 'all' || event.kind === kindFilter)
      && (assignmentFilter === 'all' || event.assignment_id === assignmentFilter)
      && (categoryFilter === 'all' || eventParts(event, assignment).category === categoryFilter);
  }).slice(0, runScope === 'all' ? LIVE_EVENT_RETENTION_LIMIT : undefined), [baseRows, assignments, modelFilter, kindFilter, assignmentFilter, categoryFilter, runScope]);

  // Options come from the unfiltered feed so choosing one never hides the others.
  const modelOptions = useMemo(() => Array.from(new Set(baseRows.map((event) => event.variant_id)
    .filter((value): value is string => Boolean(value)))).sort(), [baseRows]);
  const kindOptions = useMemo(() => Array.from(new Set(baseRows.map((event) => event.kind))).sort(), [baseRows]);
  const categoryOptions = useMemo(() => Array.from(new Set(baseRows.map((event) =>
    eventParts(event, matchingAssignment(event, assignments)).category,
  ).filter((category) => category !== '—'))).sort(), [baseRows, assignments]);
  const assignmentOptions = useMemo(() => Array.from(new Set(baseRows.map((event) => event.assignment_id)
    .filter((value): value is string => Boolean(value)))).sort(), [baseRows]);
  const designatedRoles = useMemo(() => designatedRolesByAssignment(activityEvents), [activityEvents]);
  const advancedFilterCount = [kindFilter, categoryFilter, assignmentFilter, runScope].filter((value) => value !== 'all').length;
  const hasFilters = advancedFilterCount > 0 || modelFilter !== 'all';

  const sortedVisible = useMemo(() => [...visible].sort((left, right) => {
    const value = (event: LiveEvent) => streamSortValue(event, matchingAssignment(event, assignments),
      event.variant_id ? resolveModelSummary(models, event.dataset_id, event.variant_id) : undefined, sortField);
    const leftValue = value(left);
    const rightValue = value(right);
    // Missing observations always follow measured values, including measured zero.
    if (leftValue === '' && rightValue !== '') return 1;
    if (rightValue === '' && leftValue !== '') return -1;
    const comparison = typeof leftValue === 'number' && typeof rightValue === 'number'
      ? leftValue - rightValue : String(leftValue).localeCompare(String(rightValue), undefined, { numeric: true });
    return comparison === 0 ? 0
      : sortDirection === 'asc' ? comparison : -comparison;
  }), [visible, assignments, models, sortField, sortDirection]);
  const pageCount = Math.ceil(sortedVisible.length / STREAM_PAGE_SIZE);
  const safePage = Math.min(page, Math.max(0, pageCount - 1));
  const pageEvents = sortedVisible.slice(safePage * STREAM_PAGE_SIZE, (safePage + 1) * STREAM_PAGE_SIZE);

  function handleSort(field: StreamSortField): void {
    if (sortField === field) setSortDirection((direction) => direction === 'asc' ? 'desc' : 'asc');
    else { setSortField(field); setSortDirection(field === 'time' ? 'desc' : 'asc'); }
  }
  function resetFilters(): void {
    setModelFilter('all'); setKindFilter('all'); setCategoryFilter('all');
    setAssignmentFilter('all'); setRunScope('all');
  }
  useEffect(() => { setPage(0); }, [modelFilter, kindFilter, categoryFilter, assignmentFilter, runScope, sortField, sortDirection]);
  useEffect(() => { if (scrollRef.current) scrollRef.current.scrollTop = 0; }, [safePage]);

  return (
    <section className={`panel stream-panel${className ? ` ${className}` : ''}`} id={id} aria-label={title}>
      <div className="panel-head stream-head">
        <div className="stream-heading">
          <h2>{title}</h2>
          <StreamStatusIndicator streamConnection={streamConnection} feedConnection={connection} />
        </div>
        <div className="stream-controls">
          <select aria-label="Filter by model" value={modelFilter} onChange={(event) => setModelFilter(event.target.value)}>
            <option value="all">All models</option>
            {modelOptions.map((model) => <option key={model} value={model}>{model}</option>)}
          </select>
        </div>
      </div>
      <div className="stream-toolbar">
        <details className="stream-filters">
          <summary>Filters{advancedFilterCount > 0 ? ` (${advancedFilterCount})` : ''}</summary>
          <div className="stream-controls stream-filter-fields">
            <label>Run<select aria-label="Filter by run scope" value={runScope} onChange={(event) => setRunScope(event.target.value as 'all' | 'latest')}>
              <option value="all">All runs</option><option value="latest">Latest run</option>
            </select></label>
            <label>Event<select aria-label="Filter by event kind" value={kindFilter} onChange={(event) => setKindFilter(event.target.value)}>
              <option value="all">All events</option>
              {kindOptions.map((kind) => <option key={kind} value={kind}>{displayLabel(kind)}</option>)}
            </select></label>
            <label>Category<select aria-label="Filter by category" value={categoryFilter} onChange={(event) => setCategoryFilter(event.target.value)}>
              <option value="all">All categories</option>
              {categoryOptions.map((category) => <option key={category} value={category}>{category}</option>)}
            </select></label>
            <label>Assignment<select aria-label="Filter by assignment" value={assignmentFilter} onChange={(event) => setAssignmentFilter(event.target.value)}>
              <option value="all">All assignments</option>
              {assignmentOptions.map((assignment) => <option key={assignment} value={assignment}>{assignment}</option>)}
            </select></label>
          </div>
        </details>
        <div className="stream-view-meta">
          {hasFilters ? <button type="button" className="stream-reset" onClick={resetFilters}>Clear filters</button> : null}
          <span>{visible.length} {hasFilters ? 'matching' : 'recent'} events</span>
        </div>
      </div>
      {isReconciling ? <p className="panel-note">Syncing feed history…</p> : null}
      {visible.length === 0 ? (
        isReconciling ? <ReconcilePlaceholder label="Loading feed history…" />
          : <EmptyState hasRecords={events.length > 0} hasFilters={hasFilters} connection={connection} />
      ) : (
        <div className="stream-scroll" ref={scrollRef} role="region" aria-label="Live activity results" tabIndex={0}>
          <table className="stream-table">
            <thead><tr>
              <SortHeader label="Result" field="result" sortField={sortField} sortDirection={sortDirection} onSort={handleSort} />
              <SortHeader label="Task" field="task" sortField={sortField} sortDirection={sortDirection} onSort={handleSort} />
              <SortHeader label="Model" field="model" sortField={sortField} sortDirection={sortDirection} onSort={handleSort} />
              <th scope="col"><span className="sr-only">Tribunal</span></th>
              {ASSIGNMENT_METRIC_COLUMNS.map(({ key, label }) => <SortHeader key={key} label={label} field={key} sortField={sortField} sortDirection={sortDirection} onSort={handleSort} />)}
              <SortHeader label="Time" field="time" sortField={sortField} sortDirection={sortDirection} onSort={handleSort} />
            </tr></thead>
            <tbody>
              {pageEvents.map((event) => {
                const assignment = matchingAssignment(event, assignments);
                const model = event.variant_id ? resolveModelSummary(models, event.dataset_id, event.variant_id) : undefined;
                const parts = eventParts(event, assignment);
                const rowVotes = event.assignment_id ? tribunalVotes.get(assignmentRowKey(event)) : undefined;
                const result = streamResult(event, assignment);
                const eventHref = event.assignment_id
                  ? `/evaluations/${event.dataset_id}/${event.run_id}/assignments/${event.assignment_id}`
                  : `/evaluations/${event.dataset_id}/${event.run_id}`;
                const task = parts.task !== '—' ? parts.task : displayLabel(event.kind)!;
                const role = event.role ?? assignment?.role ?? designatedRoles.get(`${event.dataset_id}:${event.run_id}:${event.assignment_id}`);
                const modelHref = event.variant_id
                  ? `/models/${event.dataset_id}/${event.variant_id}${role ? `?role=${role}` : ''}` : undefined;
                return (
                  <tr key={`${event.dataset_id}:${event.run_id}:${event.assignment_id ?? event.event_id}`}>
                    <td className="stream-result-cell">
                      <Link to={eventHref} className="stream-result" data-tone={result.tone} aria-label={`${result.label}: ${task}. View details`}>
                        <span aria-hidden="true">{result.symbol}</span>
                        <span className="stream-result-label" aria-hidden="true">{result.label}</span>
                      </Link>
                    </td>
                    <td className="stream-task-cell">
                      <Link to={eventHref} className="stream-task-link" aria-label={`${task} — View details`}>
                        <span title={task}>{task}</span><span className="stream-detail-cue" aria-hidden="true">Details →</span>
                      </Link>
                      {parts.category !== '—' ? <span className="stream-secondary">{parts.category}</span> : null}
                    </td>
                    <td className="stream-model-cell">
                      {modelHref ? <Link to={modelHref} className="stream-model-link" title={model?.served_model_tag ?? event.variant_id}>{model?.served_model_tag ?? event.variant_id}</Link> : <span>Platform</span>}
                      {event.assignment_id || event.role ? <span className="stream-secondary stream-role">{streamEventRoleLabel(event, assignment, designatedRoles)}</span> : null}
                    </td>
                    <td className="stream-tribunal-cell">
                      {rowVotes ? <TribunalVotes votes={rowVotes} /> : null}
                    </td>
                    {ASSIGNMENT_METRIC_COLUMNS.map(({ key, label, format }) => {
                      const displayed = event.assignment_id
                        ? streamMetricDisplay(assignmentMetric(event, assignment, key), format)
                        : { text: STREAM_EMPTY_METRIC, unavailable: true, reason: 'not applicable' };
                      return <td className="stream-metric-cell" key={key} data-label={label}>
                        <span className={`stream-metric-value${displayed.unavailable ? ' unavailable' : ''}`}
                          title={displayed.unavailable ? displayed.reason?.replaceAll('_', ' ') : undefined}>
                          {displayed.text}
                        </span>
                      </td>;
                    })}
                    <td className="stream-time"><time dateTime={event.observed_at} title={new Date(event.observed_at).toLocaleString()}>{eventTime(event.observed_at)}</time></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {pageCount > 1 ? <div className="table-pagination" data-testid="stream-pagination">
        <button type="button" onClick={() => setPage((value) => Math.max(0, value - 1))} disabled={safePage === 0} aria-label="Previous page">Previous</button>
        <span className="page-info">Page {safePage + 1} of {pageCount} ({visible.length} events)</span>
        <button type="button" onClick={() => setPage((value) => Math.min(pageCount - 1, value + 1))} disabled={safePage >= pageCount - 1} aria-label="Next page">Next</button>
      </div> : null}
    </section>
  );
}
