import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers';

test.describe('Analytics Page', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/admin/analytics');
    await expect(page.getByRole('heading', { level: 1, name: 'Analytics' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('page loads with heading', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Analytics' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Refresh' })).toBeVisible();
  });

  test('shows overview cards', async ({ page }) => {
    // Wait for data to load — overview cards should show stats
    await expect(page.getByRole('paragraph').filter({ hasText: 'Templates' })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('paragraph').filter({ hasText: 'Definitions' })).toBeVisible();
    await expect(page.getByRole('paragraph').filter({ hasText: /^Instances$/ })).toBeVisible();
  });

  test('shows template statistics section', async ({ page }) => {
    await expect(page.getByText('Template Usage')).toBeVisible({ timeout: 10_000 });
  });

  test('shows user activity section', async ({ page }) => {
    await expect(page.getByText('User Activity')).toBeVisible({ timeout: 10_000 });
  });

  test('handles empty data gracefully', async ({ page }) => {
    // The page should not show any error alerts when data is empty or loaded
    // Wait for loading to complete
    await page.waitForTimeout(2000);

    // No error alert should be visible
    const errorAlerts = page.locator('[role="alert"]').filter({ hasText: /error|fail/i });
    await expect(errorAlerts).toHaveCount(0);
  });

  test('refresh button reloads data', async ({ page }) => {
    // Wait for initial load — use #main-content to avoid matching the nav sidebar link
    await expect(page.locator('#main-content').getByText('Templates')).toBeVisible({ timeout: 10_000 });

    // Click refresh
    await page.getByRole('button', { name: 'Refresh' }).click();

    // Page should still show data after refresh (no errors)
    await expect(page.locator('#main-content').getByText('Templates')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Template Usage')).toBeVisible();
    await expect(page.getByText('User Activity')).toBeVisible();
  });
});
