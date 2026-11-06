// Mirror health check. Reports mirror process, public endpoint reachability,
// snapshot high-water, source freshness, read-model digest, live status,
// and publisher-to-mirror drift. Also verifies the public boundary: mutation
// routes must be absent from the public listener and unauthenticated ingest
// on the private listener must be rejected.
//
// Never prints secrets, tokens, or private endpoint details.
//
// Usage:  node scripts/mirror-health.mjs
//
// Exits with code 0 if the public listener is reachable and serves pointer.json;
// exits with code 1 otherwise.

import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(__dirname, '..');

const PUBLIC_ORIGIN = process.env.ZTD_PUBLIC_ORIGIN ?? 'http://127.0.0.1:8082';
const PRIVATE_ORIGIN = process.env.ZTD_PRIVATE_ORIGIN ?? 'http://127.0.0.1:8081';

async function fetchJSON(url, timeoutMs) {
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const response = await fetch(url, {
      method: 'GET',
      credentials: 'omit',
      headers: { Accept: 'application/json' },
      signal: controller.signal,
    });
    clearTimeout(timer);
    if (!response.ok) {
      return { ok: false, status: response.status, body: null };
    }
    const body = await response.json();
    return { ok: true, status: response.status, body };
  } catch {
    return { ok: false, status: 0, body: null };
  }
}

async function fetchPOST(url, timeoutMs) {
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const response = await fetch(url, {
      method: 'POST',
      credentials: 'omit',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
      signal: controller.signal,
    });
    clearTimeout(timer);
    return { ok: response.ok, status: response.status, body: null };
  } catch {
    return { ok: false, status: 0, body: null };
  }
}

// Fetch a layout object's raw bytes. Digest-addressed objects are checked against their digest
// over these bytes, after transport decoding.
async function fetchBytes(url, timeoutMs) {
  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const response = await fetch(url, { method: 'GET', credentials: 'omit', signal: controller.signal });
    clearTimeout(timer);
    if (!response.ok) return { ok: false, status: response.status, bytes: null };
    return { ok: true, status: response.status, bytes: Buffer.from(await response.arrayBuffer()) };
  } catch {
    return { ok: false, status: 0, bytes: null };
  }
}

function ztdBin() {
  const env = process.env.ZTD_BIN;
  if (env) return env;
  let dir = projectRoot;
  for (let i = 0; i < 5; i++) {
    const candidate = resolve(dir, 'ztd');
    if (existsSync(candidate)) return candidate;
    dir = resolve(dir, '..');
  }
  return '';
}

