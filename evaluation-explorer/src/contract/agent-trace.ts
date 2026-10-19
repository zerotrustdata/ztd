// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The sealed agent trace published as an `assignment_agent_trace` proof
// artifact: the Go `AgentTrace` (internal/services/evaluation/
// campaign_agent_trace.go) byte for byte. The browser checks the body against
// its binding and recomputes evidence_digest before rendering any of it.
// Receipts and anchors are canonical protojson (proto field names) kept as
// decoded objects; their signatures verify offline with
// `ztd public verify-agent-trace`.

import { ValidationError, assert, assertBoolean, assertInteger, assertObject, assertString, rejectUnknown } from './validators';

export const AGENT_TRACE_SCHEMA_VERSIONS = ['agent-trace.v3', 'agent-trace.v4', 'agent-trace.v5', 'agent-trace.v6', 'agent-trace.v7'] as const;
export type AgentTraceSchemaVersion = (typeof AGENT_TRACE_SCHEMA_VERSIONS)[number];

/** One canonical protojson message (ActionReceipt or ConformanceAnchor). */
export type AgentTraceProto = Readonly<Record<string, unknown>>;

export interface AgentTraceMessage {
  role: string;
  text: string;
}

export interface AgentTraceTool {
  name: string;
  description: string;
  json_schema: string;
}

export interface AgentTraceInput {
  messages: AgentTraceMessage[];
  tools: AgentTraceTool[];
}

/** One message of a held conversation; tool messages carry one tool_result. */
export interface AgentTraceContextMessage {
  role: string;
  text?: string;
  tool_calls?: { call_id?: string; name: string; arguments_json: string }[];
  tool_result?: { call_id?: string; name: string; result_json: string };
}

/**
 * The previous repetition's complete conversation, which a held-context
 * repetition (agent-trace.v5) continued in the same workspace.
 */
export interface AgentTraceHeldContext {
  assignment_id: string;
  repetition: number;
  evidence_digest: string;
  workspace: { root: string; operator_working_directory: string };
  messages: AgentTraceContextMessage[];
}

export interface AgentTraceRuntime {
  operator_id: string;
  session_id: string;
  anchor?: AgentTraceProto;
}

export interface AgentTraceModelCall {
  provider_attempt_id: string;
  transaction_id: string;
  purpose: string;
  loop_turn: number;
  tool_call_id?: string;
  persona?: string;
  model_variant_id?: string;
  model_role?: string;
  served_model_tag?: string;
  model_digest?: string;
  inference_result?: AgentTraceProto;
  tools_declared: string[];
  result_digest: string;
  usage_reported: boolean;
  prompt_tokens: number;
  completion_tokens: number;
  ttft_nanos?: number;
  generation_duration_nanos?: number;
  finish_reason?: string;
  load_state?: string;
  retry_count: number;
  thinking?: string;
  text?: string;
  tool_call_count: number;
  error?: string;
  receipt?: AgentTraceProto;
}

export interface AgentTraceToolCall {
  call_id: string;
  model_call_id: string;
  provider_call_id?: string;
  tool_name: string;
  arguments_json: string;
  command?: string;
  result_json?: string;
  error_type?: string;
  error?: string;
  success: boolean;
  is_operator_tool: boolean;
  loop_turn: number;
  action_type?: string;
  transaction_id?: string;
  policy_decision?: string;
  receipt?: AgentTraceProto;
  retrieval?: AgentTraceRetrieval;
}

/** The typed query that the local Operator answered for a v6 context call. */
export interface AgentTraceRetrievalQuery {
  operator_session_id?: string;
  investigation_id?: string;
  action_type?: string;
  exclude_audit_reads?: boolean;
  not_before?: string;
  limit?: number;
  offset?: number;
}

export interface AgentTraceRetrievalReceipt {
  receipt_id: string;
  sha256: string;
}

export interface AgentTraceRetrieval {
  query: AgentTraceRetrievalQuery;
  receipts: AgentTraceRetrievalReceipt[];
}

