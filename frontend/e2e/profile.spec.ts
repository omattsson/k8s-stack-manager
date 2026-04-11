import { test, expect } from '@playwright/test';
import { loginAsUser, uniqueName, API_BASE } from './helpers';

test.describe('Profile Page', () => {
  let username: string;

  test.beforeEach(async ({ page }) => {
    username = await loginAsUser(page);
    await page.goto('/profile');
    await expect(page.getByRole('heading', { level: 1, name: 'My Profile' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('page loads with user information', async ({ page }) => {
    await expect(page.getByText('Username:')).toBeVisible();
    await expect(page.locator('#main-content').getByText(username, { exact: true }).first()).toBeVisible();
    await expect(page.getByText('Role:')).toBeVisible();
  });

  test('shows API keys section', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'API Keys' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Generate API Key' })).toBeVisible();
  });

  test('create and view a new API key', async ({ page }) => {
    const keyName = uniqueName('e2e-key');

    await page.getByRole('button', { name: 'Generate API Key' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Generate API Key')).toBeVisible();

    await dialog.getByLabel('Key Name').fill(keyName);
    await dialog.getByRole('button', { name: 'Generate' }).click();

    // Raw key dialog should appear with warning
    await expect(page.getByText('This key will not be shown again. Copy it now.')).toBeVisible({
      timeout: 10_000,
    });

    // The raw key should be visible — format is sk_<64-char hex>
    await expect(page.getByText(/^sk_[0-9a-f]{64}$/)).toBeVisible({ timeout: 5_000 });

    // Close the raw key dialog
    await page.getByRole('button', { name: 'Done' }).click();

    // Key should now appear in the table
    await expect(page.getByText(keyName)).toBeVisible({ timeout: 10_000 });

    // Cleanup: delete the key via API
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const meResp = await page.request.get(`${API_BASE}/api/v1/auth/me`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const me = await meResp.json();
    const keysResp = await page.request.get(`${API_BASE}/api/v1/users/${me.id}/api-keys`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const keys = await keysResp.json();
    const created = keys.find((k: { name: string }) => k.name === keyName);
    if (created) {
      await page.request.delete(`${API_BASE}/api/v1/users/${me.id}/api-keys/${created.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });

  test('API key prefix is masked in table', async ({ page }) => {
    const keyName = uniqueName('e2e-masked');

    // Create key via API so we can inspect it
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const meResp = await page.request.get(`${API_BASE}/api/v1/auth/me`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const me = await meResp.json();

    await page.request.post(`${API_BASE}/api/v1/users/${me.id}/api-keys`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { name: keyName },
    });

    // Reload to see the key
    await page.reload();
    await expect(page.getByText(keyName)).toBeVisible({ timeout: 10_000 });

    // The prefix column should show a truncated value with "..."
    const keyRow = page.getByRole('row').filter({ hasText: keyName });
    await expect(keyRow.getByText(/\.\.\./).first()).toBeVisible();

    // Cleanup
    const keysResp = await page.request.get(`${API_BASE}/api/v1/users/${me.id}/api-keys`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const keys = await keysResp.json();
    const created = keys.find((k: { name: string }) => k.name === keyName);
    if (created) {
      await page.request.delete(`${API_BASE}/api/v1/users/${me.id}/api-keys/${created.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });

  test('delete an API key', async ({ page }) => {
    const keyName = uniqueName('e2e-del');

    // Create key via API
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const meResp = await page.request.get(`${API_BASE}/api/v1/auth/me`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const me = await meResp.json();

    await page.request.post(`${API_BASE}/api/v1/users/${me.id}/api-keys`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { name: keyName },
    });

    // Reload to see the key
    await page.reload();
    await expect(page.getByText(keyName)).toBeVisible({ timeout: 10_000 });

    // Click revoke button for this key
    await page.getByRole('button', { name: `Revoke key ${keyName}` }).click();

    // Confirmation dialog
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Revoke API Key')).toBeVisible();
    await dialog.getByRole('button', { name: 'Revoke' }).click();

    // Dialog closes and key disappears
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(keyName)).not.toBeVisible({ timeout: 10_000 });
  });
});
