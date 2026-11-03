// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { decodeCampaignProjectionEnvelope } from '../src/contract/campaign-wire';
import { normalizeActivityFamily, parseWireActivityFamily } from '../src/contract/activity-family';
import { fixtureCampaignResultEnvelope, fixtureEnrichedAssignmentResult } from '../src/fixtures/fixtures';
import { MAX_ACTIVITY_RECORDS, MAX_TOOL_LOOP_RECORDS, ValidationError, decodeViewRecord } from '../src/contract/validators';

describe('activity-family wire parsing', () => {
  it('accepts unavailable families when protobuf omits records', () => {
    const family = parseWireActivityFamily(
      {
        availability: 'PUBLIC_ACTIVITY_AVAILABILITY_UNAVAILABLE',
        unavailable_reason: 'PUBLIC_UNAVAILABLE_REASON_SOURCE_NOT_CAPTURED',
      },
      'activity.tool_decisions',
      () => ({}),
    );
    expect(family.records).toEqual([]);
    expect(normalizeActivityFamily(family)).toEqual({
      availability: 'unavailable',
      unavailable_reason: 'source_not_captured',
    });
  });

  it('decodes campaign envelopes with omitted activity records', () => {
    expect(() => decodeCampaignProjectionEnvelope(fixtureCampaignResultEnvelope)).not.toThrow();
  });
});

describe('activity family record bounds', () => {
  const wireRecords: Record<string, unknown> = {
    tool_calls: {
      tool_label: 'file_read',
      execution_outcome: 'EVALUATION_VERDICT_STATUS_PASS',
      semantic_outcome: 'EVALUATION_VERDICT_STATUS_PASS',
      evidence_source: 'PUBLIC_EVIDENCE_SOURCE_APPLICATION_REPORTED',
    },
    policy_decisions: {
      tool_label: 'file_read',
      outcome: 'EVALUATION_POLICY_DECISION_OUTCOME_ALLOW',
      evidence_source: 'PUBLIC_EVIDENCE_SOURCE_APPLICATION_REPORTED',
    },
    governed_actions: {
      action_label: 'file_read',
      reported_policy_outcome: 'EVALUATION_POLICY_DECISION_OUTCOME_ALLOW',
      receipt_status: 'PUBLIC_RECEIPT_STATUS_UNAVAILABLE',
      evidence_source: 'PUBLIC_EVIDENCE_SOURCE_APPLICATION_REPORTED',
    },
  };
  const viewRecords: Record<string, unknown> = {
    tool_calls: { tool_label: 'file_read', execution_outcome: 'pass', semantic_outcome: 'pass', evidence_source: 'application_reported' },
    policy_decisions: { tool_label: 'file_read', outcome: 'allow', evidence_source: 'application_reported' },
    governed_actions: { action_label: 'file_read', reported_policy_outcome: 'allow', receipt_status: 'unavailable', evidence_source: 'application_reported' },
  };

  const wireEnvelope = (family: string, count: number) => {
    const record = fixtureCampaignResultEnvelope.record as unknown as { activity_summary: object };
    return {
      ...fixtureCampaignResultEnvelope,
      record: {
        ...record,
        activity_summary: {
          ...record.activity_summary,
          [family]: { availability: 'PUBLIC_ACTIVITY_AVAILABILITY_OBSERVED', records: Array(count).fill(wireRecords[family]) },
        },
      },
    };
  };
  const viewResult = (family: string, count: number) => ({
    ...fixtureEnrichedAssignmentResult,
    activity_summary: {
      ...fixtureEnrichedAssignmentResult.activity_summary,
      [family]: { availability: 'observed', records: Array(count).fill(viewRecords[family]) },
    },
  });

  for (const family of Object.keys(wireRecords)) {
    it(`bounds ${family} by tool loop length on the wire and in the view`, () => {
      expect(() => decodeCampaignProjectionEnvelope(wireEnvelope(family, MAX_TOOL_LOOP_RECORDS))).not.toThrow();
      expect(() => decodeCampaignProjectionEnvelope(wireEnvelope(family, MAX_TOOL_LOOP_RECORDS + 1))).toThrow(ValidationError);
      expect(() => decodeViewRecord('assignment_result', viewResult(family, MAX_TOOL_LOOP_RECORDS))).not.toThrow();
      expect(() => decodeViewRecord('assignment_result', viewResult(family, MAX_TOOL_LOOP_RECORDS + 1))).toThrow(ValidationError);
    });
  }

  it('keeps the general bound on the other families', () => {
    const parse = (count: number) =>
      parseWireActivityFamily(
        { availability: 'PUBLIC_ACTIVITY_AVAILABILITY_OBSERVED', records: Array(count).fill({}) },
        'activity.tool_decisions',
        () => ({}),
      );
    expect(() => parse(MAX_ACTIVITY_RECORDS)).not.toThrow();
    expect(() => parse(MAX_ACTIVITY_RECORDS + 1)).toThrow(ValidationError);
  });
});
