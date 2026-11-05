// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Normalized client store. Page components never parse transport records
// directly; they read normalized slices from this store. The store ingests
// projection records, decodes and validates them, indexes by identity, and
// deduplicates by stable event ID. It uses useSyncExternalStore so React
// re-renders only when a subscribed slice changes.
//
// Index keys are composite (dataset_id + record identity): the real corpus
// publishes the same variant_id in multiple datasets (the registry models
// appear in both the exploratory baseline and the legacy public snapshot),
// so bare-identity keys would let one dataset overwrite another.

import { useRef, useSyncExternalStore } from 'react';
import type {
  AssignmentResult,
  CatalogSnapshot,
  EvaluationSummary,
  FeedSnapshot,
  FeedTaskIndexEntry,
  FeedTaskResult,
  FreshnessState,
  LiveEvent,
  MethodologySnapshot,
  ModelRole,
  ModelSummary,
  ProjectionRecord,
  SnapshotRecord,
  SuiteSummary,
} from '../contract/types';
import { decodeLiveEventWire, isLiveEventKind } from '../contract/live-event-wire';
import { decodeViewRecord, getParsedProjectionPayload, isProjectionRecord, ValidationError } from '../contract/validators';
import {
  deriveFreshness,
  type FeedConnectionState,
  type FeedStatus,
  type StreamConnectionState,
} from '../utils/feed-state';
import { CAMPAIGN_MESSAGE_TYPES, decodeCampaignProjectionEnvelope } from '../contract/campaign-wire';
import {
  adaptDecodedCampaignProjectionEnvelope,
  beginCampaignAdaptContextTransaction,
  campaignProgressCounts,
  cloneCampaignAdaptContext,
  campaignRunIdFromDatasetId,
  createCampaignAdaptContext,
  recordCampaignMatrixTotal,
  type CampaignAdaptContext,
} from './campaign-adapter';
import { LIVE_EVENT_STORE_LIMIT } from '../constants';
import { retainLatestStreamEvents, upsertLiveEvent } from '../views/derived';

/** Composite index key: dataset first so the same identity can coexist. */
export function recordKey(datasetId: string, id: string): string {
  return `${datasetId}${id}`;
}

