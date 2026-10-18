// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import type { EvaluationSummary, LiveEvent } from '../src/contract/types';
import {
  latestAssignmentActivity,
  resolveCampaignAssignmentProgress,
  resolveRoleSlots,
} from '../src/views/campaign-activity';

const datasetId = 'ds-live-model-role';

function evaluation(overrides: Partial<EvaluationSummary> = {}): EvaluationSummary {
  return {
    schema_version: '1.4.0',
    kind: 'evaluation_summary',
    dataset_id: datasetId,
    quality_state: 'live_in_progress',
    observed_at: '2026-09-22T12:05:00Z',
    run_id: 'eval-model-role-run',
    campaign_id: 'eval-qwen',
    suite_id: 'default-suite',
    arm: 'homogeneous-model-role',
    evaluation_unit: 'model',
    model_role_mapping: {
      primary: 'qwen3-8b',
      assistant: 'qwen3-8b',
      lite: 'qwen3-8b',
    },
    lifecycle_state: 'running',
    assignment_total: 75,
    assignment_completed: 5,
    assignment_failed: 1,
    terminal_outcomes: { completed: 5, model_failed: 1, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
    started_at: '2026-09-22T12:00:00Z',
    verifier_state: 'not_applicable',
    headline_metrics: {},
    ...overrides,
  };
}

function liveEvent(overrides: Partial<LiveEvent> = {}): LiveEvent {
  return {
    schema_version: '1.4.0',
    kind: 'stage_updated',
    dataset_id: datasetId,
    quality_state: 'live_in_progress',
    observed_at: '2026-09-22T12:05:00Z',
    event_id: 'event-default',
    run_id: 'eval-model-role-run',
    lifecycle_status: 'running',
    completed: 6,
    total: 75,
    ...overrides,
  };
}

describe('campaign activity role slots', () => {
  it('binds the run model to every role slot and activates the current assignment role', () => {
    const run = evaluation();
    const events = [
      liveEvent({
        event_id: 'event-primary-done',
        kind: 'assignment_completed',
        assignment_id: 'assignment-6',
        task_id: 'tool-selection-03',
        variant_id: 'qwen3-8b',
        role: 'primary',
        observed_at: '2026-09-22T12:04:40Z',
      }),
      liveEvent({
        event_id: 'event-lite',
        kind: 'assignment_started',
        assignment_id: 'assignment-7',
        task_id: 'tool-selection-04',
        variant_id: 'qwen3-8b',
        role: 'lite',
        stage_label: 'model role invoked · lite · qwen3-8b · tool-selection-04',
        observed_at: '2026-09-22T12:05:00Z',
      }),
    ];
    const activity = latestAssignmentActivity(events);
    expect(activity).toBeDefined();

    const slots = resolveRoleSlots(
      run,
      events,
      activity!,
      resolveCampaignAssignmentProgress([run]),
    );

    expect(slots.map((slot) => slot.role)).toEqual(['primary', 'assistant', 'lite']);
    expect(slots.find((slot) => slot.role === 'lite')).toMatchObject({
      variantId: 'qwen3-8b',
      status: 'active',
      progressPercent: 8,
      taskId: 'tool-selection-04',
    });
    expect(slots.find((slot) => slot.role === 'primary')).toMatchObject({
      variantId: 'qwen3-8b',
      status: 'idle',
    });
    expect(slots.find((slot) => slot.role === 'assistant')).toMatchObject({
      variantId: 'qwen3-8b',
      status: 'idle',
    });
  });

  it('keeps homogeneous runs in the Primary slot only', () => {
    const run = evaluation({
      evaluation_unit: 'model',
      model_role_mapping: {},
      run_id: 'eval-homogeneous-run',
    });
    const events = [
      liveEvent({
        run_id: 'eval-homogeneous-run',
        event_id: 'event-primary',
        assignment_id: 'assignment-7',
        task_id: 'tool-selection-04',
        variant_id: 'ministral-3-8b',
        role: 'primary',
        kind: 'assignment_started',
        observed_at: '2026-09-22T12:05:00Z',
      }),
    ];
    const activity = latestAssignmentActivity(events);
    const slots = resolveRoleSlots(
      run,
      events,
      activity!,
      resolveCampaignAssignmentProgress([run]),
    );

    expect(slots.find((slot) => slot.role === 'primary')).toMatchObject({
      variantId: 'ministral-3-8b',
      status: 'active',
      progressPercent: 8,
    });
    expect(slots.find((slot) => slot.role === 'assistant')?.variantId).toBeUndefined();
    expect(slots.find((slot) => slot.role === 'lite')?.variantId).toBeUndefined();
  });
});
