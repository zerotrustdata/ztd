// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { ReleaseProvenance } from '../contract/types';

// VERSION is the platform release owner. The bundle records its build release;
// it does not derive a release from the newest campaign or verifier version.
export const CURRENT_PLATFORM_RELEASE = __ZTD_PLATFORM_RELEASE__;

function hasKnownRelease(record: ReleaseProvenance): record is ReleaseProvenance & { release: string } {
  return Boolean(record.release) && (record.release_basis === 'recorded' || record.release_basis === 'asserted');
}

export function matchesRelease(record: ReleaseProvenance, release: string): boolean {
  return release === 'all' || (hasKnownRelease(record) && record.release === release);
}

export function releaseLabel(record: ReleaseProvenance): string {
  return hasKnownRelease(record) ? `${record.release} (${record.release_basis})` : 'Unknown release';
}

function releaseParts(release: string): number[] {
  return release.replace(/^v/, '').split(/[.-]/).map((part) => Number.parseInt(part, 10) || 0);
}

// Orders release tags newest first by numeric version (v1.10.0 > v1.9.0).
export function compareReleasesDescending(a: string, b: string): number {
  const left = releaseParts(a);
  const right = releaseParts(b);
  for (let index = 0; index < Math.max(left.length, right.length); index += 1) {
    const delta = (right[index] ?? 0) - (left[index] ?? 0);
    if (delta !== 0) return delta;
  }
  return b.localeCompare(a);
}

// The default view release is the newest release the feed actually carries.
// The build release is only the fallback before any attributed record lands,
// so a platform upgrade never blanks a page whose evidence predates it.
export function defaultViewRelease(records: Iterable<ReleaseProvenance>): string {
  const releases = new Set<string>();
  for (const record of records) {
    if (hasKnownRelease(record)) releases.add(record.release);
  }
  return Array.from(releases).sort(compareReleasesDescending)[0] ?? CURRENT_PLATFORM_RELEASE;
}
