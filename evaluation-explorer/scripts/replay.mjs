// Publish a deterministic scripted live evaluation through the real mirror
// at a configurable speed. This is the Checkpoint A scripted-SSE producer:
// it emits typed live event records generated from
// the checked-in fixtures, and sends them through `ztd public publish` +
// `ztd public push`. The browser receives them through the same SSE
// transport as real events.
//
// Each invocation mints a fresh live-run identity (dataset_id, run_id,
// event_id, assignment_id) so a replayed run is a new logical run rather
// than a duplicate of a previous one. observed_at is stamped at publish
// time so elapsed times in the UI are real.
//
// Before streaming events, the script publishes the live dataset's
// projection records (catalog_snapshot and evaluation_summary) so the
// dataset exists in the catalog before the first event arrives.
//
// Usage:  node scripts/replay.mjs [--interval <ms>] [--once] [--run-tag <tag>]
//   --interval   delay between events in milliseconds (default: 1000)
//   --once       publish the projections and all events without delays
//   --run-tag    identity suffix for this run (default: current timestamp)
//
// The mirror is started detached if it is not already running. The feed
// must be initialized (run scripts/seed.mjs or `ztd public init` first).

import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, existsSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { fixturePublishLines } from './fixture-lines.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(__dirname, '..');

const PRIVATE_PORT = process.env.ZTD_PRIVATE_PORT ?? '8081';
const PUBLIC_PORT = process.env.ZTD_PUBLIC_PORT ?? '8082';
const LIVE_DATASET_PREFIX = 'ds-live-demo-';
const LIVE_RUN_PREFIX = 'run-live-demo';

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
}

function checkPort(port) {
  const result = spawnSync('ss', ['-ltn', `sport = :${port}`], { encoding: 'utf8' });
  if (result.status !== 0) return false;
  return result.stdout.includes(`:${port}`);
}

function waitForPort(port, label, timeoutMs) {
  return new Promise((resolvePromise, reject) => {
    const deadline = Date.now() + timeoutMs;
    const check = () => {
      if (checkPort(port)) return resolvePromise();
      if (Date.now() > deadline) return reject(new Error(`${label} on port ${port} did not start within ${timeoutMs}ms`));
      setTimeout(check, 200);
    };
    check();
  });
}

async function ensureMirror() {
  if (checkPort(PRIVATE_PORT) && checkPort(PUBLIC_PORT)) return;
  console.log('Mirror not running; starting it detached...');
  const proc = spawn(ztdBin(), [
    'public', 'mirror', 'run',
    '--listen', `127.0.0.1:${PRIVATE_PORT}`,
    '--public-listen', `127.0.0.1:${PUBLIC_PORT}`,
  ], { cwd: ztdCwd(), detached: true, stdio: 'ignore' });
  proc.unref();
  await waitForPort(PUBLIC_PORT, 'mirror public listener', 10000);
  if (!checkPort(PRIVATE_PORT)) {
    throw new Error(`mirror private listener on port ${PRIVATE_PORT} did not start`);
  }
  console.log(`Mirror is up on ${PRIVATE_PORT} (private) and ${PUBLIC_PORT} (public).`);
}

function defaultRunTag() {
  return new Date().toISOString().replace(/[-:T]/g, '').replace(/\..+$/, '');
}

// Rewrite a record's live-run identity fields to this invocation's run tag.
// Fixture run ids are run-live-demo[-failed|-stopped]-20260914; they map to
// run-live-<tag>[-failed|-stopped]. Dataset id ds-live-demo-20260914 maps to
// ds-live-<tag>. event_id and assignment_id gain a -<tag> suffix.
function rewriteIdentity(record, runTag, now) {
  const next = { ...record };
  if (typeof next.dataset_id === 'string' && next.dataset_id.startsWith(LIVE_DATASET_PREFIX)) {
    next.dataset_id = `ds-live-${runTag}`;
  }
  if (typeof next.run_id === 'string' && next.run_id.startsWith(LIVE_RUN_PREFIX)) {
    const suffix = next.run_id
      .slice(LIVE_RUN_PREFIX.length)
      .replace(/-\d{8}$/, '');
    next.run_id = `run-live-${runTag}${suffix}`;
  }
  if (typeof next.event_id === 'string') next.event_id = `${next.event_id}-${runTag}`;
  if (typeof next.assignment_id === 'string') next.assignment_id = `${next.assignment_id}-${runTag}`;
  if (typeof next.observed_at === 'string') next.observed_at = now;
  if (typeof next.generated_at === 'string') next.generated_at = now;
  if (typeof next.started_at === 'string') next.started_at = now;
  if (typeof next.source_revision_label === 'string') next.source_revision_label = `live-demo-${runTag}`;
  if (typeof next.title === 'string' && next.dataset_id && next.dataset_id.startsWith('ds-live-')) {
    next.title = `Live demo run (${runTag})`;
  }
  return next;
}

