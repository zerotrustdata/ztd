// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { releaseLabel } from '../content/release';
import { useStoreState } from '../state/store';
import { formatNumber } from './shared';
import { formatDuration, formatRelativeTime, formatTimestamp } from '../utils/format';
import { recentCampaignRows, roleLabel, terminalOutcomeBreakdown } from '../views/derived';

function formatClock(iso: string | undefined): string {
  if (!iso) return 'Unavailable';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return 'Unavailable';
  return date.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
}

/** Most recent run for each dataset in the feed. */
export function RunsPanel() {
  const catalogs = useStoreState((state) => Array.from(state.catalogs.values()));
  const evaluations = useStoreState((state) => Array.from(state.evaluations.values()));
  const models = useStoreState((state) => Array.from(state.models.values()));
  const recent = useMemo(
    () => recentCampaignRows(catalogs, evaluations, models),
    [catalogs, evaluations, models],
  );
  return (
    <section className="panel campaign-evidence-panel" aria-label="Runs">
      <div className="panel-head campaign-evidence-head">
        <div>
          <h2>Runs</h2>
        </div>
        <Link to="/evaluations" className="panel-link">
          Explore all →
        </Link>
      </div>
      <ul className="campaign-legend" aria-label="Assignment outcome legend">
        <li><span className="campaign-legend-swatch campaign-legend-passed" />Passed</li>
        <li><span className="campaign-legend-swatch campaign-legend-failed" />Model failed</li>
        <li title="Ended without a verdict on the model: unavailable, invalid evidence, provider or harness failure, or stopped">
          <span className="campaign-legend-swatch campaign-legend-no-verdict" />No verdict
        </li>
        <li><span className="campaign-legend-swatch campaign-legend-remaining" />Remaining</li>
      </ul>
      {recent.length === 0 ? (
        <p className="panel-empty">No runs recorded yet.</p>
      ) : (
        <ul className="campaign-evidence-list" aria-label="Recent runs" tabIndex={0}>
          {recent.map((campaign) => {
            const status = campaign.runStatus;
            const href = campaign.runId
              ? `/evaluations/${campaign.datasetId}/${campaign.runId}`
              : `/?dataset=${campaign.datasetId}`;
            const breakdown = terminalOutcomeBreakdown(campaign.terminalOutcomes);
            const passed = breakdown?.verdicts.find((entry) => entry.status === 'completed')?.count ?? 0;
            const failed = breakdown?.verdicts.find((entry) => entry.status === 'model_failed')?.count ?? 0;
            const noVerdict = breakdown?.nonVerdictCount ?? 0;
            const terminal = breakdown?.total ?? 0;
            const total = Math.max(campaign.assignmentTotal, terminal);
            const share = (count: number) => (total > 0 ? (count / total) * 100 : 0);
            const noVerdictTitle = breakdown?.nonVerdicts
              .map((entry) => `${formatNumber(entry.count)} ${entry.status.replace(/_/g, ' ')}`)
              .join(', ');
            const verification = campaign.verifierState === 'passed'
              ? 'Verification passed'
              : campaign.verifierState === 'failed'
                ? 'Verification failed'
                : status === 'running' || status === 'scheduled'
                  ? 'Verification pending'
                  : campaign.verifierState === 'not_applicable'
                    ? 'Verification not applicable'
                    : 'Verification not run';
            const title = campaign.modelName ?? campaign.label;
            const titleModel = campaign.models.find((model) => model.role === 'primary') ?? campaign.models[0];
            const otherModels = campaign.models.filter((model) => model.name !== campaign.modelName);
            const startedAt = campaign.startedAt ?? campaign.observedAt;
            return (
              <li key={campaign.datasetId} className="campaign-evidence-row">
                <div className="campaign-evidence-title-row">
                  {titleModel ? (
                    <Link
                      to={`/models/${campaign.datasetId}/${titleModel.variantId}?role=${titleModel.role}`}
                      className="campaign-evidence-title"
                      title={title}
                    >
                      {title}
                    </Link>
                  ) : (
                    <span className="campaign-evidence-title" title={title}>{title}</span>
                  )}
                  <span className={`campaign-evidence-state status-${status}`} title={status === 'unknown' ? 'Run status unavailable: no process-aware host status has been published.' : `Last published run status: ${status}`}>
                    <span className="status-dot" aria-hidden="true" />
                    {status}
                  </span>
                </div>
                <div className="campaign-evidence-meta">
                  <span title={campaign.runId ?? campaign.label}>
                    <Link to={href}>{campaign.runId ?? campaign.label}</Link>
                  </span>
                  <span>{campaign.detail}</span>
                  <span>{releaseLabel(campaign)}</span>
                </div>
                {otherModels.length > 0 ? (
                  <ul className="campaign-evidence-roles" aria-label="Models by role">
                    {campaign.models.map((model) => (
                      <li key={model.role} title={model.variantId}>
                        <span className="campaign-evidence-role">{roleLabel(model.role)}</span>{' '}
                        <Link to={`/models/${campaign.datasetId}/${model.variantId}?role=${model.role}`}>
                          {model.name}
                        </Link>
                      </li>
                    ))}
                  </ul>
                ) : null}
                {campaign.runId && total > 0 ? (
                  <>
                    <div
                      className="campaign-outcome-track"
                      role="progressbar"
                      aria-label={`${title} assignment outcomes`}
                      aria-valuemin={0}
                      aria-valuemax={total}
                      aria-valuenow={terminal}
                      aria-valuetext={`${passed} passed, ${failed} model failed, ${noVerdict} no verdict, ${total - terminal} remaining`}
                    >
                      <span className="campaign-outcome-passed" style={{ width: `${share(passed)}%` }} title={`${formatNumber(passed)} passed`} />
                      <span className="campaign-outcome-failed" style={{ width: `${share(failed)}%` }} title={`${formatNumber(failed)} model failed`} />
                      <span className="campaign-outcome-no-verdict" style={{ width: `${share(noVerdict)}%` }} title={noVerdictTitle} />
                    </div>
                    <div className="campaign-evidence-foot">
                      <span>
                        {formatNumber(passed)} passed · {formatNumber(failed)} failed · {formatNumber(noVerdict)} no verdict
                        {total > terminal ? ` · ${formatNumber(total - terminal)} remaining` : ''}
                      </span>
                      <span className={campaign.verifierState === 'failed' ? 'campaign-verification-failed' : undefined}>{verification}</span>
                    </div>
                  </>
                ) : (
                  <div className="campaign-evidence-foot campaign-evidence-foot-unavailable">
                    <span>Run-level outcomes unavailable</span>
                    <span>{verification}</span>
                  </div>
                )}
                <div className="campaign-evidence-times">
                  <span title={formatTimestamp(startedAt)}>
                    Started {formatClock(startedAt)} · {formatRelativeTime(startedAt)}
                  </span>
                  {campaign.runSeconds !== undefined ? (
                    <span title={`Time spent executing assignments; stop/resume gaps excluded${campaign.endedAt ? `. Ended ${formatTimestamp(campaign.endedAt)}` : ''}`}>
                      Run time {formatDuration(campaign.runSeconds)}
                    </span>
                  ) : null}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

