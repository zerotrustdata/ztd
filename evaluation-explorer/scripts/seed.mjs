// Seed the Gateway-owned public mirror with projection records for local development.
//
// The public mirror is owned by the Gateway (`./ztd gw start --public-spectator`).
// This script requires that Gateway-owned mirror to be listening on the public
// port and fails closed otherwise; it never starts a mirror process.
//
// Real campaign and native evaluation data is published by the Go evaluation
// service: `./ztd eval runs start` publishes by default, `./ztd eval runs resume
// --publish` publishes on resume, and `./ztd eval runs publish <run-id>` catches
// up an existing run. This script only loads pre-generated JSONL (fixtures or an
// explicit --records file) and publishes it through the real `ztd public publish`
// CLI in batches that fit the configured feed limits (max 100 records / 4 MiB of
// record bytes per batch), followed by `ztd public push` and `ztd public status`.
//
// Reseeding is idempotent: if the mirror already holds every dataset with the
// identical catalog_snapshot content, publishing is skipped. If the mirror
// holds a stale or partial copy of a seeded dataset, or the outbox cannot be
// reconciled, the script refuses to publish: resetting the Gateway-owned feed
// is a manual step (stop the Gateway, remove .ztd/public-feed and
// .ztd/public-mirror, then start it with --public-spectator).
//
// Usage:  node scripts/seed.mjs [--reset] [--records <path>] [--fixtures] [--append-revision <version>]
//   --reset      refused by this script; the error explains the manual feed reset
//   --records    publish a pre-generated JSONL file
//   --fixtures   publish the deterministic TypeScript fixtures (mock dev only)
//   --append-revision appends one exact schema revision without resetting prior history
//
// Environment:
//   ZTD_BIN          path to the ztd binary (auto-detected if unset)
//   ZTD_CWD          working directory for ztd commands (default: repo root)
//   ZTD_PUBLIC_PORT  public read-only listener port of the Gateway-owned mirror (default: 8082)

import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { fixturePublishLines } from './fixture-lines.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(__dirname, '..');

const PUBLIC_PORT = process.env.ZTD_PUBLIC_PORT ?? '8082';
const PUBLIC_ORIGIN = `http://127.0.0.1:${PUBLIC_PORT}`;
const SOURCE_ID = 'opendevops-local';
// Fallback limits; the actual limits are read from the export config.
const DEFAULT_BATCH_MAX_RECORDS = 100;
const DEFAULT_BATCH_MAX_BYTES = 4 << 20;
const HISTORY_PAGE_LIMIT = 100;
const HISTORY_TIMEOUT_MS = 5000;
const SNAPSHOT_TIMEOUT_MS = 3000;

function ztdBin() {
  const env = process.env.ZTD_BIN;
  if (env) return env;
  let dir = projectRoot;
  for (let i = 0; i < 5; i++) {
    const candidate = resolve(dir, 'ztd');
    if (existsSync(candidate)) return candidate;
    dir = resolve(dir, '..');
  }
  throw new Error('ztd binary not found; set ZTD_BIN to the full path');
}

function ztdCwd() {
  return process.env.ZTD_CWD ?? resolve(ztdBin(), '..');
}

