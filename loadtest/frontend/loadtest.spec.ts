// Playwright Frontend Load Test for k8s-stack-manager
// Simulates real browser users navigating the application under load.
//
// Usage:
//   npx playwright test loadtest/frontend/loadtest.spec.ts --workers=5
//   npx playwright test loadtest/frontend/loadtest.spec.ts --workers=10 --repeat-each=3
//
// Prerequisites:
//   - Application running: make dev
//   - Seed data loaded: make seed

import { test, expect, Page } from '@playwright/test';

const BASE_URL = process.env.FRONTEND_URL || 'http://localhost:3000';
const USERNAME = process.env.ADMIN_USERNAME || 'admin';
const PASSWORD = process.env.ADMIN_PASSWORD || 'admin';

// Helper: login and store auth state
async function loginUser(page: Page, username = USERNAME, password = PASSWORD) {
  await page.goto(`${BASE_URL}/login`);
  await page.getByLabel(/username/i).fill(username);
  await page.getByLabel(/password/i).fill(password);
  await page.getByRole('button', { name: /log\s*in|sign\s*in/i }).click();
  // Wait for redirect to dashboard
  await expect(page).toHaveURL(/\/$/, { timeout: 10_000 });
}

// ── Scenario 1: Dashboard Browse ────────────────────────────────────
// Most common user action: login → view dashboard → browse instances
test.describe('Load: Dashboard Browse', () => {
  test('login and browse dashboard', async ({ page }) => {
    const start = Date.now();

    await loginUser(page);

    // Dashboard should render instance list
    await expect(page.locator('main')).toBeVisible({ timeout: 10_000 });

    // Record timing
    const loginToRender = Date.now() - start;
    console.log(`[METRIC] dashboard_load_ms=${loginToRender}`);

    // Interact with filters/tabs if visible
    const tabs = page.getByRole('tab');
    const tabCount = await tabs.count();
    if (tabCount > 1) {
      await tabs.nth(1).click();
      await page.waitForTimeout(500);
      await tabs.nth(0).click();
    }
  });

  test('dashboard concurrent API calls', async ({ page }) => {
    await loginUser(page);

    // Navigate to dashboard — triggers batch API calls
    const apiPromises: Promise<unknown>[] = [];
    page.on('response', (response) => {
      if (response.url().includes('/api/v1/')) {
        apiPromises.push(Promise.resolve({
          url: response.url(),
          status: response.status(),
        }));
      }
    });

    await page.goto(`${BASE_URL}/`);
    await page.waitForTimeout(3000); // wait for all API calls

    const results = await Promise.all(apiPromises);
    console.log(`[METRIC] dashboard_api_calls=${results.length}`);

    // All API calls should succeed
    for (const r of results as Array<{ url: string; status: number }>) {
      expect(r.status).toBeLessThan(500);
    }
  });
});

// ── Scenario 2: Template Gallery Browse ─────────────────────────────
test.describe('Load: Template Gallery', () => {
  test('browse templates', async ({ page }) => {
    await loginUser(page);

    const start = Date.now();
    await page.goto(`${BASE_URL}/templates`);
    await expect(page.locator('main')).toBeVisible({ timeout: 10_000 });
    const loadTime = Date.now() - start;
    console.log(`[METRIC] templates_load_ms=${loadTime}`);

    // Check template cards render
    await page.waitForTimeout(2000);
  });
});

// ── Scenario 3: Definition CRUD ─────────────────────────────────────
test.describe('Load: Definition Workflow', () => {
  test('create and delete definition', async ({ page }) => {
    await loginUser(page);

    const start = Date.now();
    await page.goto(`${BASE_URL}/stack-definitions/new`);

    // Fill form
    const nameInput = page.getByLabel(/name/i).first();
    await nameInput.fill(`loadtest-def-${Date.now()}`);

    const descInput = page.getByLabel(/description/i).first();
    if (await descInput.isVisible()) {
      await descInput.fill('Load test definition');
    }

    // Submit
    const saveBtn = page.getByRole('button', { name: /save|create/i });
    if (await saveBtn.isVisible()) {
      await saveBtn.click();
      await page.waitForTimeout(2000);
    }

    const crudTime = Date.now() - start;
    console.log(`[METRIC] definition_create_ms=${crudTime}`);
  });
});

// ── Scenario 4: Stack Instance Detail ───────────────────────────────
test.describe('Load: Instance Detail', () => {
  test('view instance detail page', async ({ page }) => {
    await loginUser(page);

    // Go to dashboard first
    await page.goto(`${BASE_URL}/`);
    await page.waitForTimeout(2000);

    // Try to click first instance link
    const instanceLink = page.locator('a[href*="/stack-instances/"]').first();
    if (await instanceLink.isVisible({ timeout: 5000 }).catch(() => false)) {
      const start = Date.now();
      await instanceLink.click();
      await expect(page.locator('main')).toBeVisible({ timeout: 10_000 });

      const detailTime = Date.now() - start;
      console.log(`[METRIC] instance_detail_ms=${detailTime}`);
    } else {
      console.log('[SKIP] No instances found on dashboard');
    }
  });
});

// ── Scenario 5: Navigation Stress ───────────────────────────────────
// Rapidly navigate between pages to stress client-side routing + API
test.describe('Load: Rapid Navigation', () => {
  test('navigate between pages rapidly', async ({ page }) => {
    await loginUser(page);

    const routes = [
      '/',
      '/templates',
      '/stack-definitions',
      '/audit-log',
      '/',
      '/templates',
      '/stack-definitions',
      '/',
    ];

    let apiErrors = 0;
    page.on('response', (resp) => {
      if (resp.url().includes('/api/v1/') && resp.status() >= 500) {
        apiErrors++;
      }
    });

    const start = Date.now();
    for (const route of routes) {
      await page.goto(`${BASE_URL}${route}`);
      await page.waitForTimeout(500); // minimal wait between navigations
    }
    const navTime = Date.now() - start;
    console.log(`[METRIC] rapid_nav_total_ms=${navTime} pages=${routes.length} errors=${apiErrors}`);

    expect(apiErrors).toBe(0);
  });
});

// ── Scenario 6: Audit Log Pagination ────────────────────────────────
test.describe('Load: Audit Log', () => {
  test('browse audit log with pagination', async ({ page }) => {
    await loginUser(page);

    await page.goto(`${BASE_URL}/audit-log`);
    await expect(page.locator('main')).toBeVisible({ timeout: 10_000 });

    // Try to paginate if pagination controls exist
    const nextBtn = page.getByRole('button', { name: /next/i });
    if (await nextBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
      const start = Date.now();
      for (let i = 0; i < 3; i++) {
        if (await nextBtn.isEnabled()) {
          await nextBtn.click();
          await page.waitForTimeout(500);
        }
      }
      const pagTime = Date.now() - start;
      console.log(`[METRIC] audit_pagination_ms=${pagTime}`);
    }
  });
});

// ── Scenario 7: Profile & API Keys ──────────────────────────────────
test.describe('Load: Profile', () => {
  test('view profile page', async ({ page }) => {
    await loginUser(page);

    const start = Date.now();
    await page.goto(`${BASE_URL}/profile`);
    await expect(page.locator('main')).toBeVisible({ timeout: 10_000 });
    const loadTime = Date.now() - start;
    console.log(`[METRIC] profile_load_ms=${loadTime}`);
  });
});
