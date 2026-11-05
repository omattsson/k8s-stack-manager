import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import {
  loginAsDevops,
  uniqueName,
  createAndPublishTemplate,
  instantiateTemplate,
  API_BASE,
  setYamlEditorText,
  tokenFromPage,
  apiCreateDefinition,
  apiAddChart,
  runE2eSql,
  e2eDbConfigured,
  E2E_DB_SKIP_REASON,
} from './helpers';

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
    const response = await request.delete(`${API_BASE}/api/v1/stack-definitions/${defId}`, {
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

// Chart values that the deploy cannot parse (#756). The tests type into Monaco,
// so the hint and the save button follow the editor as a user sees them.
test.describe('Definition form: chart values validation (#756)', () => {
  const created: string[] = [];

  test.afterEach(async ({ page, request }) => {
    if (created.length === 0) return;
    const token = await tokenFromPage(page);
    for (const name of created.splice(0)) {
      const res = await request.get(`${API_BASE}/api/v1/stack-definitions?name=${encodeURIComponent(name)}&pageSize=100`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      const body = await res.json();
      for (const d of body.data ?? []) {
        if (d.name === name) {
          await request.delete(`${API_BASE}/api/v1/stack-definitions/${d.id}`, {
            headers: { Authorization: `Bearer ${token}` },
          });
        }
      }
    }
  });

  async function openForm(page: Page, name: string) {
    await loginAsDevops(page);
    await page.goto('/stack-definitions/new');
    await page.getByRole('textbox', { name: 'Name', exact: true }).fill(name);
    await page.getByRole('button', { name: 'Add Chart' }).click();
    await page.getByLabel('Chart Name').fill('vchart');
    await page.getByLabel('Repository URL').fill('https://charts.example.com');
  }

  test('an unquoted template blocks the save until it is quoted', async ({ page }) => {
    await openForm(page, uniqueName('def-yaml-block'));
    const save = page.getByRole('button', { name: 'Save Definition' });

    await setYamlEditorText(page, 'Default Values (YAML)', 'tag: {{.Branch}}');
    await expect(save).toBeDisabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveText('Fix the YAML errors before you save.');

    await setYamlEditorText(page, 'Default Values (YAML)', 'tag: "{{.Branch}}"');
    await expect(save).toBeEnabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);
  });

  test('a duplicate key does not block the save and the API 400 shows on the form', async ({ page }) => {
    const name = uniqueName('def-yaml-dup');
    created.push(name);
    await openForm(page, name);
    const save = page.getByRole('button', { name: 'Save Definition' });

    await setYamlEditorText(page, 'Default Values (YAML)', 'a: 1\na: 2');
    await expect(page.getByTestId('yaml-hint')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);
    await expect(save).toBeEnabled();

    await save.click();
    await expect(page.getByRole('alert').filter({ hasText: /HTTP 400: Invalid YAML in/ })).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/\/stack-definitions\/new/);
  });

  test('a retry after a chart 400 creates no second definition and no duplicate chart', async ({ page }) => {
    const name = uniqueName('def-yaml-retry');
    created.push(name);
    await openForm(page, name);
    await setYamlEditorText(page, 'Default Values (YAML)', 'replicaCount: 1');
    await page.getByRole('button', { name: 'Add Chart' }).click();
    await page.getByLabel('Chart Name').nth(1).fill('wchart');
    await page.getByLabel('Repository URL').nth(1).fill('https://charts.example.com');
    await setYamlEditorText(page, 'Default Values (YAML)', 'a: 1\na: 2', 1);

    const save = page.getByRole('button', { name: 'Save Definition' });
    await save.click();
    await expect(page.getByRole('alert').filter({ hasText: /HTTP 400: Invalid YAML in/ })).toBeVisible({ timeout: 10_000 });

    await setYamlEditorText(page, 'Default Values (YAML)', 'a: 1', 1);
    await save.click();
    await page.waitForURL(/\/stack-definitions$/, { timeout: 10_000 });

    const headers = { Authorization: `Bearer ${await tokenFromPage(page)}` };
    const list = await (await page.request.get(
      `${API_BASE}/api/v1/stack-definitions?name=${encodeURIComponent(name)}&pageSize=100`, { headers },
    )).json();
    const matches = (list.data ?? []).filter((d: { name: string }) => d.name === name);
    expect(matches).toHaveLength(1);
    const def = await (await page.request.get(`${API_BASE}/api/v1/stack-definitions/${matches[0].id}`, { headers })).json();
    expect(def.charts.map((c: { chart_name: string }) => c.chart_name).sort()).toEqual(['vchart', 'wchart']);
  });

  // The API refuses such a value now, so the row is seeded in the e2e database.
  test('a definition with a stored invalid value can be renamed; a changed invalid value blocks', async ({ page }) => {
    test.skip(!e2eDbConfigured(), E2E_DB_SKIP_REASON);
    await loginAsDevops(page);
    await page.goto('/');
    const token = await tokenFromPage(page);
    const name = uniqueName('def-stored-bad');
    const renamed = `${name}-renamed`;
    created.push(name, renamed);
    const defId = await apiCreateDefinition(page.request, token, name);
    const chartId = await apiAddChart(page.request, token, defId, { chart_name: 'vchart', default_values: 'tag: x\n' });
    expect(chartId).toMatch(/^[0-9a-f-]{36}$/);
    await runE2eSql(`UPDATE chart_configs SET default_values='tag: {{.Branch}}' WHERE id='${chartId}'`);

    await page.goto(`/stack-definitions/${defId}/edit`);
    const nameField = page.getByRole('textbox', { name: 'Name', exact: true });
    await expect(nameField).toHaveValue(name, { timeout: 10_000 });
    const save = page.getByRole('button', { name: 'Save Definition' });
    await expect(save).toBeEnabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toHaveCount(0);

    await setYamlEditorText(page, 'Default Values (YAML)', 'tag: {{.Commit}}');
    await expect(save).toBeDisabled({ timeout: 10_000 });
    await expect(page.getByTestId('yaml-error-hint')).toBeVisible();
    await setYamlEditorText(page, 'Default Values (YAML)', 'tag: {{.Branch}}');
    await expect(save).toBeEnabled({ timeout: 10_000 });

    await nameField.fill(renamed);
    await save.click();
    await page.waitForURL(/\/stack-definitions$/, { timeout: 10_000 });

    const def = await (await page.request.get(`${API_BASE}/api/v1/stack-definitions/${defId}`, {
      headers: { Authorization: `Bearer ${token}` },
    })).json();
    expect(def.name).toBe(renamed);
    expect(def.charts[0].default_values).toBe('tag: {{.Branch}}');
  });
});
