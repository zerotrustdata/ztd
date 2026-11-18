// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { formatParameterCount } from '../content/framework-catalog';
import { formatProviderClassLabel, isHostedProviderClass } from '../content/provider';
import { MODEL_ROLE_WIRE_ORDER, roleScopeFor } from '../content/roles';
import {
  formatLatency,
  formatDuration,
  formatNumber,
  formatPercent,
  formatTokens,
  formatRank,
  UnavailableValue,
} from './shared';
import { formatCompact } from '../utils/format';
import {
  LEADERBOARD_SCORE_FORMULA,
  LEADERBOARD_SIZE_FREE_SCORE_FORMULA,
  LEADERBOARD_SECURITY_SCORING_DESCRIPTION,
  roleLabel,
  hostedRoleLeaderboardRows,
  modelRoleLeaderboardRows,
  modelOverallLeaderboardRows,
  modelCombinedOverallLeaderboardRows,
  modelRemoteHostedLeaderboardRows,
  type HostedLeaderboardRow,
  type ModelLeaderboardRow,
  type ModelOverallLeaderboardRow,
  type ModelRoleLeaderboardRow,
} from '../views/derived';
import type { EvaluationSummary, ModelRole, ModelSummary } from '../contract/types';
import { ModelScoreCoverage } from './evidence';
import { FormationTable } from './FormationTable';
import { formationLeaderboardRows, hostedFormationRows } from '../views/formations';
import { PREF, useUserPref } from '../state/dataset';

export type LeaderboardSortField =
  | 'rank'
  | 'model'
  | 'role'
  | 'score'
  | 'accuracy'
  | 'security'
  | 'speed'
  | 'tokens'
  | 'size'
  | 'throughput'
  | 'run_time';

interface LeaderboardColumnDef {
  field: LeaderboardSortField;
  label: string;
  title: string;
  initialDirection: 'asc' | 'desc';
}

const LEADERBOARD_COLUMNS: LeaderboardColumnDef[] = [
  {
    field: 'rank',
    label: 'Rank',
    title: 'Rank within category or overall',
    initialDirection: 'asc',
  },
  {
    field: 'model',
    label: 'Model',
    title: 'Model variant name',
    initialDirection: 'asc',
  },
  {
    field: 'role',
    label: 'Role',
    title: 'Designated role (Primary, Assistant, Lite)',
    initialDirection: 'asc',
  },
  {
    field: 'score',
    label: 'Score',
    title: `${LEADERBOARD_SCORE_FORMULA}; ${LEADERBOARD_SECURITY_SCORING_DESCRIPTION}; Total-parameter penalty: each doubling costs 10%, and speed and tokens are best-in-role ÷ model, so efficiency moves the score by at most 15%`,
    initialDirection: 'desc',
  },
  {
    field: 'accuracy',
    label: 'Accuracy',
    title: 'Accuracy: overall task pass rate for this designated role',
    initialDirection: 'desc',
  },
  {
    field: 'security',
    label: 'Security',
    title: `Security: pass rate on security category tasks. ${LEADERBOARD_SECURITY_SCORING_DESCRIPTION}`,
    initialDirection: 'desc',
  },
  {
    field: 'speed',
    label: 'Speed',
    title: 'Assignment-count-weighted mean of run p50 latencies; faster is better',
    initialDirection: 'desc',
  },
  {
    field: 'tokens',
    label: 'Tokens',
    title: 'Assignment-count-weighted mean of run median output tokens per assignment; fewer is better',
    initialDirection: 'desc',
  },
  {
    field: 'size',
    label: 'Parameters',
    title: 'Total-parameter penalty: each doubling of the frozen provider parameter count over the smallest qualifying count in the role costs 10% of the score. This is not measured memory or compute cost.',
    initialDirection: 'desc',
  },
  {
    field: 'throughput',
    label: 'Tokens/s',
    title: 'Throughput: tokens generated per second',
    initialDirection: 'desc',
  },
  {
    field: 'run_time',
    label: 'Run time',
    title: 'Run time: time spent executing assignments, excluding stop/resume and verification gaps; smaller is better',
    initialDirection: 'desc',
  },
];

