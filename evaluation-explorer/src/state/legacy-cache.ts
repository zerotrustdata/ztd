// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

/** localStorage key written by earlier Explorer builds that cached the feed bootstrap. */
export const LEGACY_BOOTSTRAP_STORAGE_KEY = 'opendevops.feed.bootstrap.v1';
/** IndexedDB database written by earlier Explorer builds that cached the feed bootstrap. */
export const LEGACY_BOOTSTRAP_DATABASE_NAME = 'opendevops.feed.cache.v1';

/**
 * Remove bootstrap caches left by earlier Explorer builds. The deletion is requested
 * without waiting for it, and any missing or failing storage API is ignored.
 */
export function purgeLegacyBootstrapCaches(): void {
  try {
    if (typeof indexedDB !== 'undefined') indexedDB.deleteDatabase(LEGACY_BOOTSTRAP_DATABASE_NAME);
  } catch {
    // Storage cleanup is best-effort.
  }
  try {
    localStorage.removeItem(LEGACY_BOOTSTRAP_STORAGE_KEY);
  } catch {
    // Storage cleanup is best-effort.
  }
}
