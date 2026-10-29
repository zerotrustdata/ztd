// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// What each model role actually did, lifted verbatim from its digest-bound
// role trace. Traces from older recorded runs are historical evidence and render
// the same way: every tool call (tool, exact arguments, resolved command, outcome,
// result) in execution order, then the role's final response.

import type { RoleTranscript, RoleTranscriptToolCall } from '../contract/types';
import { roleLabel } from '../views/derived';
import { CopyButton } from './shared';

export function prettyJSON(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

/** One readable line of JSON, with the spaces pretty-printing would insert. */
export function oneLineJSON(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 1).replace(/\n\s*/g, ' ').trim();
  } catch {
    return text.replace(/\s+/g, ' ').trim();
  }
}

export function label(value: string): string {
  return value.replace(/_/g, ' ').toLowerCase().replace(/^./, (letter) => letter.toUpperCase());
}

function ToolCall({ call, index }: { call: RoleTranscriptToolCall; index: number }) {
  const outcome = call.success ? 'Succeeded' : call.error_type ? `Failed · ${label(call.error_type)}` : 'Failed';
  return (
    <li className={`transcript-tool-call ${call.success ? 'transcript-tool-call-ok' : 'transcript-tool-call-failed'}`}>
      <div className="transcript-tool-call-header">
        <span className="transcript-step">{index + 1}</span>
        <code className="transcript-tool-name">{call.tool_name}</code>
        <span className={`transcript-outcome ${call.success ? 'tone-ok' : 'tone-critical'}`}>
          <span aria-hidden="true">{call.success ? '✓' : '✕'}</span> {outcome}
        </span>
      </div>
      {call.arguments_json ? (
        <div className="transcript-inline-row">
          <code className="transcript-inline" title="Arguments recorded in the trace">{oneLineJSON(call.arguments_json)}</code>
          <CopyButton text={call.arguments_json} label={`${call.tool_name} arguments`} />
        </div>
      ) : null}
      {call.arguments_hash ? (
        <code className="transcript-hash" title="sha256 of the exact arguments recorded in the trace">sha256 {call.arguments_hash}</code>
      ) : null}
      {call.approved_arguments_json && call.approved_arguments_json !== call.arguments_json ? (
        <div className="transcript-inline-row">
          <span className="transcript-inline-label">Approved</span>
          <code className="transcript-inline">{oneLineJSON(call.approved_arguments_json)}</code>
          <CopyButton text={call.approved_arguments_json} label={`${call.tool_name} approved arguments`} />
        </div>
      ) : null}
      {call.command ? (
        <div className="transcript-inline-row">
          <code className="transcript-inline">{call.command}</code>
          <CopyButton text={call.command} label={`${call.tool_name} command`} />
        </div>
      ) : null}
      {call.result_json ? (
        <details className="transcript-field transcript-result" open>
          <summary className="transcript-field-label">Tool result</summary>
          <CopyButton text={call.result_json} label={`${call.tool_name} result`} />
          <pre className="model-response-text"><code>{prettyJSON(call.result_json)}</code></pre>
        </details>
      ) : null}
    </li>
  );
}

function Transcript({ transcript, variantId, showRole }: { transcript: RoleTranscript; variantId: string; showRole: boolean }) {
  const toolCalls = transcript.tool_calls ?? [];
  return (
    <article className="role-transcript" aria-label={`${roleLabel(transcript.role)} transcript`}>
      {showRole ? <h3 className="role-transcript-heading">{roleLabel(transcript.role)}</h3> : null}
      {toolCalls.length > 0 ? (
        <div className="model-response-block">
          <div className="model-response-header">
            <span className="model-response-label">Tool calls ({toolCalls.length})</span>
          </div>
          <ol className="transcript-tool-calls">
            {toolCalls.map((call, index) => <ToolCall key={index} call={call} index={index} />)}
          </ol>
        </div>
      ) : null}
      {transcript.response ? (
        <div className="model-response-block">
          <div className="model-response-header">
            <span className="model-response-label">Response</span>
            <span className="model-response-variant">{showRole ? roleLabel(transcript.role) : variantId}</span>
            <CopyButton text={transcript.response} label={`${roleLabel(transcript.role)} response`} />
          </div>
          <pre className="model-response-text"><code>{transcript.response}</code></pre>
        </div>
      ) : null}
      {toolCalls.length === 0 && !transcript.response ? (
        <p className="model-response-empty">This published role transcript contains no response text or tool calls.</p>
      ) : null}
      {transcript.finish_reason || transcript.trace_digest ? (
        <p className="transcript-provenance">
          {transcript.finish_reason ? <span>Finish: {transcript.finish_reason}</span> : null}
          {transcript.trace_digest ? (
            <span>
              Trace digest <code>{transcript.trace_digest}</code>
              <CopyButton text={transcript.trace_digest} label={`${roleLabel(transcript.role)} trace digest`} />
            </span>
          ) : null}
        </p>
      ) : null}
    </article>
  );
}

export function RoleTranscripts({ transcripts, variantId }: { transcripts: RoleTranscript[]; variantId: string }) {
  const showRole = transcripts.length > 1;
  return (
    <div className="role-transcripts">
      {transcripts.map((transcript, index) => (
        <Transcript key={`${transcript.role}-${index}`} transcript={transcript} variantId={variantId} showRole={showRole} />
      ))}
    </div>
  );
}
