// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test, type Page, type Route } from '@playwright/test';
import { LIVE_EVENT_KINDS } from '../src/contract/types';
import { FIXTURE_RELEASE, fixtureEnrichedAssignmentResult, fixtureModelSummaries, fixtureEvaluationSummaries } from '../src/fixtures/fixtures';

const mirrorOrigin = process.env.ZTD_PUBLIC_MIRROR_ORIGIN ?? 'http://127.0.0.1:8082';
const datasetId = 'native-core-execution-boundary';
const runIds = [
  'f5980a8c-de6f-4e03-9ecf-34b536060697',
  '19dcd7b1-3d0f-4d05-a2f7-a46965c382bd',
  '355dcf53-eada-4faf-9dc2-d9e3800f7de7',
];
const sourceId = 'browser-native-evaluation-source';

function nativeEvaluation(runId: string) {
  const verdicts = (prefix: string) => Array.from({ length: 5 }, (_, index) => ({
    assertion_id: `${prefix}-${index + 1}`,
    assertion_version: '1.0.0',
    status: 'pass',
  }));
  return {
    schema_version: '1.3.0',
    kind: 'evaluation_summary',
    ...FIXTURE_RELEASE,
    dataset_id: datasetId,
    quality_state: 'verified_public',
    observed_at: '2026-09-15T23:38:22Z',
    run_id: runId,
    suite_id: 'core-execution-boundary@1.0.0',
    arm: 'platform',
    evaluation_unit: 'system',
    model_role_mapping: {},
    lifecycle_state: 'completed',
    assignment_total: 2,
    assignment_completed: 2,
    assignment_failed: 0,
    terminal_outcomes: { completed: 1, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
    started_at: '2026-09-15T23:38:21Z',
    ended_at: '2026-09-15T23:38:22Z',
    elapsed_seconds: 1,
    verifier_state: 'passed',
    headline_metrics: { pass_rate: { value: 1 } },
    native_result: {
      active_posture: 'doctrine',
      lane: 'platform',
      summary_status: 'pass',
      summary: '10/10 required invariants passed',
      required_verdict_count: 10,
      passed_verdict_count: 10,
      verification_valid: true,
      verification_failure_count: 0,
      scenarios: [
        {
          scenario_id: 'allowed-execution-occurs-once',
          scenario_version: '1.0.0',
          status: 'completed',
          verdicts: verdicts('allowed-effect'),
        },
        {
          scenario_id: 'prohibited-equivalent-causes-no-additional-effect',
          scenario_version: '1.0.0',
          status: 'rejected',
          verdicts: verdicts('prohibited-effect'),
        },
      ],
      metrics: [{ metric_id: 'required-verdict-pass-rate', metric_version: '1.0.0', numerator: 10, denominator: 10, value: 1, unit: 'ratio' }],
    },
  };
}

async function installMirror(page: Page, records: object[] = runIds.map(nativeEvaluation)): Promise<void> {
  await page.route(`${mirrorOrigin}/**`, async (route: Route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/bootstrap') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          protocol_version: '1.0.0',
          snapshot: {
            protocol_version: '1.0.0',
            source_id: sourceId,
            high_water_sequence: records.length,
            feed_chain_hash: 'b'.repeat(64),
            withdrawal_digest: '0'.repeat(64),
            batch_count: 1,
            generated_at: '2026-09-20T00:00:00Z',
            freshness: 'active',
          },
          source_freshness: 'active',
          recent_projections: [],
          proof_catalog_summary: { artifact_count: 0, total_byte_size: 0 },
          generated_at: '2026-09-20T00:00:00Z',
        }),
      });
      return;
    }
    if (url.pathname === '/history') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          protocol_version: '1.0.0',
          items: records.map((record, index) => ({ sequence: index + 1, record_type: LIVE_EVENT_KINDS.includes((record as { kind: typeof LIVE_EVENT_KINDS[number] }).kind) ? 'event' : 'projection', ...record })),
          has_more: false,
          limit: 100,
        }),
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
}

