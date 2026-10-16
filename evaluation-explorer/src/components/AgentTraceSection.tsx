// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The full sealed agent trace behind an assignment, fetched from its
// content-addressed proof artifact and verified in the browser before any of
// it renders: the exact model input, every model call (thinking, text, finish,
// retries, failure), every tool call with its governance decision and signed
// receipt, and the sentinel bracket around the run. Nothing is screened.

import { useEffect, useState } from 'react';
import {
  verifyAgentTraceArtifact,
  type AgentTrace,
  type AgentTraceModelCall,
  type AgentTraceProto,
  type AgentTraceRuntime,
  type AgentTraceToolCall,
} from '../contract/agent-trace';
import type { PublicEvidenceBinding } from '../contract/types';
import { ValidationError } from '../contract/validators';
import { useMirrorOrigin } from '../state/mirror';
import { useStoreState } from '../state/store';
import { PanelSection } from './evidence';
import { label, prettyJSON } from './RoleTranscripts';
import { CopyButton, formatLatency, formatTokens } from './shared';

type TraceState = { status: 'loading' } | { status: 'verified'; trace: AgentTrace } | { status: 'failed'; reason: string };

function field(value: AgentTraceProto, key: string): string | undefined {
  const entry = value[key];
  return typeof entry === 'string' && entry !== '' ? entry : undefined;
}

function enumLabel(value: string | undefined, prefix: string): string | undefined {
  return value ? label(value.replace(prefix, '')) : undefined;
}

export function TextBlock({ title, text, copyLabel, json = false }: { title: string; text: string; copyLabel: string; json?: boolean }) {
  return (
    <div className="transcript-field">
      <div className="transcript-field-label">
        {title}
        <CopyButton text={text} label={copyLabel} />
      </div>
      <pre className="model-response-text"><code>{json ? prettyJSON(text) : text}</code></pre>
    </div>
  );
}

function stageEvidence(receipt: AgentTraceProto): { kind: string; outcome: string }[] {
  const stages = receipt.deterministic_stage_evidence;
  if (!Array.isArray(stages)) return [];
  return stages.flatMap((stage) =>
    stage && typeof stage === 'object'
      ? [{
          kind: enumLabel(field(stage as AgentTraceProto, 'kind'), 'DETERMINISTIC_STAGE_KIND_') ?? 'Stage',
          outcome: enumLabel(field(stage as AgentTraceProto, 'outcome'), 'DETERMINISTIC_STAGE_OUTCOME_') ?? 'Unspecified',
        }]
      : [],
  );
}

function Receipt({ receipt, title = 'Signed receipt' }: { receipt: AgentTraceProto; title?: string }) {
  const status = field(receipt, 'status');
  const succeeded = status === 'EXECUTION_STATUS_COMPLETED';
  const raw = JSON.stringify(receipt, null, 2);
  const stages = stageEvidence(receipt);
  return (
    <div className="agent-trace-receipt">
      <div className="transcript-field-label">
        {title}
        {status ? (
          <span className={`transcript-outcome ${succeeded ? 'tone-ok' : 'tone-critical'}`}>{enumLabel(status, 'EXECUTION_STATUS_')}</span>
        ) : null}
      </div>
      <dl className="agent-trace-facts">
        {field(receipt, 'action_type') ? <><dt>Action</dt><dd><code>{field(receipt, 'action_type')}</code></dd></> : null}
        {field(receipt, 'transaction_id') ? <><dt>Transaction</dt><dd><code>{field(receipt, 'transaction_id')}</code></dd></> : null}
        {field(receipt, 'signer_key_id') ? <><dt>Signer</dt><dd><code>{field(receipt, 'signer_key_id')}</code></dd></> : null}
        {field(receipt, 'l2_status') ? <><dt>L2 consensus</dt><dd>{enumLabel(field(receipt, 'l2_status'), 'L2_STATUS_')}</dd></> : null}
        {field(receipt, 'l3_status') ? <><dt>L3 notary</dt><dd>{enumLabel(field(receipt, 'l3_status'), 'L3_STATUS_')}</dd></> : null}
        {field(receipt, 'failure_code') ? <><dt>Failure</dt><dd><code>{field(receipt, 'failure_code')}</code></dd></> : null}
        {field(receipt, 'rejection_code') ? <><dt>Rejection</dt><dd><code>{field(receipt, 'rejection_code')}</code></dd></> : null}
      </dl>
      {stages.length > 0 ? (
        <ol className="agent-trace-stages" aria-label="Deterministic stage evidence">
          {stages.map((stage, index) => (
            <li key={index}><span>{stage.kind}</span> <span>{stage.outcome}</span></li>
          ))}
        </ol>
      ) : null}
      <details className="agent-trace-raw">
        <summary>Receipt JSON</summary>
        <CopyButton text={raw} label="receipt JSON" />
        <pre className="model-response-text"><code>{raw}</code></pre>
      </details>
    </div>
  );
}