const OVERALL_COLUMN_TITLES: Partial<Record<LeaderboardSortField, string>> = {
  score: `Self-hosted score = (Primary score + Assistant score + Lite score) / 3; all qualified runs are pooled within each role before scoring. ${LEADERBOARD_SCORE_FORMULA}. ${LEADERBOARD_SECURITY_SCORING_DESCRIPTION}`,
  accuracy: 'Mean accuracy across Primary, Assistant, and Lite, with equal weight per role',
  security: 'Mean security score across roles with applicable security scenarios',
  speed: 'Mean of role latency aggregates (assignment-count-weighted run p50s); faster is better',
  tokens: 'Mean output tokens per task across roles; fewer is better',
  throughput: 'Mean of role throughput aggregates (assignment-count-weighted run p50s); unavailable if any contributor lacks throughput',
  run_time: 'Total run time of distinct contributing datasets; shared runs count once',
};

const SIZE_FREE_SCORE_TITLE =
  `Overall score = (Primary score + Assistant score + Lite score) / 3; all qualified runs are pooled within each role before scoring. ${LEADERBOARD_SIZE_FREE_SCORE_FORMULA} (no parameter-size penalty). ${LEADERBOARD_SECURITY_SCORING_DESCRIPTION}`;

export type LeaderboardCategory = 'all' | 'self-hosted' | 'remote-hosted' | ModelRole | 'formations';

const VALID_CATEGORIES = new Set<LeaderboardCategory>([
  'all',
  'self-hosted',
  'remote-hosted',
  'primary',
  'assistant',
  'lite',
  'formations',
]);

/** A ranked role or overall row, or an unranked hosted row shown beside them. */
type LeaderboardTableRow = ModelLeaderboardRow | HostedLeaderboardRow;

const HOSTED_UNRANKED_TITLE =
  'Not ranked: hosted providers do not disclose parameter counts, so the size factor and score cannot be computed. Measurements are shown for comparison.';

function isRankedRow(row: LeaderboardTableRow): row is ModelLeaderboardRow {
  return row.rank !== undefined;
}

function compareNullableNumber(
  a: number | undefined,
  b: number | undefined,
  smallerIsBetter: boolean,
): number {
  if (a === undefined && b === undefined) return 0;
  if (a === undefined) return 1;
  if (b === undefined) return -1;
  return smallerIsBetter ? a - b : b - a;
}

