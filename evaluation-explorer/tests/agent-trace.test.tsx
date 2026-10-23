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
  fetchProofCatalogEntry: vi.fn().mockResolvedValue(null),
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

/** The previous repetition's investigation a v5 trace continued. */
const heldContext = {
  assignment_id: 'assignment-0',
  repetition: 1,
  evidence_digest: 'f'.repeat(64),
  workspace: { root: '/work', operator_working_directory: '/work/run-1' },
  messages: [
    { role: 'INFERENCE_MESSAGE_ROLE_SYSTEM', text: 'You are an operations agent.' },
    { role: 'INFERENCE_MESSAGE_ROLE_USER', text: 'Why is checkout failing? 2>&1 <out>' },
    { role: 'INFERENCE_MESSAGE_ROLE_ASSISTANT', tool_calls: [{ call_id: 'call-0', name: 'fs_read', arguments_json: '{"path":"held.log"}' }] },
    { role: 'INFERENCE_MESSAGE_ROLE_TOOL', tool_result: { call_id: 'call-0', name: 'fs_read', result_json: '{"content":"held OOM"}' } },
    { role: 'INFERENCE_MESSAGE_ROLE_ASSISTANT', text: 'Earlier answer: out of memory.' },
  ],
};

/** Everything after evidence_digest, in the Go AgentTrace field order. */
function traceTail(schemaVersion: string, overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    run_id: 'run-1',
    assignment_id: 'assignment-1',
    evaluation_attempt_id: 'attempt-1',
    campaign_id: 'campaign-1',
    scenario_id: 'scenario-1',
    ...(schemaVersion === 'agent-trace.v6' ? { investigation_id: 'investigation-1' } : {}),
    designated_role: 'diagnostician',
    served_model_tag: 'qwen3:8b',
    model_digest: 'a'.repeat(64),
    model_registry_digest: 'b'.repeat(64),
    inference: { operator_id: 'operator-inference', session_id: 'session-inference', anchor: { runtime_id: 'operator-inference', key_id: 'key-inference', public_key_hex: 'ab', export_sha256: 'c'.repeat(64) } },
    data: { operator_id: 'operator-data', session_id: 'session-data' },
    workspace: { root: '/work', operator_working_directory: '/work/run-1' },
    ...(schemaVersion === 'agent-trace.v5' || schemaVersion === 'agent-trace.v6' ? { held_context: heldContext } : {}),
    ...(schemaVersion !== 'agent-trace.v3'
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

function investigationRetrievalToolCall(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    call_id: 'call-context', model_call_id: 'attempt-a', tool_name: 'query_investigation_context',
    arguments_json: '{"data_type":"history_trail","limit":10}', success: true, is_operator_tool: true,
    loop_turn: 2, action_type: 'MCP_CALL', receipt: receipt('tx-context', 'MCP_CALL'),
    retrieval: {
      query: { operator_session_id: '', investigation_id: 'investigation-1', exclude_audit_reads: true, limit: 10 },
      receipts: [{ receipt_id: 'receipt-1', sha256: 'e'.repeat(64) }],
    },
    ...overrides,
  };
}

describe('verifyAgentTraceArtifact', () => {
  const v7Call = () => ({
    ...(traceTail('agent-trace.v6').model_calls as Record<string, unknown>[])[0],
    model_variant_id: 'variant-primary', model_role: 'MODEL_ROLE_PRIMARY', served_model_tag: 'qwen3:8b', model_digest: 'a'.repeat(64),
    inference_result: { requested_model: 'qwen3:8b', served_model_digest: 'a'.repeat(64) },
  });

  it('decodes v7 signed model identities and Tribunal and episode evidence', async () => {
    const body = sealedBody('agent-trace.v7', {
      investigation_id: 'investigation-1',
      model_calls: [v7Call(), { ...v7Call(), purpose: 'tribunal_generate', tool_call_id: 'call-1', persona: 'axiom', model_role: 'MODEL_ROLE_ASSISTANT' }],
      tool_calls: [investigationRetrievalToolCall()],
      terminal_classification: 'final_diagnosis',
      clarifications: [{ loop_turn: 1, questions: ['Is checkout failing?'], selected_branch: 'yes', user_response: 'Yes' }],
      steps: [{ step_index: 0, role: 'AGENT_ROLE_CANDIDATE_GENERATOR', generator_output: { command: 'pwd' } }],
    });
    const trace = await verifyAgentTraceArtifact(body, scope(body));
    expect(trace.model_calls[1]?.model_role).toBe('MODEL_ROLE_ASSISTANT');
    expect(trace.model_calls[1]?.persona).toBe('axiom');
    expect(trace.clarifications?.[0]?.selected_branch).toBe('yes');
    expect(trace.steps).toHaveLength(1);
  });

  it('rejects v7 successful calls without their signed result or model identity', async () => {
    for (const missing of ['inference_result', 'model_variant_id', 'model_role', 'served_model_tag', 'model_digest']) {
      const call: Record<string, unknown> = v7Call();
      delete call[missing];
      const body = sealedBody('agent-trace.v7', { model_calls: [call] });
      await expect(verifyAgentTraceArtifact(body, scope(body))).rejects.toThrow();
    }
  });

  const toolCallWith = (fields: Record<string, unknown>) => ({ ...(traceTail('agent-trace.v8').tool_calls as Record<string, unknown>[])[0], ...fields });

  it('decodes approved_arguments_json on a v8 tool call and rejects it when empty', async () => {
    const approved = '{"args":["app/service.conf"],"command":"cat"}';
    const body = sealedBody('agent-trace.v8', { model_calls: [v7Call()], tool_calls: [toolCallWith({ approved_arguments_json: approved })] });
    const empty = sealedBody('agent-trace.v8', { model_calls: [v7Call()], tool_calls: [toolCallWith({ approved_arguments_json: '' })] });

    const trace = await verifyAgentTraceArtifact(body, scope(body));

    expect(trace.tool_calls[0]?.approved_arguments_json).toBe(approved);
    await expect(verifyAgentTraceArtifact(empty, scope(empty))).rejects.toMatchObject({ path: 'agent_trace.tool_calls[0].approved_arguments_json' });
  });

  it('rejects approved_arguments_json on v3 through v7 tool calls', async () => {
    for (const schemaVersion of ['agent-trace.v3', 'agent-trace.v4', 'agent-trace.v5', 'agent-trace.v6', 'agent-trace.v7']) {
      const body = sealedBody(schemaVersion, {
        ...(schemaVersion === 'agent-trace.v7' ? { model_calls: [v7Call()] } : {}),
        tool_calls: [toolCallWith({ approved_arguments_json: '{"args":[],"command":"cat"}' })],
      });
      await expect(verifyAgentTraceArtifact(body, scope(body))).rejects.toThrow(/approved_arguments_json/u);
    }
  });

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

  it('verifies a v5 trace that continued a held investigation', async () => {
    const body = sealedBody('agent-trace.v5');

    const trace = await verifyAgentTraceArtifact(body, scope(body));

    expect(trace.held_context?.repetition).toBe(1);
    expect(trace.held_context?.messages[2]?.tool_calls?.[0]?.arguments_json).toBe('{"path":"held.log"}');
    expect(trace.held_context?.messages[3]?.tool_result?.result_json).toBe('{"content":"held OOM"}');
  });

  it('verifies a v6 trace with investigation identity and authoritative receipt retrieval', async () => {
    const body = sealedBody('agent-trace.v6', { tool_calls: [investigationRetrievalToolCall()] });

    const trace = await verifyAgentTraceArtifact(body, scope(body));

    expect(trace.investigation_id).toBe('investigation-1');
    expect(trace.held_context?.repetition).toBe(1);
    expect(trace.tool_calls[0]?.retrieval?.query.investigation_id).toBe('investigation-1');
    expect(trace.tool_calls[0]?.retrieval?.receipts[0]?.sha256).toBe('e'.repeat(64));
  });

  it('rejects malformed v6 retrieval and completed context calls without retrieval', async () => {
    const malformedDigest = sealedBody('agent-trace.v6', {
      tool_calls: [investigationRetrievalToolCall({ retrieval: {
        query: { investigation_id: 'investigation-1', exclude_audit_reads: true, limit: 10 },
        receipts: [{ receipt_id: 'receipt-1', sha256: 'BAD' }],
      } })],
    });
    const wrongInvestigation = sealedBody('agent-trace.v6', {
      tool_calls: [investigationRetrievalToolCall({ retrieval: {
        query: { investigation_id: 'another-investigation', exclude_audit_reads: true, limit: 10 },
        receipts: [],
      } })],
    });
    const completedWithoutRetrieval = sealedBody('agent-trace.v6', {
      tool_calls: [investigationRetrievalToolCall({ success: false, retrieval: undefined })],
    });
    const successfulWithoutRetrieval = sealedBody('agent-trace.v6', {
      tool_calls: [investigationRetrievalToolCall({ success: true, receipt: { status: 'EXECUTION_STATUS_FAILED' }, retrieval: undefined })],
    });
    const statusWithoutRetrieval = sealedBody('agent-trace.v6', {
      tool_calls: [investigationRetrievalToolCall({
        arguments_json: '{"data_type":"investigation_status"}', action_type: undefined, receipt: undefined, retrieval: undefined,
      })],
    });

    await expect(verifyAgentTraceArtifact(malformedDigest, scope(malformedDigest))).rejects.toMatchObject({ path: 'agent_trace.tool_calls[0].retrieval.receipts[0].sha256' });
    await expect(verifyAgentTraceArtifact(wrongInvestigation, scope(wrongInvestigation))).rejects.toThrow(/must match trace investigation_id/u);
    await expect(verifyAgentTraceArtifact(completedWithoutRetrieval, scope(completedWithoutRetrieval))).rejects.toThrow(/completed context query must carry retrieval evidence/u);
    await expect(verifyAgentTraceArtifact(successfulWithoutRetrieval, scope(successfulWithoutRetrieval))).rejects.toThrow(/successful non-status context query must carry retrieval evidence/u);
    await expect(verifyAgentTraceArtifact(statusWithoutRetrieval, scope(statusWithoutRetrieval))).resolves.toMatchObject({ tool_calls: [{ retrieval: undefined }] });
  });

  it('keeps v5 closed to v6 investigation and retrieval fields', async () => {
    const investigation = sealedBody('agent-trace.v5', { investigation_id: 'investigation-1' });
    const retrieval = sealedBody('agent-trace.v5', { tool_calls: [investigationRetrievalToolCall()] });

    await expect(verifyAgentTraceArtifact(investigation, scope(investigation))).rejects.toThrow(/never carries investigation ID/u);
    await expect(verifyAgentTraceArtifact(retrieval, scope(retrieval))).rejects.toThrow(/retrieval/u);
  });

  it('rejects held context outside v5 and unknown held fields', async () => {
    const v4 = sealedBody('agent-trace.v4', { held_context: heldContext });
    const unknown = sealedBody('agent-trace.v5', { held_context: { ...heldContext, summary: 'x' } });
    const empty = sealedBody('agent-trace.v5', { held_context: { ...heldContext, messages: [] } });

    await expect(verifyAgentTraceArtifact(v4, scope(v4))).rejects.toThrow(/never carries held context/u);
    await expect(verifyAgentTraceArtifact(unknown, scope(unknown))).rejects.toThrow(/summary/u);
    await expect(verifyAgentTraceArtifact(empty, scope(empty))).rejects.toMatchObject({ path: 'agent_trace.held_context.messages' });
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
    evalStore.setMirrorOrigin('http://127.0.0.1:8082');
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

  it('renders the held investigation ahead of the input', async () => {
    const held = sealedBody('agent-trace.v5');
    evalStore.loadProofCatalog([entry(sha256(held))]);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(held.slice())));
    const heldBindings: PublicEvidenceBinding[] = [{ ...bindings[0]!, sha256: sha256(held) }];

    render(<AgentTraceSection id="trace" bindings={heldBindings} runId="run-1" assignmentId="assignment-1" />);

    expect(await screen.findByText('Held investigation · repetition 1')).toBeInTheDocument();
    expect(screen.getByText('Earlier answer: out of memory.')).toBeInTheDocument();
    expect(screen.getByText('assignment-0')).toBeInTheDocument();
  });

  it('shows nothing from a body that fails verification', async () => {
    const tampered = new TextEncoder().encode(new TextDecoder().decode(body).replace('out of memory', 'out of disk  '));
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(tampered)));

    render(<AgentTraceSection id="trace" bindings={bindings} runId="run-1" assignmentId="assignment-1" />);

    expect(await screen.findByRole('alert')).toHaveTextContent(/verification failed \(agent_trace: body does not match its published SHA-256 binding\)/u);
    expect(screen.queryByText(/Why is checkout failing\?/u)).not.toBeInTheDocument();
  });

  it('shows approved arguments only when they differ from the proposed arguments', async () => {
    const toolCall = (approved?: string) => ({
      ...(traceTail('agent-trace.v8').tool_calls as Record<string, unknown>[])[0],
      ...(approved === undefined ? {} : { approved_arguments_json: approved }),
    });
    const renderTrace = (tool: Record<string, unknown>) => {
      const modelCall = {
        ...(traceTail('agent-trace.v8').model_calls as Record<string, unknown>[])[0],
        model_variant_id: 'variant-primary', model_role: 'MODEL_ROLE_PRIMARY', served_model_tag: 'qwen3:8b', model_digest: 'a'.repeat(64),
        inference_result: { requested_model: 'qwen3:8b', served_model_digest: 'a'.repeat(64) },
      };
      const sealed = sealedBody('agent-trace.v8', { model_calls: [modelCall], tool_calls: [tool] });
      evalStore.loadProofCatalog([entry(sha256(sealed))]);
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(sealed.slice())));
      return render(<AgentTraceSection id="trace" bindings={[{ ...bindings[0]!, sha256: sha256(sealed) }]} runId="run-1" assignmentId="assignment-1" />);
    };

    const differing = renderTrace(toolCall('{"path":"app.log","tail":50}'));
    expect(await screen.findByText(/Why is checkout failing\?/u)).toBeInTheDocument();
    expect(screen.getByText('Approved arguments')).toBeInTheDocument();
    differing.unmount();

    for (const tool of [toolCall('{"path":"app.log"}'), toolCall()]) {
      const rendered = renderTrace(tool);
      expect(await screen.findByText(/Why is checkout failing\?/u)).toBeInTheDocument();
      expect(screen.queryByText('Approved arguments')).not.toBeInTheDocument();
      rendered.unmount();
    }
  });

  it('is absent without a catalog entry', () => {
    evalStore.loadProofCatalog([]);
    const { container } = render(<AgentTraceSection id="trace" bindings={bindings} runId="run-1" assignmentId="assignment-1" />);
    expect(container).toBeEmptyDOMElement();
  });
});
