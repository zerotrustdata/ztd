// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { createHash } from 'node:crypto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ScenarioFixtureSection } from '../src/components/ScenarioFixtureSection';
import { verifyScenarioGoldArtifact, verifyScenarioInputArtifact } from '../src/contract/scenario-fixture';
import type { ProofCatalogEntry, PublicEvidenceBinding } from '../src/contract/types';
import { evalStore } from '../src/state/store';

vi.mock('../src/state/feed', () => ({
  loadRuntimeConfig: vi.fn().mockResolvedValue({ schema_version: '1.0.0', mirror_origin: 'http://127.0.0.1:8082' }),
}));

const sha256 = (bytes: Uint8Array | string) => createHash('sha256').update(bytes).digest('hex');
const encode = (value: unknown) => new TextEncoder().encode(JSON.stringify(value));

const inputFixture = (overrides: Record<string, unknown> = {}) => ({
  schema_version: 'scenario-input.v1',
  scenario_id: 'scenario-1',
  synthetic_label: 'synthetic',
  user_prompt: 'Why is checkout failing? 2>&1 <out>',
  inline_context: [{ kind: 'log', label: 'app.log', content: 'OOM killed' }],
  workspace_files: [
    { label: 'service log', rel_path: 'logs/app.log', content: 'heap exhausted' },
    { label: 'stale log', rel_path: 'logs/old.log', content: 'disk full', decoy: true },
  ],
  seed: { case_title: 'Checkout outage' },
  ...overrides,
});

const goldFixture = (overrides: Record<string, unknown> = {}) => ({
  schema_version: 'scenario-gold.v1',
  scenario_id: 'scenario-1',
  expected_behavior: 'Identify the out-of-memory kill.',
  role_criteria: [],
  pipeline_criteria: [],
  ...overrides,
});

const scope = (body: Uint8Array) => ({ sha256: sha256(body), scenarioId: 'scenario-1' });

describe('verifyScenarioInputArtifact', () => {
  it('verifies and decodes the frozen input', async () => {
    const body = encode(inputFixture());

    const input = await verifyScenarioInputArtifact(body, scope(body));

    expect(input.user_prompt).toBe('Why is checkout failing? 2>&1 <out>');
    expect(input.workspace_files.map((file) => file.decoy)).toEqual([false, true]);
    expect(input.seed.case_title).toBe('Checkout outage');
  });

  it('rejects a body that does not match its binding or belongs to another scenario', async () => {
    const body = encode(inputFixture());
    const other = encode(inputFixture({ scenario_id: 'scenario-2' }));

    await expect(verifyScenarioInputArtifact(body, { ...scope(body), sha256: 'e'.repeat(64) })).rejects.toThrow(/SHA-256 binding/u);
    await expect(verifyScenarioInputArtifact(other, scope(other))).rejects.toThrow(/different scenario/u);
  });
});

describe('verifyScenarioGoldArtifact', () => {
  it('verifies the gold criteria and keeps the full body', async () => {
    const body = encode(goldFixture());

    const gold = await verifyScenarioGoldArtifact(body, scope(body));

    expect(gold.expected_behavior).toBe('Identify the out-of-memory kill.');
    expect(gold.body.role_criteria).toEqual([]);
  });

  it('rejects gold criteria without expected behavior', async () => {
    const body = encode(goldFixture({ expected_behavior: undefined }));

    await expect(verifyScenarioGoldArtifact(body, scope(body))).rejects.toMatchObject({ path: 'scenario_gold_criteria.expected_behavior' });
  });
});

describe('ScenarioFixtureSection', () => {
  const inputBody = encode(inputFixture());
  const goldBody = encode(goldFixture());
  const bindings: PublicEvidenceBinding[] = [
    { kind: 'scenario_input_fixture', sha256: sha256(inputBody), schema_ref: 'ztd.eval.v1.PublicScenarioInputFixture' },
    { kind: 'scenario_gold_criteria', sha256: sha256(goldBody), schema_ref: 'ztd.eval.v1.PublicScenarioGoldCriteria' },
  ];
  const entry = (body: Uint8Array, filename: string): ProofCatalogEntry => ({
    artifact_id: sha256(body),
    filename,
    media_type: 'application/json',
    byte_size: body.byteLength,
    sha256: sha256(body),
    classification: 'public_safe',
    campaign_id: 'campaign-1',
    generated_at: '2026-10-15T00:00:00Z',
    verification_command: `sha256sum ${filename}`,
    immutable_url: `/proofs/${sha256(body)}`,
  });
  const serve = (input: Uint8Array, gold: Uint8Array) =>
    vi.fn((url: string) => Promise.resolve(new Response((url.endsWith(sha256(inputBody)) ? input : gold).slice())));

  beforeEach(() => {
    evalStore.loadFixtures([], []);
    evalStore.loadProofCatalog([entry(inputBody, 'scenario-scenario-1.input.json'), entry(goldBody, 'scenario-scenario-1.gold.json')]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('renders the verified prompt, workspace files, gold criteria, and downloads', async () => {
    vi.stubGlobal('fetch', serve(inputBody, goldBody));

    render(<ScenarioFixtureSection id="fixture" bindings={bindings} scenarioId="scenario-1" />);

    expect(await screen.findByText('Why is checkout failing? 2>&1 <out>')).toBeInTheDocument();
    expect(await screen.findByText('Identify the out-of-memory kill.')).toBeInTheDocument();
    expect(screen.getByText('logs/old.log · decoy')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Download input' })).toHaveAttribute('download', 'scenario-scenario-1.input.json');
    expect(screen.getByRole('link', { name: 'Download gold criteria' })).toHaveAttribute('download', 'scenario-scenario-1.gold.json');
  });

  it('shows nothing from a body that fails verification', async () => {
    const tampered = encode(inputFixture({ user_prompt: 'Something else' }));
    vi.stubGlobal('fetch', serve(tampered, goldBody));

    render(<ScenarioFixtureSection id="fixture" bindings={bindings} scenarioId="scenario-1" />);

    expect(await screen.findByRole('alert')).toHaveTextContent(/Input not shown: verification failed \(scenario_input_fixture: body does not match/u);
    expect(screen.queryByText('Something else')).not.toBeInTheDocument();
  });

  it('is absent without both bindings or a scenario identity', () => {
    const { container: partial } = render(<ScenarioFixtureSection id="fixture" bindings={[bindings[0]!]} scenarioId="scenario-1" />);
    expect(partial).toBeEmptyDOMElement();
    const { container: unscoped } = render(<ScenarioFixtureSection id="fixture" bindings={bindings} scenarioId={undefined} />);
    expect(unscoped).toBeEmptyDOMElement();
  });
});