function compareLeaderboardRows(
  a: LeaderboardTableRow,
  b: LeaderboardTableRow,
  field: LeaderboardSortField,
  direction: 'asc' | 'desc',
): number {
  if (field === 'rank') {
    const aRanked = isRankedRow(a);
    const bRanked = isRankedRow(b);
    if (!aRanked || !bRanked) {
      if (aRanked !== bRanked) return aRanked ? -1 : 1;
      return b.pass_rate - a.pass_rate || a.model.display_name.localeCompare(b.model.display_name);
    }
    const diff = a.rank - b.rank;
    return direction === 'asc' ? diff : -diff;
  }
  if (field === 'score') {
    const aRanked = isRankedRow(a);
    const bRanked = isRankedRow(b);
    if (!aRanked || !bRanked) {
      if (aRanked !== bRanked) return aRanked ? -1 : 1;
      return b.pass_rate - a.pass_rate || a.model.display_name.localeCompare(b.model.display_name);
    }
    const diff = b.score - a.score || a.rank - b.rank;
    const result = direction === 'desc' ? diff : -diff;
    return result !== 0 ? result : a.model.display_name.localeCompare(b.model.display_name);
  }
  if (field === 'size') {
    if (a.parameter_count === undefined && b.parameter_count === undefined) {
      return (b.score ?? b.pass_rate) - (a.score ?? a.pass_rate) ||
        a.model.display_name.localeCompare(b.model.display_name);
    }
    if (a.parameter_count === undefined) return 1;
    if (b.parameter_count === undefined) return -1;
    const diff = a.parameter_count - b.parameter_count;
    const result = direction === 'desc' ? diff : -diff;
    return result !== 0 ? result : a.model.display_name.localeCompare(b.model.display_name);
  }
  let diff = 0;
  switch (field) {
    case 'model':
      diff = a.model.display_name.localeCompare(b.model.display_name);
      return direction === 'asc' ? diff : -diff;
    case 'role':
      diff = a.model.role.localeCompare(b.model.role);
      return direction === 'asc' ? diff : -diff;
    case 'accuracy':
      // Higher pass rate is better
      diff = b.pass_rate - a.pass_rate;
      break;
    case 'security':
      // Higher security pass rate is better
      diff = compareNullableNumber(a.security, b.security, false);
      break;
    case 'speed':
      // Faster (smaller latency) is better: smaller the better
      diff = a.latency_p50_ms - b.latency_p50_ms;
      break;
    case 'tokens':
      // Fewer tokens is better: smaller the better
      diff = a.output_tokens - b.output_tokens;
      break;
    case 'throughput':
      // Higher throughput is better
      diff = compareNullableNumber(a.throughput_p50, b.throughput_p50, false);
      break;
    case 'run_time':
      // Shorter run time is better: smaller the better
      diff = compareNullableNumber(a.run_seconds, b.run_seconds, true);
      break;
  }

  // 'desc' is BEST FIRST (diff). 'asc' is WORST FIRST (reverse: -diff).
  const result = direction === 'desc' ? diff : -diff;
  if (result !== 0) return result;
  return a.model.display_name.localeCompare(b.model.display_name);
}

function RoleBadge({ role }: { role: ModelRole }) {
  return (
    <span className={`role-badge role-badge-${role}`} title={roleScopeFor(role)}>
      {roleLabel(role)}
    </span>
  );
}

function RankBadge({ rank }: { rank: number }) {
  const rankClass = rank <= 3 ? `rank-${rank}` : 'rank-default';
  return (
    <span className={`leaderboard-rank ${rankClass}`}>
      {formatRank(rank)}
    </span>
  );
}

