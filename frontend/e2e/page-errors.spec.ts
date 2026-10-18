import { test, expect } from '@playwright/test';
import { API_BASE, apiAddChart, apiCreateDefinition, apiLogin, loginAsAdmin, uniqueName } from './helpers';

// No uncaught page errors (for example "Uncaught (in promise) Canceled", #577)
// when the user moves between pages, also away from pages with YAML editors.
// DB-backed, no cluster required.

test.describe('Page errors', () => {
  let instanceId: string;
  let definitionId: string;

  test.beforeAll(async ({ request }) => {
    const token = await apiLogin(request);
    definitionId = await apiCreateDefinition(request, token, uniqueName('perr-def'));
    await apiAddChart(request, token, definitionId, { chart_name: 'web', default_values: 'replicaCount: 1\n' });
    const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { stack_definition_id: definitionId, name: uniqueName('perr').slice(0, 40), branch: 'main' },
    });
    expect(res.ok(), await res.text()).toBe(true);
    instanceId = (await res.json()).id;
  });

  test('moving between pages raises no uncaught error', async ({ page }) => {
    const errors: string[] = [];
    let current = '';
    page.on('pageerror', (err) => errors.push(`${current}: ${err.name}: ${err.message}`));
    await loginAsAdmin(page);

    const paths = [
      '/',
      `/stack-instances/${instanceId}`,
      '/templates',
      `/stack-definitions/${definitionId}/edit`,
      '/notifications',
      `/stack-instances/${instanceId}`,
      '/profile',
      '/does-not-exist',
    ];
    // Full page loads
    for (const path of paths) {
      current = `goto ${path}`;
      await page.goto(path);
      await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => undefined);
    }
    // In-app navigation away from a page with editors (no reload)
    current = 'in-app from the instance page';
    await page.goto(`/stack-instances/${instanceId}`);
    // Wait until the YAML editors (Monaco) are loaded.
    await expect(page.locator('.monaco-editor').first()).toBeVisible({ timeout: 20_000 });
    await page.getByRole('link', { name: 'Templates' }).first().click();
    await page.waitForURL('**/templates');
    await page.goBack();
    await page.waitForURL(`**/stack-instances/${instanceId}`);
    await page.getByRole('link', { name: 'Dashboard' }).first().click();
    await page.waitForURL((u) => u.pathname === '/');
    await page.waitForTimeout(1_000);

    expect(errors, errors.join('\n')).toEqual([]);
  });
});
