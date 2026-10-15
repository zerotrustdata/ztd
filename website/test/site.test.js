// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const SCRIPT = await readFile(new URL('../src/site.js', import.meta.url), 'utf8');

function element(id) {
  const attributes = new Map();
  return {
    id,
    hidden: false,
    tabIndex: 0,
    classList: { add() {}, remove() {} },
    addEventListener() {},
    getAttribute: name => attributes.get(name) ?? null,
    setAttribute: (name, value) => attributes.set(name, String(value))
  };
}

// Runs src/site.js against a minimal DOM. Animation frames are queued, not run, so a test steps the loop itself.
function run({ reducedMotion = false, platform = 'Linux x86_64' } = {}) {
  const frames = [];
  const tabs = ['tb-0', 'tb-w', 'tb-1'].map(id => {
    const tab = element(id);
    tab.setAttribute('aria-controls', id.replace('tb-', 'tp-'));
    return tab;
  });
  const panels = new Map(tabs.map(tab => [tab.getAttribute('aria-controls'), element(tab.getAttribute('aria-controls'))]));
  const context2d = new Proxy({}, { get: () => () => ({ addColorStop() {} }) });
  const canvas = { ...element('grid'), clientWidth: 800, clientHeight: 600, getContext: () => context2d };
  const byId = new Map([...tabs.map(tab => [tab.id, tab]), ...panels, ['grid', canvas]]);
  const document = {
    hidden: false,
    body: element('body'),
    addEventListener() {},
    getElementById: id => byId.get(id) ?? null,
    querySelectorAll: selector => (selector === '[role=tab]' ? tabs : [])
  };
  const window = {
    devicePixelRatio: 1,
    addEventListener() {},
    matchMedia: query => ({ matches: reducedMotion && query.includes('reduce') })
  };
  vm.runInNewContext(SCRIPT, {
    window,
    document,
    navigator: { platform },
    performance: { now: () => 0 },
    requestAnimationFrame: callback => frames.push(callback),
    IntersectionObserver: class { observe() {} unobserve() {} }
  });
  return { frames, tabs };
}

function step(frames, count) {
  for (let i = 1; i <= count; i++) {
    const pending = frames.splice(0);
    assert.equal(pending.length, 1, `frame ${i} schedules exactly one next frame`);
    pending[0](i * 16);
  }
}

test('reduced motion keeps the hero grid animating, slowed rather than frozen', () => {
  const { frames } = run({ reducedMotion: true });
  step(frames, 5);
  assert.equal(frames.length, 1);
});

test('the hero grid animates without reduced motion', () => {
  const { frames } = run();
  step(frames, 5);
  assert.equal(frames.length, 1);
});

test('Windows visitors land on the Windows install tab', () => {
  const windows = run({ platform: 'Win32' }).tabs;
  assert.deepEqual(windows.map(tab => tab.getAttribute('aria-selected')), ['false', 'true', 'false']);
  const linux = run().tabs;
  assert.deepEqual(linux.map(tab => tab.getAttribute('aria-selected')), [null, null, null]);
});
