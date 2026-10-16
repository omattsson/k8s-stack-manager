import { defineConfig, devices } from '@playwright/test';

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
    },
  ],
  webServer: {
    command: 'npm run dev',
    url: 'http://localhost:3000',
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
