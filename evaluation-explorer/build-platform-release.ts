// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Resolved as a path string: under the jsdom test environment the global URL
// is jsdom's, which node:fs does not accept.
export const platformRelease = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../VERSION'), 'utf8').trim();
