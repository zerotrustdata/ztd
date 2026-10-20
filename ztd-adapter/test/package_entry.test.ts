// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { describe, expect, it } from 'vitest';

import * as entry from '../src/index';

const ADAPTER_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');

// The runtime surface documented in docs/guides/build_observe_frontend.md
// ("The audited adapter"), plus the public error classes a builder catches.
// The package entry must export exactly these names.
const DOCUMENTED_EXPORTS = [
  'EndpointNotAllowedError',
  'GatewayFetchError',
  'NormalizedEventError',
  'ObserveResponseValidationError',
  'RuntimeConfigError',
  'SseStream',
  'adapterReducer',
  'authenticatePasskey',
  'createCredentialedFetch',
  'createObserveClient',
  'enrollPasskey',
  'normalizeGatewayEvent',
  'parseRuntimeConfig',
  'registerPasskey',
];

describe('package entry', () => {
  it('package.json points import consumers at dist/index.js, which is built from src/index.ts', () => {
    const pkg = JSON.parse(readFileSync(join(ADAPTER_ROOT, 'package.json'), 'utf8')) as {
      main: string;
      types: string;
      exports: { '.': { types: string; import: string } };
    };

    expect(pkg.main).toBe('dist/index.js');
    expect(pkg.types).toBe('dist/index.d.ts');
    expect(pkg.exports['.'].import).toBe('./dist/index.js');
    expect(pkg.exports['.'].types).toBe('./dist/index.d.ts');
    expect(existsSync(join(ADAPTER_ROOT, 'src', 'index.ts'))).toBe(true);
  });

  it('exports exactly the documented runtime surface', () => {
    expect(Object.keys(entry).sort()).toEqual([...DOCUMENTED_EXPORTS].sort());
  });

  it('re-exports the implementations, not copies', async () => {
    const { parseRuntimeConfig, RuntimeConfigError } = await import('../src/config/runtime_config');
    const { SseStream } = await import('../src/sse/stream');
    const { EndpointNotAllowedError } = await import('../src/api/allowlist');
    const { GatewayFetchError } = await import('../src/api/fetch');
    const { ObserveResponseValidationError } = await import('../src/api/observe_client');
    const { NormalizedEventError } = await import('../src/sse/normalizer');
    expect(entry.parseRuntimeConfig).toBe(parseRuntimeConfig);
    expect(entry.SseStream).toBe(SseStream);
    expect(entry.RuntimeConfigError).toBe(RuntimeConfigError);
    expect(entry.EndpointNotAllowedError).toBe(EndpointNotAllowedError);
    expect(entry.GatewayFetchError).toBe(GatewayFetchError);
    expect(entry.ObserveResponseValidationError).toBe(ObserveResponseValidationError);
    expect(entry.NormalizedEventError).toBe(NormalizedEventError);
  });

  it('loads the built dist/index.js under plain Node ESM and exposes the documented exports', () => {
    const distIndex = join(ADAPTER_ROOT, 'dist', 'index.js');
    expect(existsSync(distIndex), 'dist/index.js is missing; run npm run build first').toBe(true);

    // Plain Node resolution, outside Vitest: this catches extensionless relative
    // specifiers in the emitted ESM, which Vitest's resolver would mask.
    const script = `
      const entry = await import(${JSON.stringify(pathToFileURL(distIndex).href)});
      const types = Object.fromEntries(Object.keys(entry).sort().map((k) => [k, typeof entry[k]]));
      process.stdout.write(JSON.stringify(types));
    `;
    const child = spawnSync(process.execPath, ['--input-type=module', '-e', script], {
      cwd: ADAPTER_ROOT,
      encoding: 'utf8',
      timeout: 20000,
    });

    expect(child.error, 'node failed to start').toBeUndefined();
    expect(child.status, `node import of dist/index.js failed:\n${child.stderr}`).toBe(0);

    const exported = JSON.parse(child.stdout) as Record<string, string>;
    expect(Object.keys(exported).sort()).toEqual([...DOCUMENTED_EXPORTS].sort());
    for (const name of DOCUMENTED_EXPORTS) {
      expect(exported[name], `dist export ${name}`).toBe('function');
    }
  });
});
