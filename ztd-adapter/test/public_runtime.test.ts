// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it, vi } from 'vitest';

import {
  PublicEndpointNotAllowedError,
  PublicRuntimeConfigError,
  createPublicFetch,
  parsePublicRuntimeConfig,
} from '../src/public';

describe('public spectator runtime boundary', () => {
  it('accepts only a mirror origin', () => {
    expect(parsePublicRuntimeConfig({ schema_version: '1.0.0', mirror_origin: 'https://feed.example.com' })).toEqual({
      schema_version: '1.0.0',
      mirror_origin: 'https://feed.example.com',
    });
  });

  it.each(['gateway_base_url', 'passkey_rp_id', 'credentials', 'token', 'producer_url', 'fallback_origin'])(
    'rejects the private or credential field %s',
    (field) => {
      expect(() => parsePublicRuntimeConfig({
        schema_version: '1.0.0',
        mirror_origin: 'https://feed.example.com',
        [field]: 'forbidden',
      })).toThrow(PublicRuntimeConfigError);
    },
  );

  it('allows loopback HTTP and rejects remote HTTP, paths, queries, fragments, and userinfo', () => {
    expect(parsePublicRuntimeConfig({ schema_version: '1.0.0', mirror_origin: 'http://127.0.0.1:8081' }).mirror_origin).toBe('http://127.0.0.1:8081');
    for (const mirror_origin of [
      'http://feed.example.com',
      'https://feed.example.com/path',
      'https://feed.example.com?query=1',
      'https://feed.example.com#fragment',
      'https://user:password@feed.example.com',
    ]) {
      expect(() => parsePublicRuntimeConfig({ schema_version: '1.0.0', mirror_origin })).toThrow(PublicRuntimeConfigError);
    }
  });

  it('omits credentials on every allowlisted request', async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockImplementation(async () => new Response('{"protocol_version":"1.0.0"}', {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));
    const publicFetch = createPublicFetch(
      parsePublicRuntimeConfig({ schema_version: '1.0.0', mirror_origin: 'https://feed.example.com' }),
      fetchImpl,
    );

    await publicFetch({ endpoint: 'pointer' });
    expect(fetchImpl).toHaveBeenLastCalledWith('https://feed.example.com/pointer.json', expect.objectContaining({ method: 'GET', credentials: 'omit' }));
    await publicFetch({ endpoint: 'proofHead' });
    expect(fetchImpl).toHaveBeenLastCalledWith('https://feed.example.com/proof-head.json', expect.objectContaining({ method: 'GET', credentials: 'omit' }));
    await publicFetch({ endpoint: 'proofSegments', first: 1001 });
    expect(fetchImpl).toHaveBeenLastCalledWith('https://feed.example.com/proof-segments/1001.jsonl.gz', expect.objectContaining({ method: 'GET', credentials: 'omit' }));
    await publicFetch({ endpoint: 'history', first: 1 });
    expect(fetchImpl).toHaveBeenLastCalledWith('https://feed.example.com/history/1.json.gz', expect.objectContaining({ method: 'GET', credentials: 'omit' }));
    for (const call of fetchImpl.mock.calls) expect(String(call[0])).not.toContain('?');
  });

  it('refuses digests and indexes outside the closed grammar before fetch', async () => {
    const fetchImpl = vi.fn<typeof fetch>();
    const publicFetch = createPublicFetch(
      parsePublicRuntimeConfig({ schema_version: '1.0.0', mirror_origin: 'https://feed.example.com' }),
      fetchImpl,
    );
    const digest = 'a'.repeat(64);
    const refused = [
      { endpoint: 'readModel', digest: 'A'.repeat(64) },
      { endpoint: 'readModel', digest: `${'a'.repeat(63)}/` },
      { endpoint: 'proofMeta', digest: `../${'a'.repeat(61)}` },
      { endpoint: 'proofCatalog', digest: `${digest}?source=x` },
      { endpoint: 'proofCatalog', digest: 'a'.repeat(63) },
      { endpoint: 'proofSegments', first: 0 },
      { endpoint: 'proofSegments', first: '01' },
      { endpoint: 'history', first: 1.5 },
      { endpoint: 'history', first: 1e21 },
      { endpoint: 'history', first: '1?cursor=2' },
      { endpoint: 'history', first: '1/../2' },
      { endpoint: '/api/v1/health' },
    ] as const;

    for (const request of refused) {
      await expect(publicFetch(request as never)).rejects.toBeInstanceOf(PublicEndpointNotAllowedError);
    }
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
