import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate } from './helpers';

test.describe('Template Management', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
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

    await page.getByRole('textbox', { name: 'Name' }).fill(name);
    await page.getByLabel('Description').fill('An e2e test template');
    await page.getByLabel('Category').click();
    await page.getByRole('option', { name: 'Web' }).click();
    await page.getByLabel('Version').fill('1.0.0');

    await page.getByRole('button', { name: 'Save Template' }).click();
    // Redirected to preview page
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible({ timeout: 10_000 });
  });

  test('view template details on preview page', async ({ page }) => {
    const name = uniqueName('tpl-view');
    // Create first
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(name);
    await page.getByLabel('Description').fill('Template to view');
    await page.getByLabel('Category').click();
    await page.getByRole('option', { name: 'API' }).click();
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

    // Verify preview content
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
    await expect(page.getByText('Template to view')).toBeVisible();
    await expect(page.getByText('Draft')).toBeVisible();
    await expect(page.getByText('API')).toBeVisible();
  });

  test('add a chart to a template', async ({ page }) => {
    const name = uniqueName('tpl-chart');
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(name);

    // Add a chart
    await page.getByRole('button', { name: 'Add Chart' }).click();
    await expect(page.getByText('Chart #1')).toBeVisible();
    await page.getByLabel('Chart Name').fill('nginx');
    await page.getByLabel('Repository URL').fill('https://charts.bitnami.com/bitnami');

    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

    // Preview shows chart info
    await expect(page.getByText('nginx')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Charts (1)')).toBeVisible();
  });

  test('publish and unpublish a template', async ({ page }) => {
    const name = uniqueName('tpl-pub');
    // Create template
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(name);
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

    const templateId = page.url().split('/templates/')[1];

    // Edit and publish
    await page.goto(`/templates/${templateId}/edit`);
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Template' })).toBeVisible({
      timeout: 10_000,
    });
    const publishSwitch = page.getByRole('switch', { name: /Draft|Published/ });
    await publishSwitch.click();
    // Wait for the async publish API call to complete
    await expect(publishSwitch).toBeChecked({ timeout: 5_000 });

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
    const unpublishSwitch = page.getByRole('switch', { name: /Draft|Published/ });
    await unpublishSwitch.click();
    await expect(unpublishSwitch).not.toBeChecked({ timeout: 5_000 });
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\//, { timeout: 10_000 });

    await page.goto(`/templates/${templateId}`);
    await expect(page.getByText('Draft')).toBeVisible({ timeout: 10_000 });
  });

  test('clone a template', async ({ page }) => {
    const name = uniqueName('tpl-clone');
    // Create template
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(name);
    await page.getByLabel('Description').fill('Template to clone');
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

    // Clone from preview page
    await page.getByRole('button', { name: 'Clone as Template' }).click();
    // Redirects to the clone's edit page
    await page.waitForURL(/\/templates\/[^/]+\/edit/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Template' })).toBeVisible({
      timeout: 10_000,
    });
    // The cloned template should have the original name (or a copy prefix)
    const clonedNameField = page.getByRole('textbox', { name: 'Name' });
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
    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 10_000 });
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
    await expect(page.getByRole('heading', { name })).toBeVisible({ timeout: 5_000 });

    // Search for something that doesn't exist
    await page.getByPlaceholder('Search templates...').fill('nonexistent-template-xyz');
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByText('No templates found')).toBeVisible({ timeout: 10_000 });
  });

  test('delete a template via API after creation', async ({ page, request }) => {
    // Create a template via UI
    const name = uniqueName('tpl-del');
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(name);
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

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
    await page.waitForLoadState('domcontentloaded');
    await expect(page.getByRole('heading', { level: 1, name: /Edit Stack Definition/ })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('quick deploy dialog opens from gallery and validates instance name', async ({ page }) => {
    const tplName = uniqueName('tpl-qdeploy');
    await createAndPublishTemplate(page, tplName);

    // Navigate to gallery and wait for the published template to appear
    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });

    // Click Quick Deploy on the template card
    const templateCard = page.locator('.MuiCard-root').filter({ hasText: tplName });
    await templateCard.getByRole('button', { name: 'Quick Deploy' }).click();

    // Dialog should open with template name in title
    await expect(page.getByText(`Quick Deploy: ${tplName}`)).toBeVisible({ timeout: 5_000 });

    // Verify required fields are present
    await expect(page.getByLabel('Instance Name')).toBeVisible();
    await expect(page.getByLabel('Branch')).toBeVisible();

    // Try to deploy without filling instance name -- should show validation error
    await page.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByText('Instance name is required')).toBeVisible({ timeout: 5_000 });

    // Cancel closes the dialog
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.getByText(`Quick Deploy: ${tplName}`)).not.toBeVisible({ timeout: 5_000 });
  });

  test('quick deploy creates instance and navigates to it', async ({ page }) => {
    const tplName = uniqueName('tpl-qdrun');
    await createAndPublishTemplate(page, tplName);

    // Navigate to gallery
    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });

    // Open Quick Deploy dialog
    const templateCard = page.locator('.MuiCard-root').filter({ hasText: tplName });
    await templateCard.getByRole('button', { name: 'Quick Deploy' }).click();
    await expect(page.getByText(`Quick Deploy: ${tplName}`)).toBeVisible({ timeout: 5_000 });

    // Fill the instance name and deploy
    const instanceName = uniqueName('qi');
    await page.getByLabel('Instance Name').fill(instanceName);

    await page.getByRole('button', { name: 'Deploy' }).click();

    // Should navigate to the created instance page
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 15_000 });
  });

  test('favorite toggle on template gallery card', async ({ page }) => {
    const tplName = uniqueName('tpl-fav');
    await createAndPublishTemplate(page, tplName);

    // Navigate to gallery
    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });

    // Find the favorite button within the template card
    const templateCard = page.locator('.MuiCard-root').filter({ hasText: tplName });
    const favoriteButton = templateCard.getByRole('button', { name: 'Add to favorites' });
    await expect(favoriteButton).toBeVisible({ timeout: 5_000 });

    // Click to add to favorites
    await favoriteButton.click();

    // After toggling, the button label should change to "Remove from favorites"
    await expect(
      templateCard.getByRole('button', { name: 'Remove from favorites' })
    ).toBeVisible({ timeout: 5_000 });

    // Click again to remove from favorites
    await templateCard.getByRole('button', { name: 'Remove from favorites' }).click();

    // Should revert back to "Add to favorites"
    await expect(
      templateCard.getByRole('button', { name: 'Add to favorites' })
    ).toBeVisible({ timeout: 5_000 });
  });

  test('gallery category filter narrows displayed templates', async ({ page }) => {
    const tplName = uniqueName('tpl-catfilt');
    // createAndPublishTemplate uses category 'Web'
    await createAndPublishTemplate(page, tplName);

    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });

    // Filter by a category that should not include our template
    await page.getByText('Data', { exact: true }).click();
    await expect(page.getByText('No templates found')).toBeVisible({ timeout: 10_000 });

    // Filter by 'Web' should show our template again
    await page.getByText('Web', { exact: true }).click();
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });

    // 'All' should also show it
    await page.getByText('All', { exact: true }).click();
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });
  });

  test('gallery My Templates tab shows own templates', async ({ page }) => {
    const tplName = uniqueName('tpl-mytab');
    // Create a template (not necessarily published) -- it should appear in "My Templates"
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(tplName);
    await page.getByLabel('Description').fill('My draft template');
    await page.getByLabel('Category').click();
    await page.getByRole('option', { name: 'Web' }).click();
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

    // Go to gallery and switch to "My Templates" tab
    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({
      timeout: 10_000,
    });
    await page.getByRole('tab', { name: 'My Templates' }).click();

    // The draft template should be visible in "My Templates"
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });
  });
});
