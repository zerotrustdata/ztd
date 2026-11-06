// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, it, expect, beforeEach } from 'vitest';
import { EvalStore, recordKey } from '../src/state/store';
import { fixtureCampaignResultEnvelope } from '../src/fixtures/fixtures';
import type { ProjectionRecord } from '../src/contract/types';

function resultRecord(sequence: number, runId: string, assignmentId: string): ProjectionRecord {
  const envelope = {
    ...fixtureCampaignResultEnvelope,
    idempotency_key: `${runId}:${assignmentId}:result`,
    record: { ...fixtureCampaignResultEnvelope.record, run_id: runId, assignment_id: assignmentId },
  };
  return { sequence, record_type: 'projection', record_bytes: JSON.stringify(envelope) };
}

describe('EvalStore run assignment ingestion', () => {
  let store: EvalStore;

  beforeEach(() => {
    store = new EvalStore();
  });

  it('acceptRunBundle ingests records whose sequence is at or below the observed sequence', () => {
    store.acceptProjection(resultRecord(5, 'run-live', 'assign-live'));
    expect(store.getState().observedSequence).toBe(5);

    store.acceptRunBundle([resultRecord(2, 'run-bundle', 'assign-a'), resultRecord(3, 'run-bundle', 'assign-b')], { datasetId: 'ds-live-run-bundle', runId: 'run-bundle' });

    expect(store.getAssignments('run-bundle', 'ds-live-run-bundle')).toHaveLength(2);
    expect(store.getAssignments('run-live', 'ds-live-run-live')).toHaveLength(1);
    expect(store.getState().observedSequence).toBe(5);
  });

  it('acceptRunBundle leaves the feed cursor, last accepted time, and connection untouched', () => {
    store.acceptProjection(resultRecord(5, 'run-live', 'assign-live'));
    const before = store.getState();

    store.acceptRunBundle([resultRecord(1, 'run-bundle', 'assign-a')], { datasetId: 'ds-live-run-bundle', runId: 'run-bundle' });

    const after = store.getState();
    expect(after.observedSequence).toBe(before.observedSequence);
    expect(after.lastAcceptedAt).toBe(before.lastAcceptedAt);
    expect(after.pendingSnapshot).toBe(before.pendingSnapshot);
    expect(after.connection).toBe(before.connection);
    expect(after.feedStatus).toBe(before.feedStatus);
  });

  it('re-ingesting records already seen from the live stream keeps assignments and progress unchanged', () => {
    const live = resultRecord(1, 'run-overlap', 'assign-1');
    store.acceptProjection(live);
    const runId = 'run-overlap';
    const datasetId = 'ds-live-run-overlap';
    const completedBefore = store.getEvents(runId, datasetId).map((event) => event.completed);
    const assignmentBefore = store.getAssignment(datasetId, 'assign-1');

    store.acceptRunBundle([{ ...live, sequence: 9 }], { datasetId, runId });

    expect(store.getAssignments(runId, datasetId)).toHaveLength(1);
    expect(store.getAssignment(datasetId, 'assign-1')).toEqual(assignmentBefore);
    expect(store.getEvents(runId, datasetId).map((event) => event.completed)).toEqual(completedBefore);
    expect(store.getEvents(runId, datasetId).filter((event) => event.kind === 'assignment_completed')).toHaveLength(1);
    expect(store.getState().errors).toEqual([]);
  });

  it('loading a bundle leaves the live event tail and its ids unchanged', () => {
    store.acceptProjection(resultRecord(1, 'run-live', 'assign-live'));
    const { events, eventIds } = store.getState();

    store.acceptRunBundle([resultRecord(2, 'run-bundle', 'assign-a')], { datasetId: 'ds-live-run-bundle', runId: 'run-bundle' });

    expect(store.getState().events).toBe(events);
    expect(store.getState().eventIds).toBe(eventIds);
    expect(store.getAssignments('run-bundle', 'ds-live-run-bundle')).toHaveLength(1);
  });

  it('acceptRunBundle rejects a record from another run and ingests none of the bundle', () => {
    const scope = { datasetId: 'ds-live-run-a', runId: 'run-a' };

    expect(() =>
      store.acceptRunBundle([resultRecord(1, 'run-a', 'assign-1'), resultRecord(2, 'run-other', 'assign-2')], scope),
    ).toThrow();

    expect(store.getAssignments('run-a', 'ds-live-run-a')).toHaveLength(0);
    expect(store.getAssignments('run-other', 'ds-live-run-other')).toHaveLength(0);
    expect(store.getState().assignmentSources.size).toBe(0);
  });

  it('acceptRunBundle rejects a non-result record and ingests none of the bundle', () => {
    const lifecycle: ProjectionRecord = {
      sequence: 2,
      record_type: 'projection',
      record_bytes: JSON.stringify({
        schema_version: '1.0.0',
        message_type: 'PublicAssignmentLifecycleRecord',
        idempotency_key: 'run-a:assign-2:lifecycle',
        record: {
          run_id: 'run-a',
          assignment_id: 'assign-2',
          scenario_id: 'scenario-2',
          lifecycle_status: 'EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED',
          scenario_category: 'EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE',
          lane: 'EVALUATION_LANE_MODEL_ROLE',
          designated_role: 'MODEL_CAMPAIGN_ROLE_PRIMARY',
          variant_id: 'qwen3-4b',
          observed_at: '2026-09-16T14:00:00.000Z',
        },
      }),
    };

    expect(() =>
      store.acceptRunBundle([resultRecord(1, 'run-a', 'assign-1'), lifecycle], { datasetId: 'ds-live-run-a', runId: 'run-a' }),
    ).toThrow();
    expect(store.getAssignments('run-a', 'ds-live-run-a')).toHaveLength(0);
  });

  it('removeRunAssignments removes assignments and sources for only that run and keeps lifecycle events', () => {
    store.acceptProjection(resultRecord(1, 'run-a', 'assign-1'));
    store.acceptProjection(resultRecord(2, 'run-a', 'assign-2'));
    store.acceptRunBundle([resultRecord(3, 'run-b', 'assign-1')], { datasetId: 'ds-live-run-b', runId: 'run-b' });
    const datasetA = 'ds-live-run-a';
    const datasetB = 'ds-live-run-b';
    expect(store.getAssignments('run-a', datasetA)).toHaveLength(2);
    expect(store.getAssignments('run-b', datasetB)).toHaveLength(1);
    expect(store.getState().assignmentEvents.get(recordKey(datasetA, 'assign-1'))?.length).toBeGreaterThan(0);

    store.removeRunAssignments(datasetA, 'run-a');

    const state = store.getState();
    expect(store.getAssignments('run-a', datasetA)).toHaveLength(0);
    expect(store.getAssignment(datasetA, 'assign-1')).toBeUndefined();
    expect(state.assignmentSources.has(recordKey(datasetA, 'assign-1'))).toBe(false);
    expect(state.assignmentSources.has(recordKey(datasetA, 'assign-2'))).toBe(false);
    expect(state.assignmentEvents.get(recordKey(datasetA, 'assign-1'))?.length).toBeGreaterThan(0);

    expect(store.getAssignment(datasetB, 'assign-1')).toBeDefined();
    expect(state.assignmentSources.has(recordKey(datasetB, 'assign-1'))).toBe(true);
  });

  it('removeRunAssignments notifies subscribers once per removal', () => {
    store.acceptRunBundle([resultRecord(1, 'run-a', 'assign-1')], { datasetId: 'ds-live-run-a', runId: 'run-a' });
    let notifications = 0;
    const unsubscribe = store.subscribe(() => {
      notifications++;
    });

    store.removeRunAssignments('ds-live-run-a', 'run-a');
    unsubscribe();

    expect(notifications).toBe(1);
  });
});
