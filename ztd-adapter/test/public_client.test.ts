// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { createHash } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';

import { PublicClientError, PublicEndpointNotAllowedError, createPublicClient, parsePublicRuntimeConfig } from '../src/public';

const config = parsePublicRuntimeConfig({ schema_version: '1.0.0', mirror_origin: 'https://feed.example.com' });
const zero = '0'.repeat(64);
const artifactID = 'a'.repeat(64);
const generatedAt = '2026-09-13T00:00:00Z';

const snapshot = {
  protocol_version: '1.0.0',
  source_id: 'deployment-a',
  high_water_sequence: 0,
  feed_chain_hash: zero,
  batch_count: 0,
  generated_at: generatedAt,
  freshness: 'source_offline',
};

const readModelText = JSON.stringify({
  protocol_version: '1.0.0',
  snapshot,
  source_freshness: 'source_offline',
  recent_projections: [{ sequence: 1, kind: 'catalog_snapshot' }],
  proof_catalog_summary: { artifact_count: 1, total_byte_size: 12 },
  generated_at: generatedAt,
});

const catalogText = JSON.stringify({
  schema_version: '1.0.0',
  generated_at: generatedAt,
  entries: [{
    artifact_id: artifactID,
    filename: 'proof.db',
    media_type: 'application/vnd.sqlite3',
    byte_size: 12,
    sha256: artifactID,
    classification: 'public_safe',
    campaign_id: 'campaign-1',
    generated_at: generatedAt,
    verification_command: 'ztd public verify-proof --segments proof-segments.jsonl --artifacts ./proofs',
    immutable_url: `/proofs/${artifactID}`,
  }],
});

function sha256(text: string): string {
  return createHash('sha256').update(text).digest('hex');
}

function pointerBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: '1.0.0',
    source_id: 'deployment-a',
    revision: 1,
    read_model_sha256: sha256(readModelText),
    proof_catalog_sha256: sha256(catalogText),
    proof_segment_head: 0,
    high_water_sequence: 0,
    history_first_sequence: 0,
    feed_chain_hash: zero,
    active: false,
    generated_at: generatedAt,
    ...overrides,
  };
}

const liveStatusBody = {
  schema_version: '1.0.0',
  snapshot,
  active_runs: [],
  tail: [],
};

function mirror(routes: Record<string, string | { status: number; body: string }>) {
  return vi.fn<typeof fetch>().mockImplementation(async (input) => {
    const url = new URL(String(input));
    const route = routes[url.pathname];
    if (route === undefined) return new Response('', { status: 404 });
    const { status, body } = typeof route === 'string' ? { status: 200, body: route } : route;
    return new Response(body, { status, headers: { 'Content-Type': 'application/json' } });
  });
}

describe('public spectator typed client', () => {
  it('reads the pointer, the digest-addressed read model and catalog, and live status anonymously', async () => {
    const fetchImpl = mirror({
      '/pointer.json': JSON.stringify(pointerBody()),
      [`/read-model/${sha256(readModelText)}.json.gz`]: readModelText,
      [`/proof-catalog/${sha256(catalogText)}.json.gz`]: catalogText,
      '/live/status.json': JSON.stringify(liveStatusBody),
    });
    const client = createPublicClient(config, fetchImpl);

    const pointer = await client.pointer();
    expect(pointer).toMatchObject({ source_id: 'deployment-a', revision: 1 });
    await expect(client.readModel(pointer)).resolves.toMatchObject({ source_freshness: 'source_offline' });
    await expect(client.proofCatalog(pointer)).resolves.toMatchObject({ entries: [{ filename: 'proof.db' }] });
    await expect(client.liveStatus()).resolves.toMatchObject({ active_runs: [], tail: [] });

    const urls = fetchImpl.mock.calls.map((call) => String(call[0]));
    expect(urls).toEqual([
      'https://feed.example.com/pointer.json',
      `https://feed.example.com/read-model/${sha256(readModelText)}.json.gz`,
      `https://feed.example.com/proof-catalog/${sha256(catalogText)}.json.gz`,
      'https://feed.example.com/live/status.json',
    ]);
    for (const call of fetchImpl.mock.calls) expect(call[1]).toMatchObject({ credentials: 'omit', method: 'GET' });
  });

  it('returns the proof catalog entry, and null for an unknown or withdrawn artifact', async () => {
    const entry = JSON.parse(catalogText).entries[0];
    const fetchImpl = mirror({
      [`/proof-meta/${artifactID}.json`]: JSON.stringify(entry),
    });
    const client = createPublicClient(config, fetchImpl);

    await expect(client.proofCatalogEntry(artifactID)).resolves.toMatchObject({ filename: 'proof.db' });
    await expect(client.proofCatalogEntry('b'.repeat(64))).resolves.toBeNull();
  });

  it('throws when the decoded read model does not hash to the pointer digest', async () => {
    const tampered = readModelText.replace('"artifact_count":1', '"artifact_count":2');
    const fetchImpl = mirror({ [`/read-model/${sha256(readModelText)}.json.gz`]: tampered });
    const client = createPublicClient(config, fetchImpl);

    await expect(client.readModel(pointerBody() as never)).rejects.toThrow(/digest mismatch/);
    await expect(client.readModel(pointerBody() as never)).rejects.toBeInstanceOf(PublicClientError);
  });

  it('throws when the pointer names no proof catalog', async () => {
    const fetchImpl = mirror({});
    const client = createPublicClient(config, fetchImpl);
    const pointer = pointerBody();
    delete pointer.proof_catalog_sha256;

    await expect(client.proofCatalog(pointer as never)).rejects.toBeInstanceOf(PublicClientError);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it('refuses digests with uppercase or path characters before any request', async () => {
    const fetchImpl = mirror({});
    const client = createPublicClient(config, fetchImpl);
    const invalid = ['A'.repeat(64), 'a'.repeat(63), `${'a'.repeat(62)}/.`, `${'a'.repeat(63)}?`, `../${'a'.repeat(61)}`];

    for (const digest of invalid) {
      await expect(client.proofCatalogEntry(digest)).rejects.toBeInstanceOf(PublicEndpointNotAllowedError);
      await expect(client.readModel(pointerBody({ read_model_sha256: digest }) as never)).rejects.toBeInstanceOf(PublicEndpointNotAllowedError);
    }
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it('pointer validator rejects a missing read_model_sha256', async () => {
    const body = pointerBody();
    delete body.read_model_sha256;
    const client = createPublicClient(config, mirror({ '/pointer.json': JSON.stringify(body) }));

    await expect(client.pointer()).rejects.toMatchObject({ operation: 'pointer', message: expect.stringContaining('invalid response') });
  });

  it('live status validator rejects a tail item without record_bytes', async () => {
    const body = { ...liveStatusBody, tail: [{ sequence: 1, record_type: 'projection' }] };
    const client = createPublicClient(config, mirror({ '/live/status.json': JSON.stringify(body) }));

    await expect(client.liveStatus()).rejects.toBeInstanceOf(PublicClientError);
  });

  it('fails closed on malformed or unsuccessful mirror responses', async () => {
    const malformed = mirror({ '/live/status.json': '{"snapshot":"bad"}' });
    const rejected = mirror({ '/live/status.json': { status: 429, body: '{"error":"rate limited"}' } });

    await expect(createPublicClient(config, malformed).liveStatus()).rejects.toBeInstanceOf(PublicClientError);
    await expect(createPublicClient(config, rejected).liveStatus()).rejects.toMatchObject({ status: 429 });
  });
});
