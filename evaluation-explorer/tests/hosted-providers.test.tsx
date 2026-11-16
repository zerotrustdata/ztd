// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import type {
  AssignmentResult,
  EvaluationSummary,
  LiveJudgeActivity,
  ModelSummary,
  SnapshotRecord,
} from '../src/contract/types';
import { VIEW_SCHEMA_VERSION } from '../src/contract/types';
import {
  HOSTED_NO_TELEMETRY_LABEL,
  HOSTED_PROVIDER_CLASSES,
  LOCAL_PROVIDER_CLASSES,
  formatProviderClassLabel,
  formatTelemetryUnavailableReason,
  isHostedProviderClass,
  isLocalProviderClass,
} from '../src/content/provider';
import { resolveModelProfile } from '../src/content/framework-catalog';
import { fixtureEvaluationSummaries, fixtureModelSummaries } from '../src/fixtures/fixtures';
import { evalStore } from '../src/state/store';
import { PREF } from '../src/state/dataset';
import { ModelsView } from '../src/views/ModelsView';
import { ModelDetailView } from '../src/views/ModelDetailView';
import { AssignmentDetailView } from '../src/views/AssignmentDetailView';
import { EvaluationDetailView } from '../src/views/EvaluationDetailView';
import { JudgeActivity } from '../src/components/JudgeActivity';

const DATASET_ID = 'ds-live-hosted-run';
const RUN_ID = 'hosted-run';
const ASSIGNMENT_ID = 'hosted-assign-1';

function makeHostedModel(overrides: Partial<ModelSummary> = {}): ModelSummary {
  const base = fixtureModelSummaries[0]!;
  return {
    ...base,
    schema_version: VIEW_SCHEMA_VERSION,
    dataset_id: DATASET_ID,
    quality_state: 'exploratory_verified',
    variant_id: 'gemini-3-6-flash',
    display_name: 'Gemini 3.6 Flash',
    served_model_tag: 'gemini-3.6-flash',
    model_family: 'gemini',
    model_format: 'hosted',
    backend_provider_class: 'gemini',
    parameter_count: undefined,
    parameter_count_source: undefined,
    quantization_weight_class: undefined,
    artifact_size_bytes: undefined,
    context_limit: 1_048_576,
    role: 'primary',
    inventory_only: false,
    evaluation_coverage: 1,
    tool_calling_exclusions: 0,
    model_digest: 'd'.repeat(64),
    unavailable_reasons: undefined,
    ...overrides,
  };
}

function makeRunSummary(overrides: Partial<EvaluationSummary> = {}): EvaluationSummary {
  return {
    ...fixtureEvaluationSummaries[0]!,
    schema_version: VIEW_SCHEMA_VERSION,
    dataset_id: DATASET_ID,
    run_id: RUN_ID,
    quality_state: 'exploratory_verified',
    verifier_state: 'passed',
    model_role_mapping: { primary: 'gemini-3-6-flash' },
    benchmark_unavailable_reasons: [],
    ...overrides,
  };
}