async function expectNativeRun(page: Page, runId: string) {
  await page.goto(`/evaluations/${datasetId}/${runId}`);
  await expect(page.getByRole('heading', { name: runId })).toBeVisible();
  await expect(page.getByText('core-execution-boundary@1.0.0', { exact: true })).toBeVisible();
  await expect(page.getByText('10/10 required invariants passed', { exact: true })).toBeVisible();
  await expect(page.getByText('Valid (0 failures)', { exact: true })).toBeVisible();
  await expect(page.locator('.native-scenario summary').filter({ hasText: 'allowed-execution-occurs-once@1.0.0' })).toBeVisible();
  const rejected = page.locator('.native-scenario').filter({ hasText: 'prohibited-equivalent-causes-no-additional-effect' });
  await expect(rejected.locator('summary .terminal-pill')).toHaveAttribute('data-tone', 'ok');
  await expect(rejected.locator('summary')).toContainText('rejected');
  await rejected.locator('summary').click();
  await expect(rejected.locator('li').first()).toBeVisible();
  await expect(page.locator('.native-scenario li')).toHaveCount(10);
}

test('reconstructs native evaluations from public mirror history', async ({ page }) => {
  if (!process.env.ZTD_PUBLIC_MIRROR_ORIGIN) await installMirror(page);
  const consoleErrors: string[] = [];
  const failedRequests: string[] = [];
  const mirrorResponses: string[] = [];

  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text());
  });
  page.on('requestfailed', (request) => failedRequests.push(request.url()));
  page.on('response', (response) => {
    if (response.url().startsWith(`${mirrorOrigin}/`)) mirrorResponses.push(response.url());
  });

  await page.goto('/evaluations');
  await expect(page.getByRole('heading', { name: 'Evaluations', exact: true })).toBeVisible();
  for (const runId of runIds) await expect(page.getByRole('link', { name: runId })).toBeVisible();
  await expect.poll(() => mirrorResponses.some((url) => url.endsWith('/bootstrap'))).toBe(true);
  await expect.poll(() => mirrorResponses.some((url) => url.includes('/history?'))).toBe(true);

  for (const runId of runIds) await expectNativeRun(page, runId);

  await page.reload();
  await expect(page.getByRole('heading', { name: runIds[2] })).toBeVisible();
  await expect(page.getByText('10/10 required invariants passed', { exact: true })).toBeVisible();
  await expect(page.locator('.native-scenario li')).toHaveCount(10);

  await page.goto('/evaluations');
  for (const runId of runIds) await expect(page.getByRole('link', { name: runId })).toBeVisible();

  expect(consoleErrors).toEqual([]);
  expect(failedRequests.filter((url) => !url.includes('/stream?'))).toEqual([]);
});

