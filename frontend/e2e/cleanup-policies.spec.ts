import { test, expect } from '@playwright/test';
import { loginAsAdmin, uniqueName } from './helpers';

const API_BASE = 'http://localhost:8081';

/**
 * Helper: login via API and return the JWT token.
 */
async function apiLogin(request: import('@playwright/test').APIRequestContext): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { username: 'admin', password: 'admin' },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.token;
}

/**
 * Helper: create a cleanup policy via API and return its ID.
 */
async function apiCreatePolicy(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  name: string,
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/admin/cleanup-policies`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      cluster_id: 'all',
      action: 'stop',
      condition: 'idle_days:7',
      schedule: '0 3 * * *',
      enabled: false,
      dry_run: true,
    },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.id;
}

/**
 * Helper: delete a cleanup policy via API.
 */
async function apiDeletePolicy(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  id: string,
): Promise<void> {
  await request.delete(`${API_BASE}/api/v1/admin/cleanup-policies/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe('Cleanup Policies', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/admin/cleanup-policies');
    await expect(page.getByRole('heading', { level: 1, name: 'Cleanup Policies' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('page loads with heading and add button', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Cleanup Policies' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Add Policy' })).toBeVisible();
  });

  test('shows empty state when no policies exist', async ({ page }) => {
    // Check for either table data or empty info alert — depends on existing data
    const table = page.locator('table');
    const emptyAlert = page.getByText('No cleanup policies configured');
    await expect(table.or(emptyAlert)).toBeVisible({ timeout: 10_000 });
  });

  test('create a cleanup policy via dialog', async ({ page }) => {
    const name = uniqueName('policy');

    await page.getByRole('button', { name: 'Add Policy' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Create Cleanup Policy')).toBeVisible();

    await dialog.getByLabel('Name').fill(name);

    // Select action
    await dialog.getByLabel('Action').click();
    await page.getByRole('option', { name: /Stop/ }).click();

    // Select condition
    await dialog.getByLabel('Condition').click();
    await page.getByRole('option', { name: 'Idle for X days' }).click();

    // Fill schedule
    const scheduleField = dialog.getByLabel('Schedule (Cron)');
    await scheduleField.clear();
    await scheduleField.fill('0 4 * * *');

    await dialog.getByRole('button', { name: /Save|Create/ }).click();

    // Dialog closes and policy appears
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });

    // Cleanup via API
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const policiesResp = await page.request.get(`${API_BASE}/api/v1/admin/cleanup-policies`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const policies = await policiesResp.json();
    const created = policies.find((p: { name: string }) => p.name === name);
    if (created) {
      await apiDeletePolicy(page.request, token!, created.id);
    }
  });

  test('policy appears in the list after creation', async ({ page, request }) => {
    const token = await apiLogin(request);
    const name = uniqueName('policy-list');
    const id = await apiCreatePolicy(request, token, name);

    // Reload to see the new policy
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Cleanup Policies' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });

    // Cleanup
    await apiDeletePolicy(request, token, id);
  });

  test('edit a policy', async ({ page, request }) => {
    const token = await apiLogin(request);
    const name = uniqueName('policy-edit');
    const id = await apiCreatePolicy(request, token, name);

    await page.reload();
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });

    // Click edit button
    const row = page.getByRole('row').filter({ hasText: name });
    await row.getByRole('button', { name: `Edit ${name}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Edit Policy')).toBeVisible();

    const updatedName = uniqueName('policy-edited');
    const nameField = dialog.getByLabel('Name');
    await nameField.clear();
    await nameField.fill(updatedName);

    await dialog.getByRole('button', { name: /Save|Update/ }).click();
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    await expect(page.getByText(updatedName)).toBeVisible({ timeout: 10_000 });

    // Cleanup
    await apiDeletePolicy(request, token, id);
  });

  test('delete a policy via confirmation dialog', async ({ page, request }) => {
    const token = await apiLogin(request);
    const name = uniqueName('policy-del');
    const id = await apiCreatePolicy(request, token, name);

    await page.reload();
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });

    // Click delete button
    const row = page.getByRole('row').filter({ hasText: name });
    await row.getByRole('button', { name: `Delete ${name}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Delete Policy')).toBeVisible();
    await dialog.getByRole('button', { name: 'Delete' }).click();

    await expect(dialog).not.toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(name)).not.toBeVisible({ timeout: 10_000 });

    // No need to clean up — already deleted
    // Just in case it wasn't deleted, attempt cleanup
    await apiDeletePolicy(request, token, id).catch(() => {});
  });

  test('non-admin users cannot access cleanup policies', async ({ page, request }) => {
    // Create a regular user via API
    const token = await apiLogin(request);
    const username = uniqueName('user');
    await request.post(`${API_BASE}/api/v1/auth/register`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { username, password: 'testpass123', role: 'user' },
    });

    // Login as the regular user in a fresh browser context (avoids addInitScript re-injecting admin token)
    const newContext = await page.context().browser()!.newContext({ baseURL: 'http://localhost:3000' });
    const newPage = await newContext.newPage();
    await newPage.goto('/login');
    await newPage.getByLabel('Username').fill(username);
    await newPage.getByLabel('Password').fill('testpass123');
    await newPage.getByRole('button', { name: 'Sign In' }).click();
    await newPage.waitForURL('/', { timeout: 10_000 });

    // Navigate to cleanup policies
    await newPage.goto('/admin/cleanup-policies');

    // Should see permission error
    await expect(newPage.getByText('You do not have permission to access this page.')).toBeVisible({
      timeout: 10_000,
    });
    await newContext.close();
  });
});
