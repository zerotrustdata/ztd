// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { roleScopeFor } from '../content/roles';
import {
  formatLatency,
  formatDuration,
  formatNumber,
  formatPercent,
  formatThroughput,
  formatTokens,
  formatRank,
  UnavailableValue,
} from './shared';
import {
  roleLabel,
  roleLeaderRows,
  modelRoleLeaderboardRows,
  type ModelRoleLeaderboardRow,
} from '../views/derived';
import type { AssignmentResult, EvaluationSummary, ModelRole, ModelSummary } from '../contract/types';

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
  | 'eval_time';

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
    title: 'Composite score: accuracy × security factor × efficiency (speed + tokens + size)',
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
    title: 'Security: pass rate on security category tasks',
    initialDirection: 'desc',
  },
  {
    field: 'speed',
    label: 'Speed',
    title: "Speed: faster is better (lowest p50 latency in role)",
    initialDirection: 'desc',
  },
  {
    field: 'tokens',
    label: 'Tokens',
    title: "Tokens: smaller is better (fewest output tokens per task)",
    initialDirection: 'desc',
  },
  {
    field: 'size',
    label: 'Size',
    title: "Size: smaller is better (smallest parameter count)",
    initialDirection: 'desc',
  },
  {
    field: 'throughput',
    label: 'Tokens/s',
    title: 'Throughput: tokens generated per second',
    initialDirection: 'desc',
  },
  {
    field: 'eval_time',
    label: 'Eval time',
    title: 'Evaluation elapsed run duration: smaller is better',
    initialDirection: 'desc',
  },
];

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
  a: ModelRoleLeaderboardRow,
  b: ModelRoleLeaderboardRow,
  field: LeaderboardSortField,
  direction: 'asc' | 'desc',
): number {
  let diff = 0;
  switch (field) {
    case 'rank':
      diff = a.rank - b.rank;
      return direction === 'asc' ? diff : -diff;
    case 'model':
      diff = a.model.display_name.localeCompare(b.model.display_name);
      return direction === 'asc' ? diff : -diff;
    case 'role':
      diff = a.model.role.localeCompare(b.model.role);
      return direction === 'asc' ? diff : -diff;
    case 'score':
      // Higher score is better
      diff = b.score - a.score;
      break;
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
    case 'size':
      // Smaller parameter count is better: smaller the better
      diff = a.parameter_billions - b.parameter_billions;
      break;
    case 'throughput':
      // Higher throughput is better
      diff = compareNullableNumber(a.throughput_p50, b.throughput_p50, false);
      break;
    case 'eval_time':
      // Shorter elapsed duration is better: smaller the better
      diff = compareNullableNumber(a.elapsed_seconds, b.elapsed_seconds, true);
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

const LEADER_COLUMNS = 11;

/** Leaderboard browser across categories and overall, ranked by Score with sortable statistical columns. */
export function RoleLeadersPanel({
  models,
  evaluations,
  assignments = [],
  stats = [],
}: {
  models: ModelSummary[];
  evaluations: EvaluationSummary[];
  assignments?: AssignmentResult[];
  /** Inline summary counts rendered in the panel header row. */
  stats?: { label: string; value: number }[];
}) {
  const [category, setCategory] = useState<ModelRole | 'all'>('all');
  const [sortField, setSortField] = useState<LeaderboardSortField>('score');
  const [sortDirection, setSortDirection] = useState<'asc' | 'desc'>('desc');

  // Role qualification count for the header
  const roleRows = useMemo(
    () => roleLeaderRows(models, evaluations, assignments),
    [models, evaluations, assignments],
  );
  const evaluatedRoles = roleRows.filter((row) => row.leader !== undefined);

  // Compute models for all categories
  const allRows = useMemo(
    () => modelRoleLeaderboardRows(models, 'all', evaluations, assignments),
    [models, evaluations, assignments],
  );
  const primaryRows = useMemo(
    () => modelRoleLeaderboardRows(models, 'primary', evaluations, assignments),
    [models, evaluations, assignments],
  );
  const assistantRows = useMemo(
    () => modelRoleLeaderboardRows(models, 'assistant', evaluations, assignments),
    [models, evaluations, assignments],
  );
  const liteRows = useMemo(
    () => modelRoleLeaderboardRows(models, 'lite', evaluations, assignments),
    [models, evaluations, assignments],
  );

  const categoryRows = useMemo(() => {
    switch (category) {
      case 'primary':
        return primaryRows;
      case 'assistant':
        return assistantRows;
      case 'lite':
        return liteRows;
      case 'all':
      default:
        return allRows;
    }
  }, [category, allRows, primaryRows, assistantRows, liteRows]);

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

  const counts = {
    all: allRows.length,
    primary: primaryRows.length,
    assistant: assistantRows.length,
    lite: liteRows.length,
  };

  return (
    <section className="evidence-panel role-leaders-panel" aria-label="Leaderboard">
      <div className="panel-head">
        <h2>
          Leaderboard{' '}
          <span className="panel-sub">
            · {evaluatedRoles.length} of {roleRows.length} roles qualified
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

      {allRows.length === 0 ? (
        <p className="panel-empty">No fully evaluated, verified models across datasets.</p>
      ) : (
        <>
          <div className="leaderboard-toolbar">
            <div className="leaderboard-categories" role="group" aria-label="Leaderboard category">
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'all' ? ' active' : ''}`}
                onClick={() => setCategory('all')}
                aria-pressed={category === 'all'}
              >
                <span>Overall</span>
                <span className="leaderboard-count-badge">{counts.all}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'primary' ? ' active' : ''}`}
                onClick={() => setCategory('primary')}
                aria-pressed={category === 'primary'}
              >
                <span>Primary</span>
                <span className="leaderboard-count-badge">{counts.primary}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'assistant' ? ' active' : ''}`}
                onClick={() => setCategory('assistant')}
                aria-pressed={category === 'assistant'}
              >
                <span>Assistant</span>
                <span className="leaderboard-count-badge">{counts.assistant}</span>
              </button>
              <button
                type="button"
                className={`leaderboard-category-chip${category === 'lite' ? ' active' : ''}`}
                onClick={() => setCategory('lite')}
                aria-pressed={category === 'lite'}
              >
                <span>Lite</span>
                <span className="leaderboard-count-badge">{counts.lite}</span>
              </button>
            </div>
            <span className="leaderboard-meta">
              Showing {sortedRows.length} {category === 'all' ? 'leaders overall' : `${roleLabel(category)} leaders`}
            </span>
          </div>

          <div className="table-scroll">
            <table className="lab-table">
              <thead>
                <tr>
                  {LEADERBOARD_COLUMNS.map((col) => {
                    const isSorted = sortField === col.field;
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
                        title={col.title}
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
                    <td colSpan={LEADER_COLUMNS}>
                      No fully evaluated, verified {category === 'all' ? '' : `${roleLabel(category)} `}models yet.
                    </td>
                  </tr>
                ) : (
                  sortedRows.map((row, index) => {
                    const displayRank =
                      sortField === 'model' || sortField === 'role'
                        ? row.rank
                        : sortDirection === 'desc'
                          ? index + 1
                          : sortedRows.length - index;
                    return (
                      <tr key={`${row.model.dataset_id}:${row.model.variant_id}:${row.model.role}`}>
                        <td className="leaderboard-rank-cell">
                          <RankBadge rank={displayRank} />
                        </td>
                      <td>
                        <Link to={`/models/${row.model.dataset_id}/${row.model.variant_id}?role=${row.model.role}`}>
                          {row.model.display_name}
                        </Link>
                      </td>
                      <td>
                        <RoleBadge role={row.model.role} />
                      </td>
                      <td className="role-leader-score">{formatNumber(row.score, 1)}</td>
                      <td className="role-leader-accuracy">{formatPercent(row.pass_rate, 1)}</td>
                      <td>
                        {row.security !== undefined ? (
                          formatPercent(row.security, 0)
                        ) : (
                          <span className="unavailable-value" title="No security-policy scenarios for this role">
                            N/A
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
                        {row.parameter_billions}B
                      </td>
                      <td>
                        {row.throughput_p50 !== undefined ? (
                          formatThroughput(row.throughput_p50)
                        ) : (
                          <UnavailableValue />
                        )}
                      </td>
                      <td>
                        {row.elapsed_seconds !== undefined ? (
                          formatDuration(row.elapsed_seconds)
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
          </div>
        </>
      )}
    </section>
  );
}
