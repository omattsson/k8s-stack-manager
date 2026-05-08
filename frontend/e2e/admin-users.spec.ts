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

test.describe('Admin User Management', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/admin/users');
    await expect(page.getByRole('heading', { level: 1, name: 'User Management' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('page loads with heading and table', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1, name: 'User Management' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Add User' })).toBeVisible();
  });

  test('shows current users in table', async ({ page }) => {
    // Column headers
    await expect(page.getByRole('columnheader', { name: 'Username' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Display Name' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Role' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Actions' })).toBeVisible();

    // Admin user should be visible
    await expect(page.getByRole('cell', { name: 'admin' }).first()).toBeVisible();
  });

  test('create a new user via dialog', async ({ page }) => {
    const username = uniqueName('user');

    await page.getByRole('button', { name: 'Add User' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Add User')).toBeVisible();

    await dialog.getByLabel('Username').fill(username);
    await dialog.getByLabel('Password').fill('testpass123');
    await dialog.getByLabel('Display Name').fill(`Test ${username}`);

    // Select role
    await dialog.getByLabel('Role').click();
    await page.getByRole('option', { name: 'user' }).click();

    await dialog.getByRole('button', { name: 'Create' }).click();

    // Dialog closes and user appears
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('cell', { name: username, exact: true }).first()).toBeVisible({ timeout: 10_000 });

    // Cleanup via API
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const usersResp = await page.request.get(`${API_BASE}/api/v1/users`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const users = await usersResp.json();
    const created = users.find((u: { username: string }) => u.username === username);
    if (created) {
      await apiDeleteUser(page.request, token!, created.id);
    }
  });

  test('create user with specific role', async ({ page }) => {
    const username = uniqueName('devops-user');

    await page.getByRole('button', { name: 'Add User' }).click();

    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Username').fill(username);
    await dialog.getByLabel('Password').fill('testpass123');

    // Select devops role
    await dialog.getByLabel('Role').click();
    await page.getByRole('option', { name: 'devops' }).click();

    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // User should appear with devops role chip
    await expect(page.getByRole('cell', { name: username, exact: true }).first()).toBeVisible({ timeout: 10_000 });
    const row = page.getByRole('row').filter({ hasText: username });
    await expect(row.getByText('devops', { exact: true })).toBeVisible();

    // Cleanup
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const usersResp = await page.request.get(`${API_BASE}/api/v1/users`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const users = await usersResp.json();
    const created = users.find((u: { username: string }) => u.username === username);
    if (created) {
      await apiDeleteUser(page.request, token!, created.id);
    }
  });

  test('delete a user via confirmation dialog', async ({ page, request }) => {
    // Create user via API first
    const token = await apiLogin(request);
    const username = uniqueName('user-del');
    const userId = await apiCreateUser(request, token, username);

    // Reload to see the user
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'User Management' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('cell', { name: username, exact: true })).toBeVisible({ timeout: 10_000 });

    // Click delete button
    await page.getByRole('button', { name: `Delete user ${username}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Delete User' })).toBeVisible();
    await dialog.getByRole('button', { name: 'Delete' }).click();

    // Dialog closes and user disappears
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('cell', { name: username, exact: true })).not.toBeVisible({ timeout: 10_000 });

    // Already deleted, try cleanup just in case
    await apiDeleteUser(request, token, userId).catch(() => {});
  });

  test('reset password for a local user and verify login', async ({ page, request }) => {
    const token = await apiLogin(request);
    const username = uniqueName('pw-reset');
    const userId = await apiCreateUser(request, token, username);

    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'User Management' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('cell', { name: username, exact: true })).toBeVisible({ timeout: 10_000 });

    // Click reset password button
    await page.getByRole('button', { name: `Reset password for ${username}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Reset Password' })).toBeVisible();

    // Submit button should be disabled until password meets minimum length
    const submitBtn = dialog.getByRole('button', { name: 'Reset Password' });
    await expect(submitBtn).toBeDisabled();

    await dialog.getByLabel('New Password').fill('newpass123');
    await expect(submitBtn).toBeEnabled();
    await submitBtn.click();

    // Dialog should close
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // Verify login with new password works
    const loginRes = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { username, password: 'newpass123' },
    });
    expect(loginRes.ok()).toBe(true);

    // Old password should no longer work
    const oldLoginRes = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { username, password: 'testpass123' },
    });
    expect(oldLoginRes.ok()).toBe(false);

    // Cleanup
    await apiDeleteUser(request, token, userId);
  });

  test('reset password button is hidden for OIDC users', async ({ page, request }) => {
    // Create a local user and verify button exists
    const token = await apiLogin(request);
    const localUser = uniqueName('local');
    const localId = await apiCreateUser(request, token, localUser);

    await page.reload();
    await expect(page.getByRole('cell', { name: localUser, exact: true })).toBeVisible({ timeout: 10_000 });

    // Local user should have the reset button
    await expect(page.getByRole('button', { name: `Reset password for ${localUser}` })).toBeVisible();

    // The admin user (also local) should have the reset button
    await expect(page.getByRole('button', { name: /reset password for admin/i })).toBeVisible();

    // Cleanup
    await apiDeleteUser(request, token, localId);
  });

  test('cancel password reset closes dialog without changes', async ({ page, request }) => {
    const token = await apiLogin(request);
    const username = uniqueName('pw-cancel');
    const userId = await apiCreateUser(request, token, username);

    await page.reload();
    await expect(page.getByRole('cell', { name: username, exact: true })).toBeVisible({ timeout: 10_000 });

    await page.getByRole('button', { name: `Reset password for ${username}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Reset Password' })).toBeVisible();
    await dialog.getByLabel('New Password').fill('shouldnotapply');
    await dialog.getByRole('button', { name: 'Cancel' }).click();

    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // Original password should still work
    const loginRes = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { username, password: 'testpass123' },
    });
    expect(loginRes.ok()).toBe(true);

    // Cleanup
    await apiDeleteUser(request, token, userId);
  });

  test('non-admin users cannot access user management', async ({ page, request }) => {
    // Create a regular user
    const token = await apiLogin(request);
    const username = uniqueName('nonadmin');
    await apiCreateUser(request, token, username, 'user');

    // Login as the regular user in a fresh browser context (avoids addInitScript re-injecting admin token)
    const newContext = await page.context().browser()!.newContext({ baseURL: 'http://localhost:3000' });
    const newPage = await newContext.newPage();
    await newPage.goto('/login');
    await newPage.getByLabel('Username').fill(username);
    await newPage.getByLabel('Password').fill('testpass123');
    await newPage.getByRole('button', { name: 'Sign In' }).click();
    await newPage.waitForURL('/', { timeout: 10_000 });

    // Navigate to admin users page
    await newPage.goto('/admin/users');

    // Should see permission error
    await expect(newPage.getByText('You do not have permission to access this page.')).toBeVisible({
      timeout: 10_000,
    });
    await newContext.close();

    // Cleanup
    const usersResp = await request.get(`${API_BASE}/api/v1/users`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const users = await usersResp.json();
    const created = users.find((u: { username: string }) => u.username === username);
    if (created) {
      await apiDeleteUser(request, token, created.id);
    }
  });
});