function ztdStatus() {
  const bin = ztdBin();
  if (!bin) return null;
  const cwd = resolve(bin, '..');
  const result = spawnSync(bin, ['public', 'status'], { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  if (result.status !== 0 || !result.stdout.trim()) return null;
  try {
    return JSON.parse(result.stdout);
  } catch {
    return null;
  }
}

async function main() {
  console.log('=== Mirror Health Check ===');
  console.log(`Public endpoint:  ${PUBLIC_ORIGIN}`);
  console.log(`Private endpoint: ${PRIVATE_ORIGIN} (auth required, not tested for data)`);

  const status = ztdStatus();
  let publisherHighWater = null;
  if (status) {
    publisherHighWater = status.high_water_sequence ?? 0;
    console.log('\n--- Publisher Status ---');
    console.log(`  source_id:           ${status.source_id ?? 'unknown'}`);
    console.log(`  mirror_origin:       ${status.mirror_origin ?? 'unknown'}`);
    console.log(`  enabled:             ${status.enabled ?? 'unknown'}`);
    console.log(`  high_water_sequence: ${status.high_water_sequence ?? 0}`);
    console.log(`  batch_count:         ${status.batch_count ?? 0}`);
    console.log(`  feed_chain_hash:     ${status.feed_chain_hash ?? 'unknown'}`);
  } else {
    console.log('\n--- Publisher Status ---');
    console.log('  (ztd public status unavailable)');
  }

  console.log(`\n--- Public Listener (${PUBLIC_ORIGIN}) ---`);
  const pointerResponse = await fetchJSON(`${PUBLIC_ORIGIN}/pointer.json`, 3000);
  const pointer = pointerResponse.body;
  const readModelSha = typeof pointer?.read_model_sha256 === 'string' ? pointer.read_model_sha256 : '';
  if (!pointerResponse.ok || !/^[0-9a-f]{64}$/u.test(readModelSha)) {
    console.log(`  pointer:    FAILED (status ${pointerResponse.status})`);
    console.log('\n  MIRROR UNHEALTHY: public listener not reachable.');
    process.exit(1);
  }
  console.log('  pointer:    OK');
  console.log(`  revision:    ${pointer.revision ?? 'unknown'}`);
  console.log(`  active:      ${pointer.active ?? 'unknown'}`);

  const readModel = await fetchBytes(`${PUBLIC_ORIGIN}/read-model/${readModelSha}.json.gz`, 5000);
  const digestOk = readModel.ok && createHash('sha256').update(readModel.bytes).digest('hex') === readModelSha;
  const bs = digestOk ? JSON.parse(readModel.bytes.toString('utf8')) : null;
  const snapshot = bs?.snapshot;
  console.log(`  read model: ${digestOk ? 'OK (digest matches pointer)' : `FAILED (status ${readModel.status})`}`);
  if (digestOk) {
    const recent = Array.isArray(bs.recent_projections) ? bs.recent_projections.length : 0;
    console.log(`  freshness:   ${snapshot?.freshness ?? 'unknown'}`);
    console.log(`  high_water:  ${snapshot?.high_water_sequence ?? 0}`);
    console.log(`  batch_count: ${snapshot?.batch_count ?? 0}`);
    console.log(`  recent projections: ${recent}`);

  }

  if (publisherHighWater !== null) {
    const mirrorHighWater = snapshot?.high_water_sequence ?? 0;
    if (mirrorHighWater === publisherHighWater) {
      console.log(`  drift:       none (publisher and mirror at ${mirrorHighWater})`);
    } else {
      console.log(`  drift:       publisher=${publisherHighWater} mirror=${mirrorHighWater} -- run ./ztd public push`);
    }
  }

  const live = await fetchJSON(`${PUBLIC_ORIGIN}/live/status.json`, 3000);
  if (live.ok) {
    const activeRuns = Array.isArray(live.body?.active_runs) ? live.body.active_runs.length : 0;
    const tail = Array.isArray(live.body?.tail) ? live.body.tail.length : 0;
    console.log(`  live status: OK (${activeRuns} active run(s), ${tail} tail record(s))`);
  } else {
    console.log(`  live status: FAILED (status ${live.status})`);
  }

  console.log('\n--- Public Boundary ---');
  for (const route of ['/ingest', '/keys/register', '/proof-ingest']) {
    const result = await fetchPOST(`${PUBLIC_ORIGIN}${route}`, 2000);
    console.log(`  POST ${route}: ${result.status === 404 ? '404 (isolated)' : `status ${result.status} (NOT ISOLATED)`}`);
  }
  for (const route of ['/proof-catalog', '/proof-head', '/proof-head.json', '/proof-segments']) {
    const result = await fetchJSON(`${PUBLIC_ORIGIN}${route}`, 2000);
    console.log(`  GET ${route}: ${result.status === 404 ? '404 (removed)' : `status ${result.status} (NOT REMOVED)`}`);
  }

  const privateIngest = await fetchPOST(`${PRIVATE_ORIGIN}/ingest`, 2000);
  console.log(`  private /ingest no-auth: ${privateIngest.status === 401 ? '401 (auth enforced)' : `status ${privateIngest.status}`}`);

  console.log('\n=== Health Check Complete ===');
  process.exit(0);
}

main().catch((err) => {
  console.error('Health check failed:', err);
  process.exit(1);
});
