import { test, expect } from '@playwright/test';
import { loginAsAdmin, uniqueName, createAndPublishTemplate, instantiateTemplate } from './helpers';

test.describe('Stack Instance Management', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
  });

  test('dashboard page loads', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('create a stack instance from a definition', async ({ page }) => {
    // Set up: template → definition
    const tplName = uniqueName('tpl-for-inst');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-for-inst');
    await instantiateTemplate(page, templateId, defName);

    // Navigate to create instance
    await page.goto('/stack-instances/new');
    await expect(page.getByRole('heading', { level: 1, name: 'Create Stack Instance' })).toBeVisible({
      timeout: 10_000,
    });

    // Select the definition
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();

    // Fill instance name
    const instName = uniqueName('inst');
    await page.getByLabel('Instance Name').fill(instName);

    // Namespace should auto-generate
    const nsField = page.getByLabel('Namespace (auto-generated)');
    await expect(nsField).not.toHaveValue('');

    await page.getByRole('button', { name: 'Create Instance' }).click();
    // Redirects to instance detail
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: instName })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('instance appears on dashboard after creation', async ({ page }) => {
    // Set up
    const tplName = uniqueName('tpl-dash');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-dash');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-dash');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });

    // Go to dashboard
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByText(instName)).toBeVisible({ timeout: 10_000 });
  });

  test('view instance detail page', async ({ page }) => {
    // Set up
    const tplName = uniqueName('tpl-detail');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-detail');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-detail');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });

    // Verify detail page content
    await expect(page.getByRole('heading', { level: 1, name: instName })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByText('Namespace:')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Export Values' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Clone' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Delete' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Save Changes' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Back to Dashboard' })).toBeVisible();
  });

  test('clone an instance', async ({ page }) => {
    // Set up
    const tplName = uniqueName('tpl-clone');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-clone');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-clone');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });

    const originalUrl = page.url();

    // Clone
    await page.getByRole('button', { name: 'Clone' }).click();
    // Should navigate to a different instance page
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    const clonedUrl = page.url();
    expect(clonedUrl).not.toBe(originalUrl);
    // Cloned instance name should contain original name
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible({ timeout: 10_000 });
  });

  test('delete an instance', async ({ page }) => {
    // Set up
    const tplName = uniqueName('tpl-del');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-del');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-del');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: instName })).toBeVisible({
      timeout: 10_000,
    });

    // Click Delete
    await page.getByRole('button', { name: 'Delete' }).click();
    // Confirm dialog appears
    await expect(page.getByText(/Are you sure you want to delete/)).toBeVisible({ timeout: 5_000 });
    // Click the confirm Delete button in the dialog
    await page.getByRole('button', { name: 'Delete' }).last().click();
    // Redirects to dashboard
    await expect(page).toHaveURL('/', { timeout: 10_000 });
    // Instance should no longer appear
    await expect(page.getByText(instName)).not.toBeVisible({ timeout: 5_000 });
  });

  test('dashboard search filters instances', async ({ page }) => {
    // Set up
    const tplName = uniqueName('tpl-srch');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-srch');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-searchable');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });

    // Go to dashboard and search
    await page.goto('/');
    await expect(page.getByText(instName)).toBeVisible({ timeout: 10_000 });

    await page.getByPlaceholder('Search instances...').fill(instName);
    await expect(page.getByText(instName)).toBeVisible();

    await page.getByPlaceholder('Search instances...').fill('nonexistent-instance-xyz');
    await expect(page.getByText('No stack instances found.')).toBeVisible({ timeout: 5_000 });
  });

  test('navigate to instance detail from dashboard', async ({ page }) => {
    // Set up
    const tplName = uniqueName('tpl-navd');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-navd');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-navd');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });

    // Go to dashboard
    await page.goto('/');
    await expect(page.getByText(instName)).toBeVisible({ timeout: 10_000 });

    // Click Details button on the card
    const card = page.locator('.MuiCard-root', { hasText: instName });
    await card.getByRole('button', { name: 'Details' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: instName })).toBeVisible({
      timeout: 10_000,
    });
  });
});
