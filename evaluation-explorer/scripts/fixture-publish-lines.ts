// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Mirror-ready publish input lines from the deterministic fixtures.
//
// Each line is a publish input record: {"record_type":"...","record_bytes":"..."}.
// Snapshot records (catalog_snapshot, model_summary, etc.) are emitted as
// "projection" records. Live events (evaluation_queued, etc.) are emitted as
// "event" records. The record_bytes field is a JSON string of the typed view
// record. This is the exact CLI input shape that `ztd public publish` expects.
//
// seed.mjs and replay.mjs load this module in-process through Vite's
// runnerImport, so the fixtures' TypeScript sources resolve without a
// separate TypeScript runner.

import { allFixtureSnapshotRecords, fixtureLiveEvents } from '../src/fixtures/fixtures';
import type { SnapshotRecord, LiveEvent } from '../src/contract/types';

interface PublishInputLine {
  record_type: 'projection' | 'event';
  record_bytes: string;
}

function emitLine(record: SnapshotRecord | LiveEvent, recordType: 'projection' | 'event'): string {
  const line: PublishInputLine = { record_type: recordType, record_bytes: JSON.stringify(record) };
  return JSON.stringify(line);
}

// fixturePublishLines returns the fixture snapshot records as projection
// lines, followed by the live events when includeEvents is set.
export function fixturePublishLines(includeEvents: boolean): string[] {
  const lines = allFixtureSnapshotRecords.map((record) => emitLine(record, 'projection'));
  if (includeEvents) {
    for (const event of fixtureLiveEvents) {
      lines.push(emitLine(event, 'event'));
    }
  }
  return lines;
}
