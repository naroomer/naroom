// playwright.config.js — V2 browser test suite.
//
// Infrastructure:
//   - Run tests via `node e2e/pw.js` (or `npm test`), NOT `npx playwright test` directly.
//   - pw.js reserves PW_SK_PORT / PW_GO_PORT / PW_RUN_ID BEFORE Playwright starts, so
//     this config reads the correct port at load time (before globalSetup runs).
//   - globalSetup builds the Go binary, starts both servers on those pre-allocated ports.
//   - globalTeardown SIGTERMs process groups, verifies port closure (failure = hard error).
//   - Two consecutive `node e2e/pw.js` runs use different ports and PIDs.
//
// Projects:
//   - chromium-desktop : critical path (v2-critical-path-desktop.spec.js) + smoke
//   - chromium-mobile  : mobile critical path (v2-critical-path-mobile.spec.js) + smoke
//   - firefox-smoke    : rendering smoke only (v2-smoke.spec.js)
//   - webkit-smoke     : rendering smoke only (v2-smoke.spec.js)
//
// Run:
//   npm run build       # build SvelteKit first
//   npm test            # run once  (via node e2e/pw.js)
//   npm test            # run again (two consecutive passes required)
//   npx playwright test --list   # list tests (uses fallback port 4173 — OK for listing)

import { defineConfig, devices } from '@playwright/test';

// PW_SK_PORT is pre-set by e2e/pw.js BEFORE Playwright starts, so this reads the
// correct run-specific port. Fallback 4173 is only used for `--list` (no server needed).
const SK_PORT = parseInt(process.env.PW_SK_PORT ?? '4173', 10);

export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  retries: 0,
  workers: 1,
  reporter: 'list',
  globalSetup: './e2e/global-setup.js',
  globalTeardown: './e2e/global-teardown.js',

  use: {
    baseURL: `http://127.0.0.1:${SK_PORT}`,
    actionTimeout: 15_000,
  },

  projects: [
    {
      name: 'chromium-desktop',
      use: { ...devices['Desktop Chrome'], headless: true },
      testMatch: [
        '**/v2-critical-path-desktop.spec.js',
        '**/v2-smoke.spec.js',
      ],
    },
    {
      name: 'chromium-mobile',
      use: { ...devices['Pixel 5'], headless: true },
      testMatch: [
        '**/v2-critical-path-mobile.spec.js',
        '**/v2-smoke.spec.js',
      ],
    },
    {
      name: 'firefox-smoke',
      use: { ...devices['Desktop Firefox'], headless: true },
      testMatch: ['**/v2-smoke.spec.js'],
    },
    {
      name: 'webkit-smoke',
      use: { ...devices['Desktop Safari'], headless: true },
      testMatch: ['**/v2-smoke.spec.js'],
    },
  ],
});
