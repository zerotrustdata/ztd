// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { fixtureEvaluationSummaries, fixtureModelSummaries } from '../src/fixtures/fixtures';
import type { SnapshotRecord } from '../src/contract/types';
import { RUN_BUNDLE_LRU_LIMIT } from '../src/constants';
import { evalStore } from '../src/state/store';
import { resetRunBundleCache } from '../src/state/run-bundles';
import { ModelDetailView } from '../src/views/ModelDetailView';
import { BUNDLE_ORIGIN, buildRunBundle, loadBundleFixtures, stubBundleFetch } from './run-bundle-fixture';

const VARIANT = 'model-a';
const OTHER_VARIANT = 'model-b';

function modelRecord(variantId: string, runId: string): SnapshotRecord {
  return {
    ...fixtureModelSummaries[0]!,
    dataset_id: `ds-live-${runId}`,
    variant_id: variantId,
    role: 'primary',
  } as SnapshotRecord;
}

function runRecord(runId: string, variantId: string, sha256: string): SnapshotRecord {
  const { native_result: _native, ...summary } = fixtureEvaluationSummaries[0]!;
  void _native;
  return {
    ...summary,
    dataset_id: `ds-live-${runId}`,
    run_id: runId,
    lifecycle_state: 'completed',
    model_role_mapping: { primary: variantId },
    assignment_bundle_sha256: sha256,
  } as SnapshotRecord;
}

describe('ModelDetailView run bundles', () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    resetRunBundleCache();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('loads only the bundles of runs that belong to this variant', async () => {
    const ownRun = buildRunBundle('run-own', ['assign-own']);
    const otherVariantRun = buildRunBundle('run-other-variant', ['assign-other']);
    // Same variant, but its dataset has no model summary for this variant.
    const unrelatedDatasetRun = buildRunBundle('run-unrelated', ['assign-unrelated']);
    loadBundleFixtures([
      modelRecord(VARIANT, 'run-own'),
      modelRecord(OTHER_VARIANT, 'run-other-variant'),
      runRecord('run-own', VARIANT, ownRun.sha256),
      runRecord('run-other-variant', OTHER_VARIANT, otherVariantRun.sha256),
      runRecord('run-unrelated', VARIANT, unrelatedDatasetRun.sha256),
    ]);
    const fetchMock = stubBundleFetch([ownRun, otherVariantRun, unrelatedDatasetRun]);

    render(
      <MemoryRouter initialEntries={[`/models/ds-live-run-own/${VARIANT}?role=primary`]}>
        <Routes>
          <Route path="/models/:datasetId/:variantId" element={<ModelDetailView />} />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByText('1 of 1 runs loaded')).toBeInTheDocument());
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${BUNDLE_ORIGIN}/runs/${ownRun.sha256}.jsonl.gz`);
  });

  it('keeps every run of a variant loaded when the variant has more runs than the bundle limit', async () => {
    const runIds = Array.from({ length: RUN_BUNDLE_LRU_LIMIT + 3 }, (_, index) => `variant-run-${index}`);
    const bundles = runIds.map((runId) => buildRunBundle(runId, ['assign-1']));
    loadBundleFixtures(
      runIds.flatMap((runId, index) => [modelRecord(VARIANT, runId), runRecord(runId, VARIANT, bundles[index]!.sha256)]),
    );
    stubBundleFetch(bundles);

    render(
      <MemoryRouter initialEntries={[`/models/ds-live-${runIds[0]}/${VARIANT}?role=primary`]}>
        <Routes>
          <Route path="/models/:datasetId/:variantId" element={<ModelDetailView />} />
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => expect(screen.getByText(`${runIds.length} of ${runIds.length} runs loaded`)).toBeInTheDocument());
    for (const runId of runIds) {
      expect(evalStore.getAssignments(runId, `ds-live-${runId}`)).toHaveLength(1);
    }
  });
});
