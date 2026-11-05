// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Loads several runs' assignment bundles with a fixed number in flight. Each
// run's outcome is reported in input order; a failing run is reported as its
// outcome and never rejects the whole load.

import { loadRunAssignments, RunBundleError, type RunAssignmentsLoad } from './run-bundles';

export interface RunRef {
  datasetId: string;
  runId: string;
}

export type RunLoader = (datasetId: string, runId: string) => Promise<RunAssignmentsLoad>;

export interface RunLoadOutcome extends RunRef {
  outcome: RunAssignmentsLoad | RunBundleError;
}

async function settle(run: RunRef, load: RunLoader): Promise<RunAssignmentsLoad | RunBundleError> {
  try {
    return await load(run.datasetId, run.runId);
  } catch (error) {
    if (error instanceof RunBundleError) return error;
    return new RunBundleError('malformed', `run ${run.runId} failed to load`);
  }
}

/** Loads each run with at most `concurrency` loads in flight. */
export async function loadRunsBounded(
  runs: RunRef[],
  concurrency: number,
  load: RunLoader = loadRunAssignments,
): Promise<RunLoadOutcome[]> {
  const outcomes = new Array<RunLoadOutcome>(runs.length);
  let next = 0;
  const worker = async (): Promise<void> => {
    while (next < runs.length) {
      const index = next++;
      const run = runs[index]!;
      outcomes[index] = { datasetId: run.datasetId, runId: run.runId, outcome: await settle(run, load) };
    }
  };
  const limit = Number.isFinite(concurrency) && concurrency >= 1 ? Math.floor(concurrency) : 1;
  const workers = Math.min(limit, runs.length);
  await Promise.all(Array.from({ length: workers }, worker));
  return outcomes;
}
