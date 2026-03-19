import { test, expect } from '@playwright/test';
import { loginAsAdmin, uniqueName, createAndPublishTemplate } from './helpers';

test.describe('Template Management', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
  });

  test('gallery page loads and shows heading', async ({ page }) => {
    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('create a new template', async ({ page }) => {
    const name = uniqueName('tpl');
    await page.goto('/templates/new');
    await expect(page.getByRole('heading', { level: 1, name: 'Create Template' })).toBeVisible({
      timeout: 10_000,
    });

    await page.getByLabel('Name', { exact: true }).fill(name);
    await page.getByLabel('Description').fill('An e2e test template');
    await page.getByLabel('Category').click();
    await page.getByRole('option', { name: 'Web' }).click();
    await page.getByLabel('Version').fill('1.0.0');

    await page.getByRole('button', { name: 'Save Template' }).click();
    // Redirected to preview page
    await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible({ timeout: 10_000 });
  });

  test('view template details on preview page', async ({ page }) => {
    const name = uniqueName('tpl-view');
    // Create first
    await page.goto('/templates/new');
    await page.getByLabel('Name', { exact: true }).fill(name);
    await page.getByLabel('Description').fill('Template to view');
    await page.getByLabel('Category').click();
    await page.getByRole('option', { name: 'API' }).click();
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });

    // Verify preview content
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
    await expect(page.getByText('Template to view')).toBeVisible();
    await expect(page.getByText('Draft')).toBeVisible();
    await expect(page.getByText('API')).toBeVisible();
  });

  test('add a chart to a template', async ({ page }) => {
    const name = uniqueName('tpl-chart');
    await page.goto('/templates/new');
    await page.getByLabel('Name', { exact: true }).fill(name);

    // Add a chart
    await page.getByRole('button', { name: 'Add Chart' }).click();
    await expect(page.getByText('Chart #1')).toBeVisible();
    await page.getByLabel('Chart Name').fill('nginx');
    await page.getByLabel('Repository URL').fill('https://charts.bitnami.com/bitnami');

    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });

    // Preview shows chart info
    await expect(page.getByText('nginx')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Charts (1)')).toBeVisible();
  });

  test('publish and unpublish a template', async ({ page }) => {
    const name = uniqueName('tpl-pub');
    // Create template
    await page.goto('/templates/new');
    await page.getByLabel('Name', { exact: true }).fill(name);
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });

    const templateId = page.url().split('/templates/')[1];

    // Edit and publish
    await page.goto(`/templates/${templateId}/edit`);
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Template' })).toBeVisible({
      timeout: 10_000,
    });
    const publishSwitch = page.getByRole('switch').first();
    await publishSwitch.check();
    await page.waitForTimeout(1000);

    // Save
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\//, { timeout: 10_000 });

    // Verify published on preview page
    await page.goto(`/templates/${templateId}`);
    await expect(page.getByText('Published')).toBeVisible({ timeout: 10_000 });

    // Unpublish
    await page.goto(`/templates/${templateId}/edit`);
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Template' })).toBeVisible({
      timeout: 10_000,
    });
    const unpublishSwitch = page.getByRole('switch').first();
    await unpublishSwitch.uncheck();
    await page.waitForTimeout(1000);
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\//, { timeout: 10_000 });

    await page.goto(`/templates/${templateId}`);
    await expect(page.getByText('Draft')).toBeVisible({ timeout: 10_000 });
  });

  test('clone a template', async ({ page }) => {
    const name = uniqueName('tpl-clone');
    // Create template
    await page.goto('/templates/new');
    await page.getByLabel('Name', { exact: true }).fill(name);
    await page.getByLabel('Description').fill('Template to clone');
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });

    // Clone from preview page
    await page.getByRole('button', { name: 'Clone as Template' }).click();
    // Redirects to the clone's edit page
    await page.waitForURL(/\/templates\/[^/]+\/edit/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Template' })).toBeVisible({
      timeout: 10_000,
    });
    // The cloned template should have the original name (or a copy prefix)
    const clonedNameField = page.getByLabel('Name', { exact: true });
    const clonedName = await clonedNameField.inputValue();
    expect(clonedName).toContain(name);
  });

  test('template gallery shows published templates', async ({ page }) => {
    const name = uniqueName('tpl-gallery');
    await createAndPublishTemplate(page, name);

    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
    // Published tab is default
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });
  });

  test('template gallery search filters templates', async ({ page }) => {
    const name = uniqueName('tpl-search');
    await createAndPublishTemplate(page, name);

    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });

    // Search for the template
    await page.getByPlaceholder('Search templates...').fill(name);
    await expect(page.getByText(name)).toBeVisible({ timeout: 5_000 });

    // Search for something that doesn't exist
    await page.getByPlaceholder('Search templates...').fill('nonexistent-template-xyz');
    await expect(page.getByText('No templates found.')).toBeVisible({ timeout: 5_000 });
  });

  test('delete a template via API after creation', async ({ page, request }) => {
    // Create a template via UI
    const name = uniqueName('tpl-del');
    await page.goto('/templates/new');
    await page.getByLabel('Name', { exact: true }).fill(name);
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/[^/]+$/, { timeout: 10_000 });

    const templateId = page.url().split('/templates/')[1];

    // Get auth token from localStorage
    const token = await page.evaluate(() => localStorage.getItem('token'));

    // Delete via API (no UI delete button on preview page for templates in gallery)
    const response = await request.delete(`http://localhost:8081/api/v1/templates/${templateId}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(response.ok()).toBeTruthy();

    // Verify template is gone
    await page.goto(`/templates/${templateId}`);
    await expect(page.getByRole('alert')).toBeVisible({ timeout: 10_000 });
  });

  test('instantiate a published template into a definition', async ({ page }) => {
    const tplName = uniqueName('tpl-inst');
    const templateId = await createAndPublishTemplate(page, tplName);

    // Navigate to Use Template
    await page.goto(`/templates/${templateId}/use`);
    await expect(page.getByRole('heading', { level: 1, name: /Use Template/ })).toBeVisible({
      timeout: 10_000,
    });

    const defName = uniqueName('def-from-tpl');
    const nameField = page.getByLabel('Definition Name');
    await nameField.clear();
    await nameField.fill(defName);

    await page.getByRole('button', { name: 'Create Stack Definition' }).click();
    // Redirects to definition edit
    await page.waitForURL(/\/stack-definitions\/[^/]+\/edit/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: /Edit Definition/ })).toBeVisible({
      timeout: 10_000,
    });
  });
});
