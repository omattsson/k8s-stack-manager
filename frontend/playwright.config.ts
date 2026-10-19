import { defineConfig, devices } from '@playwright/test';

// The session lifecycle spec (#584) needs a backend with short session limits.
// `make test-e2e` and `make test-e2e-session` restart the backend with those
// limits, set E2E_SESSION=1 and run only the `session` project.
const SESSION_SPEC = /session-lifecycle\.spec\.ts$/;

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
      testIgnore: [/mobile\.spec\.ts$/, SESSION_SPEC],
    },
    {
      // Phone layout checks (#577): page width, tappable app bar, input font size.
      name: 'mobile',
      // Chromium with the iPhone viewport, touch and user agent: CI installs Chromium only.
      use: { ...devices['iPhone 13'], browserName: 'chromium' },
      testMatch: /mobile\.spec\.ts$/,
    },
    // Only with E2E_SESSION=1, so a plain `npx playwright test` against the
    // normal backend never runs it.
    ...(process.env.E2E_SESSION
      ? [
          {
            name: 'session',
            use: { ...devices['Desktop Chrome'] },
            testMatch: SESSION_SPEC,
            // The scenarios wait for the configured limits; keep them serial.
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
