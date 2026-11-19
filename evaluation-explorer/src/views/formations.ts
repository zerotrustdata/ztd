// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { TERMINAL_STATUSES, type ConfidenceInterval, type EvaluationSummary, type FormationSummary, type TerminalStatus } from '../contract/types';
import { leaderboardScore, leaderboardSizeFreeScore } from './derived';

export interface FormationSource {
  run: EvaluationSummary;
  summary: FormationSummary;
}

export interface FormationGroup {
  key: string;
  suite_id: string;
  summary: FormationSummary;
  sources: FormationSource[];
}

/** Suites and frozen stack definitions stay separate, including project overrides. */
export function formationGroups(runs: EvaluationSummary[]): FormationGroup[] {
  const groups = new Map<string, FormationSource[]>();
  for (const run of runs) {
    if (run.native_result || run.evaluation_unit !== 'system') continue;
    for (const summary of run.formations ?? []) {
      const key = JSON.stringify([run.suite_id, summary.stack_id, summary.stack_digest]);
      const sources = groups.get(key) ?? [];
      sources.push({ run, summary });
      groups.set(key, sources);
    }
  }
  return Array.from(groups, ([key, sources]) => {
    sources.sort((a, b) => b.run.observed_at.localeCompare(a.run.observed_at));
    const first = sources[0]!;
    if (sources.length === 1) return { key, suite_id: first.run.suite_id, sources, summary: first.summary };
    const pooledRate = (field: 'pass_rate' | 'security_pass_rate'): ConfidenceInterval | undefined => {
      const rates = sources.map(({ summary }) => summary[field]);
      // A missing contributor must not inherit a different run's security evidence.
      if (rates.some((rate) => !rate)) return undefined;
      const denominator = rates.reduce((sum, rate) => sum + rate!.denominator, 0);
      if (denominator === 0) return undefined;
      const estimate = rates.reduce((sum, rate) => sum + rate!.estimate * rate!.denominator, 0) / denominator;
      return { estimate, lower: estimate, upper: estimate, denominator };
    };
    const metric = (field: 'latency_p50_ms' | 'output_tokens' | 'output_throughput_p50') => {
      let weighted = 0;
      let count = 0;
      for (const { summary } of sources) {
        const value = summary[field].value;
        if (value === undefined) return { unavailable_reason: 'incomplete_contributor_evidence' };
        const weight = summary.pass_rate?.denominator ?? 0;
        weighted += value * weight;
        count += weight;
      }
      return count > 0 ? { value: weighted / count } : { unavailable_reason: 'source_unavailable' };
    };
    const stableSpecification = (field: 'parameter_count' | 'artifact_size_bytes') =>
      sources.every(({ summary }) => summary[field] === first.summary[field]) ? first.summary[field] : undefined;
    return {
      key, suite_id: first.run.suite_id, sources,
      summary: {
        ...first.summary,
        assignment_total: sources.reduce((sum, { summary }) => sum + summary.assignment_total, 0),
        terminal_outcomes: Object.fromEntries(TERMINAL_STATUSES.map((status) => [status,
          sources.reduce((sum, { summary }) => sum + summary.terminal_outcomes[status], 0),
        ])) as Record<TerminalStatus, number>,
        pass_rate: pooledRate('pass_rate'), security_pass_rate: pooledRate('security_pass_rate'),
        latency_p50_ms: metric('latency_p50_ms'), output_tokens: metric('output_tokens'), output_throughput_p50: metric('output_throughput_p50'),
        parameter_count: stableSpecification('parameter_count'), artifact_size_bytes: stableSpecification('artifact_size_bytes'),
        run_seconds: sources.every(({ summary }) => summary.run_seconds !== undefined)
          ? sources.reduce((sum, { summary }) => sum + summary.run_seconds!, 0) : undefined,
      },
    };
  });
}

/** Complete, verified evidence for a formation leaderboard row, apart from its size. */
function hasFormationEvidence({ run, summary }: FormationSource): boolean {
  return run.lifecycle_state === 'completed' && run.verifier_state === 'passed' && run.quality_state === 'exploratory_verified' &&
    summary.assignment_total > 0 &&
    TERMINAL_STATUSES.reduce((sum, status) => sum + summary.terminal_outcomes[status], 0) === summary.assignment_total &&
    (summary.pass_rate?.denominator ?? 0) > 0 &&
    (summary.latency_p50_ms.value ?? 0) > 0 && (summary.output_tokens.value ?? 0) > 0;
}

function qualifies(source: FormationSource): boolean {
  return hasFormationEvidence(source) && (source.summary.parameter_count ?? 0) > 0;
}

export function qualifiedFormationRuns(runs: EvaluationSummary[]): EvaluationSummary[] {
  return runs.filter((run) => run.evaluation_unit === 'system' && !run.native_result && run.formations?.some((summary) => qualifies({ run, summary })));
}

/** Reports whether a member variant of a run's dataset is served by a hosted provider. */
export type HostedMemberLookup = (datasetId: string, variantId: string) => boolean;

export interface FormationLeaderboardRow extends FormationGroup {
  rank: number;
  score: number;
  /** Hosted member variants, sorted; empty for an all-local formation. */
  hosted_variants: string[];
}

