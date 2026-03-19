import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers';

test.describe('Authentication', () => {
  test('unauthenticated users are redirected to login', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: 'Sign In' })).toBeVisible();
  });

  test('login page shows Sign In form', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByLabel('Username')).toBeVisible();
    await expect(page.getByLabel('Password')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign In' })).toBeVisible();
  });

  test('successful login redirects to dashboard', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Username').fill('admin');
    await page.getByLabel('Password').fill('admin');
    await page.getByRole('button', { name: 'Sign In' }).click();
    await expect(page).toHaveURL('/', { timeout: 10_000 });
    // Dashboard heading
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('invalid credentials show error message', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Username').fill('admin');
    await page.getByLabel('Password').fill('wrongpassword');
    await page.getByRole('button', { name: 'Sign In' }).click();
    await expect(page.getByRole('alert')).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText('Invalid username or password')).toBeVisible();
  });

  test('logout returns to login page', async ({ page }) => {
    await loginAsAdmin(page);
    // Click logout button in the nav bar
    await page.getByRole('button', { name: 'Logout' }).click();
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: 'Sign In' })).toBeVisible();
  });

  test('protected routes redirect to login when not authenticated', async ({ page }) => {
    await page.goto('/templates');
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });

    await page.goto('/stack-definitions');
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });

    await page.goto('/audit-log');
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });
  });

  test('session persists after page reload', async ({ page }) => {
    await loginAsAdmin(page);
    await page.reload();
    await expect(page).toHaveURL('/', { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });
  });
});
