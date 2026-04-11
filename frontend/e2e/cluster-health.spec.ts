import { test, expect } from '@playwright/test';
import { loginAsAdmin, API_BASE, ADMIN_PASSWORD } from './helpers';

const MOCK_CLUSTER_ID = 'cluster-001';
const MOCK_CLUSTER_NAME = 'test-cluster';

const mockClusters = [
  { id: MOCK_CLUSTER_ID, name: MOCK_CLUSTER_NAME, is_default: true },
];

const mockSummary = {
  node_count: 2,
  ready_node_count: 2,
  total_cpu: '8000m',
  allocatable_cpu: '6000m',
  total_memory: '16Gi',
  allocatable_memory: '12Gi',
  namespace_count: 5,
};

const mockNodes = [
  {
    name: 'node-1',
    status: 'Ready',
    capacity: { cpu: '4000m', memory: '8Gi' },
    pod_count: 10,
    conditions: [{ type: 'Ready', status: 'True' }],
  },
  {
    name: 'node-2',
    status: 'Ready',
    capacity: { cpu: '4000m', memory: '8Gi' },
    pod_count: 8,
    conditions: [{ type: 'Ready', status: 'True' }],
  },
];

const mockNamespaces = [
  { name: 'default', phase: 'Active', created_at: '2025-01-01T00:00:00Z' },
  { name: 'kube-system', phase: 'Active', created_at: '2025-01-01T00:00:00Z' },
];

/**
 * Set up route mocks for all cluster health API endpoints so tests are
 * deterministic and do not depend on actual cluster state.
 */
async function mockClusterAPIs(page: import('@playwright/test').Page) {
  await page.route(/\/api\/v1\/clusters$/, (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockClusters),
      });
    }
    return route.continue();
  });

  await page.route(`**/api/v1/clusters/${MOCK_CLUSTER_ID}/health/summary`, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockSummary),
    }),
  );

  await page.route(`**/api/v1/clusters/${MOCK_CLUSTER_ID}/health/nodes`, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockNodes),
    }),
  );

  await page.route(`**/api/v1/clusters/${MOCK_CLUSTER_ID}/namespaces`, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockNamespaces),
    }),
  );
}