function emitLine(record, recordType) {
  return JSON.stringify({ record_type: recordType, record_bytes: JSON.stringify(record) });
}

function publishLines(lines, tmpDir, batchLabel) {
  const batchPath = resolve(tmpDir, `batch-${batchLabel}.jsonl`);
  writeFileSync(batchPath, lines.join('\n') + '\n', 'utf8');
  runZtd(['public', 'publish', batchPath]);
  runZtd(['public', 'push']);
}

function parseArgs() {
  const args = process.argv.slice(2);
  let interval = 1000;
  let once = false;
  let runTag = defaultRunTag();
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--interval' && i + 1 < args.length) {
      interval = parseInt(args[++i], 10);
      if (!Number.isFinite(interval) || interval < 0) {
        throw new Error(`invalid --interval: ${args[i]}`);
      }
    } else if (args[i] === '--once') {
      once = true;
    } else if (args[i] === '--run-tag' && i + 1 < args.length) {
      runTag = args[++i];
      if (!/^[A-Za-z0-9][A-Za-z0-9-]*$/.test(runTag)) {
        throw new Error(`invalid --run-tag: ${runTag}`);
      }
    }
  }
  return { interval, once, runTag };
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function main() {
  const { interval, once, runTag } = parseArgs();
  const allLines = await fixturePublishLines(true);

  const projections = [];
  const events = [];
  for (const line of allLines) {
    let parsed;
    try {
      parsed = JSON.parse(line);
    } catch {
      continue;
    }
    let record;
    try {
      record = JSON.parse(parsed.record_bytes);
    } catch {
      continue;
    }
    if (parsed.record_type === 'event') {
      events.push(record);
    } else if (parsed.record_type === 'projection' && typeof record.dataset_id === 'string' && record.dataset_id.startsWith(LIVE_DATASET_PREFIX)) {
      projections.push(record);
    }
  }

  if (events.length === 0) {
    console.log('No live events to replay.');
    return;
  }

  console.log(`Replaying ${events.length} live events for run tag ${runTag} (interval=${interval}ms, once=${once})`);

  await ensureMirror();

  const tmpDir = mkdtempSync(resolve(tmpdir(), 'ztd-replay-'));
  try {
    // Publish the live dataset's projection records first so the dataset
    // exists in the catalog before the first event arrives.
    const now = new Date().toISOString();
    const projectionLines = projections.map((record) => emitLine(rewriteIdentity(record, runTag, now), 'projection'));
    if (projectionLines.length > 0) {
      console.log(`Publishing ${projectionLines.length} live-dataset projection record(s)...`);
      publishLines(projectionLines, tmpDir, 'projections');
    }

    if (once) {
      const eventLines = events.map((record) => emitLine(rewriteIdentity(record, runTag, new Date().toISOString()), 'event'));
      publishLines(eventLines, tmpDir, 'events');
    } else {
      for (let i = 0; i < events.length; i++) {
        console.log(`[${new Date().toISOString()}] Publishing event ${i + 1}/${events.length}: ${events[i].kind}`);
        publishLines([emitLine(rewriteIdentity(events[i], runTag, new Date().toISOString()), 'event')], tmpDir, String(i + 1));
        if (i < events.length - 1) await sleep(interval);
      }
    }
    console.log('--- final status ---');
    runZtd(['public', 'status']);
    console.log('Replay complete.');
  } finally {
    rmSync(tmpDir, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