export interface AgentTrace {
  schema_version: AgentTraceSchemaVersion;
  evidence_digest: string;
  run_id: string;
  assignment_id: string;
  evaluation_attempt_id: string;
  campaign_id: string;
  scenario_id: string;
  designated_role: string;
  served_model_tag: string;
  model_digest: string;
  model_registry_digest: string;
  investigation_id?: string;
  inference: AgentTraceRuntime;
  data: AgentTraceRuntime;
  workspace: { root: string; operator_working_directory: string };
  held_context?: AgentTraceHeldContext;
  input?: AgentTraceInput;
  model_calls: AgentTraceModelCall[];
  tool_calls: AgentTraceToolCall[];
  final_output: string;
  terminal_classification?: string;
  clarifications?: { loop_turn: number; questions: string[]; selected_branch?: string; user_response?: string }[];
  steps?: AgentTraceProto[];
  tool_turn_limit_reached: boolean;
  provider_failure?: string;
  provider_failure_class?: string;
  bracket?: { before: AgentTraceProto; after: AgentTraceProto };
}

const SHA256 = /^[0-9a-f]{64}$/u;

function optionalString(value: Record<string, unknown>, key: string, path: string): void {
  if (value[key] !== undefined) assertString(value[key], `${path}.${key}`);
}

function optionalInteger(value: Record<string, unknown>, key: string, path: string): void {
  if (value[key] !== undefined) assertInteger(value[key], `${path}.${key}`);
}

function requiredStrings(value: Record<string, unknown>, keys: readonly string[], path: string): void {
  for (const key of keys) assertString(value[key], `${path}.${key}`);
}

/** Go encodes an empty slice as null; both mean no entries. */
function list(value: unknown, path: string): unknown[] {
  if (value === null) return [];
  assert(Array.isArray(value), path, 'expected array');
  return value;
}

function proto(value: unknown, path: string): AgentTraceProto {
  assertObject(value, path);
  return value;
}

function optionalProto(value: Record<string, unknown>, key: string, path: string): void {
  if (value[key] !== undefined) proto(value[key], `${path}.${key}`);
}

function parseRuntime(value: unknown, path: string): AgentTraceRuntime {
  assertObject(value, path);
  rejectUnknown(value, ['operator_id', 'session_id', 'anchor'], path);
  requiredStrings(value, ['operator_id', 'session_id'], path);
  optionalProto(value, 'anchor', path);
  return value as unknown as AgentTraceRuntime;
}

function parseInput(value: unknown, path: string): AgentTraceInput {
  assertObject(value, path);
  rejectUnknown(value, ['messages', 'tools'], path);
  const messages = list(value.messages, `${path}.messages`);
  messages.forEach((message, index) => {
    const messagePath = `${path}.messages[${index}]`;
    assertObject(message, messagePath);
    rejectUnknown(message, ['role', 'text'], messagePath);
    requiredStrings(message, ['role', 'text'], messagePath);
  });
  const tools = list(value.tools, `${path}.tools`);
  tools.forEach((tool, index) => {
    const toolPath = `${path}.tools[${index}]`;
    assertObject(tool, toolPath);
    rejectUnknown(tool, ['name', 'description', 'json_schema'], toolPath);
    requiredStrings(tool, ['name', 'description', 'json_schema'], toolPath);
  });
  return { messages: messages as AgentTraceMessage[], tools: tools as AgentTraceTool[] };
}

function parseWorkspace(value: unknown, path: string): AgentTrace['workspace'] {
  assertObject(value, path);
  rejectUnknown(value, ['root', 'operator_working_directory'], path);
  requiredStrings(value, ['root', 'operator_working_directory'], path);
  return value as unknown as AgentTrace['workspace'];
}

function parseContextMessage(value: unknown, path: string): AgentTraceContextMessage {
  assertObject(value, path);
  rejectUnknown(value, ['role', 'text', 'tool_calls', 'tool_result'], path);
  assertString(value.role, `${path}.role`);
  optionalString(value, 'text', path);
  let toolCalls: AgentTraceContextMessage['tool_calls'];
  if (value.tool_calls !== undefined) {
    toolCalls = list(value.tool_calls, `${path}.tool_calls`).map((call, index) => {
      const callPath = `${path}.tool_calls[${index}]`;
      assertObject(call, callPath);
      rejectUnknown(call, ['call_id', 'name', 'arguments_json'], callPath);
      optionalString(call, 'call_id', callPath);
      requiredStrings(call, ['name', 'arguments_json'], callPath);
      return call as unknown as NonNullable<AgentTraceContextMessage['tool_calls']>[number];
    });
  }
  if (value.tool_result !== undefined) {
    const resultPath = `${path}.tool_result`;
    assertObject(value.tool_result, resultPath);
    rejectUnknown(value.tool_result, ['call_id', 'name', 'result_json'], resultPath);
    optionalString(value.tool_result, 'call_id', resultPath);
    requiredStrings(value.tool_result, ['name', 'result_json'], resultPath);
  }
  return { ...(value as unknown as AgentTraceContextMessage), tool_calls: toolCalls };
}

