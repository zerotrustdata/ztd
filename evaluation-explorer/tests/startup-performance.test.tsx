// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useMirrorOrigin } from '../src/state/mirror';
import { evalStore } from '../src/state/store';
import { startFeed, stopFeed } from '../src/state/startup';
import { LEGACY_BOOTSTRAP_STORAGE_KEY } from '../src/state/legacy-cache';
import { fixtureCatalogExploratory } from '../src/fixtures/fixtures';
import type { FeedTaskIndexEntry } from '../src/contract/types';
import { MIRROR_ORIGIN, makeMirror, readModelBody, requestedPaths, serveLayout, sha256 } from './mirror-layout-fixture';

const origin = MIRROR_ORIGIN;

function Origin() { return <span>{useMirrorOrigin()}</span>; }

beforeEach(() => {
  localStorage.clear();
  evalStore.loadFixtures([], []);
});
afterEach(() => { stopFeed(); vi.unstubAllGlobals(); });

describe('current snapshot startup', () => {
  it('loads the pointer and its read model, then polls', async () => {
    const mirror = makeMirror();
    const fetchImpl = serveLayout(mirror);
    await startFeed({ fetchImpl });
    expect(requestedPaths(fetchImpl)).toEqual([
      '/runtime.json',
      '/pointer.json',
      `/read-model/${sha256(readModelBody(mirror))}.json.gz`,
    ]);
    expect(evalStore.getState().observedSequence).toBe(100);
    expect(evalStore.getState().currentSnapshot).toEqual(mirror.snapshot);
    expect(evalStore.getState().pendingSnapshot).toBeNull();
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toEqual(fixtureCatalogExploratory);
    expect(evalStore.getState().streamConnection).toBe('connected');
  });

  it('shares runtime configuration with mounted consumers', async () => {
    const fetchImpl = serveLayout(makeMirror()); vi.stubGlobal('fetch', fetchImpl);
    await startFeed({ fetchImpl });
    render(<><Origin /><Origin /><Origin /></>);
    expect(await screen.findAllByText(origin)).toHaveLength(3);
    expect(fetchImpl.mock.calls.filter(([input]) => String(input) === '/runtime.json')).toHaveLength(1);
  });

  it('installs the read model cursor and entities without a cursor-zero notification', async () => {
    const observed: number[] = [];
    const unsubscribe = evalStore.subscribe(() => observed.push(evalStore.getState().observedSequence));
    try {
      await startFeed({ fetchImpl: serveLayout(makeMirror()) });
      expect(observed.find((sequence) => sequence > 0)).toBe(100);
      expect(evalStore.getState().catalogs.size).toBe(1);
    } finally { unsubscribe(); }
  });

  it('installs the read model task index into the store', async () => {
    const results = [{
      run_id: 'run-1', assignment_id: 'assignment-1', lifecycle_status: 'completed',
      summary_status: 'passed', completed_at: '2026-10-23T00:00:00Z',
    }];
    const taskIndex: FeedTaskIndexEntry[] = [{ task_id: 'task-a', results }];
    await startFeed({ fetchImpl: serveLayout(makeMirror({ taskIndex })) });
    expect(evalStore.getTaskResults('task-a')).toEqual(results);
  });

  it('fetches the read model again on every start because nothing is cached', async () => {
    await startFeed({ fetchImpl: serveLayout(makeMirror()) });
    const second = serveLayout(makeMirror());
    await startFeed({ fetchImpl: second });
    expect(requestedPaths(second)).toEqual(['/runtime.json', '/pointer.json', expect.stringMatching(/^\/read-model\//u)]);
    expect(evalStore.getState().observedSequence).toBe(100);
  });

  it('removes the legacy bootstrap cache key when startup begins', async () => {
    localStorage.setItem(LEGACY_BOOTSTRAP_STORAGE_KEY, 'stale');
    await startFeed({ fetchImpl: serveLayout(makeMirror()) });
    expect(localStorage.getItem(LEGACY_BOOTSTRAP_STORAGE_KEY)).toBeNull();
  });

  it('keeps accepted state visible when a new snapshot is unreachable', async () => {
    await startFeed({ fetchImpl: serveLayout(makeMirror()) });
    await startFeed({ fetchImpl: async () => { throw new Error('offline'); } });
    expect(evalStore.getCatalog(fixtureCatalogExploratory.dataset_id)).toEqual(fixtureCatalogExploratory);
    expect(evalStore.getState().connection).toBe('offline');
  });
});
