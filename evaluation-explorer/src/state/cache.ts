// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { FeedBootstrap } from '../contract/types';
import { isFeedBootstrap } from '../contract/validators';

export const BOOTSTRAP_STORAGE_KEY = 'opendevops.feed.bootstrap.v1';
export const BOOTSTRAP_DATABASE_NAME = 'opendevops.feed.cache.v1';

const BOOTSTRAP_DATABASE_VERSION = 1;
const BOOTSTRAP_OBJECT_STORE = 'feed';
const BOOTSTRAP_RECORD_KEY = 'current';
const LOCAL_PROJECTION_LIMIT = 250;

export interface CachedBootstrapRecord {
  schema_version: '1.0.0';
  mirror_origin: string;
  bootstrap: FeedBootstrap;
  cached_at: string;
}

let saveGeneration = 0;

/**
 * Load the newest valid bootstrap record from IndexedDB or the small localStorage cache.
 * Large payloads are structured-cloned by IndexedDB instead of parsed from a JSON string.
 */
export async function loadCachedBootstrap(): Promise<CachedBootstrapRecord | null> {
  const localRecord = loadLocalBootstrap();
  const indexedRecord = await readIndexedBootstrap();
  if (!indexedRecord) return localRecord;
  if (!localRecord) return indexedRecord;
  return Date.parse(indexedRecord.cached_at) > Date.parse(localRecord.cached_at) ? indexedRecord : localRecord;
}

function loadLocalBootstrap(): CachedBootstrapRecord | null {
  try {
    const raw = localStorage.getItem(BOOTSTRAP_STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (isLargeLocalCandidate(parsed)) {
      clearLocalBootstrap();
      return null;
    }
    if (!isCachedBootstrapRecord(parsed)) {
      clearLocalBootstrap();
      return null;
    }
    if (parsed.bootstrap.recent_projections.length > LOCAL_PROJECTION_LIMIT) {
      clearLocalBootstrap();
      return null;
    }
    return parsed;
  } catch {
    clearLocalBootstrap();
    return null;
  }
}

function isLargeLocalCandidate(value: unknown): boolean {
  if (!value || typeof value !== 'object') return false;
  const bootstrap = (value as { bootstrap?: unknown }).bootstrap;
  if (!bootstrap || typeof bootstrap !== 'object') return false;
  const projections = (bootstrap as { recent_projections?: unknown }).recent_projections;
  return Array.isArray(projections) && projections.length > LOCAL_PROJECTION_LIMIT;
}

function isCachedBootstrapRecord(value: unknown): value is CachedBootstrapRecord {
  if (!value || typeof value !== 'object') return false;
  const candidate = value as Partial<CachedBootstrapRecord>;
  if (candidate.schema_version !== '1.0.0' || typeof candidate.mirror_origin !== 'string' || candidate.mirror_origin === '' || typeof candidate.cached_at !== 'string' || !Number.isFinite(Date.parse(candidate.cached_at))) {
    return false;
  }
  try {
    isFeedBootstrap(candidate.bootstrap);
    return true;
  } catch {
    return false;
  }
}

async function openBootstrapDatabase(): Promise<IDBDatabase | null> {
  if (typeof indexedDB === 'undefined') return null;
  return new Promise((resolve) => {
    let settled = false;
    const finish = (db: IDBDatabase | null) => {
      if (settled) {
        db?.close();
        return;
      }
      settled = true;
      resolve(db);
    };
    try {
      const request = indexedDB.open(BOOTSTRAP_DATABASE_NAME, BOOTSTRAP_DATABASE_VERSION);
      request.onupgradeneeded = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains(BOOTSTRAP_OBJECT_STORE)) db.createObjectStore(BOOTSTRAP_OBJECT_STORE);
      };
      request.onsuccess = () => {
        const db = request.result;
        if (!db.objectStoreNames.contains(BOOTSTRAP_OBJECT_STORE)) {
          finish(null);
          db.close();
          return;
        }
        finish(db);
      };
      request.onerror = () => finish(null);
      request.onblocked = () => finish(null);
    } catch {
      finish(null);
    }
  });
}