function parseHeldContext(value: unknown, path: string): AgentTraceHeldContext {
  assertObject(value, path);
  rejectUnknown(value, ['assignment_id', 'repetition', 'evidence_digest', 'workspace', 'messages'], path);
  requiredStrings(value, ['assignment_id', 'evidence_digest'], path);
  assert(SHA256.test(value.evidence_digest as string), `${path}.evidence_digest`, 'expected lowercase SHA-256');
  assertInteger(value.repetition, `${path}.repetition`);
  const messages = list(value.messages, `${path}.messages`).map((message, index) => parseContextMessage(message, `${path}.messages[${index}]`));
  assert(messages.length > 0, `${path}.messages`, 'a held context carries the previous conversation');
  return {
    ...(value as unknown as AgentTraceHeldContext),
    workspace: parseWorkspace(value.workspace, `${path}.workspace`),
    messages,
  };
}

const MODEL_CALL_FIELDS = [
  'provider_attempt_id', 'transaction_id', 'purpose', 'loop_turn', 'tools_declared', 'result_digest', 'usage_reported',
  'prompt_tokens', 'completion_tokens', 'ttft_nanos', 'generation_duration_nanos', 'finish_reason', 'load_state',
  'retry_count', 'thinking', 'text', 'tool_call_count', 'error', 'receipt',
  'tool_call_id', 'persona',
] as const;

function parseModelCall(value: unknown, path: string, schemaVersion: AgentTraceSchemaVersion): AgentTraceModelCall {
  assertObject(value, path);
  const identity = ['model_variant_id', 'model_role', 'served_model_tag', 'model_digest', 'inference_result'];
  rejectUnknown(value, schemaVersion === 'agent-trace.v7' ? [...MODEL_CALL_FIELDS, ...identity] : MODEL_CALL_FIELDS, path);
  requiredStrings(value, ['provider_attempt_id', 'transaction_id', 'purpose', 'result_digest'], path);
  for (const key of ['loop_turn', 'prompt_tokens', 'completion_tokens', 'retry_count', 'tool_call_count']) {
    assertInteger(value[key], `${path}.${key}`);
  }
  assertBoolean(value.usage_reported, `${path}.usage_reported`);
  const declared = list(value.tools_declared, `${path}.tools_declared`);
  declared.forEach((name, index) => assertString(name, `${path}.tools_declared[${index}]`));
  for (const key of ['finish_reason', 'load_state', 'thinking', 'text', 'error']) optionalString(value, key, path);
  optionalInteger(value, 'ttft_nanos', path);
  optionalInteger(value, 'generation_duration_nanos', path);
  optionalProto(value, 'receipt', path);
  optionalString(value, 'tool_call_id', path);
  optionalString(value, 'persona', path);
  if (schemaVersion === 'agent-trace.v7') {
    requiredStrings(value, identity.filter((key) => key !== 'inference_result'), path);
    for (const key of ['model_variant_id', 'model_role', 'served_model_tag', 'model_digest']) {
      assert((value[key] as string).length > 0, `${path}.${key}`, 'expected non-empty model identity');
    }
    assert(SHA256.test(value.model_digest as string), `${path}.model_digest`, 'expected lowercase SHA-256');
    optionalProto(value, 'inference_result', path);
    if (!value.error) proto(value.inference_result, `${path}.inference_result`);
  }
  return { ...value, tools_declared: declared } as unknown as AgentTraceModelCall;
}

const TOOL_CALL_FIELDS = [
  'call_id', 'model_call_id', 'provider_call_id', 'tool_name', 'arguments_json', 'command', 'result_json', 'error_type',
  'error', 'success', 'is_operator_tool', 'loop_turn', 'action_type', 'transaction_id', 'policy_decision', 'receipt',
] as const;

const RETRIEVAL_QUERY_FIELDS = [
  'operator_session_id', 'investigation_id', 'action_type', 'exclude_audit_reads', 'not_before', 'limit', 'offset',
] as const;