function ToolCall({ call }: { call: AgentTraceToolCall }) {
  const outcome = call.success ? 'Succeeded' : call.error_type ? `Failed · ${label(call.error_type)}` : 'Failed';
  return (
    <li className={`transcript-tool-call ${call.success ? 'transcript-tool-call-ok' : 'transcript-tool-call-failed'}`}>
      <div className="transcript-tool-call-header">
        <code className="transcript-tool-name">{call.tool_name}</code>
        <span className={`transcript-outcome ${call.success ? 'tone-ok' : 'tone-critical'}`}>
          <span aria-hidden="true">{call.success ? '✓' : '✕'}</span> {outcome}
        </span>
        {call.policy_decision ? <span className="agent-trace-chip">Policy: {label(call.policy_decision)}</span> : null}
        {call.is_operator_tool ? <span className="agent-trace-chip">Governed</span> : <span className="agent-trace-chip">Local</span>}
      </div>
      <TextBlock title="Arguments" text={call.arguments_json} copyLabel={`${call.tool_name} arguments`} json />
      {call.command ? <TextBlock title="Resolved command" text={call.command} copyLabel={`${call.tool_name} command`} /> : null}
      {call.result_json ? <TextBlock title="Tool result" text={call.result_json} copyLabel={`${call.tool_name} result`} json /> : null}
      {call.error ? <TextBlock title="Error" text={call.error} copyLabel={`${call.tool_name} error`} /> : null}
      {call.receipt ? <Receipt receipt={call.receipt} /> : null}
    </li>
  );
}

function ModelCall({ call, index, toolCalls }: { call: AgentTraceModelCall; index: number; toolCalls: AgentTraceToolCall[] }) {
  const facts = [
    label(call.purpose),
    call.finish_reason ? `Finish: ${call.finish_reason}` : undefined,
    call.retry_count > 0 ? `${call.retry_count} ${call.retry_count === 1 ? 'retry' : 'retries'}` : undefined,
    call.usage_reported ? `${formatTokens(call.prompt_tokens)} in · ${formatTokens(call.completion_tokens)} out` : 'Usage not reported',
    call.ttft_nanos !== undefined ? `TTFT ${formatLatency(call.ttft_nanos / 1e6)}` : undefined,
    call.generation_duration_nanos !== undefined ? `Generation ${formatLatency(call.generation_duration_nanos / 1e6)}` : undefined,
  ].filter(Boolean);
  return (
    <li className="agent-trace-turn">
      <div className="model-response-header">
        <span className="model-response-label">Turn {call.loop_turn} · model call {index + 1}</span>
        <span className="agent-trace-turn-facts">{facts.join(' · ')}</span>
      </div>
      {call.thinking ? (
        <details className="transcript-field agent-trace-thinking" open>
          <summary className="transcript-field-label">Thinking</summary>
          <CopyButton text={call.thinking} label={`turn ${call.loop_turn} thinking`} />
          <pre className="model-response-text"><code>{call.thinking}</code></pre>
        </details>
      ) : null}
      {call.text ? <TextBlock title="Text" text={call.text} copyLabel={`turn ${call.loop_turn} text`} /> : null}
      {call.error ? <TextBlock title="Provider error" text={call.error} copyLabel={`turn ${call.loop_turn} error`} /> : null}
      {toolCalls.length > 0 ? (
        <ol className="transcript-tool-calls">
          {toolCalls.map((tool) => <ToolCall key={tool.call_id} call={tool} />)}
        </ol>
      ) : null}
      {call.receipt ? <Receipt receipt={call.receipt} title="Inference receipt" /> : null}
    </li>
  );
}

