// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import fs from 'node:fs';
import { defineConfig, devices } from '@playwright/test';

if (process.platform === 'linux') {
  const snapLib = '/snap/gnome-46-2404/current/usr/lib/x86_64-linux-gnu';
  if (fs.existsSync(snapLib) && !process.env.LD_LIBRARY_PATH?.includes(snapLib)) {
    process.env.LD_LIBRARY_PATH = process.env.LD_LIBRARY_PATH
      ? `${snapLib}:${process.env.LD_LIBRARY_PATH}`
      : snapLib;
  }
}

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  reporter: 'line',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    {
      name: 'production-chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: {
    command: 'npm run preview',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