function parseRetrieval(value: unknown, path: string, investigationId: string | undefined): AgentTraceRetrieval {
  assertObject(value, path);
  rejectUnknown(value, ['query', 'receipts'], path);
  assertObject(value.query, `${path}.query`);
  rejectUnknown(value.query, RETRIEVAL_QUERY_FIELDS, `${path}.query`);
  for (const key of ['operator_session_id', 'investigation_id', 'action_type', 'not_before']) {
    optionalString(value.query, key, `${path}.query`);
  }
  for (const key of ['limit', 'offset']) optionalInteger(value.query, key, `${path}.query`);
  const receipts = list(value.receipts, `${path}.receipts`).map((receipt, index) => {
    const receiptPath = `${path}.receipts[${index}]`;
    assertObject(receipt, receiptPath);
    rejectUnknown(receipt, ['receipt_id', 'sha256'], receiptPath);
    requiredStrings(receipt, ['receipt_id', 'sha256'], receiptPath);
    assert((receipt.receipt_id as string).length > 0, `${receiptPath}.receipt_id`, 'expected non-empty receipt ID');
    assert(SHA256.test(receipt.sha256 as string), `${receiptPath}.sha256`, 'expected lowercase SHA-256');
    return receipt as unknown as AgentTraceRetrievalReceipt;
  });
  assertString(value.query.investigation_id, `${path}.query.investigation_id`);
  assert(value.query.investigation_id === investigationId, `${path}.query.investigation_id`, 'must match trace investigation_id');
  assert(value.query.exclude_audit_reads === true, `${path}.query.exclude_audit_reads`, 'expected true');
  assertInteger(value.query.limit, `${path}.query.limit`);
  assert(value.query.limit >= 1 && value.query.limit <= 50, `${path}.query.limit`, 'expected a limit from 1 through 50');
  assert(value.query.operator_session_id === undefined || value.query.operator_session_id === '', `${path}.query.operator_session_id`, 'operator-local query must not be session-scoped');
  assert(value.query.action_type === undefined, `${path}.query.action_type`, 'operator-local query does not filter action type');
  assert(value.query.not_before === undefined, `${path}.query.not_before`, 'operator-local query does not filter by time');
  assert(value.query.offset === undefined, `${path}.query.offset`, 'operator-local query does not use an offset');
  return { query: value.query as unknown as AgentTraceRetrievalQuery, receipts };
}

function parseToolCall(value: unknown, path: string, schemaVersion: AgentTraceSchemaVersion, investigationId?: string): AgentTraceToolCall {
  assertObject(value, path);
  const fields = schemaVersion === 'agent-trace.v6' || schemaVersion === 'agent-trace.v7' ? [...TOOL_CALL_FIELDS, 'retrieval'] : TOOL_CALL_FIELDS;
  rejectUnknown(value, fields, path);
  requiredStrings(value, ['call_id', 'model_call_id', 'tool_name', 'arguments_json'], path);
  for (const key of ['provider_call_id', 'command', 'result_json', 'error_type', 'error', 'action_type', 'transaction_id', 'policy_decision']) {
    optionalString(value, key, path);
  }
  assertBoolean(value.success, `${path}.success`);
  assertBoolean(value.is_operator_tool, `${path}.is_operator_tool`);
  assertInteger(value.loop_turn, `${path}.loop_turn`);
  optionalProto(value, 'receipt', path);
  let retrieval: AgentTraceRetrieval | undefined;
  if (value.retrieval !== undefined) retrieval = parseRetrieval(value.retrieval, `${path}.retrieval`, investigationId);
  return { ...(value as unknown as AgentTraceToolCall), retrieval };
}

const TRACE_FIELDS = [
  'schema_version', 'evidence_digest', 'run_id', 'assignment_id', 'evaluation_attempt_id', 'campaign_id', 'scenario_id',
  'designated_role', 'served_model_tag', 'model_digest', 'model_registry_digest', 'investigation_id', 'inference', 'data', 'workspace', 'held_context', 'input',
  'model_calls', 'tool_calls', 'final_output', 'tool_turn_limit_reached', 'provider_failure', 'provider_failure_class', 'bracket',
  'terminal_classification', 'clarifications', 'steps',
] as const;

const INVESTIGATION_CONTEXT_TOOL = 'query_investigation_context';
const AUDIT_RECEIPT_LIST_TOOL = 'audit_receipt_list';
const INVESTIGATION_STATUS_TYPE = 'investigation_status';
const COMPLETED_EXECUTION_STATUS = 'EXECUTION_STATUS_COMPLETED';

function parseContextCallArguments(call: AgentTraceToolCall, path: string): Record<string, unknown> {
  let args: unknown;
  try {
    args = JSON.parse(call.arguments_json) as unknown;
  } catch {
    throw new ValidationError('expected JSON object', `${path}.arguments_json`);
  }
  assertObject(args, `${path}.arguments_json`);
  if (call.tool_name === INVESTIGATION_CONTEXT_TOOL) {
    assertString(args.data_type, `${path}.arguments_json.data_type`);
  }
  return args;
}

