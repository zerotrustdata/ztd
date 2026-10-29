// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Compact in-row reading of an assignment: the outcome, the calls, and the
// reply. The assignment page is the next layer down.

import { Link } from 'react-router-dom';
import type { AssignmentResult } from '../contract/types';
import { oneLineJSON } from './RoleTranscripts';

function label(value: string): string {
  return value.replace(/_/g, ' ');
}

function calls(assignment: AssignmentResult): string[] {
  const fromTrace = (assignment.role_transcripts ?? []).flatMap((transcript) => transcript.tool_calls ?? []);
  if (fromTrace.length > 0) {
    return fromTrace.map((call) => {
      const args = call.arguments_json ? ` ${oneLineJSON(call.arguments_json)}` : '';
      return `${call.tool_name}${call.success ? '' : ' failed'}${args}`;
    });
  }
  const activity = assignment.activity_summary?.tool_calls;
  if (activity?.availability !== 'observed') return [];
  return activity.records.map((record) => `${record.tool_label || 'tool'} · ${label(record.semantic_outcome)}`);
}

function reply(assignment: AssignmentResult): string | undefined {
  return assignment.model_response
    ?? assignment.role_transcripts?.find((transcript) => transcript.response)?.response
    ?? assignment.failure_output;
}

export function AssignmentInsight({ assignment }: { assignment: AssignmentResult }) {
  const toolCalls = calls(assignment);
  const response = reply(assignment);
  const href = `/evaluations/${assignment.dataset_id}/${assignment.run_id}/assignments/${assignment.assignment_id}`;
  return (
    <div className="assignment-insight">
      <div className="assignment-insight-facts">
        {assignment.trajectory_outcome ? <span>{label(assignment.trajectory_outcome)}</span> : null}
        {assignment.guided_retry_count ? <span>{assignment.guided_retry_count} guided {assignment.guided_retry_count === 1 ? 'retry' : 'retries'}</span> : null}
        {assignment.tools_declared?.length ? <span>Declared {assignment.tools_declared.join(', ')}</span> : null}
      </div>
      {assignment.failure_reason ? <p className="assignment-insight-reason">{assignment.failure_reason}</p> : null}
      {toolCalls.length > 0 ? (
        <ol className="assignment-insight-calls">
          {toolCalls.map((call, index) => <li key={index}><code>{call}</code></li>)}
        </ol>
      ) : null}
      {response ? <pre className="assignment-insight-reply"><code>{response}</code></pre> : null}
      <Link to={href}>Open assignment</Link>
    </div>
  );
}
