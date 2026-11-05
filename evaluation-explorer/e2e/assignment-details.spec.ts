// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { expect, test, type Page } from '@playwright/test';
import { installMirrorLayout } from './mirror-layout';
import { FIXTURE_RELEASE, fixtureEnrichedAssignmentResult, fixtureLiveEvents } from '../src/fixtures/fixtures';
import type { AssignmentResult, EvaluationSummary } from '../src/contract/types';

const mirrorOrigin = 'http://127.0.0.1:8082';
const datasetId = 'ds-browser-assignment';
const runId = 'run-browser-assignment';
const sourceId = 'browser-assignment-source';

function assignment(overrides: Partial<AssignmentResult> = {}): AssignmentResult {
  return {
    ...fixtureEnrichedAssignmentResult,
    dataset_id: datasetId,
    run_id: runId,
    assignment_id: 'assignment-1',
    variant_id: 'browser-model',
    ...overrides,
  };
}

function evaluation(): EvaluationSummary {
  return {
    schema_version: '1.4.0',
    kind: 'evaluation_summary',
    ...FIXTURE_RELEASE,
    dataset_id: datasetId,
    quality_state: 'exploratory_partial',
    observed_at: '2026-09-20T00:00:00Z',
    run_id: runId,
    suite_id: 'browser-suite',
    arm: 'ensemble_ungoverned',
    evaluation_unit: 'model',
    lifecycle_state: 'completed',
    assignment_total: 3,
    assignment_completed: 3,
    assignment_failed: 0,
    terminal_outcomes: { completed: 3, model_failed: 0, grader_failed: 0, invalid_evidence: 0, stopped: 0, provider_failed: 0, execution_failed: 0, escalated: 0 },
    verifier_state: 'not_applicable',
    headline_metrics: { pass_rate: { value: 0.67 } },
  };
}

async function installMirror(page: Page, records: unknown[], options: { malformed?: boolean } = {}): Promise<string[]> {
  const feed = options.malformed
    ? records.map((record, index) => (index === 0 ? { ...(record as object), private_prompt: 'must never render' } : record))
    : records;
  return installMirrorLayout(page, { mirrorOrigin, sourceId, records: feed });
}

function expectPublicOnly(requests: string[]): void {
  expect(requests.every((request) => {
    const url = new URL(request);
    const { pathname } = url;
    return url.origin === mirrorOrigin && (pathname === '/pointer.json' || pathname === '/live/status.json' || pathname.startsWith('/read-model/') || pathname.startsWith('/proof-meta/'));
  })).toBe(true);
  expect(requests.some((request) => /api|gateway|operator|ensemble|audit|receipt|tool/i.test(request))).toBe(false);
}

