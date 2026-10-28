// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import PAYMENT_LINKS from './payment-links.json' with { type: 'json' };

// /buy/<sku> is not linked from the homepage. paymentLink remains for the Worker,
// which still answers a direct /buy/<sku> request from payment-links.json.
export const BUY_PATH = /^\/buy\/([a-z0-9-]+)$/;
const BUY_HREF = /href="\/buy\/([a-z0-9-]+)"/g;
const LIVE_PAYMENT_LINK = /^https:\/\/buy\.stripe\.com\/(?!test_)[A-Za-z0-9]+$/;

export function paymentLink(sku, links = PAYMENT_LINKS) {
  const url = Object.hasOwn(links, sku) ? links[sku] : null;
  return url && LIVE_PAYMENT_LINK.test(url) ? url : null;
}

// The homepage must not offer a purchase. A /buy/ link fails the build.
export function validatePaymentLinks(html) {
  const skus = [...html.matchAll(BUY_HREF)].map(match => match[1]);
  if (skus.length) {
    throw new Error(`homepage must not link to /buy/ (${[...new Set(skus)].join(', ')})`);
  }
}
