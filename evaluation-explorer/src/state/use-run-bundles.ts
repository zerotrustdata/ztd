// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useEffect, useMemo, useState } from 'react';
import { isLiveBundleRun, loadRunAssignments, retainRuns, RunBundleError, startLiveBundleReload } from './run-bundles';
import { loadRunsBounded, type RunLoader, type RunRef } from './load-runs-bounded';
import { evalStore, useStoreState } from './store';

export interface RunBundlesProgress {
  /** Runs whose bundle loaded or needed no bundle. */
  loaded: number;
  total: number;
  /** One `Run <id>: <message>` line per run whose bundle failed. */
  failures: string[];
}

interface Progress {
  key: string;
  loaded: number;
  failures: string[];
}

/** Content identity of a run list: duplicates collapse and order does not matter. */
function runsKey(runs: RunRef[]): string {
  const pairs = new Set(runs.map((run) => JSON.stringify([run.datasetId, run.runId])));
  return JSON.stringify(Array.from(pairs).sort());
}

function runsFromKey(key: string): RunRef[] {
  return (JSON.parse(key) as string[]).map((pair) => {
    const [datasetId, runId] = JSON.parse(pair) as [string, string];
    return { datasetId, runId };
  });
}

/** What each run's load depends on, so the load runs again when a summary arrives, names a bundle, or settles. */
function runSummariesKey(runs: RunRef[]): string {
  return JSON.stringify(
    runs.map((run) => {
      const summary = evalStore.getEvaluation(run.datasetId, run.runId);
      return [summary?.assignment_bundle_sha256 ?? null, summary?.lifecycle_state ?? null];
    }),
  );
}

/**
 * Loads the assignment bundles of the given runs, a few at a time, and pins them
 * while the caller uses them: the pin is released on change or unmount. Callers may
 * pass a new array with the same runs on every render; the load is keyed on content.
 * A queued or running run has no digest yet, so its live object is read again every
 * minute while the tab is visible.
 */
export function useRunBundles(runs: RunRef[], concurrency: number): RunBundlesProgress {
  const key = runsKey(runs);
  const stableRuns = useMemo(() => runsFromKey(key), [key]);
  const summariesKey = useStoreState(() => runSummariesKey(stableRuns));
  const [progress, setProgress] = useState<Progress | null>(null);

  // Every run stays loaded while the caller uses it.
  useEffect(() => {
    if (stableRuns.length === 0) return;
    return retainRuns(stableRuns);
  }, [stableRuns]);

  useEffect(() => {
    if (stableRuns.length === 0) return;
    let active = true;
    let loaded = 0;
    const countLoaded: RunLoader = (datasetId, runId) =>
      loadRunAssignments(datasetId, runId).then((result) => {
        loaded++;
        // A repeat load of the same runs keeps the count it already reached until its outcome is known.
        if (active) {
          setProgress((previous) => (previous?.key === key && previous.loaded >= loaded ? previous : { key, loaded, failures: previous?.key === key ? previous.failures : [] }));
        }
        return result;
      });
    loadRunsBounded(stableRuns, concurrency, countLoaded).then((outcomes) => {
      if (!active) return;
      const failures = outcomes.flatMap(({ runId, outcome }) =>
        outcome instanceof RunBundleError ? [`Run ${runId}: ${outcome.message}`] : [],
      );
      setProgress({ key, loaded: stableRuns.length - failures.length, failures });
    });
    const liveRuns = stableRuns.filter((run) => isLiveBundleRun(evalStore.getEvaluation(run.datasetId, run.runId)));
    const stopReload = liveRuns.length > 0 ? startLiveBundleReload(() => void loadRunsBounded(liveRuns, concurrency)) : undefined;
    return () => {
      active = false;
      stopReload?.();
    };
  }, [key, summariesKey, stableRuns, concurrency]);

  const current = progress?.key === key ? progress : null;
  return {
    loaded: current?.loaded ?? 0,
    total: stableRuns.length,
    failures: current?.failures ?? [],
  };
}