function makeAssignment(overrides: Partial<AssignmentResult> = {}): AssignmentResult {
  return {
    schema_version: VIEW_SCHEMA_VERSION,
    kind: 'assignment_result',
    dataset_id: DATASET_ID,
    quality_state: 'exploratory_verified',
    observed_at: '2026-11-16T07:00:00Z',
    assignment_id: ASSIGNMENT_ID,
    run_id: RUN_ID,
    task_id: 'tech-network-summary',
    scenario_id: 'tech-network-summary',
    variant_id: 'gemini-3-6-flash',
    role: 'primary',
    repetition: 1,
    terminal_status: 'completed',
    verification_disposition: 'passed',
    verification_metadata: {
      provenance: 'bound',
      verifier_state: 'passed',
      verifier_release_version: 'v1.10.2',
      verifier_contract_version: '2.0.0',
      report_digest: 'e'.repeat(64),
      population_digest: 'f'.repeat(64),
    },
    metric_values: { pass: { value: 1 } },
    stage_summary: [],
    resource_summary: {
      latency_ms: { value: 920 },
      input_tokens: { value: 400 },
      output_tokens: { value: 85 },
      retries: { value: 0 },
    },
    benchmark_observations: {
      timing: { generation_ms: { value: 800 }, whole_task_ms: { value: 920 } },
      unavailable_reasons: [],
    },
    activity_summary: {
      model_activity: {
        availability: 'observed',
        records: [
          {
            model_role: 'primary',
            agent_persona: 'sage',
            variant_id: 'gemini-3-6-flash',
            usage_availability: 'reported',
            input_tokens: { value: 400 },
            output_tokens: { value: 85 },
            generation_duration_nanos: { value: 800_000_000 },
            total_duration_nanos: { value: 920_000_000 },
            retry_count: { value: 0 },
            finish_state: 'stop',
            load_state: 'unavailable',
          },
        ],
      },
      tool_decisions: { availability: 'observed', records: [] },
      tool_calls: { availability: 'observed', records: [] },
      policy_decisions: { availability: 'observed', records: [] },
      governed_actions: { availability: 'observed', records: [] },
    },
    ...overrides,
  };
}

describe('hosted provider helpers', () => {
  it('distinguishes hosted and local provider classes matching backend constants', () => {
    expect(HOSTED_PROVIDER_CLASSES).toEqual(['gemini', 'anthropic', 'openai']);
    expect(LOCAL_PROVIDER_CLASSES).toEqual(['ollama', 'llamacpp']);
    for (const hosted of HOSTED_PROVIDER_CLASSES) {
      expect(isHostedProviderClass(hosted)).toBe(true);
      expect(isLocalProviderClass(hosted)).toBe(false);
      expect(formatProviderClassLabel(hosted)).toBe(`${hosted} (hosted)`);
    }
    for (const local of LOCAL_PROVIDER_CLASSES) {
      expect(isHostedProviderClass(local)).toBe(false);
      expect(isLocalProviderClass(local)).toBe(true);
      expect(formatProviderClassLabel(local)).toBe(local);
    }
  });

  it('normalizes source_not_captured to hosted telemetry label only for hosted providers', () => {
    expect(formatTelemetryUnavailableReason('source_not_captured', 'gemini')).toBe(HOSTED_NO_TELEMETRY_LABEL);
    expect(formatTelemetryUnavailableReason('source not captured', 'anthropic')).toBe(HOSTED_NO_TELEMETRY_LABEL);
    expect(formatTelemetryUnavailableReason('source_not_captured', 'ollama')).toBe('source_not_captured');
    expect(formatTelemetryUnavailableReason('source_unavailable', 'openai')).toBe('source_unavailable');
  });

  it('resolves hosted model families and backend_provider_class in model profiles', () => {
    const geminiProfile = resolveModelProfile(makeHostedModel());
    expect(geminiProfile.family).toBe('Gemini');
    expect(geminiProfile.provider).toBe('Google');
    expect(geminiProfile.backend_provider_class).toBe('gemini');
    expect(geminiProfile.is_hosted).toBe(true);

    const claudeProfile = resolveModelProfile(
      makeHostedModel({
        variant_id: 'claude-sonnet-5-5',
        display_name: 'Claude Sonnet 5.5',
        served_model_tag: 'claude-sonnet-5-5',
        model_family: 'claude',
        backend_provider_class: 'anthropic',
      }),
    );
    expect(claudeProfile.family).toBe('Claude');
    expect(claudeProfile.provider).toBe('Anthropic');
    expect(claudeProfile.is_hosted).toBe(true);

    const openaiProfile = resolveModelProfile(
      makeHostedModel({
        variant_id: 'gpt-5',
        display_name: 'GPT-5',
        served_model_tag: 'gpt-5',
        model_family: undefined,
        backend_provider_class: 'openai',
      }),
    );
    expect(openaiProfile.family).toBe('GPT');
    expect(openaiProfile.provider).toBe('OpenAI');
    expect(openaiProfile.is_hosted).toBe(true);
  });
});

