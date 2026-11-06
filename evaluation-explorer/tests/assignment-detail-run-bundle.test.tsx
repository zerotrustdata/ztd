// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { fixtureEnrichedAssignmentResult, fixtureEvaluationSummaries } from '../src/fixtures/fixtures';
import type { SnapshotRecord } from '../src/contract/types';
import { resetRunBundleCache } from '../src/state/run-bundles';
import { AssignmentDetailView } from '../src/views/AssignmentDetailView';
import { buildRunBundle, loadBundleFixtures, stubBundleFetch } from './run-bundle-fixture';

const RUN = 'run-assignment-bundle';
const OTHER_RUN = 'run-assignment-other';

function evaluationRecord(runId: string, sha256?: string): SnapshotRecord {
  const { native_result: _native, ...summary } = fixtureEvaluationSummaries[0]!;
  void _native;
  return {
    ...summary,
    dataset_id: `ds-live-${runId}`,
    run_id: runId,
    lifecycle_state: 'completed',
    ...(sha256 === undefined ? {} : { assignment_bundle_sha256: sha256 }),
  } as SnapshotRecord;
}

function assignmentRecord(runId: string, assignmentId: string, repetition = fixtureEnrichedAssignmentResult.repetition): SnapshotRecord {
  return {
    ...fixtureEnrichedAssignmentResult,
    dataset_id: `ds-live-${runId}`,
    run_id: runId,
    assignment_id: assignmentId,
    repetition,
  };
}

function renderAssignment(assignmentId: string, runId = RUN) {
  return render(
    <MemoryRouter initialEntries={[`/evaluations/ds-live-${runId}/${runId}/assignments/${assignmentId}`]}>
      <Routes>
        <Route path="/evaluations/:datasetId/:runId/assignments/:assignmentId" element={<AssignmentDetailView />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('AssignmentDetailView run bundle', () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    resetRunBundleCache();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('shows the loading note instead of not found while the run bundle loads', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1']);
    loadBundleFixtures([evaluationRecord(RUN, bundle.sha256)]);
    stubBundleFetch([bundle]);

    renderAssignment('assign-1');
    expect(screen.getByTestId('reconcile-placeholder')).toBeInTheDocument();
    expect(screen.queryByTestId(/^empty-/)).not.toBeInTheDocument();

    expect(await screen.findByRole('link', { name: RUN })).toBeInTheDocument();
    expect(screen.queryByTestId('reconcile-placeholder')).not.toBeInTheDocument();
  });

  it('shows not found for an assignment id that the loaded run does not contain', async () => {
    const bundle = buildRunBundle(RUN, ['assign-1']);
    loadBundleFixtures([evaluationRecord(RUN, bundle.sha256)]);
    stubBundleFetch([bundle]);

    renderAssignment('assign-missing');

    expect(await screen.findByTestId(/^empty-/)).toBeInTheDocument();
    expect(screen.queryByTestId('reconcile-placeholder')).not.toBeInTheDocument();
  });

  it('builds sibling repetitions only from the assignment run', () => {
    loadBundleFixtures([
      evaluationRecord(RUN),
      evaluationRecord(OTHER_RUN),
      assignmentRecord(RUN, 'assign-1'),
      assignmentRecord(RUN, 'assign-2', 2),
      assignmentRecord(OTHER_RUN, 'assign-other-run', 2),
    ]);

    renderAssignment('assign-1');

    expect(screen.getByRole('link', { name: /assign-2/ })).toHaveAttribute(
      'href',
      `/evaluations/ds-live-${RUN}/${RUN}/assignments/assign-2`,
    );
    expect(screen.queryByRole('link', { name: /assign-other-run/ })).not.toBeInTheDocument();
  });
});