function validateV6ContextRetrievals(calls: AgentTraceToolCall[], investigationId: string | undefined): void {
  calls.forEach((call, index) => {
    const path = `agent_trace.tool_calls[${index}]`;
    if (call.tool_name !== INVESTIGATION_CONTEXT_TOOL && call.tool_name !== AUDIT_RECEIPT_LIST_TOOL) {
      assert(call.retrieval === undefined, `${path}.retrieval`, 'retrieval is only valid for query_investigation_context or audit_receipt_list');
      return;
    }
    const completedReceipt = call.receipt?.status === COMPLETED_EXECUTION_STATUS;
    if (call.retrieval === undefined) {
      assert(!completedReceipt, `${path}.retrieval`, 'a completed context query must carry retrieval evidence');
      if (!call.success) return;
      if (call.tool_name === INVESTIGATION_CONTEXT_TOOL) {
        const args = parseContextCallArguments(call, path);
        assert(args.data_type === INVESTIGATION_STATUS_TYPE, `${path}.retrieval`, 'a successful non-status context query must carry retrieval evidence');
      }
      return;
    }
    assert(call.success, `${path}.retrieval`, 'retrieval evidence requires a successful tool call');
    assert(call.action_type === 'MCP_CALL', `${path}.retrieval`, 'retrieval evidence requires a governed MCP_CALL');
    assert(investigationId !== undefined, `${path}.retrieval`, 'retrieval evidence requires trace investigation_id');
    if (call.tool_name === INVESTIGATION_CONTEXT_TOOL) {
      const args = parseContextCallArguments(call, path);
      assert(
        args.data_type === 'history_trail' || args.data_type === 'operator_actions',
        `${path}.retrieval`,
        'only history_trail and operator_actions return receipt retrievals',
      );
    }
  });
}

/** Decodes one trace against the closed AgentTrace schema. */
export function parseAgentTrace(value: unknown): AgentTrace {
  const path = 'agent_trace';
  assertObject(value, path);
  rejectUnknown(value, TRACE_FIELDS, path);
  assert(
    typeof value.schema_version === 'string' && (AGENT_TRACE_SCHEMA_VERSIONS as readonly string[]).includes(value.schema_version),
    `${path}.schema_version`,
    `expected one of ${AGENT_TRACE_SCHEMA_VERSIONS.join(', ')}`,
  );
  const schemaVersion = value.schema_version as AgentTraceSchemaVersion;
  assertString(value.evidence_digest, `${path}.evidence_digest`);
  assert(SHA256.test(value.evidence_digest), `${path}.evidence_digest`, 'expected lowercase SHA-256');
  const liveContext = schemaVersion === 'agent-trace.v6' || schemaVersion === 'agent-trace.v7';
  if (liveContext) {
    optionalString(value, 'investigation_id', path);
    if (value.investigation_id !== undefined) {
      assert((value.investigation_id as string).length > 0, `${path}.investigation_id`, 'expected non-empty investigation ID');
    }
  } else {
    assert(value.investigation_id === undefined, `${path}.investigation_id`, `${schemaVersion} never carries investigation ID`);
  }
  requiredStrings(
    value,
    ['run_id', 'assignment_id', 'evaluation_attempt_id', 'campaign_id', 'scenario_id', 'designated_role', 'served_model_tag', 'model_digest', 'model_registry_digest', 'final_output'],
    path,
  );
  const inference = parseRuntime(value.inference, `${path}.inference`);
  const data = parseRuntime(value.data, `${path}.data`);
  const workspace = parseWorkspace(value.workspace, `${path}.workspace`);
  let input: AgentTraceInput | undefined;
  if (value.schema_version === 'agent-trace.v3') {
    assert(value.input === undefined, `${path}.input`, 'agent-trace.v3 never carries input');
  } else {
    input = parseInput(value.input, `${path}.input`);
    assert(input.messages.length > 0, `${path}.input.messages`, `${value.schema_version} records the exact model input`);
  }
  let heldContext: AgentTraceHeldContext | undefined;
  if ((schemaVersion === 'agent-trace.v5' || liveContext) && value.held_context !== undefined) {
    heldContext = parseHeldContext(value.held_context, `${path}.held_context`);
  } else {
    assert(value.held_context === undefined, `${path}.held_context`, `${schemaVersion} never carries held context`);
  }
  const modelCalls = list(value.model_calls, `${path}.model_calls`).map((call, index) => parseModelCall(call, `${path}.model_calls[${index}]`, schemaVersion));
  const toolCalls = list(value.tool_calls, `${path}.tool_calls`).map((call, index) => parseToolCall(call, `${path}.tool_calls[${index}]`, schemaVersion, value.investigation_id as string | undefined));
  if (liveContext) validateV6ContextRetrievals(toolCalls, value.investigation_id as string | undefined);
  optionalString(value, 'terminal_classification', path);
  if (value.steps !== undefined) list(value.steps, `${path}.steps`).forEach((step, index) => proto(step, `${path}.steps[${index}]`));
  if (value.clarifications !== undefined) {
    list(value.clarifications, `${path}.clarifications`).forEach((turn, index) => {
      const turnPath = `${path}.clarifications[${index}]`;
      assertObject(turn, turnPath);
      rejectUnknown(turn, ['loop_turn', 'questions', 'selected_branch', 'user_response'], turnPath);
      assertInteger(turn.loop_turn, `${turnPath}.loop_turn`);
      list(turn.questions, `${turnPath}.questions`).forEach((question, i) => assertString(question, `${turnPath}.questions[${i}]`));
      optionalString(turn, 'selected_branch', turnPath);
      optionalString(turn, 'user_response', turnPath);
    });
  }
  assertBoolean(value.tool_turn_limit_reached, `${path}.tool_turn_limit_reached`);
  optionalString(value, 'provider_failure', path);
  optionalString(value, 'provider_failure_class', path);
  let bracket: AgentTrace['bracket'];
  if (value.bracket !== undefined) {
    assertObject(value.bracket, `${path}.bracket`);
    rejectUnknown(value.bracket, ['before', 'after'], `${path}.bracket`);
    bracket = { before: proto(value.bracket.before, `${path}.bracket.before`), after: proto(value.bracket.after, `${path}.bracket.after`) };
  }
  return {
    ...(value as unknown as AgentTrace),
    inference,
    data,
    workspace,
    held_context: heldContext,
    input,
    model_calls: modelCalls,
    tool_calls: toolCalls,
    bracket,
  };
}

