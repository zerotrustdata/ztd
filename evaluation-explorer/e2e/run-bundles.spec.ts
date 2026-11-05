// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test, type Page, type Route } from '@playwright/test';
import { buildRunBundle, campaignDatasetFor, campaignEvaluationSummary, type RunBundle } from './run-bundle';

const mirrorOrigin = process.env.ZTD_PUBLIC_MIRROR_ORIGIN ?? 'http://127.0.0.1:8082';
const runId = 'run-browser-bundles';
const sourceId = 'browser-run-bundles-source';
const assignmentIds = ['bundle-assign-1', 'bundle-assign-2'];

/** Serves the public feed with the given records, and each proof path from its bytes; unknown paths abort. */
async function installMirror(page: Page, records: object[], proofs: Map<string, Buffer>): Promise<void> {
  await page.route(`${mirrorOrigin}/**`, async (route: Route) => {
    const url = new URL(route.request().url());
    if (url.pathname.startsWith('/proofs/')) {
      const body = proofs.get(url.pathname.slice('/proofs/'.length));
      if (body === undefined) {
        await route.fulfill({ status: 404, body: 'not found' });
        return;
      }
      await route.fulfill({ body, contentType: 'application/gzip' });
      return;
    }
    if (url.pathname === '/bootstrap') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          protocol_version: '1.0.0',
          snapshot: {
            protocol_version: '1.0.0',
            source_id: sourceId,
            high_water_sequence: records.length,
            feed_chain_hash: 'c'.repeat(64),
            withdrawal_digest: '0'.repeat(64),
            batch_count: 1,
            generated_at: '2026-09-20T00:00:00Z',
            freshness: 'active',
          },
          source_freshness: 'active',
          recent_projections: records.map((record, index) => ({ ...record, sequence: index + 1 })).reverse(),
          task_index: [],
          proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 },
          generated_at: '2026-09-20T00:00:00Z',
        }),
      });
      return;
    }
    if (url.pathname === '/history') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ protocol_version: '1.0.0', items: [], has_more: false, limit: 100 }),
      });
      return;
    }
    if (url.pathname === '/snapshot') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          protocol_version: '1.0.0',
          source_id: sourceId,
          high_water_sequence: records.length,
          feed_chain_hash: 'c'.repeat(64),
          withdrawal_digest: '0'.repeat(64),
          batch_count: 1,
          generated_at: '2026-09-20T00:00:00Z',
          freshness: 'active',
        }),
      });
      return;
    }
    if (url.pathname === '/stream') {
      await route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': connected\n\n' });
      return;
    }
    await route.abort();
  });
  await page.route('**/runtime.json', async (route) => {
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ schema_version: '1.0.0', mirror_origin: mirrorOrigin }),
    });
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

  const bootstrapResponse = page.waitForResponse((response) => new URL(response.url()).pathname === '/bootstrap');
  await page.goto('/leaderboard');
  await bootstrapResponse;
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
