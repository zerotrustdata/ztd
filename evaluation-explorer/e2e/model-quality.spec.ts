// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test, type Page, type Route } from '@playwright/test';
import { fixtureEvaluationSummaries, fixtureModelSummaries } from '../src/fixtures/fixtures';
import type { EvaluationSummary, ModelSummary } from '../src/contract/types';

const mirrorOrigin = 'http://127.0.0.1:8082';
const verifiedDataset = 'ds-browser-verified';
const partialDataset = 'ds-browser-partial';
const runId = 'run-browser-verified';
const sourceId = 'browser-model-quality-source';

function model(datasetId: string, qualityState: ModelSummary['quality_state'], role: ModelSummary['role'] = 'primary'): ModelSummary {
  return {
    ...fixtureModelSummaries[0]!,
    dataset_id: datasetId,
    variant_id: 'quality-model',
    display_name: role === 'primary' ? 'Quality Model' : 'Quality Model Assistant',
    role,
    quality_state: qualityState,
    schema_version: '1.4.0',
    pass_rate: { estimate: 0.75, lower: 0.6, upper: 0.85, denominator: 4 },
  };
}

function evaluation(qualityState: EvaluationSummary['quality_state']): EvaluationSummary {
  return {
    ...fixtureEvaluationSummaries[0]!,
    dataset_id: verifiedDataset,
    run_id: runId,
    schema_version: '1.4.0',
    quality_state: qualityState,
    verifier_state: qualityState === 'exploratory_verified' ? 'passed' : 'not_applicable',
  };
}

function recordsForFeed(records: unknown[]) {
  return records.map((record, index) => ({
    sequence: index + 1,
    record_type: 'projection',
    ...(record as Record<string, unknown>),
  }));
}

async function installMirror(page: Page, records: unknown[]): Promise<string[]> {
  const requests: string[] = [];
  await page.route(`${mirrorOrigin}/**`, async (route: Route) => {
    const url = new URL(route.request().url());
    requests.push(url.origin + url.pathname + url.search);
    if (url.pathname === '/bootstrap') {
      const snapshot = {
        protocol_version: '1.0.0',
        source_id: sourceId,
        high_water_sequence: records.length,
        feed_chain_hash: 'b'.repeat(64),
        withdrawal_digest: '0'.repeat(64),
        batch_count: 1,
        generated_at: '2026-09-20T00:00:00Z',
        freshness: 'active',
      };
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          protocol_version: '1.0.0',
          snapshot,
          source_freshness: 'active',
          recent_projections: records.map((record, index) => ({ ...(record as object), sequence: index + 1 })).reverse(),
          proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 },
          generated_at: snapshot.generated_at,
        }),
      });
      return;
    }
    if (url.pathname === '/history') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ protocol_version: '1.0.0', items: recordsForFeed(records), has_more: false, limit: 100 }),
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
          feed_chain_hash: 'b'.repeat(64),
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
  return requests;
}

function expectPublicOnly(requests: string[]): void {
  expect(requests.every((request) => {
    const url = new URL(request);
    return url.origin === mirrorOrigin && ['/bootstrap', '/history', '/snapshot', '/stream'].some((path) => url.pathname === path);
  })).toBe(true);
  expect(requests.some((request) => /api|gateway|operator|ensemble|audit|receipt|tool/i.test(request))).toBe(false);
}

test('replaces partial model revisions with verified revisions and keeps scope isolated', async ({ page }) => {
  const records = [
    model(verifiedDataset, 'exploratory_partial'),
    evaluation('exploratory_partial'),
    evaluation('exploratory_verified'),
    model(verifiedDataset, 'exploratory_verified'),
    model(partialDataset, 'exploratory_partial'),
    model(verifiedDataset, 'exploratory_partial', 'assistant'),
  ];
  const requests = await installMirror(page, records);

  await page.goto('/evaluations');
  await expect(page.getByRole('heading', { name: 'Evaluations', exact: true })).toBeVisible();
  await expect(page.getByTestId('quality-exploratory_verified')).toBeVisible();
  await expect(page.getByTestId('quality-exploratory_verified')).toHaveText('Verified');

  await page.goto('/models');
  await expect(page.getByRole('table', { name: 'Model catalog' }).getByRole('link', { name: 'Quality Model', exact: true })).toHaveCount(1);
  await expect(page.getByText('Quality Model Assistant')).toBeVisible();
  await expect(page.locator('tr').filter({ has: page.getByRole('link', { name: 'Quality Model Assistant' }) })).toContainText('Not fully verified');
  await expect(page.getByRole('table', { name: 'Model catalog' }).getByRole('row').filter({ has: page.getByRole('link', { name: 'Quality Model', exact: true }) })).toContainText('Not fully verified');

  await page.getByLabel('Filter by quality state').selectOption('exploratory_verified');
  await expect(page.locator(`a.model-link[href="/models/${verifiedDataset}/quality-model?role=primary"]`)).toBeVisible();
  await expect(page.getByRole('link', { name: 'Quality Model Assistant' })).not.toBeVisible();
  await expect(page.getByText('Quality Model Assistant')).not.toBeVisible();

  await page.reload();
  await expect(page.locator(`a.model-link[href="/models/${verifiedDataset}/quality-model?role=primary"]`)).toBeVisible();
  await expect(page.getByTestId('quality-exploratory_verified')).toBeVisible();
  expectPublicOnly(requests);
});

