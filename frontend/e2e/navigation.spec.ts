import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers';

test.describe('Navigation & Layout', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
  });

  test('app bar shows navigation buttons', async ({ page }) => {
    await expect(page.getByRole('link', { name: 'K8s Stack Manager' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Dashboard' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Templates' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Definitions' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Audit Log' })).toBeVisible();
    // admin sees Users link
    await expect(page.getByRole('button', { name: 'Users' })).toBeVisible();
  });

  test('displays logged-in user info', async ({ page }) => {
    await expect(page.getByRole('button', { name: 'admin' })).toBeVisible();
    await expect(page.getByText('admin', { exact: true }).first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'Logout' })).toBeVisible();
  });

  test('Dashboard link navigates to /', async ({ page }) => {
    await page.goto('/templates');
    await page.getByRole('button', { name: 'Dashboard' }).click();
    await expect(page).toHaveURL('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('Templates link navigates to /templates', async ({ page }) => {
    await page.getByRole('button', { name: 'Templates' }).click();
    await expect(page).toHaveURL('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('Definitions link navigates to /stack-definitions', async ({ page }) => {
    await page.getByRole('button', { name: 'Definitions' }).click();
    await expect(page).toHaveURL('/stack-definitions');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('Audit Log link navigates to /audit-log', async ({ page }) => {
    await page.getByRole('button', { name: 'Audit Log' }).click();
    await expect(page).toHaveURL('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('Users link navigates to /admin/users (admin only)', async ({ page }) => {
    await page.getByRole('button', { name: 'Users' }).click();
    await expect(page).toHaveURL('/admin/users');
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible({ timeout: 10_000 });
  });

  test('profile link navigates to /profile', async ({ page }) => {
    // The username button in the nav bar links to /profile
    await page.getByRole('button', { name: 'admin' }).click();
    await expect(page).toHaveURL('/profile');
  });

  test('app title link navigates to home', async ({ page }) => {
    await page.goto('/templates');
    await page.getByRole('link', { name: 'K8s Stack Manager' }).click();
    await expect(page).toHaveURL('/');
  });
});
