// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { TERMINAL_STATUSES, type EvaluationHeadlineMetrics, type EvaluationSummary } from '../contract/types';

/** Run success includes every terminal outcome, including provider failures.
 * Derive it from published counts so historical verdict-only headlines also
 * display the total rate. Queued and running assignments have no outcome yet.
 */
export function evaluationPassRate(run: EvaluationSummary): EvaluationHeadlineMetrics['pass_rate'] {
  if (run.native_result) return run.headline_metrics.pass_rate;
  const total = TERMINAL_STATUSES.reduce((sum, status) => sum + run.terminal_outcomes[status], 0);
  return {
    ...(total > 0
      ? { value: run.terminal_outcomes.completed / total }
      : { unavailable_reason: 'source_unavailable' as const }),
    unit: 'ratio',
    observed_count: total,
    eligible_count: total,
    unavailable_count: 0,
  };
}