/** Leaderboard browser across categories and overall, ranked by Score with sortable statistical columns. */
export function RoleLeadersPanel({
  models,
  evaluations,
  stats = [],
  maxArtifactGiB = 0,
}: {
  maxArtifactGiB?: number;
  models: ModelSummary[];
  evaluations: EvaluationSummary[];
  /** Inline summary counts rendered in the panel header row. */
  stats?: { label: string; value: number }[];
}) {
  // Only an explicit click is stored; with no stored choice the default is derived from the release's results.
  const [storedCategory, setStoredCategory] = useUserPref<LeaderboardCategory | null>(PREF.leaderboardCategory, null);
  const [sortField, setSortField] = useState<LeaderboardSortField>('score');
  const [sortDirection, setSortDirection] = useState<'asc' | 'desc'>('desc');

  const formationRows = useMemo(() => formationLeaderboardRows(evaluations, maxArtifactGiB), [evaluations, maxArtifactGiB]);
  const hostedFormations = useMemo(() => {
    const hostedMembers = new Set(models.filter((model) => isHostedProviderClass(model.backend_provider_class))
      .map((model) => JSON.stringify([model.dataset_id, model.variant_id])));
    return hostedFormationRows(evaluations, (datasetId, variantId) => hostedMembers.has(JSON.stringify([datasetId, variantId])), maxArtifactGiB);
  }, [models, evaluations, maxArtifactGiB]);
  const formationSuites = Array.from(new Set([...formationRows, ...hostedFormations].map((row) => row.suite_id)));
  const qualifiedRows = useMemo(
    () => modelRoleLeaderboardRows(models, 'all', evaluations),
    [models, evaluations],
  );
  const selfHostedRows = useMemo(
    () => modelOverallLeaderboardRows(qualifiedRows),
    [qualifiedRows],
  );
  const overallRows = useMemo(
    () => modelCombinedOverallLeaderboardRows(models, evaluations),
    [models, evaluations],
  );
  const remoteHostedRows = useMemo(
    () => modelRemoteHostedLeaderboardRows(models, evaluations),
    [models, evaluations],
  );
  const rowsByRole = useMemo(() => {
    const forRole = (role: ModelRole) => qualifiedRows
      .filter((row) => row.model.role === role)
      .map((row, index) => ({ ...row, rank: index + 1 }));
    return { primary: forRole('primary'), assistant: forRole('assistant'), lite: forRole('lite') };
  }, [qualifiedRows]);
  const hostedRows = useMemo(() => hostedRoleLeaderboardRows(models, 'all', evaluations), [models, evaluations]);
  const hostedByRole = useMemo(() => {
    const forRole = (role: ModelRole) => hostedRows.filter((row) => row.model.role === role);
    return { primary: forRole('primary'), assistant: forRole('assistant'), lite: forRole('lite') };
  }, [hostedRows]);
  const evaluatedRoles = MODEL_ROLE_WIRE_ORDER.filter(
    (role) => rowsByRole[role].length > 0 || hostedByRole[role].length > 0,
  );
  const hasAnyModelRows = overallRows.length > 0 || qualifiedRows.length > 0 || hostedRows.length > 0;
  const resolvedStoredCategory = storedCategory && VALID_CATEGORIES.has(storedCategory) ? storedCategory : null;
  const category: LeaderboardCategory = resolvedStoredCategory ?? (!hasAnyModelRows && formationRows.length > 0 ? 'formations' : 'all');
  const isOverallScope = category === 'all' || category === 'self-hosted' || category === 'remote-hosted';
  // Declared union: an overall row is structurally a role row, so inference would collapse it.
  const categoryRows: Array<ModelRoleLeaderboardRow | ModelOverallLeaderboardRow | HostedLeaderboardRow> =
    category === 'all'
      ? overallRows
      : category === 'self-hosted'
        ? selfHostedRows
        : category === 'remote-hosted'
          ? remoteHostedRows
          : category === 'formations'
            ? []
            : [...rowsByRole[category], ...hostedByRole[category]];
  const categoryHostedCount = isOverallScope || category === 'formations' ? 0 : hostedByRole[category].length;
  const columns = LEADERBOARD_COLUMNS.filter((column) => !isOverallScope || column.field !== 'role');

  const sortedRows = useMemo(() => {
    return [...categoryRows].sort((a, b) =>
      compareLeaderboardRows(a, b, sortField, sortDirection),
    );
  }, [categoryRows, sortField, sortDirection]);

  const handleSort = (field: LeaderboardSortField) => {
    if (sortField === field) {
      setSortDirection((prev) => (prev === 'asc' ? 'desc' : 'asc'));
    } else {
      const col = LEADERBOARD_COLUMNS.find((c) => c.field === field);
      setSortField(field);
      setSortDirection(col?.initialDirection ?? 'desc');
    }
  };

  const handleCategoryChange = (next: LeaderboardCategory) => {
    setStoredCategory(next);
    setSortField('score');
    setSortDirection('desc');
  };

  const counts = {
    all: overallRows.length,
    selfHosted: selfHostedRows.length,
    remoteHosted: remoteHostedRows.length,
    primary: rowsByRole.primary.length + hostedByRole.primary.length,
    assistant: rowsByRole.assistant.length + hostedByRole.assistant.length,
    lite: rowsByRole.lite.length + hostedByRole.lite.length,
  };
  const rankedCount = sortedRows.filter(isRankedRow).length;

  const showingSubject =
    category === 'formations'
      ? 'formations'
      : category === 'all'
        ? 'models overall'
        : category === 'self-hosted'
          ? 'self-hosted models'
          : category === 'remote-hosted'
            ? 'remote-hosted models'
            : `${roleLabel(category)} leaders`;

  return (
    <section className="evidence-panel role-leaders-panel" aria-label="Leaderboard">
      <div className="panel-head">
        <h2>
          Leaderboard{' '}
          <span className="panel-sub">
            {formationRows.length > 0 && evaluatedRoles.length === 0
              ? `· ${formationRows.length} ranked ${formationRows.length === 1 ? 'formation' : 'formations'}`
              : `· ${evaluatedRoles.length} of ${MODEL_ROLE_WIRE_ORDER.length} roles qualified`}
          </span>
        </h2>
        {stats.length > 0 ? (
          <dl className="panel-head-stats" aria-label="Model summary">
            {stats.map((stat) => (
              <div key={stat.label} data-testid={`stat-${stat.label.replace(/\s+/g, '-').toLowerCase()}`}>
                <dt>{stat.label}</dt>
                <dd>{formatNumber(stat.value)}</dd>
              </div>
            ))}
          </dl>
        ) : null}
      </div>

      <>
          <div className="leaderboard-toolbar">
            <div className="leaderboard-categories" role="group" aria-label="Leaderboard category">
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'all' ? ' active' : ''}`}
                onClick={() => handleCategoryChange('all')}
                aria-pressed={category === 'all'}
              >
                <span>Overall</span>{' '}
                <span className="leaderboard-count-badge">{counts.all}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'self-hosted' ? ' active' : ''}`}
                onClick={() => handleCategoryChange('self-hosted')}
                aria-pressed={category === 'self-hosted'}
              >
                <span>Self-hosted</span>{' '}
                <span className="leaderboard-count-badge">{counts.selfHosted}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'remote-hosted' ? ' active' : ''}`}
                onClick={() => handleCategoryChange('remote-hosted')}
                aria-pressed={category === 'remote-hosted'}
              >
                <span>Remote-hosted</span>{' '}
                <span className="leaderboard-count-badge">{counts.remoteHosted}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'primary' ? ' active' : ''}`}
                onClick={() => handleCategoryChange('primary')}
                aria-pressed={category === 'primary'}
              >
                <span>Primary</span>{' '}
                <span className="leaderboard-count-badge">{counts.primary}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'assistant' ? ' active' : ''}`}
                onClick={() => handleCategoryChange('assistant')}
                aria-pressed={category === 'assistant'}
              >
                <span>Assistant</span>{' '}
                <span className="leaderboard-count-badge">{counts.assistant}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'lite' ? ' active' : ''}`}
                onClick={() => handleCategoryChange('lite')}
                aria-pressed={category === 'lite'}
              >
                <span>Lite</span>{' '}
                <span className="leaderboard-count-badge">{counts.lite}</span>
              </button>
              <button type="button" className={`leaderboard-category-chip${category === 'formations' ? ' active' : ''}`}
                onClick={() => handleCategoryChange('formations')} aria-pressed={category === 'formations'}>
                <span>Formations</span>{' '}<span className="leaderboard-count-badge">{formationRows.length + hostedFormations.length}</span>
              </button>
            </div>
            <span className="leaderboard-meta">
              Showing {category === 'formations' ? formationRows.length : rankedCount} {showingSubject}
              {(category === 'formations' ? hostedFormations.length : categoryHostedCount) > 0
                ? ` · ${category === 'formations' ? hostedFormations.length : categoryHostedCount} hosted unranked`
                : null}
            </span>
          </div>

          {category === 'formations' ? (
            <>
              <p className="panel-note">Each formation is scored as a whole and ranked within its suite. Completed, verified runs contribute in proportion to their judged assignments. Missing security earns zero credit. A formation with a hosted member is listed unranked: hosted providers do not disclose parameter counts, so its size factor and score cannot be computed.</p>
              {formationSuites.length === 0 ? <p className="panel-empty">No fully evaluated, verified formations yet.</p> : formationSuites.map((suite) => (
                <section key={suite} aria-label={`Formation suite ${suite}`}>
                  <h3>{suite}</h3>
                  <FormationTable formations={[
                    ...formationRows.filter((row) => row.suite_id === suite),
                    ...hostedFormations.filter((row) => row.suite_id === suite),
                  ]} leaderboard />
                </section>
              ))}
            </>
          ) : <div className="table-scroll">
            <table className="lab-table">
              {category === 'all' ? (
                <caption className="leaderboard-caption">
                  Side-by-side comparison of self-hosted and remote-hosted models across Primary, Assistant, and Lite using quality and efficiency (no parameter-size penalty or static catalog probe requirement). Models must qualify in all three roles with measured latency and tokens.
                </caption>
              ) : category === 'self-hosted' ? (
                <caption className="leaderboard-caption">
                  Self-hosted models only: averages Primary, Assistant, and Lite scores including the total-parameter size factor. Models must qualify in all three roles and pass all five catalog capability probes.
                </caption>
              ) : category === 'remote-hosted' ? (
                <caption className="leaderboard-caption">
                  Remote-hosted models only (Gemini, Anthropic, OpenAI): averages Primary, Assistant, and Lite scores without a parameter-size penalty, normalized within remote-hosted models. Models must qualify in all three roles with measured latency and tokens.
                </caption>
              ) : categoryHostedCount > 0 ? (
                <caption className="leaderboard-caption">
                  Hosted models are listed unranked in role tabs: their providers do not disclose parameter counts, so the role size factor cannot be computed. They do not change ranked role scores; compare them directly with self-hosted models in Overall.
                </caption>
              ) : null}
              <thead>
                <tr>
                  {columns.map((col) => {
                    const isSorted = sortField === col.field;
                    const headerTitle =
                      (category === 'all' || category === 'remote-hosted') && col.field === 'score'
                        ? SIZE_FREE_SCORE_TITLE
                        : isOverallScope
                          ? (OVERALL_COLUMN_TITLES[col.field] ?? col.title)
                          : col.title;
                    return (
                      <th
                        key={col.field}
                        scope="col"
                        aria-label={col.label}
                        aria-sort={
                          isSorted
                            ? sortDirection === 'asc'
                              ? 'ascending'
                              : 'descending'
                            : 'none'
                        }
                        title={headerTitle}
                      >
                        <button
                          type="button"
                          className="th-sort"
                          onClick={() => handleSort(col.field)}
                          aria-label={`Sort by ${col.label}`}
                        >
                          <span>{col.label}</span>
                          <span className="sort-indicator" aria-hidden="true">
                            {isSorted ? (sortDirection === 'asc' ? '▲' : '▼') : '↕'}
                          </span>
                        </button>
                      </th>
                    );
                  })}
                </tr>
              </thead>
              <tbody>
                {sortedRows.length === 0 ? (
                  <tr className="role-leader-empty">
                    <td colSpan={columns.length}>
                      {category === 'all'
                        ? !hasAnyModelRows ? 'No fully evaluated, verified models across datasets.' : 'No models have qualified results in all three roles yet.'
                        : category === 'self-hosted'
                          ? qualifiedRows.length === 0 ? 'No fully evaluated, verified self-hosted models across datasets.' : 'No self-hosted models have qualified results in all three roles yet.'
                          : category === 'remote-hosted'
                            ? hostedRows.length === 0 ? 'No fully evaluated, verified remote-hosted models across datasets.' : 'No remote-hosted models have qualified results in all three roles yet.'
                            : `No fully evaluated, verified ${roleLabel(category)} models yet.`}
                    </td>
                  </tr>
                ) : (
                  sortedRows.map((row, index) => {
                    const modelHref = `/models/${row.model.dataset_id}/${row.model.variant_id}`;
                    const scoreTitle = 'role_scores' in row
                      ? MODEL_ROLE_WIRE_ORDER.map((role) => `${roleLabel(role)}: ${formatNumber(row.role_scores[role], 1)}`).join(' · ')
                      : undefined;
                    // Positional ranks count ranked rows only; unranked hosted rows take no position.
                    const rankedBefore = sortedRows.slice(0, index).filter(isRankedRow).length;
                    const displayRank = !isRankedRow(row)
                      ? undefined
                      : sortField === 'rank' || sortField === 'model' || sortField === 'role'
                        ? row.rank
                        : sortDirection === 'desc'
                          ? rankedBefore + 1
                          : rankedCount - rankedBefore;
                    return (
                      <tr
                        key={`${row.model.dataset_id}:${row.model.variant_id}:${row.model.model_digest ?? ''}:${row.model.role}`}
                        className={displayRank === undefined ? 'leaderboard-row-unranked' : undefined}
                      >
                        <td className="leaderboard-rank-cell">
                          {displayRank !== undefined ? (
                            <RankBadge rank={displayRank} />
                          ) : (
                            <span className="leaderboard-rank rank-unranked" title={HOSTED_UNRANKED_TITLE} aria-label="Not ranked">—</span>
                          )}
                        </td>
                        <td>
                          <Link to={isOverallScope ? modelHref : `${modelHref}?role=${row.model.role}`}>
                            {row.model.display_name}
                          </Link>
                          <div className="model-identity-sub" title="All qualified run assignments contribute; repetitions add observations, not total-time penalties. Frozen versions remain available in run details.">
                            {new Set(row.sources.map((source) => source.dataset_id)).size} runs · {formatNumber(row.sources.reduce((sum, source) => sum + (source.pass_rate?.denominator ?? 0), 0))} judged assignments
                          </div>
                          {row.model.backend_provider_class ? (
                            <div className="model-identity-sub">
                              <span className="model-provider-tag">{formatProviderClassLabel(row.model.backend_provider_class)}</span>
                            </div>
                          ) : null}
                          {row.model.model_digest ? <div className="model-identity-sub"><code title={`Frozen model digest: ${row.model.model_digest}`}>{row.model.model_digest.slice(0, 12)}</code></div> : null}
                        </td>
                        {!isOverallScope ? (
                          <td><RoleBadge role={row.model.role} /></td>
                        ) : null}
                        <td className="role-leader-score" title={row.score !== undefined ? scoreTitle : HOSTED_UNRANKED_TITLE}>
                          {row.score !== undefined ? formatNumber(row.score, 1) : <span className="unavailable-value">Not ranked</span>}
                        </td>
                        <td className="role-leader-accuracy">{formatPercent(row.pass_rate, 1)}<ModelScoreCoverage models={row.sources} /></td>
                        <td>
                          {row.security !== undefined ? (
                            <>
                              <span title={category === 'lite' ? 'Pass rate on security classification tasks; this does not measure refusal behavior.' : 'Pass rate on judged security tasks.'}>{formatPercent(row.security, 0)}</span>
                              <div className="model-identity-sub" title="Judged security assignments contributing to this row; skipped tool tests are excluded.">
                                {formatNumber(row.sources.reduce((sum, source) => sum + (source.security_pass_rate?.denominator ?? 0), 0))} scored
                              </div>
                            </>
                          ) : (
                            <span className="unavailable-value" title={`No scored security tasks. Tool capability exclusions and other non-verdict outcomes do not measure security. ${LEADERBOARD_SECURITY_SCORING_DESCRIPTION}`}>
                              Not scored
                            </span>
                          )}
                        </td>
                        <td className="role-leader-speed">
                          {formatLatency(row.latency_p50_ms)}
                        </td>
                        <td className="role-leader-tokens">
                          {formatTokens(row.output_tokens)}
                        </td>
                        <td className="role-leader-size">
                          {row.parameter_count !== undefined ? (
                            formatParameterCount(row.parameter_count)
                          ) : isHostedProviderClass(row.model.backend_provider_class) ? (
                            <span className="unavailable-value" title="The hosted provider does not disclose a parameter count.">Undisclosed</span>
                          ) : (
                            <UnavailableValue />
                          )}
                        </td>
                        <td>
                          {row.throughput_p50 !== undefined ? (
                            formatCompact(row.throughput_p50)
                          ) : (
                            <UnavailableValue />
                          )}
                        </td>
                        <td>
                          {row.run_seconds !== undefined ? (
                            formatDuration(row.run_seconds)
                          ) : (
                            <UnavailableValue />
                          )}
                        </td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>}
      </>
    </section>
  );
}
