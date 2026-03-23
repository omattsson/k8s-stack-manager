import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate } from './helpers';

test.describe('Template Version History', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('published template shows Version History tab on preview page', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-tab');
    const templateId = await createAndPublishTemplate(page, tplName);

    await page.goto(`/templates/${templateId}`);
    await expect(page.getByRole('tab', { name: 'Version History' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('Version History tab shows version entry after publish', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-entry');
    const templateId = await createAndPublishTemplate(page, tplName);

    await page.goto(`/templates/${templateId}`);
    await page.getByRole('tab', { name: 'Version History' }).click();

    // Should show at least one version (v1)
    await expect(page.getByText(/v\d+/)).toBeVisible({ timeout: 10_000 });
  });

  test('version entry shows version number and timestamp', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-detail');
    const templateId = await createAndPublishTemplate(page, tplName);

    await page.goto(`/templates/${templateId}`);
    await page.getByRole('tab', { name: 'Version History' }).click();

    // v1 chip should be visible
    await expect(page.getByText('v1')).toBeVisible({ timeout: 10_000 });

    // Timestamp should appear (the component shows relative time + locale date)
    // Look for the "by <username>" text that accompanies the timestamp
    await expect(page.getByText(/by .+ ago/)).toBeVisible({ timeout: 10_000 });
  });

  test('republishing creates a second version entry', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-multi');
    const templateId = await createAndPublishTemplate(page, tplName);

    const token = await page.evaluate(() => localStorage.getItem('token'));

    // Unpublish
    const unpubRes = await page.request.post(
      `http://localhost:8081/api/v1/templates/${templateId}/unpublish`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(unpubRes.ok()).toBe(true);

    // Re-publish to create a second version
    const pubRes = await page.request.post(
      `http://localhost:8081/api/v1/templates/${templateId}/publish`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(pubRes.ok()).toBe(true);

    // Navigate to preview and open Version History tab
    await page.goto(`/templates/${templateId}`);
    await page.getByRole('tab', { name: 'Version History' }).click();

    // Should show v1 and v2
    await expect(page.getByText('v1')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('v2')).toBeVisible({ timeout: 10_000 });
  });

  test('version has expand/collapse functionality', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-expand');
    const templateId = await createAndPublishTemplate(page, tplName);

    await page.goto(`/templates/${templateId}`);
    await page.getByRole('tab', { name: 'Version History' }).click();

    // Wait for v1 to appear
    await expect(page.getByText('v1')).toBeVisible({ timeout: 10_000 });

    // Click the expand button
    const expandBtn = page.getByRole('button', { name: 'Expand version details' });
    await expect(expandBtn).toBeVisible({ timeout: 5_000 });
    await expandBtn.click();

    // The expanded content should show snapshot details (e.g. "Template Snapshot")
    await expect(page.getByText('Template Snapshot')).toBeVisible({ timeout: 10_000 });

    // Click collapse
    const collapseBtn = page.getByRole('button', { name: 'Collapse version details' });
    await expect(collapseBtn).toBeVisible({ timeout: 5_000 });
    await collapseBtn.click();

    // Snapshot details should be hidden
    await expect(page.getByText('Template Snapshot')).not.toBeVisible({ timeout: 5_000 });
  });
});
