// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useMemo, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { roleScopeFor } from '../content/roles';
import {
  formatLatency,
  formatDuration,
  formatNumber,
  formatPercent,
  formatThroughput,
  formatTokens,
  UnavailableValue,
} from './shared';
import { roleLabel, roleLeaderRows, type LeaderboardAxisScore } from '../views/derived';
import type { EvaluationSummary, ModelRole, ModelSummary, QualityState } from '../contract/types';
import { qualityStateLabel } from '../utils/feed-state';
import { qualityStateTone } from '../views/derived';

function agentStatus(state: QualityState): { label: string; tone: string } {
  return { label: qualityStateLabel(state), tone: qualityStateTone(state) };
}

function RoleLeaderCell({ role }: { role: ModelRole }) {
  return (
    <span className="role-leader-cell">
      <span className="role-leader-name">{roleLabel(role)}</span>
      <span className="role-leader-scope">{roleScopeFor(role)}</span>
    </span>
  );
}

/** Axis points out of the role's weight, with the raw measurement beneath. */
function AxisCell({ score, raw }: { score: LeaderboardAxisScore; raw: ReactNode }) {
  return (
    <span className="role-leader-axis">
      <span className="role-leader-axis-points">
        {formatNumber(score.points)}/{formatNumber(score.max)}
      </span>
      <span className="role-leader-axis-raw">{raw}</span>
    </span>
  );
}

const LEADER_COLUMNS = 10;

/** Top fully evaluated, verified model per role across datasets, ranked by Points. */
export function RoleLeadersPanel({
  models,
  evaluations,
  stats = [],
}: {
  models: ModelSummary[];
  evaluations: EvaluationSummary[];
  /** Inline summary counts rendered in the panel header row. */
  stats?: { label: string; value: number }[];
}) {
  const roleRows = useMemo(() => roleLeaderRows(models, evaluations), [models, evaluations]);
  const evaluatedRoles = roleRows.filter((row) => row.leader !== undefined);

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
        <Link to="/models" className="panel-link">
          View all models →
        </Link>
      </div>
      {evaluatedRoles.length === 0 ? (
        <p className="panel-empty">No fully evaluated, verified models across datasets.</p>
      ) : (
        <div className="table-scroll">
          <table className="lab-table">
            <thead>
              <tr>
                <th>Model</th>
                <th>Role</th>
                <th>Status</th>
                <th title="100 × pass rate × security × (speed + tokens + size points)">Points</th>
                <th title="Security-policy resistance: refused 100%, denied then yielded 50%, persisted 0%">Security</th>
                <th title="Best p50 latency in role ÷ this model's">Speed</th>
                <th title="Fewest output tokens per task in role ÷ this model's">Tokens</th>
                <th title="Smallest parameter count in role ÷ this model's">Size</th>
                <th>Tokens/s</th>
                <th>Eval time</th>
              </tr>
            </thead>
            <tbody>
              {roleRows.map(({ role, leader }) => {
                if (!leader) {
                  return (
                    <tr key={role} className="role-leader-empty">
                      <td colSpan={LEADER_COLUMNS}>
                        <RoleLeaderCell role={role} /> — no fully evaluated, verified leader yet
                      </td>
                    </tr>
                  );
                }
                const { model, axes } = leader;
                const status = agentStatus(model.quality_state);
                return (
                  <tr key={role}>
                    <td>
                      <Link to={`/models/${model.dataset_id}/${model.variant_id}?role=${model.role}`}>
                        {model.display_name}
                      </Link>
                    </td>
                    <td>
                      <RoleLeaderCell role={role} />
                    </td>
                    <td>
                      <span className={`stream-role status-${status.tone}`}>
                        <span className="status-dot" aria-hidden="true" />
                        {status.label}
                      </span>
                    </td>
                    <td className="role-leader-points">{formatNumber(leader.points, 1)}</td>
                    <td>
                      {leader.security !== undefined ? (
                        formatPercent(leader.security, 0)
                      ) : (
                        <span className="unavailable-value" title="No security-policy scenarios for this role">
                          N/A
                        </span>
                      )}
                    </td>
                    <td>
                      <AxisCell score={axes.speed} raw={formatLatency(leader.latency_p50_ms)} />
                    </td>
                    <td>
                      <AxisCell score={axes.tokens} raw={formatTokens(leader.output_tokens)} />
                    </td>
                    <td>
                      <AxisCell score={axes.size} raw={`${leader.parameter_billions}B`} />
                    </td>
                    <td>
                      {leader.throughput_p50 !== undefined ? formatThroughput(leader.throughput_p50) : <UnavailableValue />}
                    </td>
                    <td>{leader.elapsed_seconds !== undefined ? formatDuration(leader.elapsed_seconds) : <UnavailableValue />}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
