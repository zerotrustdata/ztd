// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  LEGACY_BOOTSTRAP_DATABASE_NAME,
  LEGACY_BOOTSTRAP_STORAGE_KEY,
  purgeLegacyBootstrapCaches,
} from '../src/state/legacy-cache';

describe('legacy bootstrap cache purge', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    localStorage.clear();
  });

  it('removes the legacy localStorage bootstrap key', () => {
    localStorage.setItem(LEGACY_BOOTSTRAP_STORAGE_KEY, 'stale');
    purgeLegacyBootstrapCaches();
    expect(localStorage.getItem(LEGACY_BOOTSTRAP_STORAGE_KEY)).toBeNull();
  });

  it('requests deletion of the legacy IndexedDB database', () => {
    const deleteDatabase = vi.fn();
    vi.stubGlobal('indexedDB', { deleteDatabase } as unknown as IDBFactory);
    purgeLegacyBootstrapCaches();
    expect(deleteDatabase).toHaveBeenCalledWith(LEGACY_BOOTSTRAP_DATABASE_NAME);
  });

  it('swallows an error thrown by indexedDB.deleteDatabase', () => {
    vi.stubGlobal('indexedDB', {
      deleteDatabase: () => { throw new Error('blocked by browser policy'); },
    } as unknown as IDBFactory);
    expect(() => purgeLegacyBootstrapCaches()).not.toThrow();
  });

  it('swallows an error thrown by localStorage.removeItem', () => {
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new DOMException('storage disabled', 'SecurityError');
    });
    expect(() => purgeLegacyBootstrapCaches()).not.toThrow();
  });
});
