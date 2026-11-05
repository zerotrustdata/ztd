// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Display cap for the live stream table; the store keeps the full active run
// history so progress can be restamped in observed_at order before limiting.
export const LIVE_EVENT_RETENTION_LIMIT = 100;
export const LIVE_EVENT_STORE_LIMIT = 500;
/**
 * Number of loaded runs whose assignment results stay in the store. Eviction
 * takes the least recently used unpinned run. Runs pinned by a mounted view are
 * never evicted, so the loaded set exceeds this limit while pinned runs alone
 * exceed it.
 */
export const RUN_BUNDLE_LRU_LIMIT = 8;

/** Run bundles fetched at once when several runs load together. */
export const RUN_BUNDLE_CONCURRENCY = 3;

/** Pointer poll cadence while the tab is visible. */
export const POINTER_POLL_MS = 60_000;
/** Live status poll cadence while the tab is visible and the pointer names an active run. */
export const LIVE_STATUS_POLL_MS = 10_000;
/** Live status poll cadence when the mirror is on a loopback origin (local development). */
export const LIVE_STATUS_POLL_LOOPBACK_MS = 2_000;
/** A mounted view on a queued or running run reloads its live run bundle at most this often. */
export const LIVE_RUN_BUNDLE_RELOAD_MS = 60_000;
