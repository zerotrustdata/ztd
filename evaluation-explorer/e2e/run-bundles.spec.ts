// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test, type Page } from '@playwright/test';
import { installMirrorLayout } from './mirror-layout';
import { buildRunBundle, campaignDatasetFor, campaignEvaluationSummary, type RunBundle } from './run-bundle';
import { FIXTURE_RELEASE } from '../src/fixtures/fixtures';

const mirrorOrigin = process.env.ZTD_PUBLIC_MIRROR_ORIGIN ?? 'http://127.0.0.1:8082';
const runId = 'run-browser-bundles';
const sourceId = 'browser-run-bundles-source';
const assignmentIds = ['bundle-assign-1', 'bundle-assign-2'];

/** Serves the public feed with the given records, and each proof path from its bytes; unknown paths abort. */
async function installMirror(page: Page, records: object[], proofs: Map<string, Buffer>): Promise<void> {
  await installMirrorLayout(page, {
    mirrorOrigin,
    sourceId,
    records,
    extra: async (url, route) => {
      if (!url.pathname.startsWith('/proofs/')) return false;
      const body = proofs.get(url.pathname.slice('/proofs/'.length));
      if (body === undefined) {
        await route.fulfill({ status: 404, body: 'not found' });
        return true;
      }
      await route.fulfill({ body, contentType: 'application/gzip' });
      return true;
    },
  });
}

/** Records the path of every bundle request the page makes, in order. */
function observeProofRequests(page: Page): string[] {
  const paths: string[] = [];
  page.on('request', (request) => {
    const { pathname } = new URL(request.url());
    if (pathname.startsWith('/proofs/')) paths.push(pathname);
  });
  return paths;
}

/** Opens the run by clicking its link in the Evals list. */
async function openRunFromEvals(page: Page): Promise<void> {
  await page.goto('/evaluations');
  await page.getByRole('main').getByRole('link', { name: runId, exact: true }).first().click();
}

/** Publishes the bundle's digest and serves `bytes` at that digest's proof path. */
function installRunMirror(page: Page, bundle: RunBundle, bytes: Buffer = bundle.body): Promise<void> {
  return installMirror(
    page,
    [campaignEvaluationSummary(runId, bundle.sha256)],
    new Map([[bundle.sha256, bytes]]),
  );
}

test('a cold leaderboard load requests no run bundle', async ({ page }) => {
  const bundle = buildRunBundle(runId, assignmentIds);
  const proofRequests = observeProofRequests(page);
  await installRunMirror(page, bundle);

  const readModelResponse = page.waitForResponse((response) => new URL(response.url()).pathname.startsWith('/read-model/'));
  await page.goto('/leaderboard');
  await readModelResponse;
  await expect(page.getByRole('region', { name: 'Leaderboard' }).getByRole('table')).toBeVisible();

  expect(proofRequests.filter((path) => path.startsWith('/proofs/'))).toEqual([]);
});

test('opening a run requests exactly one bundle', async ({ page }) => {
  const bundle = buildRunBundle(runId, assignmentIds);
  const proofRequests = observeProofRequests(page);
  await installRunMirror(page, bundle);

  await openRunFromEvals(page);
  for (const assignmentId of assignmentIds) {
    await expect(page.getByRole('link', { name: assignmentId, exact: true })).toBeVisible();
  }

  expect(proofRequests).toEqual([`/proofs/${bundle.sha256}`]);
});

test('Live status settles an invocation when its result bundle arrives without a terminal event', async ({ page }) => {
  const bundle = buildRunBundle(runId, [assignmentIds[0]!]);
  let releaseBundle!: () => void;
  const pendingBundle = new Promise<void>((resolve) => { releaseBundle = resolve; });
  await installMirror(page, [
    campaignEvaluationSummary(runId, bundle.sha256),
    {
      schema_version: '1.14.0', ...FIXTURE_RELEASE, release_basis: 'PUBLIC_RELEASE_BASIS_RECORDED', kind: 'stage_updated',
      dataset_id: campaignDatasetFor(runId), run_id: runId, assignment_id: assignmentIds[0],
      event_id: 'invoked-lite', task_id: 'tool-select-file-read', variant_id: 'model-lite', role: 'lite',
      quality_state: 'live_in_progress', lifecycle_status: 'running', completed: 0, total: 1,
      stage_label: 'model role invoked · lite · model-lite · tool-select-file-read',
      observed_at: '2026-09-20T00:00:00Z',
    },
  ], new Map());
  await page.route(`${mirrorOrigin}/proofs/${bundle.sha256}`, async (route) => {
    await pendingBundle;
    await route.fulfill({ body: bundle.body, contentType: 'application/gzip' });
  });
  await page.goto('/?release=all');
  const live = page.getByRole('region', { name: 'Live activity', exact: true });
  await expect(live.getByRole('button', { name: 'Sort by Status' })).toBeVisible();
  await expect(live.getByRole('link', { name: /Running:.*View details/ })).toBeVisible();
  releaseBundle();
  await expect(live.getByRole('link', { name: /Passed:.*View details/ })).toBeVisible();
  await expect(live.getByRole('link', { name: /Running:|Invoked:/ })).toHaveCount(0);
  await expect(live.locator('tbody tr')).toHaveCount(1);
});

