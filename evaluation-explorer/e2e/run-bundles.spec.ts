// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test, type Page } from '@playwright/test';
import { installMirrorLayout } from './mirror-layout';
import { buildRunBundle, campaignDatasetFor, campaignEvaluationSummary, type RunBundle } from './run-bundle';

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
