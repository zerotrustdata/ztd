// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Offline tests for scripts/stripe-payment-links.sh. Each test copies the script into a fixture website root under
// <repo>/.ztd/tmp (INV-FS-11) and runs it against testdata/fake-stripe.js, which keeps Stripe account state in a
// JSON file and logs every call, so tests can assert the exact Stripe objects a run creates, reuses, or deactivates.

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('../scripts/stripe-payment-links.sh', import.meta.url));
const fakeStripe = fileURLToPath(new URL('../testdata/fake-stripe.js', import.meta.url));
const scratchRoot = fileURLToPath(new URL('../../.ztd/tmp/', import.meta.url));
const SKUS = ['compliance-cli-pack', 'diagnostic', 'fleet-50', 'oem', 'retainer', 'sovereign-250', 'sprint'];
const LIVE_LINK = /^https:\/\/buy\.stripe\.com\/(?!test_)[A-Za-z0-9]+$/;

function hostTool(name) {
  for (const directory of (process.env.PATH || '').split(delimiter)) {
    const candidate = join(directory, name);
    try {
      if (statSync(candidate).isFile()) return candidate;
    } catch {
      // not in this PATH entry
    }
  }
  return null;
}

const missing = ['bash', 'jq'].filter(tool => !hostTool(tool));
const skip = missing.length > 0 && `${missing.join(', ')} not installed`;

