import { test, expect, APIRequestContext, Page } from '@playwright/test';
import {
  API_BASE,
  apiAddChart,
  apiCreateDefinition,
  apiCreateInstance,
  apiLogin,
  ensureDefaultCluster,
  loginAsAdmin,
  uniqueName,
} from './helpers';

/**
 * Instance page layout (#718): for a running or partial instance the
 * "Access URLs" section comes before "Cluster Resources" (one pod card per
 * chart would otherwise push the URLs down). Without a cluster no deploy
 * succeeds, so the instance status and the namespace status are mocked.
 */

let api: APIRequestContext;
let adminToken: string;
let definitionId: string;
const createdInstances: string[] = [];

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

const namespaceStatus = {
  namespace: 'e2e-layout',
  status: 'healthy',
  charts: [{ chart_name: 'e2e-layout-chart', release_name: 'e2e-layout-chart', status: 'healthy', deployments: [], services: [] }],
  ingresses: [{ name: 'web', host: 'web.e2e.example.com', path: '/', tls: true, url: 'https://web.e2e.example.com/' }],
  last_checked: '2026-10-19T10:00:00Z',
};

async function mockRunningInstance(page: Page, id: string, status: string) {
  await page.route(new RegExp(`/api/v1/stack-instances/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    if (!res.ok()) return route.fulfill({ response: res });
    return route.fulfill({ response: res, json: { ...(await res.json()), status } });
  });
  // A RegExp, not a glob: the page asks for /pods?exec_hints=true.
  await page.route(new RegExp(`/api/v1/stack-instances/${id}/pods(\\?.*)?$`), (route) =>
    route.fulfill({ json: namespaceStatus }));
}

test.describe('Instance detail layout', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-layout-def'));
    await apiAddChart(api, adminToken, definitionId, { chart_name: 'e2e-layout-chart' });
  });

  // The page can still refetch through a mocked route when a test ends.
  test.afterEach(async ({ page }) => {
    await page.unrouteAll({ behavior: 'ignoreErrors' });
  });

  test.afterAll(async () => {
    if (!api) return;
    for (const id of createdInstances) {
      await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    }
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    await api.dispose();
  });

  for (const status of ['running', 'partial']) {
    test(`Access URLs come before Cluster Resources for a ${status} instance`, async ({ page }) => {
      const id = await apiCreateInstance(api, adminToken, definitionId, uniqueName('e2e-layout'));
      createdInstances.push(id);
      await loginAsAdmin(page);
      await mockRunningInstance(page, id, status);
      await page.goto(`/stack-instances/${id}`);

      // Both sections are subtitle2 Typography, which MUI renders as h6.
      await expect(page.getByRole('heading', { name: /^(Access URLs|Cluster Resources)$/ }))
        .toHaveText(['Access URLs', 'Cluster Resources'], { timeout: 10_000 });
    });
  }

  test('an error instance shows Cluster Resources but no Access URLs', async ({ page }) => {
    const id = await apiCreateInstance(api, adminToken, definitionId, uniqueName('e2e-layout'));
    createdInstances.push(id);
    await loginAsAdmin(page);
    await mockRunningInstance(page, id, 'error');
    await page.goto(`/stack-instances/${id}`);

    await expect(page.getByText('Cluster Resources', { exact: true })).toBeVisible({ timeout: 10_000 });
    // Proves the mocked namespace status was applied (only a loaded status renders this).
    await expect(page.getByText('Cluster Status:', { exact: true })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Access URLs', { exact: true })).toHaveCount(0);
  });
});
