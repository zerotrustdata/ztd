// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// The Judge cell of a live assignment row: every pinned-judge inference on the
// assignment, as it starts and completes, with everything recorded about it.

import type { LiveJudgeActivity, LiveJudgeFailure } from '../contract/types';
import { formatLatency, formatNumber, formatThroughput } from '../utils/format';

const NANOS_PER_MILLISECOND = 1_000_000;

const JUDGE_FAILURE_LABELS: Record<LiveJudgeFailure, string> = {
  request_invalid: 'request invalid',
  dispatch_failed: 'dispatch failed',
  response_mismatch: 'response mismatch',
  result_digest: 'result digest mismatch',
  verdict_invalid: 'verdict invalid',
  receipt_unverified: 'receipt unverified',
};

function judgmentLabel(judgment: LiveJudgeActivity): string {
  switch (judgment.subject) {
    case 'criterion':
      return judgment.criterion_id ?? 'criterion';
    case 'final_reply':
      return 'Diagnosis quality';
    case 'memory':
      return 'Memory quality';
  }
}

function verdictLabel(judgment: LiveJudgeActivity): string {
  switch (judgment.state) {
    case 'judging':
      return 'Judging…';
    case 'met':
      return 'Met';
    case 'not_met':
      return 'Not met';
    case 'scored':
      return `Score ${judgment.score}/5`;
    case 'failed':
      return `Failed: ${judgment.failure ? JUDGE_FAILURE_LABELS[judgment.failure] : 'unknown'}`;
  }
}

function nanos(value: number | undefined): string | undefined {
  return value === undefined ? undefined : formatLatency(value / NANOS_PER_MILLISECOND);
}

function tokens(value: number | undefined): string | undefined {
  return value === undefined ? undefined : formatNumber(value);
}

/** Output tokens per second of generation, from the signed result. */
function judgeThroughput(judgment: LiveJudgeActivity): string | undefined {
  const { completion_tokens: completion, generation_duration_ns: generation } = judgment;
  if (completion === undefined || generation === undefined || generation <= 0) return undefined;
  return formatThroughput(completion / (generation / 1_000_000_000));
}

function judgmentDetails(judgment: LiveJudgeActivity): Array<[string, string | undefined]> {
  return [
    ['Judge model', judgment.served_model_tag],
    ['Judge variant', judgment.judge_variant_id],
    ['Model digest', judgment.model_digest],
    ['Rubric', judgment.rubric_version],
    ['Position', `${judgment.index} of ${judgment.count}`],
    ['Rationale', judgment.rationale],
    ['Tokens in', tokens(judgment.prompt_tokens)],
    ['Tokens out', tokens(judgment.completion_tokens)],
    ['Thinking tokens', tokens(judgment.thinking_tokens)],
    ['Cache tokens', tokens(judgment.cache_tokens)],
    ['Usage', judgment.state === 'judging' ? undefined : judgment.usage_reported ? 'reported' : 'not reported'],
    ['Load', nanos(judgment.load_duration_ns)],
    ['Prompt eval', nanos(judgment.prompt_eval_duration_ns)],
    ['Generation', nanos(judgment.generation_duration_ns)],
    ['Total (provider)', nanos(judgment.total_duration_ns)],
    ['Time to first token', nanos(judgment.time_to_first_token_ns)],
    ['Throughput', judgeThroughput(judgment)],
    ['Finish reason', judgment.finish_reason],
    ['Executed at', judgment.executed_at_unix_ms === undefined ? undefined : new Date(judgment.executed_at_unix_ms).toISOString()],
    ['Wall time (live)', nanos(judgment.dispatch_elapsed_ns)],
    ['Attempt', judgment.provider_attempt_id],
    ['Transaction', judgment.transaction_id],
  ];
}

/** The row summary: judge model, judgments done, verdicts, time, and tokens. */
function judgeSummary(judgments: LiveJudgeActivity[]): string {
  const phases = new Map<string, number>();
  for (const judgment of judgments) phases.set(judgment.subject === 'criterion' ? 'criterion' : 'quality', judgment.count);
  const expected = [...phases.values()].reduce((sum, count) => sum + count, 0);
  const done = judgments.filter((judgment) => judgment.state !== 'judging');
  const verdicts: string[] = [];
  const met = done.filter((judgment) => judgment.state === 'met').length;
  const notMet = done.filter((judgment) => judgment.state === 'not_met').length;
  if (met + notMet > 0) verdicts.push(`${met} met`, `${notMet} not met`);
  const scores = done.filter((judgment) => judgment.state === 'scored').map((judgment) => `${judgment.score}/5`);
  if (scores.length > 0) verdicts.push(`scores ${scores.join(', ')}`);
  const failed = done.filter((judgment) => judgment.state === 'failed').length;
  if (failed > 0) verdicts.push(`${failed} failed`);
  const sum = (field: 'total_duration_ns' | 'prompt_tokens' | 'completion_tokens') =>
    done.reduce((total, judgment) => total + (judgment[field] ?? 0), 0);
  const parts = [judgments[0]!.served_model_tag, `${done.length}/${expected} judged`, ...verdicts];
  if (sum('total_duration_ns') > 0) parts.push(nanos(sum('total_duration_ns'))!);
  if (sum('prompt_tokens') + sum('completion_tokens') > 0) parts.push(`${formatNumber(sum('prompt_tokens'))} in / ${formatNumber(sum('completion_tokens'))} out tok`);
  return parts.join(' · ');
}

/** The Judge cell of one assignment row. */
export function JudgeActivity({ judgments }: { judgments: LiveJudgeActivity[] }) {
  return (
    <div className="judge-activity" role="group" aria-label="Judge">
      <span className="judge-summary">{judgeSummary(judgments)}</span>
      <div className="judge-judgments">
        {judgments.map((judgment) => (
          <details key={judgment.provider_attempt_id ?? `${judgment.subject}:${judgment.criterion_id ?? ''}`} className="judge-judgment" data-state={judgment.state}>
            <summary>
              <span className="judge-subject">{judgmentLabel(judgment)}</span>
              <span className="judge-verdict">{verdictLabel(judgment)}</span>
            </summary>
            <dl>
              {judgmentDetails(judgment).filter((entry): entry is [string, string] => entry[1] !== undefined && entry[1] !== '').map(([label, value]) => (
                <div key={label}><dt>{label}</dt><dd>{value}</dd></div>
              ))}
            </dl>
          </details>
        ))}
      </div>
    </div>
  );
}