function Runtime({ title, runtime }: { title: string; runtime: AgentTraceRuntime }) {
  return (
    <div className="agent-trace-runtime">
      <span className="transcript-field-label">{title}</span>
      <dl className="agent-trace-facts">
        <dt>Operator</dt><dd><code>{runtime.operator_id}</code></dd>
        <dt>Session</dt><dd><code>{runtime.session_id}</code></dd>
        {runtime.anchor ? <>
          <dt>Signing key</dt><dd><code>{field(runtime.anchor, 'key_id') ?? '—'}</code></dd>
          <dt>Public key</dt><dd><code>{field(runtime.anchor, 'public_key_hex') ?? '—'}</code></dd>
          <dt>Ledger export</dt><dd><code>{field(runtime.anchor, 'export_sha256') ?? '—'}</code></dd>
        </> : <><dt>Anchor</dt><dd>Not recorded</dd></>}
      </dl>
    </div>
  );
}

function TraceBody({ trace }: { trace: AgentTrace }) {
  const callsByModel = new Map<string, AgentTraceToolCall[]>();
  for (const call of trace.tool_calls) {
    callsByModel.set(call.model_call_id, [...(callsByModel.get(call.model_call_id) ?? []), call]);
  }
  const attached = new Set(trace.model_calls.map((call) => call.provider_attempt_id));
  const unattached = trace.tool_calls.filter((call) => !attached.has(call.model_call_id));
  return (
    <>
      <div className="model-response-block">
        <div className="model-response-header"><span className="model-response-label">Exact model input</span></div>
        {trace.input ? (
          <>
            <ol className="agent-trace-messages">
              {trace.input.messages.map((message, index) => (
                <li key={index}>
                  <TextBlock title={enumLabel(message.role, 'INFERENCE_MESSAGE_ROLE_') ?? message.role} text={message.text} copyLabel={`input message ${index + 1}`} />
                </li>
              ))}
            </ol>
            {trace.input.tools.length > 0 ? (
              <details className="agent-trace-tools">
                <summary>Tools offered ({trace.input.tools.length})</summary>
                <ul>
                  {trace.input.tools.map((tool) => (
                    <li key={tool.name}>
                      <code className="transcript-tool-name">{tool.name}</code>
                      <p>{tool.description}</p>
                      <TextBlock title="JSON schema" text={tool.json_schema} copyLabel={`${tool.name} schema`} json />
                    </li>
                  ))}
                </ul>
              </details>
            ) : null}
          </>
        ) : (
          <p className="model-response-empty">Exact input not captured ({trace.schema_version}).</p>
        )}
      </div>

      <div className="model-response-block">
        <div className="model-response-header"><span className="model-response-label">Turns ({trace.model_calls.length})</span></div>
        {trace.model_calls.length > 0 ? (
          <ol className="agent-trace-turns">
            {trace.model_calls.map((call, index) => (
              <ModelCall key={call.provider_attempt_id} call={call} index={index} toolCalls={callsByModel.get(call.provider_attempt_id) ?? []} />
            ))}
          </ol>
        ) : <p className="model-response-empty">The trace records no model calls.</p>}
        {unattached.length > 0 ? (
          <ol className="transcript-tool-calls">
            {unattached.map((call) => <ToolCall key={call.call_id} call={call} />)}
          </ol>
        ) : null}
      </div>

      <div className="model-response-block">
        <div className="model-response-header">
          <span className="model-response-label">Final output</span>
          {trace.tool_turn_limit_reached ? <span className="transcript-outcome tone-critical">Tool turn limit reached</span> : null}
        </div>
        {trace.final_output ? (
          <TextBlock title="Output" text={trace.final_output} copyLabel="final output" />
        ) : <p className="model-response-empty">No final output was produced.</p>}
        {trace.provider_failure ? (
          <TextBlock
            title={`Provider failure${trace.provider_failure_class ? ` · ${label(trace.provider_failure_class)}` : ''}`}
            text={trace.provider_failure}
            copyLabel="provider failure"
          />
        ) : null}
      </div>

      <div className="model-response-block">
        <div className="model-response-header"><span className="model-response-label">Runtimes</span></div>
        <div className="agent-trace-runtimes">
          <Runtime title="Inference Operator" runtime={trace.inference} />
          <Runtime title="Data Operator" runtime={trace.data} />
        </div>
        {trace.bracket ? (
          <div className="agent-trace-bracket">
            <Receipt receipt={trace.bracket.before} title="Sentinel before" />
            <Receipt receipt={trace.bracket.after} title="Sentinel after" />
          </div>
        ) : <p className="model-response-empty">No sentinel bracket was recorded.</p>}
      </div>
    </>
  );
}

