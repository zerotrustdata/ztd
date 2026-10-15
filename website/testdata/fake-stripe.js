#!/usr/bin/env node
// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Stand-in for the Stripe CLI used by test/stripe-payment-links.test.js. It implements the
// `stripe get|post <path> [--live] [-c] -d key=value...` calls that scripts/stripe-payment-links.sh makes, keeps
// account state in $FAKE_STRIPE_STATE, and appends every call to that state's log. Like the real CLI, it prints API
// errors as JSON and exits 0. Setting $FAKE_STRIPE_FAIL to "<method> <path>" makes that call return an error.

import { readFileSync, writeFileSync } from 'node:fs';

const [method, path, ...rest] = process.argv.slice(2);
const params = {};
const flags = [];
for (let i = 0; i < rest.length; i++) {
  if (rest[i] === '-d') {
    const pair = rest[++i];
    const at = pair.indexOf('=');
    const key = pair.slice(0, at);
    const value = pair.slice(at + 1);
    if (key.endsWith('[]')) (params[key] ??= []).push(value);
    else params[key] = value;
  } else {
    flags.push(rest[i]);
  }
}

const stateFile = process.env.FAKE_STRIPE_STATE;
const state = JSON.parse(readFileSync(stateFile, 'utf8'));
const live = flags.includes('--live');
state.log.push({ method, path, live, confirmed: flags.includes('-c'), params });

const nextId = prefix => `${prefix}_${++state.counter}`;
const metadata = () => Object.fromEntries(Object.entries(params).filter(([key]) => key.startsWith('metadata[')).map(([key, value]) => [key.slice(9, -1), value]));
const customText = key => params[`custom_text[${key}][message]`] ? { message: params[`custom_text[${key}][message]`] } : null;

function respond() {
  if (process.env.FAKE_STRIPE_FAIL === `${method} ${path}`) return { error: { message: 'injected failure' } };
  if (method === 'get' && path === '/v1/prices') {
    const keys = params['lookup_keys[]'] ?? [];
    return { data: state.prices.filter(price => keys.includes(price.lookup_key) && (params.active !== 'true' || price.active)).slice(0, Number(params.limit ?? 10)) };
  }
  if (method === 'post' && path === '/v1/products') {
    const product = { id: nextId('prod'), name: params.name, description: params.description, metadata: metadata() };
    state.products.push(product);
    return product;
  }
  const productUpdate = /^\/v1\/products\/(.+)$/.exec(path);
  if (method === 'post' && productUpdate) {
    const product = state.products.find(item => item.id === productUpdate[1]);
    Object.assign(product, { name: params.name, description: params.description });
    return product;
  }
  if (method === 'post' && path === '/v1/prices') {
    if (params.transfer_lookup_key === 'true') {
      for (const price of state.prices) if (price.lookup_key === params.lookup_key) price.lookup_key = null;
    }
    const price = {
      id: nextId('price'),
      product: params.product,
      unit_amount: Number(params.unit_amount),
      currency: params.currency,
      lookup_key: params.lookup_key,
      active: true,
      recurring: params['recurring[interval]'] ? { interval: params['recurring[interval]'] } : null
    };
    state.prices.push(price);
    return price;
  }
  if (method === 'get' && path === '/v1/payment_links') {
    return { data: state.links.filter(link => params.active !== 'true' || link.active) };
  }
  if (method === 'post' && path === '/v1/payment_links') {
    const id = nextId('plink');
    const link = {
      id,
      url: `https://buy.stripe.com/${live ? '' : 'test_'}${id.replace('_', '')}`,
      active: true,
      price: params['line_items[0][price]'],
      consent_collection: { terms_of_service: params['consent_collection[terms_of_service]'] ?? null },
      invoice_creation: { enabled: params['invoice_creation[enabled]'] === 'true' },
      custom_text: { terms_of_service_acceptance: customText('terms_of_service_acceptance'), submit: customText('submit') },
      metadata: metadata()
    };
    state.links.push(link);
    return link;
  }
  const linkUpdate = /^\/v1\/payment_links\/(.+)$/.exec(path);
  if (method === 'post' && linkUpdate) {
    const link = state.links.find(item => item.id === linkUpdate[1]);
    link.active = params.active !== 'false';
    return link;
  }
  return { error: { message: `fake stripe does not implement ${method} ${path}` } };
}

const body = respond();
writeFileSync(stateFile, JSON.stringify(state));
process.stdout.write(`${JSON.stringify(body, null, 2)}\n`);
