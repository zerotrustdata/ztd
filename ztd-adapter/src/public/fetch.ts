// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import type { PublicRuntimeConfig } from './runtime_config.js';

const DIGEST_PATTERN = /^[0-9a-f]{64}$/;
const INDEX_PATTERN = /^[1-9][0-9]*$/;

/**
 * The closed set of public layout requests. Callers name an object and supply
 * only its validated key; the path is built here and nowhere else, so no free
 * path or query string can reach fetch.
 */
export type PublicRequest =
  | { readonly endpoint: 'pointer' | 'liveStatus' }
  | { readonly endpoint: 'readModel' | 'runBundle' | 'fixture'; readonly digest: string }
  | { readonly endpoint: 'history'; readonly first: number | string };

export type PublicEndpoint = PublicRequest['endpoint'];

export class PublicEndpointNotAllowedError extends Error {
  constructor(public readonly endpoint: string, public readonly suffix: string) {
    super(`public endpoint not allowed: ${endpoint}${suffix}`);
    this.name = 'PublicEndpointNotAllowedError';
  }
}

export interface PublicResponse {
  readonly status: number;
  readonly ok: boolean;
  /** Response bytes after transport decoding. Digests are computed over these. */
  readonly bytes: ArrayBuffer;
  readonly headers: Headers;
}

export interface PublicFetch {
  (request: PublicRequest, signal?: AbortSignal): Promise<PublicResponse>;
}

function digestKey(endpoint: PublicEndpoint, value: unknown): string {
  if (typeof value !== 'string' || !DIGEST_PATTERN.test(value)) {
    throw new PublicEndpointNotAllowedError(endpoint, `/${String(value)}`);
  }
  return value;
}

function indexKey(endpoint: PublicEndpoint, value: unknown): string {
  if (typeof value === 'number' && !Number.isSafeInteger(value)) {
    throw new PublicEndpointNotAllowedError(endpoint, `/${String(value)}`);
  }
  const text = typeof value === 'number' ? String(value) : value;
  if (typeof text !== 'string' || !INDEX_PATTERN.test(text)) {
    throw new PublicEndpointNotAllowedError(endpoint, `/${String(value)}`);
  }
  return text;
}

function publicPath(request: PublicRequest): string {
  switch (request.endpoint) {
    case 'pointer': return '/pointer.json';
    case 'liveStatus': return '/live/status.json';
    case 'readModel': return `/read-model/${digestKey(request.endpoint, request.digest)}.json.gz`;
    case 'runBundle': return `/runs/${digestKey(request.endpoint, request.digest)}.jsonl.gz`;
    case 'fixture': return `/fixtures/${digestKey(request.endpoint, request.digest)}`;
    case 'history': return `/history/${indexKey(request.endpoint, request.first)}.json.gz`;
    default: throw new PublicEndpointNotAllowedError(String((request as { endpoint: unknown }).endpoint), '');
  }
}

/** Decodes response bytes as JSON. Empty or invalid input yields undefined, which validators reject. */
export function decodePublicJSON(bytes: ArrayBuffer): unknown {
  const text = new TextDecoder().decode(bytes);
  if (text === '') return undefined;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return undefined;
  }
}

export function createPublicFetch(config: PublicRuntimeConfig, fetchImpl: typeof fetch = fetch): PublicFetch {
  return async (request, signal) => {
    const path = publicPath(request);
    const response = await fetchImpl(`${config.mirror_origin}${path}`, {
      method: 'GET',
      credentials: 'omit',
      signal,
    });
    return { status: response.status, ok: response.ok, bytes: await response.arrayBuffer(), headers: response.headers };
  };
}