function useVerifiedTrace(url: string | undefined, binding: PublicEvidenceBinding | undefined, runId: string, assignmentId: string): TraceState {
  const [state, setState] = useState<TraceState>({ status: 'loading' });
  const sha256 = binding?.sha256;
  useEffect(() => {
    if (!url || !sha256) return;
    let cancelled = false;
    setState({ status: 'loading' });
    fetch(url)
      .then((response) => {
        if (!response.ok) throw new Error(`fetch returned HTTP ${response.status}`);
        return response.arrayBuffer();
      })
      .then((buffer) => verifyAgentTraceArtifact(new Uint8Array(buffer), { sha256, runId, assignmentId }))
      .then((trace) => {
        if (!cancelled) setState({ status: 'verified', trace });
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        const reason = error instanceof ValidationError ? `${error.path}: ${error.message}` : error instanceof Error ? error.message : String(error);
        setState({ status: 'failed', reason });
      });
    return () => {
      cancelled = true;
    };
  }, [url, sha256, runId, assignmentId]);
  return state;
}

export function agentTraceBinding(bindings: PublicEvidenceBinding[] | undefined): PublicEvidenceBinding | undefined {
  return bindings?.find((binding) => binding.kind === 'assignment_agent_trace');
}

export function AgentTraceSection({ id, bindings, runId, assignmentId }: {
  id: string; bindings: PublicEvidenceBinding[] | undefined; runId: string; assignmentId: string;
}) {
  const origin = useMirrorOrigin();
  const binding = agentTraceBinding(bindings);
  const artifact = useStoreState((state) => (binding ? state.proofArtifacts.get(binding.sha256) : undefined));
  const url = origin && artifact ? `${origin}${artifact.immutable_url}` : undefined;
  const state = useVerifiedTrace(url, binding, runId, assignmentId);

  if (!binding || !artifact) return null;
  const command = artifact.verification_command;

  return (
    <PanelSection id={id} className="assignment-agent-trace" title="Full agent trace">
      <div className="agent-trace-proof">
        <code>sha256 {binding.sha256}</code>
        {url ? <a href={url} download={artifact.filename}>Download trace</a> : <span>Download trace</span>}
        <CopyButton text={command} label="verify command" />
      </div>
      <p className="task-section-note">
        Verified in this browser against its published SHA-256 and evidence digest before display. Receipt signatures and the
        sentinel bracket verify offline with <code>{command}</code>.
      </p>
      {state.status === 'loading' ? <p className="model-response-empty">Loading and verifying the trace…</p> : null}
      {state.status === 'failed' ? (
        <p className="agent-trace-failed" role="alert">Trace not shown: verification failed ({state.reason}).</p>
      ) : null}
      {state.status === 'verified' ? (
        <>
          <p className="transcript-provenance">
            <span className="transcript-outcome tone-ok"><span aria-hidden="true">✓</span> Verified</span>
            <span>{state.trace.schema_version}</span>
            <span>Evidence digest <code>{state.trace.evidence_digest}</code></span>
            <span>{state.trace.served_model_tag}</span>
          </p>
          <TraceBody trace={state.trace} />
        </>
      ) : null}
    </PanelSection>
  );
}