test('follows the evidence from a campaign through its assignment, model and task', async ({ page }) => {
  const assignment = { ...fixtureEnrichedAssignmentResult, dataset_id: 'ds-live-navigation', run_id: 'navigation-run', assignment_id: 'navigation-assignment', variant_id: 'navigation-model' };
  const model = { ...fixtureModelSummaries[0]!, dataset_id: assignment.dataset_id, variant_id: assignment.variant_id, role: assignment.role, display_name: 'Navigation model' };
  const run = { ...fixtureEvaluationSummaries[0]!, dataset_id: assignment.dataset_id, run_id: assignment.run_id, model_role_mapping: { [assignment.role]: assignment.variant_id } };
  await installMirror(page, [run, model, assignment]);
  await page.goto('/evaluations?release=all');
  await page.getByRole('link', { name: run.run_id, exact: true }).click();
  await expect(page.getByRole('heading', { level: 1, name: run.run_id })).toBeVisible();
  await page.getByRole('navigation', { name: 'Evidence chain' }).getByRole('link', { name: /Assignments/ }).click();
  await expect.poll(() => page.locator('#run-results').evaluate((element) => element.getBoundingClientRect().top)).toBeLessThan(200);
  await page.getByRole('region', { name: 'Assignments (1)' }).getByRole('link', { name: assignment.assignment_id }).click();
  await expect(page.getByRole('heading', { level: 1, name: assignment.task_id })).toBeVisible();
  await page.getByRole('navigation', { name: 'Evidence chain' }).locator('a[href="#assignment-trail"]').first().click();
  await expect(page.getByRole('heading', { name: 'What the model did' })).toBeVisible();
  await page.locator('.evidence-meta').getByRole('link', { name: assignment.variant_id }).click();
  await expect(page.getByRole('heading', { level: 1, name: model.display_name })).toBeVisible();
  await page.getByRole('region', { name: 'Assignments (1)' }).getByRole('link', { name: /navigation-assignment/ }).click();
  await page.goto(`/tasks/${assignment.task_id}`);
  await expect(page.getByRole('heading', { level: 1, name: assignment.task_id })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Recent assignments of this task' }).getByRole('link', { name: assignment.assignment_id })).toBeVisible();
});

test('Live panels fit above the footer and scroll independently through resizing', async ({ page }) => {
  const runs = Array.from({ length: 5 }, (_, index) => ({
    ...fixtureEvaluationSummaries[0]!,
    dataset_id: `ds-live-scroll-${index}`, run_id: `scroll-run-${index}`,
  }));
  const events = Array.from({ length: 60 }, (_, index) => ({
    schema_version: '1.4.0', ...FIXTURE_RELEASE, release_basis: 'PUBLIC_RELEASE_BASIS_RECORDED',
    kind: 'assignment_completed', dataset_id: runs[0]!.dataset_id, run_id: runs[0]!.run_id,
    event_id: `scroll-event-${index}`, assignment_id: `scroll-assignment-${index}`,
    task_id: 'tool-arg-run-commands', variant_id: 'qwen3:8b', role: 'primary',
    quality_state: 'live_in_progress', lifecycle_status: 'completed', completed: index + 1, total: 60,
    observed_at: new Date(Date.UTC(2026, 9, 16, 14, index)).toISOString(),
  }));
  await installMirror(page, [...runs, ...events]);
  await page.goto('/?release=all');
  const runPanel = page.getByRole('region', { name: 'Runs', exact: true });
  const livePanel = page.getByRole('region', { name: 'Live activity', exact: true });
  const runList = runPanel.locator('.campaign-evidence-list');
  const eventList = page.getByRole('region', { name: 'Live activity results', exact: true });
  const footer = page.getByRole('contentinfo');
  await expect(runList.locator(':scope > li')).toHaveCount(5);
  await expect(eventList.locator('tbody tr')).toHaveCount(25);

  // Smaller CSS viewports also cover the space available when the browser is zoomed in.
  for (const viewport of [
    { width: 1920, height: 1080 }, { width: 1440, height: 720 },
    { width: 1100, height: 600 }, { width: 900, height: 900 }, { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    expect(await page.evaluate(() => document.documentElement.scrollHeight <= window.innerHeight)).toBe(true);
    const footerBox = (await footer.boundingBox())!;
    expect(footerBox.y + footerBox.height).toBeLessThanOrEqual(viewport.height);
    const liveBox = (await livePanel.boundingBox())!;
    expect(liveBox.y + liveBox.height).toBeLessThanOrEqual(footerBox.y);
    expect(footerBox.y - liveBox.y - liveBox.height).toBeLessThanOrEqual(24);
    for (const list of [runList, eventList]) {
      expect(await list.evaluate((element) => element.clientHeight)).toBeGreaterThan(0);
      await list.evaluate((element) => { element.scrollTop = 0; });
    }
    const runBox = (await runPanel.boundingBox())!;
    expect(runBox.y + runBox.height).toBeLessThanOrEqual(footerBox.y);
    await expect(livePanel.getByRole('button', { name: 'Next page' })).toBeInViewport();

    const runsOverflow = await runList.evaluate((element) => element.scrollHeight > element.clientHeight);
    await runList.hover();
    await page.mouse.wheel(0, 300);
    if (runsOverflow) await expect.poll(() => runList.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    expect(await eventList.evaluate((element) => element.scrollTop)).toBe(0);
    const runScroll = await runList.evaluate((element) => element.scrollTop);
    await eventList.hover();
    await page.mouse.wheel(0, 300);
    await expect.poll(() => eventList.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    expect(await runList.evaluate((element) => element.scrollTop)).toBe(runScroll);
    // Reaching the end of either list must not transfer wheel scrolling to the page.
    for (const list of [runList, eventList]) {
      await list.evaluate((element) => { element.scrollTop = element.scrollHeight; });
      await list.hover();
      await page.mouse.wheel(0, 600);
    }
    expect(await page.evaluate(() => window.scrollY)).toBe(0);
    await livePanel.locator('summary').click();
    expect(await eventList.evaluate((element) => element.clientHeight)).toBeGreaterThan(0);
    await expect(livePanel.getByRole('button', { name: 'Next page' })).toBeInViewport();
    await livePanel.locator('summary').click();

    await runList.evaluate((element) => { element.scrollTop = 0; });
    await runList.focus();
    await page.keyboard.press('PageDown');
    if (runsOverflow) await expect.poll(() => runList.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  }
});

for (const viewport of [
  { width: 1440, height: 1000, theme: 'dark' },
  { width: 1440, height: 1000, theme: 'light' },
  { width: 900, height: 1000, theme: 'dark' },
  { width: 390, height: 844, theme: 'dark' },
]) {
  test(`Live activity stays compact and opens evidence at ${viewport.width}px in ${viewport.theme} mode`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const failureReason = 'The model called file_read_on_operator instead of the required run_commands_with_operator.';
    const taskIds = ['tool-arg-run-commands', 'tool-arg-grep-pattern', 'tool-arg-file-path', 'tech-log-parse', 'tech-error-diagnosis', 'tool-select-file-read'];
    const assignments = taskIds.map((task, index) => ({
      ...fixtureEnrichedAssignmentResult,
      dataset_id: 'ds-live-redesign', run_id: 'live-design-run', assignment_id: `live-assignment-${index}`,
      task_id: task, scenario_id: task, scenario_summary: undefined, scenario_category: undefined, variant_id: 'qwen3:8b', role: index % 2 === 0 ? 'primary' : 'assistant',
      terminal_status: index === 0 ? 'model_failed' : index === 3 ? 'provider_failed' : 'completed',
      metric_values: { pass: { value: index === 0 ? 0 : 1 } },
      failure_reason: index === 0 ? failureReason : undefined,
      resource_summary: { latency_ms: { value: 12000 + index * 1300 }, output_tokens: { value: 480 + index * 20 } },
      benchmark_observations: { timing: { generation_ms: { value: 9000 } }, unavailable_reasons: [] },
    }));
    const events = assignments.map((assignment, index) => ({
      schema_version: '1.4.0', ...FIXTURE_RELEASE, release_basis: 'PUBLIC_RELEASE_BASIS_RECORDED',
      kind: assignment.terminal_status === 'completed' ? 'assignment_completed' : 'assignment_failed',
      dataset_id: assignment.dataset_id, run_id: assignment.run_id, assignment_id: assignment.assignment_id,
      event_id: `live-event-${index}`, task_id: assignment.task_id, variant_id: assignment.variant_id, role: assignment.role,
      quality_state: 'live_in_progress', lifecycle_status: 'completed', completed: index + 1, total: 75,
      observed_at: new Date(Date.UTC(2026, 9, 16, 14, 58 - index)).toISOString(),
    }));
    const run = { ...fixtureEvaluationSummaries[0]!, dataset_id: 'ds-live-redesign', run_id: 'live-design-run', model_role_mapping: { primary: 'qwen3:8b', assistant: 'qwen3:8b' } };
    await installMirror(page, [run, ...assignments, ...events]);
    await page.goto('/?release=all');
    await page.evaluate((theme) => document.documentElement.setAttribute('data-theme', theme), viewport.theme);
    const live = page.getByRole('region', { name: 'Live activity', exact: true });
    await expect(live.locator('tbody tr')).toHaveCount(6);
    const runs = page.getByRole('region', { name: 'Runs', exact: true });
    await expect(runs.getByRole('heading', { name: 'Runs', exact: true })).toBeVisible();
    const runsBox = (await runs.boundingBox())!;
    const liveBox = (await live.boundingBox())!;
    if (viewport.width > 1000) {
      expect(runsBox.x + runsBox.width).toBeLessThan(liveBox.x);
      expect(Math.abs(runsBox.y - liveBox.y)).toBeLessThan(1);
    } else {
      expect(runsBox.y + runsBox.height).toBeLessThan(liveBox.y);
    }
    await expect(live.getByText(failureReason)).toHaveCount(0);
    await expect(page.getByRole('region', { name: 'What am I looking at?' })).toHaveCount(0);
    await expect(live.getByRole('combobox', { name: 'Filter by category' })).not.toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect(await live.locator('.stream-scroll').evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
    if (viewport.width > 700) {
      const heights = await live.locator('tbody tr').evaluateAll((elements) => elements.map((element) => element.getBoundingClientRect().height));
      expect(Math.max(...heights) - Math.min(...heights)).toBeLessThan(2);
      expect(Math.max(...heights)).toBeLessThan(80);
    }
    await page.screenshot({ path: testInfo.outputPath('live-view.png'), fullPage: true });
    await expect(live.getByRole('button', { name: /Needs attention|Pause|Resume/i })).toHaveCount(0);
    await live.getByRole('link', { name: 'tool-arg-run-commands — View details' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'tool-arg-run-commands' })).toBeVisible();
    await expect(page.getByText(failureReason, { exact: true }).first()).toBeVisible();
  });
}


test('Evals tables keep sortable detail columns at desktop and mobile widths', async ({ page }) => {
  const records = [
    { ...fixtureEvaluationSummaries[0]!, run_id: 'alpha-run', campaign_id: 'Alpha campaign' },
    { ...fixtureEvaluationSummaries[0]!, run_id: 'beta-run', campaign_id: 'Beta campaign' },
  ];
  await installMirror(page, records);
  await page.goto('/evaluations?release=all');
  await page.getByRole('tab', { name: /Campaigns/ }).click();
  const campaigns = page.getByRole('table', { name: 'Campaigns', exact: true });
  await expect(campaigns.locator('tbody tr')).toHaveCount(2);
  await campaigns.getByRole('button', { name: 'Sort by Campaign', exact: true }).click();
  await expect(campaigns.locator('tbody tr').first()).toContainText('Alpha campaign');
  await campaigns.getByRole('button', { name: 'Sort by Campaign', exact: true }).click();
  await expect(campaigns.locator('tbody tr').first()).toContainText('Beta campaign');
  await expect(campaigns.getByRole('columnheader', { name: /Campaign/ })).toHaveAttribute('aria-sort', 'descending');

  for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.getByRole('tab', { name: /Runs/ }).click();
    await expect(page.getByRole('table', { name: 'Model campaigns', exact: true })).toBeVisible();
    await page.getByRole('tab', { name: /Campaigns/ }).click();
    await expect(campaigns).toBeVisible();
    await expect(page.locator('.campaign-cards-grid, .formations-grid, .data-card-list')).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    if (viewport.width < 960) {
      const scrollArea = campaigns.locator('..');
      await scrollArea.evaluate((element) => { element.scrollLeft = 0; });
      await scrollArea.focus();
      await page.keyboard.press('ArrowRight');
      await expect.poll(() => scrollArea.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
    }
    await page.getByRole('tab', { name: /Formations/ }).click();
    const formations = page.getByRole('table', { name: 'Execution formations', exact: true });
    await expect(formations).toBeVisible();
    await page.getByRole('searchbox', { name: 'Search formations' }).fill('Qwen Powerhouse');
    await expect(formations.locator('tbody tr')).toHaveCount(1);
    await expect(formations.locator('tbody a')).toHaveCount(3);
    await page.getByRole('searchbox', { name: 'Search formations' }).fill('');
    await formations.getByRole('button', { name: 'Sort by Total VRAM', exact: true }).click();
    await expect(formations.getByRole('columnheader', { name: /Total VRAM/ })).toHaveAttribute('aria-sort', /ascending|descending/);
  }
  await page.getByRole('tab', { name: /Runs/ }).click();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.locator('.evaluations-view .data-table-wrap').evaluateAll((elements) => elements.forEach((element) => { element.scrollLeft = 0; }));
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: 'test-results/evals-tables-desktop.png', fullPage: true });
});
