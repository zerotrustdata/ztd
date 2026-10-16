// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { createHash } from 'node:crypto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { AgentTraceSection } from '../src/components/AgentTraceSection';
import { verifyAgentTraceArtifact } from '../src/contract/agent-trace';
import type { ProofCatalogEntry, PublicEvidenceBinding } from '../src/contract/types';
import { evalStore } from '../src/state/store';

vi.mock('../src/state/feed', () => ({
  loadRuntimeConfig: vi.fn().mockResolvedValue({ schema_version: '1.0.0', mirror_origin: 'http://127.0.0.1:8082' }),
}));

const sha256 = (bytes: Uint8Array | string) => createHash('sha256').update(bytes).digest('hex');

const receipt = (transactionId: string, actionType: string) => ({
  transaction_id: transactionId,
  status: 'EXECUTION_STATUS_COMPLETED',
  executed_at_unix_ms: '1760000000000',
  signer_key_id: 'key-data',
  signature: 'c2ln',
  l2_status: 'L2_STATUS_NOT_REQUIRED',
  l3_status: 'L3_STATUS_NOT_REQUIRED',
  action_type: actionType,
  deterministic_stage_evidence: [{ stage_id: 'l1', kind: 'DETERMINISTIC_STAGE_KIND_L1_DOCTRINE', outcome: 'DETERMINISTIC_STAGE_OUTCOME_VERIFIED' }],
});

/** Everything after evidence_digest, in the Go AgentTrace field order. */
function traceTail(schemaVersion: string, overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    run_id: 'run-1',
    assignment_id: 'assignment-1',
    evaluation_attempt_id: 'attempt-1',
    campaign_id: 'campaign-1',
    scenario_id: 'scenario-1',
    designated_role: 'diagnostician',
    served_model_tag: 'qwen3:8b',
    model_digest: 'a'.repeat(64),
    model_registry_digest: 'b'.repeat(64),
    inference: { operator_id: 'operator-inference', session_id: 'session-inference', anchor: { runtime_id: 'operator-inference', key_id: 'key-inference', public_key_hex: 'ab', export_sha256: 'c'.repeat(64) } },
    data: { operator_id: 'operator-data', session_id: 'session-data' },
    workspace: { root: '/work', operator_working_directory: '/work/run-1' },
    ...(schemaVersion === 'agent-trace.v4'
      ? { input: { messages: [{ role: 'INFERENCE_MESSAGE_ROLE_USER', text: 'Why is checkout failing? 2>&1 <out>' }], tools: [{ name: 'fs_read', description: 'Read a file.', json_schema: '{"type":"object"}' }] } }
      : {}),
    model_calls: [{
      provider_attempt_id: 'attempt-a', transaction_id: 'tx-model', purpose: 'agent_turn', loop_turn: 1, tools_declared: ['fs_read'],
      result_digest: 'd'.repeat(64), usage_reported: true, prompt_tokens: 120, completion_tokens: 40, finish_reason: 'tool_calls',
      retry_count: 1, thinking: 'The log should say why.', text: 'Reading the log.', tool_call_count: 1,
    }],
    tool_calls: [{
      call_id: 'call-1', model_call_id: 'attempt-a', tool_name: 'fs_read', arguments_json: '{"path":"app.log"}', command: 'cat app.log',
      result_json: '{"content":"OOM"}', success: true, is_operator_tool: true, loop_turn: 1, action_type: 'fs_read',
      policy_decision: 'allow', receipt: receipt('tx-read', 'fs_read'),
    }],
    final_output: 'Checkout is out of memory.',
    tool_turn_limit_reached: false,
    bracket: { before: receipt('tx-before', 'fs_read'), after: receipt('tx-after', 'fs_read') },
    ...overrides,
  };
}

/** Seals a trace the way SealAgentTrace does: digest over the body with the digest value emptied. */
function sealedBody(schemaVersion = 'agent-trace.v4', overrides: Record<string, unknown> = {}): Uint8Array {
  const tail = JSON.stringify(traceTail(schemaVersion, overrides)).slice(1);
  const digest = sha256(`{"schema_version":"${schemaVersion}","evidence_digest":"",${tail}`);
  return new TextEncoder().encode(`{"schema_version":"${schemaVersion}","evidence_digest":"${digest}",${tail}`);
}

const scope = (body: Uint8Array) => ({ sha256: sha256(body), runId: 'run-1', assignmentId: 'assignment-1' });

