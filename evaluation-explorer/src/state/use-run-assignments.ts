// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useEffect, useState } from 'react';
import type { AssignmentResult } from '../contract/types';
import { isLiveBundleRun, loadRunAssignments, retainRuns, RunBundleError, startLiveBundleReload } from './run-bundles';
import { evalStore, useStoreState } from './store';

export type RunAssignmentsStatus = 'idle' | 'loading' | 'ready' | 'no_bundle' | 'error';

export interface RunAssignmentsView {
  status: RunAssignmentsStatus;
  assignments: AssignmentResult[];
  error?: RunBundleError;
}

interface LoadState {
  key: string;
  status: Exclude<RunAssignmentsStatus, 'idle' | 'loading'>;
  error?: RunBundleError;
}

const NO_ASSIGNMENTS: AssignmentResult[] = [];

/** Assignment results for one run, loading its bundle on demand. A queued or running
 *  run has no digest yet, so its live bundle is read again every minute while mounted.
 *  Live stream records for a run without a bundle still appear in `assignments`. */
export function useRunAssignments(datasetId: string | undefined, runId: string | undefined): RunAssignmentsView {
  // The run's summary names its bundle, and a deep link mounts before the bootstrap
  // delivers it. The digest and lifecycle are part of the load's identity so the load
  // runs again once the summary arrives, names a different bundle, or changes state.
  const bundleSha256 = useStoreState(
    () => (datasetId !== undefined && runId !== undefined ? evalStore.getEvaluation(datasetId, runId)?.assignment_bundle_sha256 : undefined),
  );
  const lifecycleState = useStoreState(
    () => (datasetId !== undefined && runId !== undefined ? evalStore.getEvaluation(datasetId, runId)?.lifecycle_state : undefined),
  );
  const isLive = lifecycleState !== undefined && isLiveBundleRun({ assignment_bundle_sha256: bundleSha256, lifecycle_state: lifecycleState });
  const key = datasetId !== undefined && runId !== undefined
    ? JSON.stringify([datasetId, runId, bundleSha256 ?? null, lifecycleState ?? null])
    : undefined;
  const [loadState, setLoadState] = useState<LoadState | null>(null);

  // The open run is pinned while mounted, so background loads cannot evict it.
  useEffect(() => {
    if (datasetId === undefined || runId === undefined) return;
    return retainRuns([{ datasetId, runId }]);
  }, [datasetId, runId]);

  useEffect(() => {
    if (datasetId === undefined || runId === undefined || key === undefined) return;
    let active = true;
    const load = () => {
      loadRunAssignments(datasetId, runId).then(
        (result) => {
          if (active) setLoadState({ key, status: result.status === 'ready' ? 'ready' : 'no_bundle' });
        },
        (error: unknown) => {
          if (!active) return;
          const bundleError = error instanceof RunBundleError ? error : new RunBundleError('malformed', 'run bundle failed to load');
          setLoadState({ key, status: 'error', error: bundleError });
        },
      );
    };
    load();
    const stopReload = isLive ? startLiveBundleReload(load) : undefined;
    return () => {
      active = false;
      stopReload?.();
    };
  }, [datasetId, runId, key, isLive]);

  const assignments = useStoreState(
    () => (datasetId !== undefined && runId !== undefined ? evalStore.getAssignments(runId, datasetId) : NO_ASSIGNMENTS),
  );

  if (key === undefined) return { status: 'idle', assignments: NO_ASSIGNMENTS };
  if (loadState === null || loadState.key !== key) return { status: 'loading', assignments };
  return { status: loadState.status, assignments, error: loadState.error };
}
