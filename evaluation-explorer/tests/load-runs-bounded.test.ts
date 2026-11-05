// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { loadRunsBounded } from '../src/state/load-runs-bounded';
import { RunBundleError, type RunAssignmentsLoad } from '../src/state/run-bundles';

interface Deferred {
  resolve: (value: RunAssignmentsLoad) => void;
  reject: (error: unknown) => void;
  promise: Promise<RunAssignmentsLoad>;
}

function deferred(): Deferred {
  let resolve!: (value: RunAssignmentsLoad) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<RunAssignmentsLoad>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { resolve, reject, promise };
}

const runs = ['run-a', 'run-b', 'run-c', 'run-d', 'run-e'].map((runId) => ({ datasetId: 'ds-bounded', runId }));

describe('loadRunsBounded', () => {
  it('never has more loads in flight than the concurrency limit', async () => {
    const pending = new Map<string, Deferred>();
    let inFlight = 0;
    let peak = 0;
    const load = (_datasetId: string, runId: string) => {
      const gate = deferred();
      pending.set(runId, gate);
      inFlight++;
      peak = Math.max(peak, inFlight);
      return gate.promise.finally(() => {
        inFlight--;
      });
    };

    const done = loadRunsBounded(runs, 2, load);
    await Promise.resolve();
    expect(pending.size).toBe(2);

    // Resolve in reverse order; each completion starts the next queued run.
    for (const runId of ['run-b', 'run-a', 'run-d', 'run-c', 'run-e']) {
      pending.get(runId)?.resolve({ status: 'no_bundle' });
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
    await done;

    expect(peak).toBe(2);
    expect(pending.size).toBe(5);
  });

  it('reports outcomes in input order even when loads finish out of order', async () => {
    const gates = new Map<string, Deferred>();
    const load = (_datasetId: string, runId: string) => {
      const gate = deferred();
      gates.set(runId, gate);
      return gate.promise;
    };

    const done = loadRunsBounded(runs, 3, load);
    gates.get('run-c')!.resolve({ status: 'no_bundle' });
    gates.get('run-a')!.resolve({ status: 'ready', assignments: [] });
    await new Promise((resolve) => setTimeout(resolve, 0));
    gates.get('run-b')!.resolve({ status: 'no_bundle' });
    await new Promise((resolve) => setTimeout(resolve, 0));
    gates.get('run-d')!.resolve({ status: 'no_bundle' });
    await new Promise((resolve) => setTimeout(resolve, 0));
    gates.get('run-e')!.resolve({ status: 'no_bundle' });

    const outcomes = await done;
    expect(outcomes.map((o) => o.runId)).toEqual(['run-a', 'run-b', 'run-c', 'run-d', 'run-e']);
    expect(outcomes.every((o) => o.datasetId === 'ds-bounded')).toBe(true);
    expect(outcomes[0]!.outcome).toEqual({ status: 'ready', assignments: [] });
  });

  it('reports a failing run as its outcome without rejecting the load', async () => {
    const load = async (_datasetId: string, runId: string): Promise<RunAssignmentsLoad> => {
      if (runId === 'run-b') throw new RunBundleError('digest_mismatch', 'run bundle digest does not match');
      if (runId === 'run-c') throw new Error('unexpected');
      return { status: 'no_bundle' };
    };

    const outcomes = await loadRunsBounded(runs, 3, load);

    expect(outcomes).toHaveLength(5);
    expect(outcomes[0]!.outcome).toEqual({ status: 'no_bundle' });
    expect(outcomes[1]!.outcome).toBeInstanceOf(RunBundleError);
    expect((outcomes[1]!.outcome as RunBundleError).reason).toBe('digest_mismatch');
    expect(outcomes[2]!.outcome).toBeInstanceOf(RunBundleError);
    expect((outcomes[2]!.outcome as RunBundleError).reason).toBe('malformed');
    expect(outcomes[4]!.outcome).toEqual({ status: 'no_bundle' });
  });

  it('resolves to an empty list for no runs', async () => {
    await expect(loadRunsBounded([], 3, async () => ({ status: 'no_bundle' }))).resolves.toEqual([]);
  });
});