test('does not promote model rows from an evaluation-only update', async ({ page }) => {
  const records = [model(verifiedDataset, 'exploratory_partial'), evaluation('exploratory_partial'), evaluation('exploratory_verified')];
  await installMirror(page, records);

  await page.goto('/models');
  await expect(page.getByRole('link', { name: 'Quality Model' })).toBeVisible();
  await expect(page.getByTestId('quality-exploratory_partial')).toBeVisible();
  await expect(page.getByTestId('quality-exploratory_verified')).not.toBeVisible();
});

test('shows frozen deployment specifications and filters installed artifact budgets', async ({ page }) => {
  const deployed: ModelSummary = {
    ...model(verifiedDataset, 'exploratory_partial'),
    variant_id: 'gemma4-e4b', display_name: 'Deployed Gemma', served_model_tag: 'gemma4:e4b',
    model_digest: 'd'.repeat(64), parameter_count: 8_000_000_000, parameter_count_source: 'model_metadata',
    quantization_weight_class: 'Q8_0', model_format: 'gguf', context_limit: 4096,
    artifact_size_bytes: 4 * 1024 ** 3, evaluation_coverage: 1,
  };
  const requests = await installMirror(page, [
    deployed,
    { ...deployed, variant_id: 'large', display_name: 'Large artifact', artifact_size_bytes: 8 * 1024 ** 3 },
    { ...deployed, variant_id: 'unknown', display_name: 'Unknown artifact', artifact_size_bytes: undefined },
    evaluation('exploratory_partial'),
  ]);
  await page.goto('/models');
  const catalog = page.getByRole('table', { name: 'Model catalog' });
  const row = catalog.getByRole('row').filter({ has: page.getByRole('link', { name: 'Deployed Gemma' }) });
  await expect(row).toContainText('8B total');
  await expect(row).toContainText('Q8_0');
  await expect(row).toContainText('4.00 GiB');
  await expect(catalog.getByRole('link', { name: 'Unknown artifact' })).toBeVisible();
  await page.getByRole('spinbutton', { name: 'Maximum artifact size in GiB' }).fill('4');
  await expect(catalog.getByRole('link', { name: 'Large artifact' })).not.toBeVisible();
  await expect(catalog.getByRole('link', { name: 'Unknown artifact' })).not.toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('banner').getByRole('button', { name: 'Close navigation drawer' }).click();
  const card = page.getByRole('article').filter({ has: page.getByRole('link', { name: 'Deployed Gemma' }) });
  await expect(card).toContainText('Q8_0');
  await expect(card).toContainText('4.00 GiB');
  await card.getByRole('link', { name: 'Deployed Gemma' }).click();
  const identity = page.getByRole('region', { name: 'Identity' });
  await expect(identity).toContainText('d'.repeat(64));
  await expect(identity).toContainText('Integer model metadata');
  await expect(identity).toContainText('4.00 GiB (4,294,967,296 bytes)');
  expectPublicOnly(requests);
});


test('combines repeated campaigns into one model row and keeps repetition costs per assignment', async ({ page }) => {
  const records = ['ds-reps-one', 'ds-reps-three'].flatMap((dataset, index) => (['primary', 'assistant', 'lite'] as const).map((role) => ({
    ...model(dataset, 'exploratory_verified', role), display_name: 'Aggregate model',
    model_digest: index === 0 ? undefined : 'a'.repeat(64), evaluation_coverage: 1,
    parameter_count: 4_000_000_000, latency_p50_ms: { value: 1000 }, output_tokens: { value: 100 },
    pass_rate: { estimate: index === 0 ? 0.5 : 1, lower: 0, upper: 1, denominator: index === 0 ? 10 : 30 },
    security_resistance: { value: 1, refused: index === 0 ? 1 : 3, yielded: 0, persisted: 0 },
  })));
  const requests = await installMirror(page, [...records, ...['ds-reps-one', 'ds-reps-three'].map((dataset_id) => ({ ...evaluation('exploratory_verified'), dataset_id, run_id: `${dataset_id}-run` }))]);
  await page.goto('/leaderboard');
  const panel = page.getByRole('region', { name: 'Leaderboard' });
  await expect(panel.getByRole('link', { name: 'Aggregate model' })).toHaveCount(1);
  await expect(panel).toContainText('2 runs · 120 judged assignments');
  await expect(panel).toContainText('87.5%');
  await expect(page.getByTestId('stat-measured-models')).toContainText('1');
  await panel.getByRole('button', { name: /^Primary/ }).click();
  await expect(panel.getByRole('link', { name: 'Aggregate model' })).toHaveCount(1);
  await expect(panel).toContainText('2 runs · 40 judged assignments');
  await panel.getByRole('link', { name: 'Aggregate model' }).click();
  const versions = page.getByRole('region', { name: 'Recorded model versions' });
  await expect(versions).toContainText('Digest unavailable');
  await expect(versions).toContainText('a'.repeat(64));
  expectPublicOnly(requests);
});