export async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', bytes.slice().buffer as ArrayBuffer);
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

/** The trace's leading members, which fix where evidence_digest sits. */
function leadingMembers(schemaVersion: string, digest: string): string {
  return `{"schema_version":"${schemaVersion}","evidence_digest":"${digest}",`;
}

export interface AgentTraceBinding {
  sha256: string;
  runId: string;
  assignmentId: string;
}

/**
 * Verifies one published trace body and decodes it. The body must hash to its
 * binding; evidence_digest must equal the SHA-256 of the same bytes with the
 * digest value emptied, which is exactly the Go digest preimage because the
 * trace encodes its fields in fixed order with schema_version and
 * evidence_digest first; and the trace must belong to the assignment. Any
 * failure throws, so an unverified body is never rendered.
 */
export async function verifyAgentTraceArtifact(body: Uint8Array, binding: AgentTraceBinding): Promise<AgentTrace> {
  assert((await sha256Hex(body)) === binding.sha256, 'agent_trace', 'body does not match its published SHA-256 binding');
  let decoded: unknown;
  try {
    decoded = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(body));
  } catch {
    throw new ValidationError('body is not UTF-8 JSON', 'agent_trace');
  }
  const trace = parseAgentTrace(decoded);
  const leading = new TextEncoder().encode(leadingMembers(trace.schema_version, trace.evidence_digest));
  assert(
    body.byteLength > leading.byteLength && leading.every((byte, index) => body[index] === byte),
    'agent_trace.evidence_digest',
    'body does not lead with schema_version and evidence_digest',
  );
  const emptied = new TextEncoder().encode(leadingMembers(trace.schema_version, ''));
  const preimage = new Uint8Array(emptied.byteLength + body.byteLength - leading.byteLength);
  preimage.set(emptied);
  preimage.set(body.subarray(leading.byteLength), emptied.byteLength);
  assert((await sha256Hex(preimage)) === trace.evidence_digest, 'agent_trace.evidence_digest', 'evidence_digest does not match the trace');
  assert(
    trace.run_id === binding.runId && trace.assignment_id === binding.assignmentId,
    'agent_trace',
    'trace belongs to a different assignment',
  );
  return trace;
}
