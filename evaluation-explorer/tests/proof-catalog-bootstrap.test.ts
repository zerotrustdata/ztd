// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { beforeEach, describe, expect, it } from 'vitest';
import { evalStore } from '../src/state/store';
import { startFeed, stopFeed } from '../src/state/startup';
import { makeMirror, requestedPaths, serveLayout } from './mirror-layout-fixture';

describe('proof catalog bootstrap', () => {
  beforeEach(() => {
    stopFeed();
    evalStore.loadFixtures([], []);
  });

  it('never fetches the proof catalog during startup, even when artifacts are published', async () => {
    const fetchImpl = serveLayout(makeMirror({ proofArtifactCount: 1 }));

    await startFeed({ fetchImpl });
    stopFeed();

    const paths = requestedPaths(fetchImpl);
    expect(paths.some((path) => path.startsWith('/proof-catalog'))).toBe(false);
    expect(paths.some((path) => path.startsWith('/history'))).toBe(false);
    expect(evalStore.getState().proofArtifactCount).toBe(1);
  });
});