async function readIndexedBootstrap(): Promise<CachedBootstrapRecord | null> {
  const generation = saveGeneration;
  const db = await openBootstrapDatabase();
  if (!db) return null;
  return new Promise((resolve) => {
    let value: unknown;
    let failed = false;
    let transaction: IDBTransaction;
    try {
      transaction = db.transaction(BOOTSTRAP_OBJECT_STORE, 'readonly');
      const request = transaction.objectStore(BOOTSTRAP_OBJECT_STORE).get(BOOTSTRAP_RECORD_KEY);
      request.onsuccess = () => { value = request.result; };
      request.onerror = () => { failed = true; };
      transaction.oncomplete = () => {
        db.close();
        if (failed || value === undefined) {
          resolve(null);
          return;
        }
        if (!isCachedBootstrapRecord(value)) {
          void deleteIndexedBootstrap(generation);
          resolve(null);
          return;
        }
        resolve(value);
      };
      transaction.onerror = transaction.onabort = () => {
        db.close();
        resolve(null);
      };
    } catch {
      db.close();
      resolve(null);
    }
  });
}

/**
 * Persist small bootstraps synchronously in localStorage and large records in
 * IndexedDB. Storage failures never block feed startup.
 */
export async function saveCachedBootstrap(mirrorOrigin: string, bootstrap: FeedBootstrap): Promise<void> {
  const generation = ++saveGeneration;
  const record: CachedBootstrapRecord = {
    schema_version: '1.0.0',
    mirror_origin: mirrorOrigin,
    bootstrap,
    cached_at: new Date().toISOString(),
  };

  if (bootstrap.recent_projections.length > LOCAL_PROJECTION_LIMIT) {
    clearLocalBootstrap();
    const saved = await writeIndexedBootstrap(record, generation);
    if (!saved && generation === saveGeneration) await deleteIndexedBootstrap(generation);
    return;
  }

  try {
    localStorage.setItem(BOOTSTRAP_STORAGE_KEY, JSON.stringify(record));
  } catch {
    await clearCachedBootstrap();
    return;
  }
  await deleteIndexedBootstrap(generation);
}

async function writeIndexedBootstrap(record: CachedBootstrapRecord, generation: number): Promise<boolean> {
  const db = await openBootstrapDatabase();
  if (!db) return false;
  if (generation !== saveGeneration) {
    db.close();
    return false;
  }
  return new Promise((resolve) => {
    try {
      const transaction = db.transaction(BOOTSTRAP_OBJECT_STORE, 'readwrite');
      transaction.objectStore(BOOTSTRAP_OBJECT_STORE).put(record, BOOTSTRAP_RECORD_KEY);
      transaction.oncomplete = () => { db.close(); resolve(true); };
      transaction.onerror = transaction.onabort = () => { db.close(); resolve(false); };
    } catch {
      db.close();
      resolve(false);
    }
  });
}

async function deleteIndexedBootstrap(generation?: number): Promise<void> {
  const db = await openBootstrapDatabase();
  if (!db) return;
  if (generation !== undefined && generation !== saveGeneration) {
    db.close();
    return;
  }
  await new Promise<void>((resolve) => {
    try {
      const transaction = db.transaction(BOOTSTRAP_OBJECT_STORE, 'readwrite');
      transaction.objectStore(BOOTSTRAP_OBJECT_STORE).delete(BOOTSTRAP_RECORD_KEY);
      transaction.oncomplete = transaction.onerror = transaction.onabort = () => resolve();
    } catch {
      resolve();
    }
  });
  db.close();
}

/**
 * Clear any cached bootstrap snapshot from localStorage.
 */
export async function clearCachedBootstrap(): Promise<void> {
  const generation = ++saveGeneration;
  clearLocalBootstrap();
  await deleteIndexedBootstrap(generation);
}

function clearLocalBootstrap(): void {
  try {
    localStorage.removeItem(BOOTSTRAP_STORAGE_KEY);
  } catch {
    // Ignore storage errors
  }
}

