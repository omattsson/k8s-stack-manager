import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate, API_BASE } from './helpers';

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

    // Should show at least one version entry (e.g. v1.0.0)
    await expect(page.getByRole('list').getByText('v1.0.0')).toBeVisible({ timeout: 10_000 });
  });

  test('version entry shows version number and timestamp', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-detail');
    const templateId = await createAndPublishTemplate(page, tplName);

    await page.goto(`/templates/${templateId}`);
    await page.getByRole('tab', { name: 'Version History' }).click();

    // v1.0.0 chip should be visible in the version list
    await expect(page.getByRole('list').getByText('v1.0.0')).toBeVisible({ timeout: 10_000 });

    // Timestamp should appear (the component shows relative time + locale date)
    // formatRelativeTime returns "just now" when < 1 min, "Xm ago" otherwise
    await expect(page.getByText(/by .+ (just now|ago)/)).toBeVisible({ timeout: 10_000 });
  });

  test('republishing without changes adds no version; a changed release adds one', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-multi');
    const templateId = await createAndPublishTemplate(page, tplName);

    const token = await page.evaluate(() => localStorage.getItem('token'));
    const headers = { Authorization: `Bearer ${token}` };

    // Unpublish and publish again without changes: idempotent, no new snapshot
    const unpubRes = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/unpublish`, { headers });
    expect(unpubRes.ok()).toBe(true);
    const repubRes = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, { headers });
    expect(repubRes.ok()).toBe(true);

    // Same content and same version again: idempotent, no snapshot
    const sameRes = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, {
      headers,
      data: { version: '1.0.0' },
    });
    expect(sameRes.status()).toBe(200);
    expect((await sameRes.json()).snapshot_created).toBe(false);

    // Change the working copy, then publish a new version
    const getRes = await page.request.get(`${API_BASE}/api/v1/templates/${templateId}`, { headers });
    const current = await getRes.json();
    const putRes = await page.request.put(`${API_BASE}/api/v1/templates/${templateId}`, {
      headers,
      data: {
        name: current.name,
        description: `${current.description} (changed)`,
        category: current.category,
        version: '1.1.0',
        default_branch: current.default_branch,
      },
    });
    expect(putRes.ok()).toBe(true);
    const pubRes = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, {
      headers,
      data: { version: '1.1.0', change_summary: 'Changed description' },
    });
    expect(pubRes.ok()).toBe(true);

    // A changed working copy with an existing version string is rejected
    const putRes2 = await page.request.put(`${API_BASE}/api/v1/templates/${templateId}`, {
      headers,
      data: {
        name: current.name,
        description: `${current.description} (changed again)`,
        category: current.category,
        version: '1.1.0',
        default_branch: current.default_branch,
      },
    });
    expect(putRes2.ok()).toBe(true);
    const dupRes = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, {
      headers,
      data: { version: '1.0.0' },
    });
    expect(dupRes.status()).toBe(409);

    await page.goto(`/templates/${templateId}`);
    await page.getByRole('tab', { name: 'Version History' }).click();

    const versionList = page.getByRole('list');
    await expect(versionList.getByRole('listitem')).toHaveCount(2, { timeout: 10_000 });
    await expect(versionList.getByText('v1.1.0')).toBeVisible();
    await expect(versionList.getByText('v1.0.0')).toBeVisible();
  });

  test('unpublished changes banner shows the diff with the working copy', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-draft');
    const templateId = await createAndPublishTemplate(page, tplName);

    // Edit a value in the builder and save: the release stays the same
    await page.goto(`/templates/${templateId}/edit`);
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Template' })).toBeVisible({ timeout: 10_000 });
    await page.getByLabel('Version').fill('1.1.0');
    await page.getByLabel('Description').fill('Changed description');
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

    await expect(page.getByText('Unpublished changes. Users get version 1.0.0.')).toBeVisible({ timeout: 10_000 });
    await page.getByRole('button', { name: 'Show changes' }).click();
    await expect(page.getByRole('dialog').getByText('Version 1.0.0 vs working copy')).toBeVisible({ timeout: 10_000 });
  });

  test('version has expand/collapse functionality', async ({ page }) => {
    const tplName = uniqueName('tpl-ver-expand');
    const templateId = await createAndPublishTemplate(page, tplName);

    await page.goto(`/templates/${templateId}`);
    await page.getByRole('tab', { name: 'Version History' }).click();

    // Wait for version entry to appear
    await expect(page.getByRole('list').getByText('v1.0.0')).toBeVisible({ timeout: 10_000 });

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
