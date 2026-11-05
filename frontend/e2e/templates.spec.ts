import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import {
  loginAsDevops,
  uniqueName,
  createAndPublishTemplate,
  API_BASE,
  setYamlEditorText,
  tokenFromPage,
  apiCreateTemplate,
  apiAddTemplateChart,
  runE2eSql,
  e2eDbConfigured,
  E2E_DB_SKIP_REASON,
} from './helpers';

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
    await page.getByLabel('Version', { exact: true }).fill('1.0.0');

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

    // Publish from the detail page
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible({ timeout: 10_000 });
    await page.getByRole('button', { name: 'Publish', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('textbox', { name: /^Version/ })).toBeVisible();
    await dialog.getByRole('textbox', { name: /^Version/ }).fill('1.0.0');
    await dialog.getByRole('button', { name: 'Publish', exact: true }).click();
    await expect(page.getByText('Published version 1.0.0.')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Released: v1.0.0')).toBeVisible();
    await expect(page.getByText('Published', { exact: true })).toBeVisible();
    // Wait until the dialog has closed: during the close animation the next
    // checks would still find the old dialog.
    await expect(dialog).toBeHidden();

    // Publishing the same version again without changes creates no new version
    // (a new version string counts as a change and would create a snapshot)
    await page.getByRole('button', { name: 'Publish', exact: true }).click();
    await expect(dialog.getByRole('textbox', { name: /^Version/ })).toHaveValue('1.0.0');
    await dialog.getByRole('button', { name: 'Publish', exact: true }).click();
    await expect(page.getByText(/No changes since version 1\.0\.0\. No new version was created/)).toBeVisible({ timeout: 10_000 });
    await expect(dialog).toBeHidden();

    // Unpublish (asks for confirmation)
    await page.getByRole('button', { name: 'Unpublish', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Unpublish', exact: true }).click();
    await expect(page.getByText('Draft', { exact: true })).toBeVisible({ timeout: 10_000 });
  });

  test('clone a template', async ({ page }) => {
    const name = uniqueName('tpl-clone');
    // Create template
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(name);
    await page.getByLabel('Description').fill('Template to clone');
    await page.getByRole('button', { name: 'Save Template' }).click();
    await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });

    // Clone from preview page: the dialog asks for the name of the copy
    await page.getByRole('button', { name: 'Clone as Template' }).click();
    const dialog = page.getByRole('dialog', { name: 'Clone as Template' });
    await expect(dialog.getByRole('textbox', { name: 'Name' })).toHaveValue(`${name} (Copy)`);
    await dialog.getByRole('button', { name: 'Clone' }).click();
    // Redirects to the clone's edit page
    await page.waitForURL(/\/templates\/[^/]+\/edit/, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Template' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('textbox', { name: 'Name' })).toHaveValue(`${name} (Copy)`);
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
    const response = await request.delete(`${API_BASE}/api/v1/templates/${templateId}`, {
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
    test.skip(!process.env.E2E_KUBECONFIG, 'Needs a cluster: set E2E_KUBECONFIG (see make test-e2e)');
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

    // Filter by a category that should not include our template. Other
    // templates in that category can exist, so check only our template.
    await page.getByRole('button', { name: 'Data', exact: true }).click();
    await expect(page.getByRole('heading', { name: tplName })).toBeHidden({ timeout: 10_000 });

    // Filter by 'Web' should show our template again
    await page.getByRole('button', { name: 'Web', exact: true }).click();
    await expect(page.getByRole('heading', { name: tplName })).toBeVisible({ timeout: 10_000 });

    // 'All' should also show it
    await page.getByRole('button', { name: 'All', exact: true }).click();
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

// Chart values that the deploy cannot parse (#756). The tests type into Monaco,
// so the hint and the save buttons follow the editor as a user sees them.
test.describe('Template chart values validation (#756)', () => {
  const templateNames: string[] = [];
  const definitionNames: string[] = [];

  test.afterEach(async ({ page, request }) => {
    const token = await tokenFromPage(page);
    const headers = { Authorization: `Bearer ${token}` };
    for (const [kind, names] of [['templates', templateNames], ['stack-definitions', definitionNames]] as const) {
      for (const name of names.splice(0)) {
        const res = await request.get(`${API_BASE}/api/v1/${kind}?name=${encodeURIComponent(name)}&pageSize=100`, { headers });
        const body = await res.json();
        for (const item of body.data ?? []) {
          if (item.name === name) await request.delete(`${API_BASE}/api/v1/${kind}/${item.id}`, { headers });
        }
      }
    }
  });

  async function openBuilder(page: Page, name: string) {
    await loginAsDevops(page);
    await page.goto('/templates/new');
    await page.getByRole('textbox', { name: 'Name' }).fill(name);
    await page.getByLabel('Category').click();
    await page.getByRole('option', { name: 'Web' }).click();
    await page.getByLabel('Version', { exact: true }).fill('1.0.0');
    await page.getByRole('button', { name: 'Add Chart' }).click();
    await page.getByLabel('Chart Name').fill('vchart');
    await page.getByLabel('Repository URL').fill('https://charts.example.com');
  }

  /** Create and publish a template with one chart through the API, then open Use Template. */
  async function openInstantiate(page: Page, name: string) {
    await loginAsDevops(page);
    await page.goto('/');
    const token = await tokenFromPage(page);
    const headers = { Authorization: `Bearer ${token}` };
    const templateId = await apiCreateTemplate(page.request, token, name);
    templateNames.push(name);
    await apiAddTemplateChart(page.request, token, templateId, { chart_name: 'vchart', default_values: 'replicaCount: 1\n' });
    const res = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, { headers });
    expect(res.ok(), `publish failed: ${res.status()}`).toBe(true);
    await page.goto(`/templates/${templateId}/use`);
    await expect(page.getByRole('button', { name: 'Create Stack Definition' })).toBeVisible({ timeout: 10_000 });
    definitionNames.push(`${name} - My Stack`);
  }

  for (const [label, buttons] of [
    ['Default Values (YAML)', ['Save Template', 'Save and Publish']],
    ['Locked Values (YAML)', ['Save Template', 'Save and Publish']],
  ] as const) {
    test(`Builder: an unquoted template in ${label} blocks both saves until it is quoted`, async ({ page }) => {
      await openBuilder(page, uniqueName('tpl-yaml-block'));

      await setYamlEditorText(page, label, 'tag: {{.Branch}}');
      for (const b of buttons) await expect(page.getByRole('button', { name: b })).toBeDisabled({ timeout: 10_000 });
      await expect(page.getByTestId('yaml-error-hint')).toHaveText('Fix the YAML errors before you save.');

      await setYamlEditorText(page, label, 'tag: "{{.Branch}}"');
      for (const b of buttons) await expect(page.getByRole('button', { name: b })).toBeEnabled({ timeout: 10_000 });
      await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);
    });
  }

  test('Builder: a duplicate key does not block the save and the API 400 shows on the form', async ({ page }) => {
    const name = uniqueName('tpl-yaml-dup');
    templateNames.push(name);
    await openBuilder(page, name);

    await setYamlEditorText(page, 'Locked Values (YAML)', 'a: 1\na: 2');
    const save = page.getByRole('button', { name: 'Save Template' });
    await expect(page.getByTestId('yaml-hint')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);
    await expect(save).toBeEnabled();

    await save.click();
    await expect(page.getByRole('alert').filter({ hasText: /HTTP 400: Invalid YAML in/ })).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/\/templates\/new/);
  });

  test('Use Template: an unquoted template blocks the save until it is quoted', async ({ page }) => {
    await openInstantiate(page, uniqueName('tpl-use-block'));
    const save = page.getByRole('button', { name: 'Create Stack Definition' });

    await setYamlEditorText(page, 'Values (YAML)', 'tag: {{.Branch}}');
    await expect(save).toBeDisabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveText('Fix the YAML errors before you save.');

    await setYamlEditorText(page, 'Values (YAML)', 'tag: "{{.Branch}}"');
    await expect(save).toBeEnabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);
  });

  test('Use Template: a duplicate key does not block the save and the API 400 shows on the form', async ({ page }) => {
    await openInstantiate(page, uniqueName('tpl-use-dup'));
    const save = page.getByRole('button', { name: 'Create Stack Definition' });

    await setYamlEditorText(page, 'Values (YAML)', 'a: 1\na: 2');
    await expect(page.getByTestId('yaml-hint')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);
    await expect(save).toBeEnabled();

    await save.click();
    await expect(page.getByRole('alert').filter({ hasText: /HTTP 400: Invalid YAML in chart "vchart"/ })).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/\/use$/);
  });

  test('Builder: a retry after a chart 400 creates no second template and no duplicate chart', async ({ page }) => {
    const name = uniqueName('tpl-yaml-retry');
    templateNames.push(name);
    await openBuilder(page, name);
    await page.getByRole('button', { name: 'Add Chart' }).click();
    await page.getByLabel('Chart Name').nth(1).fill('wchart');
    await page.getByLabel('Repository URL').nth(1).fill('https://charts.example.com');
    await setYamlEditorText(page, 'Locked Values (YAML)', 'a: 1\na: 2', 1);

    const save = page.getByRole('button', { name: 'Save Template' });
    await save.click();
    await expect(page.getByRole('alert').filter({ hasText: /HTTP 400: Invalid YAML in chart "wchart"/ })).toBeVisible({ timeout: 10_000 });

    await setYamlEditorText(page, 'Locked Values (YAML)', 'a: 1', 1);
    await save.click();
    await page.waitForURL(/\/templates\/[0-9a-f-]{36}$/, { timeout: 10_000 });

    const headers = { Authorization: `Bearer ${await tokenFromPage(page)}` };
    const list = await (await page.request.get(
      `${API_BASE}/api/v1/templates?name=${encodeURIComponent(name)}&pageSize=100`, { headers },
    )).json();
    const matches = (list.data ?? []).filter((t: { name: string }) => t.name === name);
    expect(matches).toHaveLength(1);
    const tpl = await (await page.request.get(`${API_BASE}/api/v1/templates/${matches[0].id}`, { headers })).json();
    expect(tpl.charts.map((c: { chart_name: string }) => c.chart_name).sort()).toEqual(['vchart', 'wchart']);
  });

  // The API refuses such a value now, so the row is seeded in the e2e database.
  test('Builder: a template with a stored invalid value can be renamed; a changed invalid value blocks', async ({ page }) => {
    test.skip(!e2eDbConfigured(), E2E_DB_SKIP_REASON);
    await loginAsDevops(page);
    await page.goto('/');
    const token = await tokenFromPage(page);
    const name = uniqueName('tpl-stored-bad');
    const renamed = `${name}-renamed`;
    templateNames.push(name, renamed);
    const templateId = await apiCreateTemplate(page.request, token, name);
    const chartId = await apiAddTemplateChart(page.request, token, templateId, {
      chart_name: 'vchart',
      locked_values: 'tag: x\n',
    });
    expect(chartId).toMatch(/^[0-9a-f-]{36}$/);
    await runE2eSql(`UPDATE template_chart_configs SET locked_values='tag: {{.Branch}}' WHERE id='${chartId}'`);

    await page.goto(`/templates/${templateId}/edit`);
    const nameField = page.getByRole('textbox', { name: 'Name', exact: true });
    await expect(nameField).toHaveValue(name, { timeout: 10_000 });
    const save = page.getByRole('button', { name: 'Save Template' });
    await expect(save).toBeEnabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);

    await setYamlEditorText(page, 'Locked Values (YAML)', 'tag: {{.Commit}}');
    await expect(save).toBeDisabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toBeVisible();
    await setYamlEditorText(page, 'Locked Values (YAML)', 'tag: {{.Branch}}');
    await expect(save).toBeEnabled({ timeout: 10_000 });

    await nameField.fill(renamed);
    await save.click();
    await page.waitForURL(new RegExp(`/templates/${templateId}$`), { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: renamed })).toBeVisible({ timeout: 10_000 });
    const tpl = await (await page.request.get(`${API_BASE}/api/v1/templates/${templateId}`, {
      headers: { Authorization: `Bearer ${token}` },
    })).json();
    expect(tpl.charts[0].locked_values).toBe('tag: {{.Branch}}');
  });
});