describe('hosted provider UI surfacing', () => {
  beforeEach(() => {
    localStorage.removeItem(PREF.filters);
    evalStore.loadFixtures([], []);
  });

  it('shows provider class per model variant in ModelsView and supports filtering by hosted class', () => {
    const gemini = makeHostedModel();
    const local = makeHostedModel({
      variant_id: 'qwen3-4b',
      display_name: 'Qwen 3 4B',
      served_model_tag: 'qwen3:4b',
      model_family: 'qwen3',
      model_format: 'gguf',
      backend_provider_class: 'ollama',
      parameter_count: 4_000_000_000,
    });
    evalStore.loadFixtures([gemini, local], []);

    render(
      <MemoryRouter initialEntries={['/models?release=all']}>
        <ModelsView />
      </MemoryRouter>,
    );

    const geminiRow = screen.getByRole('link', { name: 'Gemini 3.6 Flash' }).closest('tr')!;
    expect(geminiRow).toHaveTextContent('gemini (hosted)');
    const ollamaRow = screen.getByRole('link', { name: 'Qwen 3 4B' }).closest('tr')!;
    expect(ollamaRow).toHaveTextContent('ollama');

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'gemini' } });
    expect(screen.getByRole('link', { name: 'Gemini 3.6 Flash' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Qwen 3 4B' })).not.toBeInTheDocument();
  });

  it('shows backend_provider_class in ModelDetailView hero and Identity panel', () => {
    const anthropic = makeHostedModel({
      variant_id: 'claude-opus-5-5',
      display_name: 'Claude Opus 5.5',
      served_model_tag: 'claude-opus-5-5',
      model_family: 'claude',
      backend_provider_class: 'anthropic',
    });
    evalStore.loadFixtures([anthropic, makeRunSummary({ model_role_mapping: { primary: 'claude-opus-5-5' } })], []);

    render(
      <MemoryRouter initialEntries={[`/models/${DATASET_ID}/claude-opus-5-5?role=primary`]}>
        <Routes>
          <Route path="/models/:datasetId/:variantId" element={<ModelDetailView />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByTestId('model-provider-class')).toHaveTextContent('anthropic (hosted)');
    const identity = within(screen.getByRole('region', { name: 'Identity' }));
    expect(identity.getByText('anthropic (hosted)')).toBeInTheDocument();
  });

  it('shows provider class per inference and renders hosted telemetry panel instead of source not captured', async () => {
    const user = userEvent.setup();
    const hostedModel = makeHostedModel();
    const hostedAssignment = makeAssignment({
      benchmark_observations: {
        timing: { generation_ms: { value: 800 } },
        unavailable_reasons: ['source_not_captured'],
      },
    });
    evalStore.loadFixtures([hostedModel, makeRunSummary(), hostedAssignment] as SnapshotRecord[], []);

    render(
      <MemoryRouter initialEntries={[`/evaluations/${DATASET_ID}/${RUN_ID}/assignments/${ASSIGNMENT_ID}`]}>
        <Routes>
          <Route path="/evaluations/:datasetId/:runId/assignments/:assignmentId" element={<AssignmentDetailView />} />
        </Routes>
      </MemoryRouter>,
    );

    // Assignment hero displays provider class
    expect(screen.getByTestId('assignment-provider-class')).toHaveTextContent('gemini (hosted)');

    // GPU / provider-boundary telemetry panel says "hosted: no provider-boundary telemetry", never "source not captured"
    const telemetryTile = screen.getByTestId('stat-provider-boundary-telemetry');
    expect(telemetryTile).toHaveTextContent(HOSTED_NO_TELEMETRY_LABEL);
    const hero = document.querySelector('.evidence-hero') as HTMLElement;
    expect(within(hero).queryByText(/source not captured/i)).not.toBeInTheDocument();
    expect(within(hero).queryByText(/source_not_captured/i)).not.toBeInTheDocument();
    expect(within(hero).getAllByText(HOSTED_NO_TELEMETRY_LABEL).length).toBeGreaterThanOrEqual(1);

    // Missing GPU telemetry is not shown as an evidence failure
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Evidence verification')).toHaveTextContent('Verified');

    // Model activity record shows provider class per inference
    const summary = screen.getByText(/gemini-3-6-flash · Primary · sage · gemini \(hosted\)/);
    expect(summary).toBeInTheDocument();
    await user.click(summary);
    expect(screen.getByText('Provider class')).toBeInTheDocument();
  });

  it('does not show hosted telemetry label for local assignments without GPU metrics', () => {
    const localModel = makeHostedModel({
      variant_id: 'qwen3-4b',
      display_name: 'Qwen 3 4B',
      served_model_tag: 'qwen3:4b',
      model_family: 'qwen3',
      model_format: 'gguf',
      backend_provider_class: 'ollama',
      parameter_count: 4_000_000_000,
    });
    const localAssignment = makeAssignment({
      variant_id: 'qwen3-4b',
      benchmark_observations: {
        timing: { generation_ms: { value: 800 } },
        unavailable_reasons: ['source_not_captured'],
      },
    });
    evalStore.loadFixtures([localModel, makeRunSummary({ model_role_mapping: { primary: 'qwen3-4b' } }), localAssignment] as SnapshotRecord[], []);

    render(
      <MemoryRouter initialEntries={[`/evaluations/${DATASET_ID}/${RUN_ID}/assignments/${ASSIGNMENT_ID}`]}>
        <Routes>
          <Route path="/evaluations/:datasetId/:runId/assignments/:assignmentId" element={<AssignmentDetailView />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByTestId('assignment-provider-class')).toHaveTextContent('ollama');
    expect(screen.queryByTestId('stat-provider-boundary-telemetry')).not.toBeInTheDocument();
    expect(screen.queryByText(HOSTED_NO_TELEMETRY_LABEL)).not.toBeInTheDocument();
    expect(screen.getByText('source_not_captured')).toBeInTheDocument();
  });

  it('shows provider class on JudgeActivity details and hosted-only run views', () => {
    const judgment: LiveJudgeActivity = {
      judge_variant_id: 'gemini-3-6-flash',
      served_model_tag: 'gemini-3.6-flash',
      model_digest: 'd'.repeat(64),
      subject: 'criterion',
      criterion_id: 'exact-reply',
      index: 1,
      count: 1,
      rubric_version: '1.0.0',
      state: 'met',
      rationale: 'Matched exact reply.',
      provider_attempt_id: 'attempt-judge-1',
      transaction_id: 'tx-judge-1',
      usage_reported: true,
      prompt_tokens: 120,
      completion_tokens: 24,
    };
    render(<JudgeActivity judgments={[judgment]} resolveProviderClass={() => 'gemini'} />);
    expect(screen.getByText('Provider class')).toBeInTheDocument();
    expect(screen.getByText('gemini (hosted)')).toBeInTheDocument();

    evalStore.loadFixtures(
      [
        makeHostedModel(),
        makeRunSummary({ benchmark_unavailable_reasons: ['source_not_captured'] }),
        makeAssignment(),
      ] as SnapshotRecord[],
      [],
    );
    render(
      <MemoryRouter initialEntries={[`/evaluations/${DATASET_ID}/${RUN_ID}`]}>
        <Routes>
          <Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText(HOSTED_NO_TELEMETRY_LABEL)).toBeInTheDocument();
    expect(screen.queryByText('source_not_captured')).not.toBeInTheDocument();
  });
});