function runZtd(args) {
  const result = spawnSync(ztdBin(), args, { cwd: ztdCwd(), encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  if (result.stdout) process.stdout.write(result.stdout);
  if (result.stderr) process.stderr.write(result.stderr);
  if (result.status !== 0) {
    throw new Error(`ztd ${args.join(' ')} exited with code ${result.status}`);
  }
  return result.stdout ?? '';
}

// ztdStatus returns the publisher's feed status. Any failure throws: a missing
// status must never read as high-water 0, which would look like a stale mirror
// and reach the reset path.
function ztdStatus() {
  const result = spawnSync(ztdBin(), ['public', 'status'], {
    cwd: ztdCwd(), encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (result.error) {
    throw new Error(`ztd public status could not run: ${result.error.message}`, { cause: result.error });
  }
  if (result.status !== 0) throw new Error(`ztd public status exited with code ${result.status}`);
  if (!result.stdout || !result.stdout.trim()) throw new Error('ztd public status returned no output');
  let status;
  try {
    status = JSON.parse(result.stdout);
  } catch (err) {
    throw new Error(`ztd public status returned invalid JSON: ${err.message}`, { cause: err });
  }
  if (!Number.isInteger(status?.high_water_sequence)) {
    throw new Error('ztd public status output has no high_water_sequence');
  }
  return status;
}

function checkPort(port) {
  const result = spawnSync('ss', ['-ltn', `sport = :${port}`], { encoding: 'utf8' });
  if (result.status !== 0) return false;
  return result.stdout.includes(`:${port}`);
}

// The public mirror is served only by the Gateway (`--public-spectator`). Any
// other state fails closed: nothing is started, stopped, or re-initialized here.
function requireGatewayOwnedMirror() {
  if (!checkPort(PUBLIC_PORT)) {
    throw new Error(`gateway-owned public mirror is not listening on port ${PUBLIC_PORT}; start ztd-gateway with --public-spectator`);
  }
  console.log(`Gateway-owned mirror detected on ${PUBLIC_ORIGIN}.`);
}

function feedDir() {
  return resolve(ztdCwd(), '.ztd/public-feed');
}

// Resetting the feed would delete the Gateway's signing chain and mirror state,
// so it is never performed by this script. The reset is a manual step.
function resetFeedState() {
  throw new Error('--reset with a gateway-owned mirror requires stopping the Gateway and removing .ztd/public-feed and .ztd/public-mirror first (then ./ztd gw start --public-spectator)');
}

async function generateFixtureRecords() {
  const outPath = resolve(__dirname, '.generated/fixture-records.jsonl');
  const lines = await fixturePublishLines(false);
  mkdirSync(dirname(outPath), { recursive: true });
  writeFileSync(outPath, lines.join('\n') + '\n', 'utf8');
  console.log(`Generated ${lines.length} fixture projection records -> ${outPath}`);
  return outPath;
}

// canonicalJson serializes a parsed JSON value with object keys sorted, so two
// records with identical content compare equal regardless of key order.
function canonicalJson(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`;
  if (value !== null && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

// recordContent returns the canonical content of a seeded record. The mirror
// stores each published record verbatim, and the public history API returns it
// with only the server-assigned `sequence` and `record_type` added, so those
// two keys are dropped before comparing.
function recordContent(record) {
  const content = { ...record };
  delete content.sequence;
  delete content.record_type;
  return canonicalJson(content);
}

// Load publish-input lines and index the dataset identities plus each
// dataset's catalog_snapshot content. A catalog already present in the mirror
// with identical content proves the dataset was accepted unchanged.
function loadRecords(recordsPath) {
  const content = readFileSync(recordsPath, 'utf8');
  const lines = content.split('\n').filter((line) => line.trim().length > 0);
  if (lines.length === 0) throw new Error(`records file is empty: ${recordsPath}`);
  const datasetIds = new Set();
  const catalogContents = new Map();
  const schemaVersions = new Set();
  const entries = lines.map((line, index) => {
    let parsed;
    try {
      parsed = JSON.parse(line);
    } catch (err) {
      throw new Error(`records file line ${index + 1}: invalid JSON: ${err.message}`, { cause: err });
    }
    if (typeof parsed.record_type !== 'string' || typeof parsed.record_bytes !== 'string') {
      throw new Error(`records file line ${index + 1}: missing record_type/record_bytes`);
    }
    let record;
    let datasetId;
    try {
      record = JSON.parse(parsed.record_bytes);
    } catch {
      record = null;
    }
    if (record && typeof record.schema_version === 'string') schemaVersions.add(record.schema_version);
    if (record && typeof record.dataset_id === 'string') {
      datasetId = record.dataset_id;
      datasetIds.add(datasetId);
      if (record.kind === 'catalog_snapshot' && !catalogContents.has(datasetId)) {
        catalogContents.set(datasetId, recordContent(record));
      }
    }
    return { line, bytes: Buffer.byteLength(parsed.record_bytes, 'utf8'), datasetId };
  });
  return { entries, datasetIds, catalogContents, schemaVersions };
}

// Read every mirror record for this source through the anonymous public history
// API. Gateway-owned and host mirrors serve the same routes. Any transport or
// HTTP failure throws: an empty result would make every dataset look absent and
// republish it.
async function fetchMirrorHistory() {
  const records = [];
  let cursor = '0';
  for (;;) {
    const url = `${PUBLIC_ORIGIN}/history?source=${SOURCE_ID}&cursor=${cursor}&limit=${HISTORY_PAGE_LIMIT}`;
    let response;
    try {
      response = await fetch(url, { signal: AbortSignal.timeout(HISTORY_TIMEOUT_MS) });
    } catch (err) {
      throw new Error(`mirror history unavailable at ${url}: ${err.message}`, { cause: err });
    }
    if (!response.ok) {
      throw new Error(`mirror history request failed at ${url}: HTTP ${response.status}`);
    }
    let body;
    try {
      body = await response.json();
    } catch (err) {
      throw new Error(`mirror history at ${url} returned invalid JSON: ${err.message}`, { cause: err });
    }
    if (!Array.isArray(body?.items)) {
      throw new Error(`mirror history at ${url} returned no items array`);
    }
    records.push(...body.items);
    if (!body.has_more) return records;
    const next = String(body.cursor ?? body.items.at(-1)?.sequence ?? '');
    if (body.items.length === 0 || next === cursor) {
      throw new Error(`mirror history at ${url} reports more records without advancing the cursor`);
    }
    cursor = next;
  }
}

// Classify each dataset in the records file against the mirror history:
//   seeded  - every record present and the catalog content matches
//   absent  - no records present
//   drifted - partially present, or present with different catalog content
function classifyDatasets(mirrorRecords, entries, datasetIds, catalogContents) {
  const result = { seeded: [], absent: [], drifted: [] };
  const mirrorCatalogs = new Set(
    mirrorRecords.filter((r) => r.kind === 'catalog_snapshot').map((r) => recordContent(r)),
  );
  for (const id of datasetIds) {
    const expected = entries.filter((e) => e.datasetId === id).length;
    const present = mirrorRecords.filter((r) => r.dataset_id === id).length;
    const catalog = catalogContents.get(id);
    const catalogVerified = catalog !== undefined ? mirrorCatalogs.has(catalog) : true;
    if (present === 0) {
      result.absent.push(id);
    } else if (present === expected && catalogVerified) {
      result.seeded.push(id);
    } else {
      result.drifted.push({ id, expected, present });
    }
  }
  return result;
}

function exportConfigLimits() {
  try {
    const raw = JSON.parse(readFileSync(resolve(feedDir(), 'export-config.json'), 'utf8'));
    return {
      maxRecords: raw.batch_max_records ?? DEFAULT_BATCH_MAX_RECORDS,
      maxBytes: raw.batch_max_bytes ?? DEFAULT_BATCH_MAX_BYTES,
    };
  } catch {
    return { maxRecords: DEFAULT_BATCH_MAX_RECORDS, maxBytes: DEFAULT_BATCH_MAX_BYTES };
  }
}

function chunkEntries(entries, maxRecords, maxBytes) {
  const chunks = [];
  let current = [];
  let currentBytes = 0;
  for (const entry of entries) {
    if (entry.bytes > maxBytes) {
      throw new Error(`single record_bytes exceeds batch byte limit (${entry.bytes} > ${maxBytes})`);
    }
    if (current.length >= maxRecords || currentBytes + entry.bytes > maxBytes) {
      chunks.push(current);
      current = [];
      currentBytes = 0;
    }
    current.push(entry);
    currentBytes += entry.bytes;
  }
  if (current.length > 0) chunks.push(current);
  return chunks;
}

// fetchSnapshot returns the mirror's snapshot for this source, or null only when
// the mirror positively reports it holds nothing for the source yet (the
// handler's offline answer: zero high-water, zero batches, source_offline).
// Transport errors, non-2xx statuses, invalid JSON, and missing required fields
// all throw, so a failed read can never look like an empty mirror.
async function fetchSnapshot() {
  const url = `${PUBLIC_ORIGIN}/snapshot?source=${SOURCE_ID}`;
  let response;
  try {
    response = await fetch(url, { credentials: 'omit', signal: AbortSignal.timeout(SNAPSHOT_TIMEOUT_MS) });
  } catch (err) {
    throw new Error(`mirror snapshot unavailable at ${url}: ${err.message}`, { cause: err });
  }
  if (!response.ok) {
    throw new Error(`mirror snapshot request failed at ${url}: HTTP ${response.status}`);
  }
  let body;
  try {
    body = await response.json();
  } catch (err) {
    throw new Error(`mirror snapshot at ${url} returned invalid JSON: ${err.message}`, { cause: err });
  }
  const wellFormed = body !== null && typeof body === 'object'
    && body.source_id === SOURCE_ID
    && Number.isInteger(body.high_water_sequence) && body.high_water_sequence >= 0
    && typeof body.feed_chain_hash === 'string'
    && Number.isInteger(body.batch_count) && body.batch_count >= 0
    && typeof body.freshness === 'string';
  if (!wellFormed) {
    throw new Error(`mirror snapshot at ${url} is missing required snapshot fields`);
  }
  if (body.high_water_sequence === 0 && body.batch_count === 0 && body.freshness === 'source_offline') {
    return null;
  }
  return body;
}

function publishChunks(entries) {
  const { maxRecords, maxBytes } = exportConfigLimits();
  const chunks = chunkEntries(entries, maxRecords, maxBytes);
  console.log(`Publishing ${entries.length} records in ${chunks.length} batch(es) (max ${maxRecords} records / ${maxBytes} bytes per batch)...`);
  const tmpDir = mkdtempSync(resolve(tmpdir(), 'ztd-seed-'));
  try {
    for (let i = 0; i < chunks.length; i++) {
      const chunkPath = resolve(tmpDir, `seed-batch-${String(i + 1).padStart(3, '0')}.jsonl`);
      writeFileSync(chunkPath, chunks[i].map((e) => e.line).join('\n') + '\n', 'utf8');
      runZtd(['public', 'publish', chunkPath]);
    }
  } finally {
    rmSync(tmpDir, { recursive: true, force: true });
  }
  runZtd(['public', 'push']);
}

function parseArgs() {
  const args = process.argv.slice(2);
  let reset = false;
  let records = null;
  let fixtures = false;
  let appendRevision = null;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--reset') {
      reset = true;
    } else if (args[i] === '--records' && i + 1 < args.length) {
      records = resolve(args[++i]);
    } else if (args[i] === '--fixtures') {
      fixtures = true;
    } else if (args[i] === '--append-revision' && i + 1 < args.length) {
      appendRevision = args[++i];
    }
  }
  return { reset, records, fixtures, appendRevision };
}

async function main() {
  const { reset, records, fixtures, appendRevision } = parseArgs();
  if (appendRevision && (reset || fixtures || !records)) {
    throw new Error('--append-revision requires --records and cannot be combined with --reset or --fixtures');
  }

  if (!records && !fixtures) {
    throw new Error('Specify --fixtures for mock fixture seeding or --records <path> for a pre-generated JSONL file. Real evaluations publish through ./ztd eval runs start or ./ztd eval runs publish <run-id>.');
  }
  requireGatewayOwnedMirror();
  const recordsPath = records ?? await generateFixtureRecords();
  if (!existsSync(recordsPath)) {
    throw new Error(`records file not found: ${recordsPath}`);
  }
  const { entries, datasetIds, catalogContents, schemaVersions } = loadRecords(recordsPath);
  console.log(`Loaded ${entries.length} records from ${recordsPath}`);
  console.log(`Datasets: ${[...datasetIds].join(', ')}`);

  if (appendRevision) {
    if (schemaVersions.size !== 1 || !schemaVersions.has(appendRevision)) {
      throw new Error(`--append-revision ${appendRevision} requires every record to carry that exact schema version`);
    }
    const status = ztdStatus();
    const mirrorSnap = await fetchSnapshot();
    if (status.high_water_sequence !== (mirrorSnap?.high_water_sequence ?? 0)) {
      throw new Error('publisher and mirror must be synchronized before appending a schema revision');
    }
    publishChunks(entries);
  } else if (reset) {
    resetFeedState();
  } else {
    const classified = classifyDatasets(await fetchMirrorHistory(), entries, datasetIds, catalogContents);

    if (classified.drifted.length > 0) {
      for (const d of classified.drifted) {
        console.log(`Dataset ${d.id} is stale or partial in the mirror (${d.present}/${d.expected} records or changed catalog); a feed reset is required.`);
      }
      resetFeedState();
    } else if (classified.seeded.length === datasetIds.size) {
      console.log('All seed datasets are already present with identical catalog content; skipping publish.');
      runZtd(['public', 'push']);
    } else {
      const publisherHighWater = ztdStatus().high_water_sequence;
      const mirrorSnap = await fetchSnapshot();
      const mirrorHighWater = mirrorSnap?.high_water_sequence ?? 0;
      if (publisherHighWater !== mirrorHighWater) {
        console.log(`Publisher high-water (${publisherHighWater}) differs from mirror (${mirrorHighWater}); retrying outbox.`);
        runZtd(['public', 'push']);
        const retrySnap = await fetchSnapshot();
        if ((retrySnap?.high_water_sequence ?? 0) !== publisherHighWater) {
          console.log('Mirror could not be reconciled from the outbox; a feed reset is required.');
          resetFeedState();
        }
      }
    }
  }

  const absentNow = classifyDatasets(await fetchMirrorHistory(), entries, datasetIds, catalogContents).absent;
  const absentSet = new Set(absentNow);
  const entriesToPublish = entries.filter((e) => e.datasetId === undefined || absentSet.has(e.datasetId));
  if (entriesToPublish.length > 0) {
    publishChunks(entriesToPublish);
  } else {
    console.log('Nothing to publish.');
  }

  console.log('--- status ---');
  runZtd(['public', 'status']);
  const snap = await fetchSnapshot();
  const status = ztdStatus();
  const mirrorHighWater = snap?.high_water_sequence ?? 0;
  if (mirrorHighWater !== status.high_water_sequence) {
    console.warn(`WARNING: mirror high-water ${mirrorHighWater} != publisher high-water ${status.high_water_sequence}; run ./ztd public push`);
  }
  console.log('Seed complete.');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