describe('verifyAgentTraceArtifact', () => {
  it('verifies a sealed v4 trace and decodes it', async () => {
    const body = sealedBody();

    const trace = await verifyAgentTraceArtifact(body, scope(body));

    expect(trace.input?.messages[0]?.text).toBe('Why is checkout failing? 2>&1 <out>');
    expect(trace.model_calls[0]?.thinking).toBe('The log should say why.');
    expect(trace.tool_calls[0]?.receipt?.status).toBe('EXECUTION_STATUS_COMPLETED');
  });

  it('verifies a v3 trace without input and treats null lists as empty', async () => {
    const body = sealedBody('agent-trace.v3', { model_calls: null, tool_calls: null });

    const trace = await verifyAgentTraceArtifact(body, scope(body));

    expect(trace.input).toBeUndefined();
    expect(trace.model_calls).toEqual([]);
    expect(trace.tool_calls).toEqual([]);
  });

  it('rejects a body that does not match its binding', async () => {
    const body = sealedBody();

    await expect(verifyAgentTraceArtifact(body, { ...scope(body), sha256: 'e'.repeat(64) })).rejects.toThrow(/SHA-256 binding/u);
  });

  it('rejects an edited body whose binding was recomputed', async () => {
    const edited = new TextEncoder().encode(new TextDecoder().decode(sealedBody()).replace('out of memory', 'out of disk  '));

    await expect(verifyAgentTraceArtifact(edited, scope(edited))).rejects.toThrow(/evidence_digest does not match/u);
  });

  it('rejects a v4 trace without input and a v3 trace with input', async () => {
    const v4 = sealedBody('agent-trace.v4', { input: undefined });
    const v3 = sealedBody('agent-trace.v3', { input: { messages: [{ role: 'INFERENCE_MESSAGE_ROLE_USER', text: 'x' }], tools: [] } });

    await expect(verifyAgentTraceArtifact(v4, scope(v4))).rejects.toMatchObject({ path: 'agent_trace.input' });
    await expect(verifyAgentTraceArtifact(v3, scope(v3))).rejects.toThrow(/never carries input/u);
  });

  it('rejects a trace for another assignment and unknown fields', async () => {
    const other = sealedBody('agent-trace.v4', { assignment_id: 'assignment-2' });
    const unknown = sealedBody('agent-trace.v4', { grade_detail: 'x' });

    await expect(verifyAgentTraceArtifact(other, scope(other))).rejects.toThrow(/different assignment/u);
    await expect(verifyAgentTraceArtifact(unknown, scope(unknown))).rejects.toThrow(/grade_detail/u);
  });
});

describe('AgentTraceSection', () => {
  const body = sealedBody();
  const bindings: PublicEvidenceBinding[] = [
    { kind: 'assignment_agent_trace', sha256: sha256(body), schema_ref: 'ztd.eval.v1.PublicAssignmentAgentTrace' },
  ];
  const entry = (sha: string): ProofCatalogEntry => ({
    artifact_id: sha,
    filename: 'assignment-assignment-1.agent-trace.json',
    media_type: 'application/json',
    byte_size: body.byteLength,
    sha256: sha,
    classification: 'public_safe',
    campaign_id: 'campaign-1',
    generated_at: '2026-10-15T00:00:00Z',
    verification_command: 'ztd public verify-agent-trace --trace assignment-assignment-1.agent-trace.json',
    immutable_url: `/proofs/${sha}`,
  });

  beforeEach(() => {
    evalStore.loadFixtures([], []);
    evalStore.loadProofCatalog([entry(sha256(body))]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('renders the verified trace: input, thinking, tool call, receipt, and download', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(body.slice()));
    vi.stubGlobal('fetch', fetchMock);

    render(<AgentTraceSection id="trace" bindings={bindings} runId="run-1" assignmentId="assignment-1" />);

    expect(await screen.findByText(/Why is checkout failing\?/u)).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(`http://127.0.0.1:8082/proofs/${sha256(body)}`);
    expect(screen.getByText('The log should say why.')).toBeInTheDocument();
    expect(screen.getByText('cat app.log')).toBeInTheDocument();
    expect(screen.getAllByText('Completed').length).toBeGreaterThan(0);
    expect(screen.getByRole('link', { name: 'Download trace' })).toHaveAttribute('download', 'assignment-assignment-1.agent-trace.json');
  });

  it('shows nothing from a body that fails verification', async () => {
    const tampered = new TextEncoder().encode(new TextDecoder().decode(body).replace('out of memory', 'out of disk  '));
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(tampered)));

    render(<AgentTraceSection id="trace" bindings={bindings} runId="run-1" assignmentId="assignment-1" />);

    expect(await screen.findByRole('alert')).toHaveTextContent(/verification failed \(agent_trace: body does not match its published SHA-256 binding\)/u);
    expect(screen.queryByText(/Why is checkout failing\?/u)).not.toBeInTheDocument();
  });

  it('is absent without a catalog entry', () => {
    evalStore.loadProofCatalog([]);
    const { container } = render(<AgentTraceSection id="trace" bindings={bindings} runId="run-1" assignmentId="assignment-1" />);
    expect(container).toBeEmptyDOMElement();
  });
});
