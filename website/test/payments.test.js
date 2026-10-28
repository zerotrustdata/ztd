// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import test from 'node:test';
import { paymentLink, validatePaymentLinks } from '../src/payments.js';

const LINKS = {
  'fleet-50': 'https://buy.stripe.com/aEU5kF4yQfgrdE8deY7EQ05',
  sandbox: 'https://buy.stripe.com/test_28E5kF4yQfgrdE8deY7EQ05',
  elsewhere: 'https://example.com/pay'
};

test('paymentLink resolves only live-mode Stripe Payment Links', () => {
  assert.equal(paymentLink('fleet-50', LINKS), LINKS['fleet-50']);
  assert.equal(paymentLink('sandbox', LINKS), null);
  assert.equal(paymentLink('elsewhere', LINKS), null);
  assert.equal(paymentLink('missing', LINKS), null);
  assert.equal(paymentLink('toString', LINKS), null);
});

test('validatePaymentLinks rejects homepage purchase links', () => {
  assert.doesNotThrow(() => validatePaymentLinks('<a href="mailto:x">', LINKS));
  assert.throws(() => validatePaymentLinks('<a href="/buy/fleet-50">', LINKS), /must not link to \/buy\//);
  assert.throws(() => validatePaymentLinks('<a href="/buy/fleet-50"><a href="/buy/sandbox">', LINKS), /fleet-50, sandbox/);
});
