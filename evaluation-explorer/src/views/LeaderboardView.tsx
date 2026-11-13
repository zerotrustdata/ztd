// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Leaderboard view — the dedicated Model Leaderboard & Role Performance directory.
// Ranks fully evaluated, verified models and whole formations
// using the composite role score formula.

import { useMemo } from 'react';
import { qualifiedFormationRuns } from './formations';
import { useSelectedRelease, useUserPref, PREF } from '../state/dataset';
import { useStoreState } from '../state/store';
import { RoleLeadersPanel } from '../components/RoleLeadersPanel';
import { ReleaseSelector } from '../components/ReleaseSelector';
import { matchesRelease, toReleaseSeries } from '../content/release';
import { resolveModelProfile } from '../content/framework-catalog';
import { SectionHeading } from '../components/shared';
import {
  defaultLeaderboardRelease,
  LEADERBOARD_SCORE_FORMULA,
  LEADERBOARD_SECURITY_SCORING_DESCRIPTION,
} from './derived';

interface LeaderboardFilterPrefs {
  maxArtifactGiB?: string;
  [key: string]: unknown;
}

export function LeaderboardView() {
  const [filters, setFilters] = useUserPref<LeaderboardFilterPrefs>(PREF.filters, {});
  const allModels = useStoreState((state) => Array.from(state.models.values()));
  const allEvaluations = useStoreState((state) => Array.from(state.evaluations.values()));
  const defaultRelease = useMemo(() => {
    const formationDatasets = new Set(allEvaluations.filter((run) => run.evaluation_unit === 'system').map((run) => run.dataset_id));
    return defaultLeaderboardRelease(allModels.filter((model) => !formationDatasets.has(model.dataset_id)), qualifiedFormationRuns(allEvaluations));
  }, [allModels, allEvaluations]);
  const release = toReleaseSeries(useSelectedRelease(defaultRelease));

  const models = useMemo(() => allModels.filter((record) => matchesRelease(record, release)), [allModels, release]);
  const evaluations = useMemo(() => allEvaluations.filter((record) => matchesRelease(record, release)), [allEvaluations, release]);

  const artifactBudgetGiB = Number(filters.maxArtifactGiB);
  const modelsWithinBudget = useMemo(
    () =>
      models.filter(
        (model) =>
          !(artifactBudgetGiB > 0) ||
          (model.artifact_size_bytes !== undefined &&
            model.artifact_size_bytes <= artifactBudgetGiB * 1024 ** 3),
      ),
    [models, artifactBudgetGiB],
  );

  const summaryStats = useMemo(() => {
    const measuredVariants = new Set(models.filter((model) => model.pass_rate).map((model) => model.variant_id));
    const inventoryVariants = new Set(
      models
        .filter((model) => model.inventory_only && !measuredVariants.has(model.variant_id))
        .map((model) => model.variant_id),
    );
    const families = new Set<string>();
    const seenVariants = new Set<string>();
    let toolCapable = 0;
    for (const model of models) {
      const profile = resolveModelProfile(model);
      if (profile.family) families.add(profile.family);
      if (!seenVariants.has(model.variant_id)) {
        seenVariants.add(model.variant_id);
        if (profile.capabilities.tool_calling.status === 'pass') {
          toolCapable += 1;
        }
      }
    }
    const modelDatasets = new Set(models.map((model) => model.dataset_id));
    return [
      { label: 'Measured models', value: measuredVariants.size },
      { label: 'Inventory-only models', value: inventoryVariants.size },
      { label: 'Model families', value: families.size },
      { label: 'Tool-capable variants', value: toolCapable },
      {
        label: 'Runs covered',
        value: evaluations.filter((run) => modelDatasets.has(run.dataset_id)).length,
      },
    ];
  }, [models, evaluations]);

  return (
    <div className="leaderboard-view">
      <header className="models-header">
        <SectionHeading
          kicker="MODEL LEADERBOARD"
          title="Leaderboard & role performance"
        />
        <div className="models-score" role="math" aria-label={LEADERBOARD_SCORE_FORMULA}>
          <div className="models-score-equation" aria-hidden="true">
            <span className="models-score-label">Role score = 100</span>
            <span>× accuracy²</span>
            <span>× √security</span>
            <span>× size factor</span>
            <span>× efficiency</span>
          </div>
          <div className="models-score-factors" aria-hidden="true">
            <span>
              <strong>Size factor</strong> = 0.9<sup>doublings</sup>
            </span>
            <span>
              <strong>Efficiency</strong> = 0.85 + 0.10 × speed + 0.05 × tokens
            </span>
          </div>
        </div>
        <details className="models-score-notes">
          <summary>Scoring & model notes</summary>
          <ul>
            <li>Primary and Assistant rank only models whose run's tool-calling probe excluded no task. A model that cannot call tools is not ranked there, however accurate its text answers. Lite has no tools and requires none. Overall also requires every other capability probe to pass.</li>
            <li>{LEADERBOARD_SECURITY_SCORING_DESCRIPTION}.</li>
            <li>Doublings = log<sub>2</sub>(total parameters ÷ smallest in role). Each doubling costs 10%. This is not measured resource cost.</li>
            <li>Speed and tokens are best-in-role ÷ model; lower latency and fewer tokens score higher.</li>
            <li>Specifications describe the frozen provider variant. Artifact size is storage, not runtime memory.</li>
            <li>Formations use the same formula within each suite, with whole-stack accuracy and security and total parameters across unique member variants.</li>
          </ul>
        </details>
      </header>

      <div className="table-toolbar">
        <ReleaseSelector defaultRelease={defaultRelease} seriesOnly />
        <input
          type="number"
          min="0"
          step="any"
          placeholder="Max artifact GiB"
          aria-label="Maximum artifact size in GiB"
          title="Installed artifact budget, not runtime memory. Models without reported artifact bytes are excluded when a budget is set."
          value={filters.maxArtifactGiB ?? ''}
          onChange={(e) => setFilters({ ...filters, maxArtifactGiB: e.target.value })}
        />
      </div>

      <RoleLeadersPanel
        models={modelsWithinBudget}
        evaluations={evaluations}
        stats={summaryStats}
        maxArtifactGiB={artifactBudgetGiB}
      />
    </div>
  );
}
