import { test, expect } from '@playwright/test';
import { loginAsAdmin, uniqueName, API_BASE, ADMIN_PASSWORD } from './helpers';

/**
 * Helper: login via API and return the JWT token.
 */
async function apiLogin(request: import('@playwright/test').APIRequestContext): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { username: 'admin', password: ADMIN_PASSWORD },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.token;
}

/**
 * Helper: create a user via API and return their ID.
 */
async function apiCreateUser(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  username: string,
  role: string = 'user',
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/auth/register`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { username, password: 'testpass123', role, display_name: `E2E ${username}` },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.id;
}

/**
 * Helper: delete a user via API.
 */
async function apiDeleteUser(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  id: string,
): Promise<void> {
  await request.delete(`${API_BASE}/api/v1/users/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe('Orphaned Namespaces', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/admin/orphaned-namespaces');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Orphaned Namespaces' }),
    ).toBeVisible({ timeout: 10_000 });
  });

  test('page loads with correct heading and refresh button', async ({ page }) => {
    await expect(
      page.getByRole('heading', { level: 1, name: 'Orphaned Namespaces' }),
    ).toBeVisible();
    await expect(page.getByRole('button', { name: 'Refresh' })).toBeVisible();
  });

  test('shows descriptive text about orphaned namespaces', async ({ page }) => {
    await expect(
      page.getByText('Namespaces matching the stack-* pattern'),
    ).toBeVisible({ timeout: 5_000 });
  });

  test('shows empty state or table of orphaned namespaces', async ({ page }) => {
    // After loading completes, either the empty state message or the table should be visible.
    const emptyMessage = page.getByText(
      'No orphaned namespaces found. All stack namespaces have matching instances.',
    );
    const table = page.locator('table');
    await expect(emptyMessage.or(table)).toBeVisible({ timeout: 10_000 });
  });

  test('table has correct column headers when namespaces exist', async ({ page }) => {
    // This test checks the table structure if there are orphaned namespaces.
    // If empty state is shown, we verify the empty message instead.
    const emptyMessage = page.getByText('No orphaned namespaces found');
    const table = page.locator('table');
    await expect(emptyMessage.or(table)).toBeVisible({ timeout: 10_000 });

    if (await table.isVisible()) {
      await expect(page.getByRole('columnheader', { name: 'Namespace' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Age' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Phase' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Pods' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Deployments' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Services' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Helm Releases' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Actions' })).toBeVisible();
    }
  });

  test('refresh button reloads data', async ({ page }) => {
    // Wait for initial load to complete (empty state, table, or error visible).
    const emptyMessage = page.getByText('No orphaned namespaces found');
    const table = page.locator('table');
    const errorAlert = page.getByText('Failed to load orphaned namespaces');
    await expect(emptyMessage.or(table).or(errorAlert).first()).toBeVisible({ timeout: 10_000 });

    // Intercept the API call to verify refresh triggers a new request.
    // Do not filter on status — the endpoint may return 503 when no K8s cluster is available.
    const responsePromise = page.waitForResponse(
      (resp) => resp.url().includes('/api/v1/admin/orphaned-namespaces'),
      { timeout: 10_000 },
    );

    await page.getByRole('button', { name: 'Refresh' }).click();

    const response = await responsePromise;
    expect([200, 503]).toContain(response.status());

    // After refresh, page should still show either empty state, table, or error.
    await expect(emptyMessage.or(table).or(errorAlert).first()).toBeVisible({ timeout: 10_000 });
  });

  test('breadcrumb navigation is visible', async ({ page }) => {
    await expect(page.getByRole('link', { name: 'Home' })).toBeVisible({ timeout: 5_000 });
    await expect(page.getByText('Orphaned Namespaces').first()).toBeVisible();
  });

  test('non-admin users cannot access orphaned namespaces page', async ({ page, request }) => {
    // Create a regular user via API
    const token = await apiLogin(request);
    const username = uniqueName('nonadmin');
    const userId = await apiCreateUser(request, token, username, 'user');

    // Login as the regular user in a fresh browser context (avoids addInitScript re-injecting admin token)
    const newContext = await page
      .context()
      .browser()!
      .newContext({ baseURL: 'http://localhost:3000' });
    const newPage = await newContext.newPage();
    await newPage.goto('/login');
    await newPage.getByLabel('Username').fill(username);
    await newPage.getByLabel('Password').fill('testpass123');
    await newPage.getByRole('button', { name: 'Sign In' }).click();
    await newPage.waitForURL('/', { timeout: 10_000 });

    // Navigate to orphaned namespaces page
    await newPage.goto('/admin/orphaned-namespaces');

    // Should see permission error
    await expect(
      newPage.getByText('You do not have permission to access this page.'),
    ).toBeVisible({ timeout: 10_000 });
    await newContext.close();

    // Cleanup: delete the test user
    await apiDeleteUser(request, token, userId).catch(() => {});
  });
});
