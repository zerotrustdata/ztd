// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { useStoreState } from './store';

/** The feed owns runtime configuration; evidence rows share its mirror origin. */
export function useMirrorOrigin(): string | null {
  return useStoreState((state) => state.mirrorOrigin);
}
