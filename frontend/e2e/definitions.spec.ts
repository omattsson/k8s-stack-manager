import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate, instantiateTemplate } from './helpers';

test.describe('Stack Definition Management', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('definitions list page loads', async ({ page }) => {
    await page.goto('/stack-definitions');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('create a definition from a published template', async ({ page }) => {
    const tplName = uniqueName('tpl-for-def');
    const templateId = await createAndPublishTemplate(page, tplName);

    const defName = uniqueName('def-create');
    await instantiateTemplate(page, templateId, defName);
    await page.waitForLoadState('domcontentloaded');

    // Verify we're on the edit page
    await expect(page.getByRole('heading', { level: 1, name: /Edit Stack Definition/ })).toBeVisible({
      timeout: 10_000,
    });

    // Check the name field has our value
    const nameField = page.getByRole('textbox', { name: 'Name', exact: true });
    await expect(nameField).toHaveValue(defName);
  });

  test('definition appears in the definitions list', async ({ page }) => {
    const tplName = uniqueName('tpl-list');
    const templateId = await createAndPublishTemplate(page, tplName);

    const defName = uniqueName('def-list');
    await instantiateTemplate(page, templateId, defName);

    // Navigate to definitions list
    await page.goto('/stack-definitions');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByText(defName)).toBeVisible({ timeout: 10_000 });
  });

  test('click definition in list navigates to edit page', async ({ page }) => {
    const tplName = uniqueName('tpl-nav');
    const templateId = await createAndPublishTemplate(page, tplName);

    const defName = uniqueName('def-nav');
    await instantiateTemplate(page, templateId, defName);

    await page.goto('/stack-definitions');
    await expect(page.getByText(defName)).toBeVisible({ timeout: 10_000 });
    await page.getByText(defName).click();

    await page.waitForURL(/\/stack-definitions\/[^/]+\/edit/, { timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByRole('heading', { level: 1, name: /Edit Stack Definition/ })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('edit a definition name and save', async ({ page }) => {
    const tplName = uniqueName('tpl-edit');
    const templateId = await createAndPublishTemplate(page, tplName);

    const defName = uniqueName('def-edit');
    await instantiateTemplate(page, templateId, defName);

    // We should be on the edit page after instantiation
    await page.waitForLoadState('domcontentloaded');
    const updatedName = uniqueName('def-edited');
    const nameField = page.getByRole('textbox', { name: 'Name', exact: true });
    await nameField.clear();
    await nameField.fill(updatedName);

    await page.getByRole('button', { name: 'Save Definition' }).click();
    // Wait for save and navigation
    await page.waitForURL(/\/stack-definitions/, { timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded');

    // Verify updated name in the list
    await page.goto('/stack-definitions');
    await expect(page.getByText(updatedName)).toBeVisible({ timeout: 10_000 });
  });

  test('create definition manually via form', async ({ page }) => {
    await page.goto('/stack-definitions/new');
    await expect(page.getByRole('heading', { level: 1, name: /Create Stack Definition/ })).toBeVisible({
      timeout: 10_000,
    });

    const defName = uniqueName('def-manual');
    await page.getByRole('textbox', { name: 'Name' }).fill(defName);
    await page.getByLabel('Description').fill('Manually created definition');
    await page.getByLabel('Default Branch').fill('main');

    await page.getByRole('button', { name: 'Save Definition' }).click();
    await page.waitForURL(/\/stack-definitions/, { timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded');

    await page.goto('/stack-definitions');
    await expect(page.getByText(defName)).toBeVisible({ timeout: 10_000 });
  });

  test('delete a definition via API', async ({ page, request }) => {
    const tplName = uniqueName('tpl-defd');
    const templateId = await createAndPublishTemplate(page, tplName);

    const defName = uniqueName('def-del');
    await instantiateTemplate(page, templateId, defName);

    // Extract definition ID from the URL
    const url = page.url();
    const defId = url.match(/\/stack-definitions\/([^/]+)/)?.[1];
    expect(defId).toBeTruthy();

    const token = await page.evaluate(() => localStorage.getItem('token'));

    // Delete via API
    const response = await request.delete(`http://localhost:8081/api/v1/stack-definitions/${defId}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(response.ok()).toBeTruthy();

    // Verify it's gone from the list
    await page.goto('/stack-definitions');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({
      timeout: 10_000,
    });
    // Wait for the list to load, then check the name is absent
    await page.waitForTimeout(1000);
    await expect(page.getByText(defName)).not.toBeVisible();
  });
});