function hostedVariants(sources: FormationSource[], isHostedMember: HostedMemberLookup): string[] {
  return Array.from(new Set(sources.flatMap(({ run, summary }) =>
    Object.values(summary.model_role_mapping).filter((variant) => isHostedMember(run.dataset_id, variant))))).sort();
}

function bySuite<T extends FormationGroup>(groups: T[]): T[][] {
  const suites = new Map<string, T[]>();
  for (const group of groups) {
    const suite = suites.get(group.suite_id) ?? [];
    suite.push(group);
    suites.set(group.suite_id, suite);
  }
  return Array.from(suites.values());
}

/** Size-adjusted ranking of all-local formations only. Hosted-member formations never enter
 *  these suite minimums, so their presence leaves every all-local score unchanged. */
export function formationLeaderboardRows(runs: EvaluationSummary[], isHostedMember: HostedMemberLookup, maxArtifactGiB = 0): FormationLeaderboardRow[] {
  const qualified = runs.map((run) => ({ ...run, formations: run.formations?.filter((summary) =>
    qualifies({ run, summary }) && (!(maxArtifactGiB > 0) ||
      (summary.artifact_size_bytes !== undefined && summary.artifact_size_bytes <= maxArtifactGiB * 1024 ** 3)),
  ) }));
  const groups = formationGroups(qualified)
    .map((group) => ({ ...group, hosted_variants: hostedVariants(group.sources, isHostedMember) }))
    .filter(({ summary, hosted_variants }) => hosted_variants.length === 0 &&
      (summary.parameter_count ?? 0) > 0 && (summary.pass_rate?.denominator ?? 0) > 0);
  const measurements = ({ summary }: FormationGroup) => ({
    latency: summary.latency_p50_ms.value!, tokens: summary.output_tokens.value!, size: summary.parameter_count!,
  });
  return bySuite(groups).flatMap((suite) => {
    const measured = suite.map(measurements);
    const best = {
      latency: Math.min(...measured.map((m) => m.latency)), tokens: Math.min(...measured.map((m) => m.tokens)), size: Math.min(...measured.map((m) => m.size)),
    };
    return suite.map((group) => ({
      ...group, rank: 0,
      score: leaderboardScore(group.summary.pass_rate!.estimate, group.summary.security_pass_rate?.estimate ?? 0, measurements(group), best),
    })).sort((a, b) => b.score - a.score || b.summary.pass_rate!.estimate - a.summary.pass_rate!.estimate ||
      a.summary.parameter_count! - b.summary.parameter_count! || a.summary.stack_id.localeCompare(b.summary.stack_id))
      .map((group, index) => ({ ...group, rank: index + 1 }));
  });
}

/** Size-free ranking, normalized within each suite across the given population. */
function sizeFreeFormationRows(
  runs: EvaluationSummary[], isHostedMember: HostedMemberLookup, maxArtifactGiB: number, scope: 'all' | 'hosted',
): FormationLeaderboardRow[] {
  const admitted = runs.map((run) => ({ ...run, formations: run.formations?.filter((summary) =>
    hasFormationEvidence({ run, summary }) && (!(maxArtifactGiB > 0) ||
      (summary.artifact_size_bytes !== undefined && summary.artifact_size_bytes <= maxArtifactGiB * 1024 ** 3)),
  ) }));
  const groups = formationGroups(admitted)
    .map((group) => ({ ...group, hosted_variants: hostedVariants(group.sources, isHostedMember) }))
    .filter(({ summary, hosted_variants }) => (scope === 'all' || hosted_variants.length > 0) &&
      (summary.pass_rate?.denominator ?? 0) > 0 && (summary.latency_p50_ms.value ?? 0) > 0 && (summary.output_tokens.value ?? 0) > 0);
  const measurements = ({ summary }: FormationGroup) => ({ latency: summary.latency_p50_ms.value!, tokens: summary.output_tokens.value! });
  return bySuite(groups).flatMap((suite) => {
    const measured = suite.map(measurements);
    const best = { latency: Math.min(...measured.map((m) => m.latency)), tokens: Math.min(...measured.map((m) => m.tokens)) };
    return suite.map((group) => ({
      ...group, rank: 0,
      score: leaderboardSizeFreeScore(group.summary.pass_rate!.estimate, group.summary.security_pass_rate?.estimate ?? 0, measurements(group), best),
    })).sort((a, b) => b.score - a.score || b.summary.pass_rate!.estimate - a.summary.pass_rate!.estimate ||
      a.summary.stack_id.localeCompare(b.summary.stack_id))
      .map((group, index) => ({ ...group, rank: index + 1 }));
  });
}

/** Overall: every formation with complete evidence, all-local and hosted-member alike, ranked side by side
 *  with the size-free score used by the model Overall leaderboard. */
export function formationOverallRows(runs: EvaluationSummary[], isHostedMember: HostedMemberLookup, maxArtifactGiB = 0): FormationLeaderboardRow[] {
  return sizeFreeFormationRows(runs, isHostedMember, maxArtifactGiB, 'all');
}

/** Remote-hosted: formations with at least one hosted member, normalized among themselves only. */
export function formationRemoteHostedRows(runs: EvaluationSummary[], isHostedMember: HostedMemberLookup, maxArtifactGiB = 0): FormationLeaderboardRow[] {
  return sizeFreeFormationRows(runs, isHostedMember, maxArtifactGiB, 'hosted');
}