/** Model summaries are unique per dataset, variant, and designated role. */
export function modelRecordKey(datasetId: string, variantId: string, role: ModelRole): string {
  return recordKey(datasetId, `${variantId}:${role}`);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Mirror transport metadata must never reach campaign/view decoders. */
function parseProjectionPayload(recordBytes: string): Record<string, unknown> {
  const payload: unknown = JSON.parse(recordBytes);
  if (!isRecord(payload)) {
    throw new ValidationError('expected object', 'projection');
  }
  delete payload.sequence;
  delete payload.record_type;
  return payload;
}

/** The run a bundle must belong to. Every adapted record is checked against it. */
export interface RunBundleScope {
  datasetId: string;
  runId: string;
}

function isCampaignProjectionPayload(payload: Record<string, unknown>): boolean {
  const messageType = payload.message_type;
  return typeof messageType === 'string' && (CAMPAIGN_MESSAGE_TYPES as readonly string[]).includes(messageType);
}

function normalizeQualityRecord<T extends SnapshotRecord | LiveEvent>(record: T): T {
  let qualityState = record.quality_state;
  if (record.kind === 'model_summary' && record.evaluation_coverage < 1 && qualityState === 'exploratory_verified') {
    qualityState = 'exploratory_partial';
  }
  return qualityState === record.quality_state ? record : { ...record, quality_state: qualityState };
}

const MODEL_ROLE_ID = /^(.+):(primary|assistant|lite)$/;

export function resolveModelSummary(
  models: Map<string, ModelSummary>,
  datasetId: string,
  modelId: string,
): ModelSummary | undefined {
  const roleMatch = MODEL_ROLE_ID.exec(modelId);
  if (roleMatch?.[1] && roleMatch[2]) {
    return models.get(modelRecordKey(datasetId, roleMatch[1], roleMatch[2] as ModelRole));
  }
  let fallback: ModelSummary | undefined;
  for (const model of models.values()) {
    if (model.dataset_id !== datasetId || model.variant_id !== modelId) continue;
    if (model.pass_rate) return model;
    fallback = fallback ?? model;
  }
  return fallback;
}

export interface StoreState {
  connection: FeedConnectionState;
  streamConnection: StreamConnectionState;
  feedStatus: FeedStatus | null;
  mirrorOrigin: string | null;
  sourceId: string | null;
  observedSequence: number;
  currentSnapshot: FeedSnapshot | null;
  pendingSnapshot: FeedSnapshot | null;
  catalogs: Map<string, CatalogSnapshot>;
  models: Map<string, ModelSummary>;
  suites: Map<string, SuiteSummary>;
  evaluations: Map<string, EvaluationSummary>;
  assignments: Map<string, AssignmentResult>;
  /** Validated public source records, retained before view normalization. */
  assignmentSources: Map<string, ProjectionRecord>;
  /** Complete ingested assignment history, independent of the live feed tail. */
  assignmentEvents: Map<string, LiveEvent[]>;
  /** The bootstrap's bounded recent results per task, keyed by task ID without its @version suffix. */
  taskIndex: Map<string, FeedTaskResult[]>;
  methodology: MethodologySnapshot | null;
  events: LiveEvent[];
  eventIds: Set<string>;
  errors: string[];
  lastAcceptedAt: string | undefined;
  proofArtifactCount: number;
}

function snapshotPinnedFreshness(freshness: FeedSnapshot['freshness']): FreshnessState | undefined {
  return freshness === 'intentionally_stopped' || freshness === 'safety_stopped' ? freshness : undefined;
}

function withFeedSnapshot(state: StoreState, snapshot: FeedSnapshot, message: string): FeedStatus {
  const pinnedFreshness = snapshotPinnedFreshness(snapshot.freshness);
  const lastAcceptedAt = state.lastAcceptedAt ?? snapshot.generated_at;
  return {
    connection: state.connection,
    freshness: deriveFreshness(lastAcceptedAt, pinnedFreshness),
    pinnedFreshness,
    highWaterSequence: snapshot.high_water_sequence,
    lastAcceptedAt,
    message,
  };
}

function withAcceptedRecord(state: StoreState): FeedStatus | null {
  if (!state.feedStatus) return null;
  const freshness = deriveFreshness(state.lastAcceptedAt, state.feedStatus.pinnedFreshness);
  return { ...state.feedStatus, freshness, lastAcceptedAt: state.lastAcceptedAt };
}

function emptyState(): StoreState {
  return {
    connection: 'connecting',
    streamConnection: 'disconnected',
    feedStatus: null,
    mirrorOrigin: null,
    sourceId: null,
    observedSequence: 0,
    currentSnapshot: null,
    pendingSnapshot: null,
    catalogs: new Map(),
    models: new Map(),
    suites: new Map(),
    evaluations: new Map(),
    assignments: new Map(),
    assignmentSources: new Map(),
    assignmentEvents: new Map(),
    taskIndex: new Map(),
    methodology: null,
    events: [],
    eventIds: new Set(),
    errors: [],
    lastAcceptedAt: undefined,
    proofArtifactCount: 0,
  };
}

type Listener = () => void;

export class EvalStore {
  private state: StoreState = emptyState();
  private listeners = new Set<Listener>();
  // Validation copies only the affected run's context, never the full corpus.
  private campaignContexts = new Map<string, CampaignAdaptContext>();
  /** Suppress subscriber notifications while applying a group of records. */
  private batchDepth = 0;

  getState = (): StoreState => this.state;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  /** True while stream replay has not reached its announced cursor. */
  isReconciling(): boolean {
    return this.state.pendingSnapshot !== null;
  }

  /** Coalesce related store updates. */
  beginBatch(): void {
    this.batchDepth++;
  }

  endBatch(): void {
    if (this.batchDepth <= 0) return;
    this.batchDepth--;
    if (this.batchDepth === 0) this.emit();
  }

  private emit(): void {
    for (const listener of this.listeners) listener();
  }

  private emitIfNotBatching(): void {
    if (this.batchDepth === 0) this.emit();
  }

  private setState(updater: (state: StoreState) => StoreState): void {
    this.state = updater(this.state);
    this.emitIfNotBatching();
  }

  setMirrorOrigin(mirrorOrigin: string): void {
    if (this.state.mirrorOrigin && this.state.mirrorOrigin !== mirrorOrigin) {
      this.campaignContexts = new Map();
      const state = emptyState();
      state.mirrorOrigin = mirrorOrigin;
      this.state = state;
      this.emitIfNotBatching();
      return;
    }
    this.setState((state) => ({ ...state, mirrorOrigin }));
  }

  setConnection(connection: FeedConnectionState, message?: string): void {
    this.setState((state) => ({
      ...state,
      connection,
      feedStatus: state.feedStatus
        ? { ...state.feedStatus, connection, message: message ?? state.feedStatus.message }
        : null,
    }));
  }

  setStreamConnection(streamConnection: StreamConnectionState): void {
    this.setState((state) => ({ ...state, streamConnection }));
  }

  /** Install the mirror's complete current read model atomically. Sparse
   *  entity revisions need no history replay to reach the snapshot cursor. */
  /** Replaces state with a read model. `refresh` is a newer read model of the same
   *  source and withdrawal state, so what views loaded on demand stays valid: the
   *  read model holds no assignment results, and a settled run's bundle is unchanged. */
  initBootstrap(
    snapshot: FeedSnapshot,
    recentProjections: ProjectionRecord[],
    proofCount: number,
    { refresh = false }: { refresh?: boolean } = {},
  ): void {
    if (!refresh) this.campaignContexts = new Map();
    const state = emptyState();
    if (refresh) {
      state.assignments = this.state.assignments;
      state.assignmentSources = this.state.assignmentSources;
    }
    state.connection = 'live';
    state.mirrorOrigin = this.state.mirrorOrigin;
    state.sourceId = snapshot.source_id;
    state.observedSequence = snapshot.high_water_sequence;
    state.currentSnapshot = snapshot;
    state.feedStatus = withFeedSnapshot(state, snapshot, 'Current snapshot loaded.');
    state.feedStatus.connection = 'live';
    state.proofArtifactCount = proofCount;
    // Snapshot entities are sparse revisions, ordered by their accepted sequence.
    const records = [...recentProjections].sort((a, b) => a.sequence - b.sequence);
    for (const record of records) {
      isProjectionRecord(record);
      if (record.sequence > snapshot.high_water_sequence || record.sequence < 1) {
        throw new ValidationError('record exceeds snapshot cursor', 'bootstrap.sequence');
      }
      this.ingestProjection(state, record);
    }
    if (state.errors.length > 0) {
      for (const error of state.errors) this.pushError(error);
      throw new ValidationError('current snapshot contains invalid records', 'bootstrap');
    }
    this.state = state;
    this.emitIfNotBatching();
  }

  acceptSnapshot(snapshot: FeedSnapshot): void {
    if (this.state.sourceId && snapshot.source_id !== this.state.sourceId) {
      this.pushError('public snapshot source changed');
      return;
    }
    const reference = this.state.currentSnapshot ?? this.state.pendingSnapshot;
    if (
      reference &&
      (snapshot.high_water_sequence < reference.high_water_sequence ||
        snapshot.high_water_sequence < this.state.observedSequence)
    ) {
      this.pushError('public snapshot sequence regressed');
      return;
    }
    if (
      reference &&
      snapshot.high_water_sequence === reference.high_water_sequence &&
      snapshot.feed_chain_hash !== reference.feed_chain_hash
    ) {
      this.pushError('public snapshot equivocation');
      return;
    }
    if (snapshot.high_water_sequence > this.state.observedSequence) {
      this.setState((state) => ({
        ...state,
        pendingSnapshot: snapshot,
        feedStatus: state.feedStatus
          ? { ...withFeedSnapshot(state, snapshot, 'Receiving updates.'), connection: state.connection }
          : null,
      }));
      return;
    }
    this.setState((state) => ({
      ...state,
      currentSnapshot: snapshot,
      pendingSnapshot: null,
      connection: 'live',
      feedStatus: { ...withFeedSnapshot(state, snapshot, 'Snapshot sealed.'), connection: 'live' },
    }));
  }

  /** Seals a live status snapshot after its tail was applied. The tail omits
   *  assignment results by design, so the cursor moves to the snapshot's high
   *  water rather than waiting for a record that will not arrive this way. A
   *  cached status older than what the store holds is ignored. */
  sealLiveSnapshot(snapshot: FeedSnapshot): void {
    if (snapshot.high_water_sequence < this.state.observedSequence) return;
    const reference = this.state.currentSnapshot;
    if (
      reference &&
      snapshot.high_water_sequence === reference.high_water_sequence &&
      snapshot.feed_chain_hash !== reference.feed_chain_hash
    ) {
      return;
    }
    this.setState((state) => ({
      ...state,
      currentSnapshot: snapshot,
      pendingSnapshot: null,
      observedSequence: snapshot.high_water_sequence,
      connection: 'live',
      feedStatus: { ...withFeedSnapshot(state, snapshot, 'Snapshot sealed.'), connection: 'live' },
    }));
  }

  acceptProjection(record: ProjectionRecord): void {
    if (!Number.isSafeInteger(record.sequence) || record.sequence < 1) {
      this.pushError('invalid public feed record');
      return;
    }
    if (record.sequence <= this.state.observedSequence) return;
    if (this.state.observedSequence > 0 && record.sequence < this.state.observedSequence + 1) {
      this.pushError('public feed sequence gap');
      return;
    }
    // observedSequence === 0: accept whatever sequence the retained feed
    // starts at (the mirror may have pruned early records). Later holes are
    // also valid when the mirror omits withdrawn campaign datasets without
    // renumbering the signed batch chain.
    const state: StoreState = { ...this.state };
    this.ingestProjection(state, record);
    state.observedSequence = record.sequence;
    state.lastAcceptedAt = new Date().toISOString();
    if (state.pendingSnapshot && state.pendingSnapshot.high_water_sequence === state.observedSequence) {
      const sealed = state.pendingSnapshot;
      state.currentSnapshot = sealed;
      state.pendingSnapshot = null;
      state.connection = 'live';
      state.feedStatus = { ...withFeedSnapshot(state, sealed, 'Snapshot sealed.'), connection: 'live' };
    } else {
      const nextFeedStatus = withAcceptedRecord(state);
      if (nextFeedStatus) state.feedStatus = nextFeedStatus;
    }
    this.state = state;
    this.emitIfNotBatching();
  }

  /** Ingests one run's assignment results from its bundle. Only assignment
   *  results enter the store: no live events, and the feed cursor does not move.
   *  Every record is adapted and checked against the scope before anything is
   *  committed, so a rejected bundle leaves the store unchanged. Re-ingesting a
   *  result already known from the live stream overwrites it with the same value. */
  acceptRunBundle(records: ProjectionRecord[], scope: RunBundleScope): void {
    // Adaptation advances campaign progress, so it runs on copies of this run's
    // contexts; the copies replace the live contexts only after every record passes.
    const contexts = new Map<string, CampaignAdaptContext>();
    const staged: Array<{ assignment: AssignmentResult; source: ProjectionRecord }> = [];
    for (const record of records) {
      const payload = getParsedProjectionPayload(record) ?? parseProjectionPayload(record.record_bytes);
      if (!isCampaignProjectionPayload(payload) || payload.message_type !== 'PublicAssignmentResultProjection') {
        throw new ValidationError('bundle record is not an assignment result', 'run_bundle');
      }
      const envelope = decodeCampaignProjectionEnvelope(payload);
      const runId = envelope.record.run_id;
      let context = contexts.get(runId);
      if (!context) {
        context = cloneCampaignAdaptContext(this.campaignContexts.get(runId) ?? createCampaignAdaptContext());
        contexts.set(runId, context);
      }
      for (const adapted of adaptDecodedCampaignProjectionEnvelope(envelope, context)) {
        if (adapted.kind !== 'assignment_result') continue;
        const assignment = decodeViewRecord(adapted.kind, adapted);
        if (assignment.kind !== 'assignment_result') continue;
        if (assignment.run_id !== scope.runId || assignment.dataset_id !== scope.datasetId) {
          throw new ValidationError('bundle record belongs to another run', 'run_bundle');
        }
        staged.push({ assignment, source: record });
      }
    }
    const state: StoreState = {
      ...this.state,
      assignments: new Map(this.state.assignments),
      assignmentSources: new Map(this.state.assignmentSources),
    };
    for (const { assignment, source } of staged) {
      const key = recordKey(assignment.dataset_id, assignment.assignment_id);
      state.assignments.set(key, assignment);
      state.assignmentSources.set(key, source);
    }
    for (const [runId, context] of contexts) this.campaignContexts.set(runId, context);
    this.state = state;
    this.emitIfNotBatching();
  }

  /** Drops one run's assignment results and their sources. Lifecycle events
   *  stay: they come from the bootstrap and cannot be reloaded from a bundle. */
  removeRunAssignments(datasetId: string, runId: string): void {
    this.setState((state) => {
      const assignments = new Map(state.assignments);
      const assignmentSources = new Map(state.assignmentSources);
      for (const [key, assignment] of state.assignments) {
        if (assignment.dataset_id !== datasetId || assignment.run_id !== runId) continue;
        assignments.delete(key);
        assignmentSources.delete(key);
      }
      return { ...state, assignments, assignmentSources };
    });
  }

  private ingestProjection(state: StoreState, record: ProjectionRecord): void {
    try {
      isProjectionRecord(record);
      if (record.record_type !== 'projection' && record.record_type !== 'event') return;
      const payload = getParsedProjectionPayload(record) ?? parseProjectionPayload(record.record_bytes);
      if (
        payload.kind === 'evaluation_summary' &&
        payload.native_result === undefined &&
        (payload.model_role_mapping === undefined || payload.model_role_mapping === null)
      ) {
        payload.model_role_mapping = {};
      }
      if (isCampaignProjectionPayload(payload)) {
        const envelope = decodeCampaignProjectionEnvelope(payload);
        const runId = envelope.record.run_id;
        const context = this.campaignContexts.get(runId) ?? createCampaignAdaptContext();
        const transaction = beginCampaignAdaptContextTransaction(context);
        let validated: Array<SnapshotRecord | LiveEvent>;
        try {
          const adapted = adaptDecodedCampaignProjectionEnvelope(envelope, context);
          validated = adapted.map((decoded) => decodeViewRecord(decoded.kind, decoded));
          transaction.commit();
        } catch (error) {
          transaction.rollback();
          throw error;
        }
        this.campaignContexts.set(runId, context);
        for (const decoded of validated) {
          this.indexRecord(state, decoded, record.sequence);
          if (decoded.kind === 'assignment_result') {
            state.assignmentSources.set(recordKey(decoded.dataset_id, decoded.assignment_id), record);
          }
        }
        return;
      }
      const kind = payload.kind;
      if (typeof kind !== 'string' || kind.length === 0) {
        throw new ValidationError('expected string', 'projection.kind');
      }
      const decoded = isLiveEventKind(kind) ? decodeLiveEventWire(payload) : decodeViewRecord(kind, payload);
      this.indexRecord(state, decoded, record.sequence);
      if (decoded.kind === 'assignment_result') {
        state.assignmentSources.set(recordKey(decoded.dataset_id, decoded.assignment_id), record);
      }
    } catch (error) {
      const message = error instanceof ValidationError ? `${error.path}: ${error.message}` : 'invalid public feed record';
      state.errors = [...state.errors, message].slice(-20);
    }
  }

  private indexRecord(state: StoreState, record: SnapshotRecord | LiveEvent, feedSequence?: number): void {
    record = normalizeQualityRecord(record);
    switch (record.kind) {
      case 'catalog_snapshot':
        state.catalogs.set(record.dataset_id, record);
        if (record.assignment_count > 0) {
          const runId = campaignRunIdFromDatasetId(record.dataset_id);
          if (runId) {
            const context = this.campaignContexts.get(runId) ?? createCampaignAdaptContext();
            recordCampaignMatrixTotal(context, runId, record.assignment_count);
            this.campaignContexts.set(runId, context);
            this.refreshRunProgressTotals(state, record.dataset_id, runId);
          }
        }
        break;
      case 'model_summary':
        state.models.set(modelRecordKey(record.dataset_id, record.variant_id, record.role), record);
        break;
      case 'suite_summary':
        state.suites.set(recordKey(record.dataset_id, record.suite_id), record);
        break;
      case 'evaluation_summary': {
        const key = recordKey(record.dataset_id, record.run_id);
        state.evaluations.set(key, record);
        break;
      }
      case 'assignment_result':
        state.assignments.set(recordKey(record.dataset_id, record.assignment_id), record);
        break;
      case 'methodology_snapshot':
        state.methodology = record;
        break;
      default:
        if ('event_id' in record) {
          const event =
            feedSequence !== undefined
              ? ({ ...(record as LiveEvent), feed_sequence: feedSequence } as LiveEvent)
              : (record as LiveEvent);
          if (event.assignment_id) {
            const key = recordKey(event.dataset_id, event.assignment_id);
            const history = state.assignmentEvents.get(key) ?? [];
            if (!history.some((existing) => existing.event_id === event.event_id)) {
              state.assignmentEvents.set(key, [...history, event]);
            }
          }
          if (!state.eventIds.has(event.event_id)) {
            const { events, replacedId } = upsertLiveEvent(state.events, event);
            if (replacedId) state.eventIds.delete(replacedId);
            state.eventIds.add(event.event_id);
            state.events = events;
            this.retainLiveEvents(state, LIVE_EVENT_STORE_LIMIT);
          }
        }
        break;
    }
  }

  /** Drop oldest live events by mirror feed sequence so retention tracks the feed tail. */
  private retainLiveEvents(state: StoreState, maxEvents: number): void {
    if (state.events.length <= maxEvents) return;
    const retained = retainLatestStreamEvents(state.events, maxEvents);
    state.events = retained;
    state.eventIds = new Set(retained.map((event) => event.event_id));
  }

  /** Reconcile activity progress after the catalog's matrix size is known. */
  private refreshRunProgressTotals(state: StoreState, datasetId: string, runId: string): void {
    const progress = this.campaignContexts.get(runId)?.runTotals.get(runId);
    if (!progress) return;
    const { total } = campaignProgressCounts(progress);
    if (total <= 0) return;

    state.events = state.events.map((event) =>
      event.run_id === runId && event.dataset_id === datasetId ? { ...event, total } : event,
    );
  }

  pushError(message: string): void {
    const wasBatching = this.batchDepth > 0;
    this.setState((state) => ({ ...state, errors: [...state.errors, message].slice(-20) }));
    if (wasBatching) this.emit();
  }

  clearErrors(): void {
    this.setState((state) => ({ ...state, errors: [] }));
  }

  setOffline(message: string): void {
    this.setState((state) => ({
      ...state,
      connection: 'offline',
      streamConnection: 'disconnected',
      feedStatus: state.feedStatus
        ? { ...state.feedStatus, connection: 'offline', freshness: 'source_offline', message }
        : {
            connection: 'offline',
            freshness: 'source_offline',
            highWaterSequence: 0,
            lastAcceptedAt: undefined,
            message,
          },
    }));
  }

  loadFixtures(snapshotRecords: SnapshotRecord[], liveEvents: LiveEvent[]): void {
    this.campaignContexts = new Map();
    const state = emptyState();
    state.connection = 'offline';
    state.feedStatus = {
      connection: 'offline',
      freshness: 'source_offline',
      highWaterSequence: 0,
      lastAcceptedAt: undefined,
      message: 'Using explicit design-preview data. The public mirror is not connected.',
    };
    for (const record of snapshotRecords) {
      this.indexRecord(state, record);
    }
    for (const event of liveEvents) {
      this.indexRecord(state, event, event.feed_sequence);
    }
    this.state = state;
    this.emitIfNotBatching();
  }

  /** Replaces the task index with the bootstrap's. */
  acceptTaskIndex(entries: FeedTaskIndexEntry[]): void {
    this.setState((state) => ({
      ...state,
      taskIndex: new Map(entries.map((entry) => [entry.task_id, entry.results])),
    }));
  }

  /** The most recent results the feed holds for one task, newest first. */
  getTaskResults(taskId: string): FeedTaskResult[] {
    return this.state.taskIndex.get(taskId) ?? [];
  }

  getCatalogs(): CatalogSnapshot[] {
    return Array.from(this.state.catalogs.values());
  }

  getCatalog(datasetId: string): CatalogSnapshot | undefined {
    return this.state.catalogs.get(datasetId);
  }

  getModels(datasetId: string): ModelSummary[] {
    return Array.from(this.state.models.values()).filter((m) => m.dataset_id === datasetId);
  }

  getModel(datasetId: string, variantId: string, role?: ModelRole): ModelSummary | undefined {
    if (role) {
      return this.state.models.get(modelRecordKey(datasetId, variantId, role));
    }
    let fallback: ModelSummary | undefined;
    for (const model of this.state.models.values()) {
      if (model.dataset_id !== datasetId || model.variant_id !== variantId) continue;
      if (model.pass_rate) return model;
      fallback = fallback ?? model;
    }
    return fallback;
  }

  getSuites(datasetId: string): SuiteSummary[] {
    return Array.from(this.state.suites.values()).filter((s) => s.dataset_id === datasetId);
  }

  getEvaluations(datasetId: string): EvaluationSummary[] {
    return Array.from(this.state.evaluations.values()).filter((e) => e.dataset_id === datasetId);
  }

  getEvaluation(datasetId: string, runId: string): EvaluationSummary | undefined {
    return this.state.evaluations.get(recordKey(datasetId, runId));
  }

  getAssignments(runId: string, datasetId?: string): AssignmentResult[] {
    return Array.from(this.state.assignments.values()).filter(
      (a) => a.run_id === runId && (datasetId === undefined || a.dataset_id === datasetId),
    );
  }

  getAssignment(datasetId: string, assignmentId: string): AssignmentResult | undefined {
    return this.state.assignments.get(recordKey(datasetId, assignmentId));
  }

  getEvents(runId?: string, datasetId?: string): LiveEvent[] {
    return this.state.events.filter(
      (e) =>
        (runId === undefined || e.run_id === runId) &&
        (datasetId === undefined || e.dataset_id === datasetId),
    );
  }
}

export const evalStore = new EvalStore();

// React binding hooks. Selectors that derive collections must return a
// stable reference while the underlying records are unchanged; otherwise
// useSyncExternalStore's consistency check re-renders forever.
const UNSET = Symbol('unset');

function defaultEqual<T>(a: T, b: T): boolean {
  if (Object.is(a, b)) return true;
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((value, index) => Object.is(value, b[index]));
  }
  return false;
}

export function useStoreState<T>(
  selector: (state: StoreState) => T,
  isEqual: (a: T, b: T) => boolean = defaultEqual,
): T {
  const cache = useRef<T | typeof UNSET>(UNSET);
  return useSyncExternalStore(
    evalStore.subscribe,
    () => {
      const next = selector(evalStore.getState());
      if (cache.current !== UNSET && isEqual(cache.current as T, next)) {
        return cache.current as T;
      }
      cache.current = next;
      return next;
    },
    () => selector(evalStore.getState()),
  );
}

export function useFeedStatus(): FeedStatus | null {
  return useSyncExternalStore(
    evalStore.subscribe,
    () => evalStore.getState().feedStatus,
    () => evalStore.getState().feedStatus,
  );
}

export function useConnection(): FeedConnectionState {
  return useSyncExternalStore(
    evalStore.subscribe,
    () => evalStore.getState().connection,
    () => evalStore.getState().connection,
  );
}

export function useStreamConnection(): StreamConnectionState {
  return useSyncExternalStore(
    evalStore.subscribe,
    () => evalStore.getState().streamConnection,
    () => evalStore.getState().streamConnection,
  );
}
