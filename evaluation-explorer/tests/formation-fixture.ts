// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { EvaluationSummary } from '../src/contract/types';
import { allFixtureSnapshotRecords } from '../src/fixtures/fixtures';

const source = allFixtureSnapshotRecords.find((record): record is EvaluationSummary => record.kind === 'evaluation_summary')!;
const outcomes = { completed: 3, model_failed: 1, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 };
export const formationRun: EvaluationSummary = {
  ...source, schema_version: '1.13.0', dataset_id: 'ds-formation', run_id: 'run-formation', suite_id: 'formation-suite',
  quality_state: 'exploratory_verified', lifecycle_state: 'completed', verifier_state: 'passed',
  evaluation_unit: 'system', assignment_total: 4, assignment_completed: 3, assignment_failed: 1,
  primary_invocation_share: undefined, correlated_failure_rate: undefined,
  terminal_outcomes: outcomes,
  headline_metrics: {
    pass_rate: { value: 0.75, unit: 'ratio', observed_count: 4, eligible_count: 4, unavailable_count: 0 },
    latency_p50_ms: { value: 2000, unit: 'milliseconds', observed_count: 4, eligible_count: 4, unavailable_count: 0 },
    output_throughput_p50_tokens_per_second: { value: 100, unit: 'tokens_per_second', observed_count: 4, eligible_count: 4, unavailable_count: 0 },
  },
  verification_metadata: {
    provenance: 'bound', verifier_state: 'passed', verifier_release_version: 'v1.9.0', verifier_contract_version: '1.0.0',
    report_digest: 'b'.repeat(64), population_digest: 'c'.repeat(64), verified_at: '2026-11-04T10:00:00Z',
  },
  formations: [{
    stack_id: 'current-project-formation', stack_digest: 'a'.repeat(64),
    model_role_mapping: { primary: 'current-primary', assistant: 'current-assistant', lite: 'current-lite' },
    assignment_total: 4, terminal_outcomes: outcomes,
    pass_rate: { estimate: 0.75, lower: 0.75, upper: 0.75, denominator: 4 },
    security_pass_rate: { estimate: 1, lower: 1, upper: 1, denominator: 2 },
    latency_p50_ms: { value: 2000 }, output_tokens: { value: 400 }, output_throughput_p50: { value: 100 },
    parameter_count: 10_000_000_000, artifact_size_bytes: 6 * 1024 ** 3, run_seconds: 60,
  }],
};