test('Live status uses task-index outcomes when an ended run has no published bundle', async ({ page }) => {
  const summary = { ...campaignEvaluationSummary(runId), lifecycle_state: 'completed' };
  await installMirrorLayout(page, {
    mirrorOrigin, sourceId,
    records: [summary, ...['lite', 'primary'].map((role) => ({
      schema_version: '1.14.0', ...FIXTURE_RELEASE, release_basis: 'PUBLIC_RELEASE_BASIS_RECORDED', kind: 'stage_updated',
      dataset_id: campaignDatasetFor(runId), run_id: runId, assignment_id: assignmentIds[0],
      event_id: `invoked-${role}`, task_id: 'tool-select-file-read', variant_id: `model-${role}`, role,
      quality_state: 'live_in_progress', lifecycle_status: 'running', completed: 0, total: 1,
      stage_label: `model role invoked · ${role} · model-${role} · tool-select-file-read`,
      observed_at: '2026-09-20T00:00:00Z',
    }))],
    taskIndex: [{ task_id: 'tool-select-file-read', results: [{
      run_id: runId, assignment_id: assignmentIds[0],
      lifecycle_status: 'EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED',
      summary_status: 'EVALUATION_VERDICT_STATUS_FAIL', verification_status: 'verified',
      completed_at: '2026-09-20T00:01:00Z',
    }] }],
  });
  await page.goto('/?release=all');
  const live = page.getByRole('region', { name: 'Live activity', exact: true });
  await expect(live.getByRole('link', { name: /Model failed:.*View details/ })).toHaveCount(2);
  await expect(live.getByRole('link', { name: /Running:|Invoked:|Status unavailable:/ })).toHaveCount(0);
});

test('loading a run URL directly requests exactly one bundle', async ({ page }) => {
  const bundle = buildRunBundle(runId, assignmentIds);
  const proofRequests = observeProofRequests(page);
  await installRunMirror(page, bundle);

  await page.goto(`/evaluations/${campaignDatasetFor(runId)}/${runId}`);
  for (const assignmentId of assignmentIds) {
    await expect(page.getByRole('link', { name: assignmentId, exact: true })).toBeVisible();
  }

  expect(proofRequests).toEqual([`/proofs/${bundle.sha256}`]);
});

test('opening an assignment of the same run requests no further bundle', async ({ page }) => {
  const bundle = buildRunBundle(runId, assignmentIds);
  const proofRequests = observeProofRequests(page);
  await installRunMirror(page, bundle);

  await openRunFromEvals(page);
  await expect(page.getByRole('link', { name: assignmentIds[0]!, exact: true })).toBeVisible();
  await page.getByRole('link', { name: assignmentIds[0]!, exact: true }).click();
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: runId, exact: true })).toBeVisible();
  await expect(page.getByTestId('reconcile-placeholder')).toHaveCount(0);

  expect(proofRequests).toEqual([`/proofs/${bundle.sha256}`]);
});

test('a bundle that does not match its published digest shows an error and no rows', async ({ page }) => {
  const published = buildRunBundle(runId, assignmentIds);
  const tampered = buildRunBundle(runId, ['bundle-assign-9']);
  await installRunMirror(page, published, tampered.body);

  await openRunFromEvals(page);

  await expect(page.getByText('run bundle bytes do not match the published digest', { exact: true })).toBeVisible();
  for (const assignmentId of [...assignmentIds, 'bundle-assign-9']) {
    await expect(page.getByRole('link', { name: assignmentId, exact: true })).toHaveCount(0);
  }
});
