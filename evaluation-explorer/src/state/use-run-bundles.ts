// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useEffect, useMemo, useState } from 'react';
import { loadRunAssignments, retainRuns, RunBundleError } from './run-bundles';
import { loadRunsBounded, type RunLoader, type RunRef } from './load-runs-bounded';

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

/**
 * Loads the assignment bundles of the given runs, a few at a time, and pins them
 * while the caller uses them: the pin is released on change or unmount. Callers may
 * pass a new array with the same runs on every render; the load is keyed on content.
 */
export function useRunBundles(runs: RunRef[], concurrency: number): RunBundlesProgress {
  const key = runsKey(runs);
  const stableRuns = useMemo(() => runsFromKey(key), [key]);
  const [progress, setProgress] = useState<Progress | null>(null);

  useEffect(() => {
    if (stableRuns.length === 0) return;
    // Every run stays loaded while the caller uses it.
    const releaseRuns = retainRuns(stableRuns);
    let active = true;
    let loaded = 0;
    const countLoaded: RunLoader = (datasetId, runId) =>
      loadRunAssignments(datasetId, runId).then((result) => {
        loaded++;
        if (active) setProgress({ key, loaded, failures: [] });
        return result;
      });
    loadRunsBounded(stableRuns, concurrency, countLoaded).then((outcomes) => {
      if (!active) return;
      const failures = outcomes.flatMap(({ runId, outcome }) =>
        outcome instanceof RunBundleError ? [`Run ${runId}: ${outcome.message}`] : [],
      );
      setProgress({ key, loaded: stableRuns.length - failures.length, failures });
    });
    return () => {
      active = false;
      releaseRuns();
    };
  }, [key, stableRuns, concurrency]);

  const current = progress?.key === key ? progress : null;
  return {
    loaded: current?.loaded ?? 0,
    total: stableRuns.length,
    failures: current?.failures ?? [],
  };
}
