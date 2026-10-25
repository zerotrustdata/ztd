// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { FeedBootstrap } from '../contract/types';
import { isFeedBootstrap } from '../contract/validators';

export const BOOTSTRAP_STORAGE_KEY = 'opendevops.feed.bootstrap.v1';

export interface CachedBootstrapRecord {
  schema_version: '1.0.0';
  mirror_origin: string;
  bootstrap: FeedBootstrap;
  cached_at: string;
}

/**
 * Load the cached bootstrap record from localStorage.
 * Validates integrity via schema check and isFeedBootstrap validator.
 * Returns null if missing, invalid, or corrupted.
 */
export function loadCachedBootstrap(): CachedBootstrapRecord | null {
  try {
    const raw = localStorage.getItem(BOOTSTRAP_STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || parsed.schema_version !== '1.0.0') {
      clearCachedBootstrap();
      return null;
    }
    if (typeof parsed.mirror_origin !== 'string') {
      clearCachedBootstrap();
      return null;
    }
    isFeedBootstrap(parsed.bootstrap);
    return parsed as CachedBootstrapRecord;
  } catch {
    clearCachedBootstrap();
    return null;
  }
}

/**
 * Persist the latest verified bootstrap snapshot into localStorage.
 * Gracefully handles quota and permissions errors.
 */
export function saveCachedBootstrap(mirrorOrigin: string, bootstrap: FeedBootstrap): void {
  try {
    const record: CachedBootstrapRecord = {
      schema_version: '1.0.0',
      mirror_origin: mirrorOrigin,
      bootstrap,
      cached_at: new Date().toISOString(),
    };
    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, JSON.stringify(record));
  } catch {
    // Ignore storage failures (quota exceeded, private browsing restrictions, etc.)
  }
}

/**
 * Clear any cached bootstrap snapshot from localStorage.
 */
export function clearCachedBootstrap(): void {
  try {
    localStorage.removeItem(BOOTSTRAP_STORAGE_KEY);
  } catch {
    // Ignore storage errors
  }
}

