// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The campaign-frozen scenario fixture published as `scenario_input_fixture`
// and `scenario_gold_criteria` proof artifacts: the Go `ScenarioInputFixture`
// and `ScenarioGoldCriteria` (internal/services/evaluation/scenario_fixture.go)
// canonical bytes, exactly as the campaign froze them. The browser checks each
// body against its binding and the assignment's scenario before rendering.
// Nested seed and gold criteria stay decoded objects: they render verbatim.

import { sha256Hex } from './agent-trace';
import { ValidationError, assert, assertObject, assertString } from './validators';

export interface ScenarioInlineContent {
  kind: string;
  label: string;
  content: string;
}

export interface ScenarioWorkspaceFile {
  label: string;
  rel_path: string;
  content: string;
  decoy: boolean;
}

export interface ScenarioInputFixture {
  schema_version: string;
  scenario_id: string;
  synthetic_label: string;
  user_prompt: string;
  inline_context: ScenarioInlineContent[];
  workspace_files: ScenarioWorkspaceFile[];
  seed: Readonly<Record<string, unknown>>;
}

export interface ScenarioGoldCriteria {
  schema_version: string;
  scenario_id: string;
  expected_behavior: string;
  /** The full decoded gold body, rendered verbatim. */
  body: Readonly<Record<string, unknown>>;
}

export interface ScenarioFixtureBinding {
  sha256: string;
  scenarioId: string;
}

function decode(body: Uint8Array, path: string): Record<string, unknown> {
  let decoded: unknown;
  try {
    decoded = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(body));
  } catch {
    throw new ValidationError('body is not UTF-8 JSON', path);
  }
  assertObject(decoded, path);
  return decoded;
}

async function verifyBody(body: Uint8Array, binding: ScenarioFixtureBinding, path: string): Promise<Record<string, unknown>> {
  assert((await sha256Hex(body)) === binding.sha256, path, 'body does not match its published SHA-256 binding');
  const value = decode(body, path);
  assertString(value.schema_version, `${path}.schema_version`);
  assertString(value.scenario_id, `${path}.scenario_id`);
  assert(value.scenario_id === binding.scenarioId, `${path}.scenario_id`, 'fixture belongs to a different scenario');
  return value;
}

function optionalArray(value: unknown, path: string): unknown[] {
  if (value === undefined) return [];
  assert(Array.isArray(value), path, 'must be an array');
  return value;
}

function parseInlineContent(value: unknown, path: string): ScenarioInlineContent {
  assertObject(value, path);
  assertString(value.kind, `${path}.kind`);
  assertString(value.label, `${path}.label`);
  assertString(value.content, `${path}.content`);
  return { kind: value.kind, label: value.label, content: value.content };
}

function parseWorkspaceFile(value: unknown, path: string): ScenarioWorkspaceFile {
  assertObject(value, path);
  assertString(value.label, `${path}.label`);
  assertString(value.rel_path, `${path}.rel_path`);
  assertString(value.content, `${path}.content`);
  assert(value.decoy === undefined || typeof value.decoy === 'boolean', `${path}.decoy`, 'must be a boolean');
  return { label: value.label, rel_path: value.rel_path, content: value.content, decoy: value.decoy === true };
}

export async function verifyScenarioInputArtifact(body: Uint8Array, binding: ScenarioFixtureBinding): Promise<ScenarioInputFixture> {
  const path = 'scenario_input_fixture';
  const value = await verifyBody(body, binding, path);
  assertString(value.synthetic_label, `${path}.synthetic_label`);
  assertString(value.user_prompt, `${path}.user_prompt`);
  assertObject(value.seed, `${path}.seed`);
  return {
    schema_version: value.schema_version as string,
    scenario_id: value.scenario_id as string,
    synthetic_label: value.synthetic_label,
    user_prompt: value.user_prompt,
    inline_context: optionalArray(value.inline_context, `${path}.inline_context`).map((entry, index) =>
      parseInlineContent(entry, `${path}.inline_context[${index}]`),
    ),
    workspace_files: optionalArray(value.workspace_files, `${path}.workspace_files`).map((entry, index) =>
      parseWorkspaceFile(entry, `${path}.workspace_files[${index}]`),
    ),
    seed: value.seed,
  };
}

export async function verifyScenarioGoldArtifact(body: Uint8Array, binding: ScenarioFixtureBinding): Promise<ScenarioGoldCriteria> {
  const path = 'scenario_gold_criteria';
  const value = await verifyBody(body, binding, path);
  assertString(value.expected_behavior, `${path}.expected_behavior`);
  return {
    schema_version: value.schema_version as string,
    scenario_id: value.scenario_id as string,
    expected_behavior: value.expected_behavior,
    body: value,
  };
}