test('renders rich assignment evidence from mirror history and follows sibling repetition links', async ({ page }, testInfo) => {
  const records = [
    evaluation(),
    assignment({ role_transcripts: [{ role: 'primary', response: 'Synthetic checkout diagnosis: payment timeouts.', trace_digest: 'c'.repeat(64), tool_calls: [{ tool_name: 'inspect', arguments_json: '{"service":"checkout"}', arguments_hash: 'b'.repeat(64), command: 'inspect checkout', success: true, result_json: '{"timeout_rate":18}' }] }] }),
    assignment({ assignment_id: 'assignment-2', repetition: 2 }),
    assignment({ assignment_id: 'assignment-3', repetition: 3 }),
    { ...fixtureLiveEvents[0], kind: 'stage_updated', dataset_id: datasetId, run_id: runId, assignment_id: 'assignment-1', event_id: 'planned-invocation', stage_label: 'Planned invocation', role: 'primary' },
    { ...fixtureLiveEvents[0], kind: 'stage_updated', dataset_id: datasetId, run_id: runId, assignment_id: 'assignment-1', event_id: 'scored-invocation', stage_label: 'Scored invocation', role: 'primary' },
  ];
  const requests = await installMirror(page, records);

  await page.goto(`/evaluations/${datasetId}/${runId}/assignments/assignment-1`);
  await expect(page.getByText('Follows a bounded response-format instruction.', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'What happened' })).toBeVisible();
  await expect(page.locator('.role-transcript').getByText('Synthetic checkout diagnosis: payment timeouts.', { exact: true })).toBeVisible();
  await expect(page.locator('.transcript-result')).toHaveAttribute('open', '');
  await expect(page.locator('.transcript-result code')).toContainText('"timeout_rate": 18');
  await expect(page.getByRole('heading', { name: 'Published assignment data' })).toBeVisible();
  await expect(page.locator('.assignment-event-list > li')).toHaveCount(2);
  const downloadReady = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Download assignment JSON', exact: true }).click();
  const download = await downloadReady;
  expect(download.suggestedFilename()).toBe('assignment-1.json');
  const stream = await download.createReadStream();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  const downloaded = JSON.parse(Buffer.concat(chunks).toString('utf8')) as AssignmentResult;
  expect(downloaded.role_transcripts?.[0]?.tool_calls?.[0]?.result_json).toBe('{"timeout_rate":18}');
  for (const theme of ['light', 'dark']) {
    await page.evaluate((value) => document.documentElement.setAttribute('data-theme', value), theme);
    for (const width of [390, 1024, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.screenshot({ path: testInfo.outputPath(`assignment-${theme}-${width}.png`), fullPage: true });
    }
  }

  await expect(page.getByText('1 observed')).toBeVisible();
  await page.locator('.grade-badge-summary').filter({ hasText: 'Format compliance' }).click();
  await expect(page.getByText('Criterion passed')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Evidence and methodology' })).toBeVisible();
  await expect(page.getByText('Evaluation projection')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Sibling repetitions' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'assignment-2' })).toHaveAttribute('href', `/evaluations/${datasetId}/${runId}/assignments/assignment-2`);
  await expect(page.getByRole('link', { name: 'assignment-3' })).toBeVisible();
  await expect(page.getByText('Stage timeline not published for this assignment.')).not.toBeVisible();

  await page.reload();
  await expect(page.getByText('Follows a bounded response-format instruction.', { exact: true })).toBeVisible();
  await expect(page.getByText('Evaluation projection')).toBeVisible();
  expectPublicOnly(requests);
});

test('preserves legacy and unavailable evidence without rendering private fields', async ({ page }) => {
  const legacy = assignment({
    schema_version: '1.3.0',
    assignment_id: 'legacy-assignment',
    scenario_id: undefined,
    scenario_summary: undefined,
    semantic_grade_summaries: undefined,
    activity_summary: undefined,
    evidence_bindings: undefined,
    resource_summary: undefined,
    verification_metadata: undefined,
    missingness_reason: 'Historical evidence was not captured.',
    stage_summary: [],
  });
  const requests = await installMirror(page, [evaluation(), legacy]);

  await page.goto(`/evaluations/${datasetId}/${runId}/assignments/legacy-assignment`);
  await expect(page.getByText('Historical evidence was not captured.', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Evidence and methodology' })).not.toBeVisible();
  await expect(page.getByText('No approved public proof bindings were published for this assignment.')).not.toBeVisible();
  await expect(page.getByText('Stage timeline not published for this assignment.')).not.toBeVisible();
  await expect(page.locator('.assignment-resources [data-testid="metric-latency"]')).not.toBeVisible();
  await expect(page.getByText('private_prompt')).not.toBeVisible();
  expectPublicOnly(requests);
});

test('rejects a malicious assignment record before rendering it', async ({ page }) => {
  const requests = await installMirror(page, [
    { ...assignment(), private_prompt: 'never render this prompt' },
  ], { malformed: true });

  await page.goto(`/evaluations/${datasetId}/${runId}/assignments/assignment-1`);
  await expect(page.getByText('never render this prompt')).not.toBeVisible();
  await expect(page.getByText('No assignment selected.')).not.toBeVisible();
  await expect(page.getByText('The public mirror is unreachable. Last accepted data remains visible below.')).toBeVisible();
  await expect(page.getByRole('alert')).toContainText('Feed validation errors');
  expectPublicOnly(requests);
});

test('offers the full agent trace and audit database by email without fetching either', async ({ page }) => {
  const traceSha = 'c'.repeat(64);
  const sliceSha = '9b'.repeat(32);
  const requests = await installMirror(page, [
    evaluation(),
    assignment({
      evidence_bindings: [
        ...(fixtureEnrichedAssignmentResult.evidence_bindings ?? []),
        { kind: 'assignment_agent_trace', sha256: traceSha, schema_ref: 'ztd.eval.v1.PublicAssignmentAgentTrace' },
        { kind: 'assignment_audit_slice', sha256: sliceSha, schema_ref: 'ztd.eval.v1.PublicAssignmentAuditSlice' },
      ],
    }),
  ]);

  await page.goto(`/evaluations/${datasetId}/${runId}/assignments/assignment-1`);
  const offer = page.getByRole('region', { name: 'Full evidence' });
  await expect(offer).toContainText('available under a custom agreement');
  const href = await offer.getByRole('link', { name: 'Request access' }).getAttribute('href');
  const body = new URL(href ?? '').searchParams.get('body') ?? '';
  expect(body).toContain(`Agent trace SHA-256: ${traceSha}`);
  expect(body).toContain(`Audit database SHA-256: ${sliceSha}`);
  await expect(page.getByRole('heading', { name: 'Full agent trace' })).toHaveCount(0);
  expect(requests.some((request) => request.includes(traceSha) || request.includes(sliceSha))).toBe(false);
  expectPublicOnly(requests);
});
