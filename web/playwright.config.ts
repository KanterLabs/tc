import { defineConfig, devices } from '@playwright/test';

/**
 * The browser suite intentionally talks to an already-running Roadmap
 * server. This keeps the suite useful against a container, a local binary,
 * or a deployed preview without coupling it to a particular server command.
 */
const baseURL = process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080';

export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.spec.ts',
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: process.env.CI ? 'line' : 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure'
  },
  expect: {
    timeout: 10_000
  },
  timeout: 45_000
});