// A website root holding a copy of the script (so it writes fixture/src/payment-links.json) and a `stripe` on PATH.
function fixture(t, { edit = source => source } = {}) {
  mkdirSync(scratchRoot, { recursive: true, mode: 0o700 });
  const root = mkdtempSync(join(scratchRoot, 'stripe-payment-links-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  for (const dir of ['scripts', 'src', 'bin']) mkdirSync(join(root, dir));
  const copy = join(root, 'scripts', 'stripe-payment-links.sh');
  writeFileSync(copy, edit(readFileSync(script, 'utf8')));
  chmodSync(copy, 0o755);
  const stripe = join(root, 'bin', 'stripe');
  writeFileSync(stripe, `#!/bin/sh\nexec "${process.execPath}" "${fakeStripe}" "$@"\n`);
  chmodSync(stripe, 0o755);
  const state = join(root, 'stripe-state.json');
  writeFileSync(state, JSON.stringify({ counter: 0, log: [], products: [], prices: [], links: [] }));
  const output = join(root, 'src', 'payment-links.json');
  return {
    output,
    state: () => JSON.parse(readFileSync(state, 'utf8')),
    reset: () => {
      const current = JSON.parse(readFileSync(state, 'utf8'));
      writeFileSync(state, JSON.stringify({ ...current, log: [] }));
    },
    edit: change => writeFileSync(copy, change(readFileSync(copy, 'utf8'))),
    run: (args = [], env = {}) => spawnSync(hostTool('bash'), [copy, ...args], {
      encoding: 'utf8',
      env: { ...process.env, ...env, PATH: `${join(root, 'bin')}${delimiter}${process.env.PATH}`, FAKE_STRIPE_STATE: state }
    })
  };
}

const writes = log => log.filter(call => call.method === 'post');
const activeLink = (state, sku) => state.links.find(link => link.active && link.metadata.sku === sku);

test('--live creates a product, price, and terms-gated Payment Link per SKU and writes the mapping', { skip }, t => {
  const site = fixture(t);
  const result = site.run(['--live']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '');

  const state = site.state();
  assert.ok(state.log.every(call => call.live), 'every Stripe call must target live mode');
  assert.ok(writes(state.log).every(call => call.confirmed), 'every write must pass -c');

  const mapping = JSON.parse(readFileSync(site.output, 'utf8'));
  assert.deepEqual(Object.keys(mapping), SKUS);
  for (const sku of SKUS) {
    const link = activeLink(state, sku);
    assert.equal(mapping[sku], link.url, sku);
    assert.match(link.url, LIVE_LINK, sku);
    assert.equal(link.consent_collection.terms_of_service, 'required', sku);
    assert.match(link.custom_text.terms_of_service_acceptance.message, /\(https:\/\/zerotrustdata\.io\/terms\/\)/, sku);
    assert.equal(link.metadata.price, link.price, sku);
    const price = state.prices.find(item => item.id === link.price);
    assert.equal(price.lookup_key, `ztd_${sku.replaceAll('-', '_')}_${price.recurring?.interval ?? 'once'}`, sku);
    assert.equal(link.invoice_creation.enabled, price.recurring === null, `${sku} invoices only one-time payments`);
    assert.equal(link.custom_text.submit !== null, sku === 'oem', `${sku} submit text`);
  }
  assert.match(activeLink(state, 'oem').custom_text.submit.message, /\(https:\/\/zerotrustdata\.io\/terms\/oem\/\)/);
  assert.deepEqual(
    Object.fromEntries(state.prices.map(price => [price.lookup_key, [price.unit_amount, price.recurring?.interval ?? 'once']])),
    {
      ztd_compliance_cli_pack_month: [9900, 'month'],
      ztd_fleet_50_year: [149900, 'year'],
      ztd_sovereign_250_year: [399900, 'year'],
      ztd_oem_year: [295000, 'year'],
      ztd_diagnostic_once: [250000, 'once'],
      ztd_sprint_once: [750000, 'once'],
      ztd_retainer_month: [350000, 'month']
    }
  );
});

test('a rerun reuses every price and link and only refreshes product copy', { skip }, t => {
  const site = fixture(t);
  assert.equal(site.run(['--live']).status, 0);
  const before = readFileSync(site.output, 'utf8');
  site.reset();

  const result = site.run(['--live']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(site.output, 'utf8'), before);
  const posts = writes(site.state().log);
  assert.equal(posts.length, SKUS.length);
  assert.ok(posts.every(call => /^\/v1\/products\/prod_\d+$/.test(call.path)), JSON.stringify(posts.map(call => call.path)));
});

test('a changed amount moves the lookup key to a new price and replaces only that link', { skip }, t => {
  const site = fixture(t);
  assert.equal(site.run(['--live']).status, 0);
  const before = JSON.parse(readFileSync(site.output, 'utf8'));
  const oldLink = activeLink(site.state(), 'compliance-cli-pack');

  site.edit(source => source.replace('|9900|month"', '|10900|month"'));
  const result = site.run(['--live']);
  assert.equal(result.status, 0, result.stderr);

  const state = site.state();
  const after = JSON.parse(readFileSync(site.output, 'utf8'));
  const newLink = activeLink(state, 'compliance-cli-pack');
  assert.notEqual(newLink.id, oldLink.id);
  assert.equal(state.links.find(link => link.id === oldLink.id).active, false);
  assert.equal(after['compliance-cli-pack'], newLink.url);
  assert.deepEqual({ ...after, 'compliance-cli-pack': null }, { ...before, 'compliance-cli-pack': null });
  const priceCall = state.log.find(call => call.method === 'post' && call.path === '/v1/prices' && call.params.unit_amount === '10900');
  assert.equal(priceCall.params.transfer_lookup_key, 'true');
  assert.equal(state.prices.find(price => price.id === newLink.price).lookup_key, 'ztd_compliance_cli_pack_month');
  assert.equal(state.prices.find(price => price.id === oldLink.price).lookup_key, null);
});

test('bumping LINK_REVISION replaces every link without new prices', { skip }, t => {
  const site = fixture(t);
  assert.equal(site.run(['--live']).status, 0);
  const prices = site.state().prices.length;
  site.edit(source => source.replace(/^LINK_REVISION=(\d+)$/m, (_, n) => `LINK_REVISION=${Number(n) + 1}`));

  assert.equal(site.run(['--live']).status, 0);
  const state = site.state();
  assert.equal(state.prices.length, prices);
  assert.equal(state.links.filter(link => link.active).length, SKUS.length);
  assert.equal(state.links.filter(link => !link.active).length, SKUS.length);
});

for (const failing of ['get /v1/prices', 'post /v1/products', 'post /v1/prices', 'get /v1/payment_links', 'post /v1/payment_links']) {
  test(`a Stripe API error on ${failing} stops the run and writes nothing`, { skip }, t => {
    const site = fixture(t);
    const result = site.run(['--live'], { FAKE_STRIPE_FAIL: failing });
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stderr, new RegExp(`stripe ${failing} failed: injected failure`));
    assert.equal(existsSync(site.output), false);
  });
}

test('a deactivation error stops the run and writes nothing', { skip }, t => {
  const site = fixture(t);
  assert.equal(site.run(['--live']).status, 0);
  rmSync(site.output);
  const stale = activeLink(site.state(), 'compliance-cli-pack');
  site.edit(source => source.replace(/^LINK_REVISION=(\d+)$/m, (_, n) => `LINK_REVISION=${Number(n) + 1}`));

  const result = site.run(['--live'], { FAKE_STRIPE_FAIL: `post /v1/payment_links/${stale.id}` });
  assert.equal(result.status, 1, result.stderr);
  assert.equal(existsSync(site.output), false);
});

test('test mode prints the mapping and never writes payment-links.json', { skip }, t => {
  const site = fixture(t);
  const result = site.run();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(existsSync(site.output), false);
  assert.ok(site.state().log.every(call => !call.live));
  const mapping = JSON.parse(result.stdout);
  assert.deepEqual(Object.keys(mapping), SKUS);
  assert.ok(Object.values(mapping).every(url => url.startsWith('https://buy.stripe.com/test_')));
});

test('unknown arguments are rejected before any Stripe call', { skip }, t => {
  const site = fixture(t);
  const result = site.run(['--prod']);
  assert.equal(result.status, 2);
  assert.match(result.stderr, /usage: .* \[--live\]/);
  assert.deepEqual(site.state().log, []);
});

test('stripe-payment-links.sh passes shellcheck', { skip: !hostTool('shellcheck') && 'shellcheck is not installed' }, () => {
  const result = spawnSync(hostTool('shellcheck'), ['--severity=style', script], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stdout + result.stderr);
});
