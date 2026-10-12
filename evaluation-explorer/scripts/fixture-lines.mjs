// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Load the TypeScript fixture publish lines in-process through Vite's module
// runner, shared by seed.mjs and replay.mjs.

import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runnerImport } from 'vite';

const __dirname = dirname(fileURLToPath(import.meta.url));

export async function fixturePublishLines(includeEvents) {
  const { module } = await runnerImport(resolve(__dirname, 'fixture-publish-lines.ts'), {
    configFile: false,
    root: resolve(__dirname, '..'),
    logLevel: 'error',
  });
  return module.fixturePublishLines(includeEvents);
}
