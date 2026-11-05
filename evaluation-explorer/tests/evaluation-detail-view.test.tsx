// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { fixtureEnrichedAssignmentResult, fixtureEvaluationSummaries } from '../src/fixtures/fixtures';
import type { SnapshotRecord } from '../src/contract/types';
import { resetRunBundleCache } from '../src/state/run-bundles';
import { EvaluationDetailView } from '../src/views/EvaluationDetailView';
import { BUNDLE_ORIGIN, buildRunBundle, loadBundleFixtures, stubBundleFetch } from './run-bundle-fixture';

const RUN = 'run-bundle-eval';
// Campaign projections adapt into the live dataset named for their run.
const DATASET = `ds-live-${RUN}`;


function evaluationRecord(sha256?: string): SnapshotRecord {
  const { native_result: _native, ...summary } = fixtureEvaluationSummaries[0]!;
  void _native;
  return {
    ...summary,
    dataset_id: DATASET,
    run_id: RUN,
    lifecycle_state: 'completed',
    ...(sha256 === undefined ? {} : { assignment_bundle_sha256: sha256 }),
  } as SnapshotRecord;
}

function renderRun() {
  return render(
    <MemoryRouter initialEntries={[`/evaluations/${DATASET}/${RUN}`]}>
      <Routes>
        <Route path="/evaluations/:datasetId/:runId" element={<EvaluationDetailView />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('EvaluationDetailView assignments', () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    resetRunBundleCache();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('opening a run detail requests its bundle once and a re-render requests nothing', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1', 'assign-2']);
    loadBundleFixtures([evaluationRecord(bundle.sha256)]);
    const fetchMock = stubBundleFetch([bundle]);

    const first = renderRun();
    expect(await screen.findByRole('link', { name: /assign-1/ })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${BUNDLE_ORIGIN}/proofs/${bundle.sha256}`);
    first.unmount();

    renderRun();
    expect(await screen.findByRole('link', { name: /assign-2/ })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('shows the error note and no assignment rows when the bundle digest does not match', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1']);
    const tampered = buildRunBundle(RUN, ['assign-9']);
    loadBundleFixtures([evaluationRecord(bundle.sha256)]);
    stubBundleFetch([{ bytes: tampered.bytes, sha256: bundle.sha256 }]);

    renderRun();

    expect(await screen.findByTestId('error-state')).toHaveTextContent(/digest/i);
    expect(screen.queryByRole('link', { name: /assign-9/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /assign-1/ })).not.toBeInTheDocument();
  });

  it('shows live-arrived assignments for a run without a bundle and fetches nothing', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    loadBundleFixtures([
      evaluationRecord(),
      { ...fixtureEnrichedAssignmentResult, dataset_id: DATASET, run_id: RUN, assignment_id: 'live-assignment' },
    ]);

    renderRun();

    expect(await screen.findByRole('link', { name: /live-assignment/ })).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