test.describe('Cluster Health', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
    await mockClusterAPIs(page);
    await page.goto('/admin/cluster-health');
    await expect(page.getByRole('heading', { level: 1, name: 'Cluster Health' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('page loads with correct heading', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Cluster Health' })).toBeVisible();
  });

  test('shows cluster selector dropdown', async ({ page }) => {
    // MUI Select with InputLabel "Cluster" uses aria-labelledby.
    // Wait for loading to finish so the Select is rendered.
    const clusterSelect = page.getByLabel('Cluster');
    await expect(clusterSelect).toBeVisible({ timeout: 10_000 });
  });

  test('shows auto-refresh toggle', async ({ page }) => {
    // FormControlLabel with label "Auto-refresh" wraps a Switch.
    await expect(page.getByLabel('Auto-refresh')).toBeVisible({ timeout: 10_000 });
  });

  test('summary cards render after loading', async ({ page }) => {
    // Wait for the loading spinner to disappear, then check card titles.
    await expect(page.getByRole('status')).toBeHidden({ timeout: 10_000 });
    await expect(page.getByText('Nodes').first()).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('CPU', { exact: true })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Memory', { exact: true }).first()).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Namespaces', { exact: true }).first()).toBeVisible({ timeout: 10_000 });
  });

  test('nodes table renders with correct column headers', async ({ page }) => {
    // Wait for loading to finish and the Nodes heading to appear.
    await expect(page.getByRole('heading', { level: 6, name: 'Nodes' })).toBeVisible({
      timeout: 10_000,
    });

    await expect(page.getByRole('columnheader', { name: 'Name' }).first()).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Status' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'CPU Capacity' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Memory Capacity' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Pods' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Conditions' })).toBeVisible();
  });

  test('namespaces table renders with correct column headers', async ({ page }) => {
    // Wait for loading to finish and the Namespaces heading to appear.
    await expect(page.getByRole('heading', { level: 6, name: 'Namespaces' })).toBeVisible({
      timeout: 10_000,
    });

    // Use the second table to avoid ambiguity with the nodes table "Name" column.
    const namespacesTable = page.locator('table').nth(1);
    await expect(namespacesTable.getByRole('columnheader', { name: 'Name' })).toBeVisible();
    await expect(namespacesTable.getByRole('columnheader', { name: 'Phase' })).toBeVisible();
    await expect(namespacesTable.getByRole('columnheader', { name: 'Created At' })).toBeVisible();
  });

  test('cluster selector changes selected cluster', async ({ page }) => {
    // Wait for the Select to render with mock cluster data.
    const clusterSelect = page.getByLabel('Cluster');
    await expect(clusterSelect).toBeVisible({ timeout: 10_000 });

    // Open the dropdown and verify at least one option is available.
    await clusterSelect.click();
    const options = page.getByRole('option');
    await expect(options.first()).toBeVisible({ timeout: 5_000 });

    // Verify the mock cluster name appears as an option.
    await expect(page.getByRole('option', { name: new RegExp(MOCK_CLUSTER_NAME) })).toBeVisible();
  });

  test('displays error alert when health data fails to load', async ({ page }) => {
    // Override the summary endpoint to return an error.
    await page.route(`**/api/v1/clusters/${MOCK_CLUSTER_ID}/health/summary`, (route) =>
      route.fulfill({ status: 500, body: JSON.stringify({ error: 'Internal server error' }) }),
    );

    // Also make the other health endpoints fail so the Promise.all rejects.
    await page.route(`**/api/v1/clusters/${MOCK_CLUSTER_ID}/health/nodes`, (route) =>
      route.fulfill({ status: 500, body: JSON.stringify({ error: 'Internal server error' }) }),
    );
    await page.route(`**/api/v1/clusters/${MOCK_CLUSTER_ID}/namespaces`, (route) =>
      route.fulfill({ status: 500, body: JSON.stringify({ error: 'Internal server error' }) }),
    );

    // Reload to trigger the intercepted requests.
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Cluster Health' })).toBeVisible({
      timeout: 10_000,
    });

    await expect(page.getByRole('alert')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Failed to load cluster health data')).toBeVisible();
  });
});

test.describe('Cluster Health - Access Control', () => {
  test('unauthenticated user is redirected to login', async ({ page }) => {
    await page.goto('/admin/cluster-health');
    await expect(page).toHaveURL(/\/login/, { timeout: 10_000 });
  });

  test('regular user without devops role sees permission error', async ({ page }) => {
    const username = `viewer-${Date.now()}`;

    // Log in as admin via API to get the token directly from the response
    // (not from localStorage, which requires a page load to populate).
    let adminRes;
    for (let attempt = 0; attempt < 5; attempt++) {
      adminRes = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
        data: { username: 'admin', password: ADMIN_PASSWORD },
      });
      if (adminRes.status() !== 429) break;
      await page.waitForTimeout(2000 * (attempt + 1));
    }
    expect(adminRes!.ok(), `Admin login failed with status ${adminRes!.status()}`).toBe(true);
    const { token: adminToken } = await adminRes!.json();

    // Register a regular user via the admin API.
    const registerRes = await page.request.post(`${API_BASE}/api/v1/auth/register`, {
      headers: { Authorization: `Bearer ${adminToken}` },
      data: { username, password: 'testpass123', role: 'user' },
    });
    expect(registerRes.ok(), `Register failed with status ${registerRes.status()}`).toBe(true);

    // Log in as the regular user.
    let userRes;
    for (let attempt = 0; attempt < 5; attempt++) {
      userRes = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
        data: { username, password: 'testpass123' },
      });
      if (userRes!.status() !== 429) break;
      await page.waitForTimeout(2000 * (attempt + 1));
    }
    expect(userRes!.ok(), `User login failed with status ${userRes!.status()}`).toBe(true);
    const { token: userToken } = await userRes!.json();

    // Inject the regular user token so AuthContext picks it up.
    await page.addInitScript((t) => {
      localStorage.setItem('token', t);
    }, userToken);

    await page.goto('/admin/cluster-health');

    // ProtectedRoute shows a permission error for users without devops/admin role.
    await expect(page.getByText('You do not have permission to access this page.')).toBeVisible({
      timeout: 10_000,
    });
  });
});
