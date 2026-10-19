import { defineConfig, devices } from '@playwright/test';

// Specs that must not run next to the main suite run in the `isolated`
// projects, serially, against a restarted backend: session-lifecycle (#584)
// needs short session limits, last-admin (#585) disables the built-in admin for
// a moment. `make test-e2e` and `make test-e2e-isolated` restart the backend,
// set E2E_ISOLATED=1 and run `--project=isolated --workers=1`.
const SESSION_SPEC = /session-lifecycle\.spec\.ts$/;
const LAST_ADMIN_SPEC = /last-admin\.spec\.ts$/;
const ISOLATED_SPECS = /(session-lifecycle|last-admin)\.spec\.ts$/;

export default defineConfig({
  testDir: './e2e',
  // Sets up the shared default cluster of the specs (see e2e/global-setup.ts).
  globalSetup: './e2e/global-setup.ts',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  // PW_WORKERS sets the worker count (also in CI); else 1 in CI and the Playwright default locally.
  workers: (() => {
    const n = parseInt(process.env.PW_WORKERS ?? '', 10);
    if (Number.isFinite(n)) return n;
    return process.env.CI ? 1 : undefined;
  })(),
  // In CI, never start the report server: it waits for Ctrl+C.
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'html',
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'on-first-retry',
  },
  timeout: 60_000,
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
      testIgnore: [/mobile\.spec\.ts$/, ISOLATED_SPECS],
    },
    {
      // Phone layout checks (#577): page width, tappable app bar, input font size.
      name: 'mobile',
      // Chromium with the iPhone viewport, touch and user agent: CI installs Chromium only.
      use: { ...devices['iPhone 13'], browserName: 'chromium' },
      testMatch: /mobile\.spec\.ts$/,
    },
    // Only with E2E_ISOLATED=1, so a plain `npx playwright test` against the
    // normal backend never runs them.
    ...(process.env.E2E_ISOLATED
      ? [
          {
            name: 'isolated',
            use: { ...devices['Desktop Chrome'] },
            testMatch: SESSION_SPEC,
            // The scenarios wait for the configured limits: keep them serial.
            fullyParallel: false,
            timeout: 180_000,
            // last-admin runs after this project, also when it fails: a
            // teardown project is part of `--project=isolated`. Run last, a
            // last-admin worker that dies before its afterAll (SIGKILL, OOM)
            // can leave `admin` disabled without failing the session specs
            // for the wrong reason.
            teardown: 'isolated-last-admin',
          },
          {
            // Disables `admin` for a moment. Runs alone with
            // `--project=isolated-last-admin`.
            name: 'isolated-last-admin',
            use: { ...devices['Desktop Chrome'] },
            testMatch: LAST_ADMIN_SPEC,
            fullyParallel: false,
            timeout: 180_000,
          },
        ]
      : []),
  ],
  webServer: {
    command: 'npm run dev',
    url: 'http://localhost:3000',
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
