// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// On-demand proof catalog resolution. The explorer never loads the full
// catalog: each rendered evidence binding resolves its own entry from the
// mirror, once per content address, and the result is kept in the store.

import { useEffect } from 'react';
import type { ProofCatalogEntry, PublicEvidenceBinding } from '../contract/types';
import { fetchProofCatalogEntry } from './feed';
import { useMirrorOrigin } from './mirror';
import { evalStore, useStoreState } from './store';

const inflight = new Map<string, Promise<void>>();

function resolveProofArtifact(origin: string, sourceId: string | null, sha256: string): void {
  const key = `${origin}|${sourceId ?? ''}|${sha256}`;
  if (inflight.has(key)) return;
  const request = fetchProofCatalogEntry(origin, sourceId ?? undefined, sha256)
    .then(
      (entry) => {
        if (evalStore.getState().sourceId === sourceId) evalStore.acceptProofArtifact(sha256, entry);
      },
      () => {
        // A failed lookup leaves the artifact unresolved; its proof row stays absent.
      },
    )
    .finally(() => inflight.delete(key));
  inflight.set(key, request);
}

/** The mirror catalog entry for one evidence binding, or undefined while
 *  unresolved or when the mirror holds none. */
export function useProofArtifact(binding: PublicEvidenceBinding | undefined): ProofCatalogEntry | undefined {
  const origin = useMirrorOrigin();
  const sourceId = useStoreState((state) => state.sourceId);
  const sha256 = binding?.sha256;
  const resolved = useStoreState((state) => (sha256 ? state.proofArtifacts.has(sha256) : false));
  const entry = useStoreState((state) => (sha256 ? state.proofArtifacts.get(sha256) : undefined));
  useEffect(() => {
    if (origin && sha256 && !resolved) resolveProofArtifact(origin, sourceId, sha256);
  }, [origin, sourceId, sha256, resolved]);
  return entry ?? undefined;
}
