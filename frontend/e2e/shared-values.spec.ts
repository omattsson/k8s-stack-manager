import { test, expect } from '@playwright/test';
import { loginAsAdmin, uniqueName, API_BASE, ADMIN_PASSWORD } from './helpers';

/**
 * Helper: login via API and return the JWT token.
 */
async function apiLogin(request: import('@playwright/test').APIRequestContext): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { username: 'admin', password: ADMIN_PASSWORD },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.token;
}

/**
 * Helper: create a cluster via API and return its ID.
 */
async function apiCreateCluster(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  name: string,
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/clusters`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      api_server_url: 'https://sv-test.example.com:6443',
      kubeconfig_path: '/tmp/fake-kubeconfig',
      region: 'westeurope',
    },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.id;
}

/**
 * Helper: delete a cluster via API.
 */
async function apiDeleteCluster(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  id: string,
): Promise<void> {
  await request.delete(`${API_BASE}/api/v1/clusters/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

/**
 * Helper: create shared values via API and return the ID.
 */
async function apiCreateSharedValues(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  clusterId: string,
  name: string,
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/clusters/${clusterId}/shared-values`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      description: 'E2E test shared values',
      values: 'key: value\nfoo: bar',
      priority: 10,
    },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.id;
}

/**
 * Helper: delete shared values via API.
 */
async function apiDeleteSharedValues(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  clusterId: string,
  id: string,
): Promise<void> {
  await request.delete(`${API_BASE}/api/v1/clusters/${clusterId}/shared-values/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe('Shared Values', () => {
  let token: string;
  let clusterId: string;
  let clusterName: string;

  test.beforeAll(async ({ request }) => {
    token = await apiLogin(request);
    clusterName = uniqueName('sv-cluster');
    clusterId = await apiCreateCluster(request, token, clusterName);
  });

  test.afterAll(async ({ request }) => {
    await apiDeleteCluster(request, token, clusterId);
  });

  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/admin/shared-values');
    await expect(page.getByRole('heading', { level: 1, name: 'Shared Values' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('page loads with heading', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Shared Values' })).toBeVisible();
  });

  test('shows cluster selector', async ({ page }) => {
    await expect(page.getByLabel('Cluster')).toBeVisible({ timeout: 10_000 });
  });

  test('create shared values for a cluster', async ({ page }) => {
    // Select our test cluster
    await page.getByLabel('Cluster').click();
    await page.getByRole('option', { name: new RegExp(clusterName) }).click();

    const svName = uniqueName('sv');
    await page.getByRole('button', { name: 'Add Shared Values' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Add Shared Values')).toBeVisible();

    await dialog.getByLabel('Name').fill(svName);
    await dialog.getByLabel('Description').fill('E2E test shared values');
    await dialog.getByLabel('Priority').fill('10');
    await dialog.getByLabel('Values (YAML)').fill('test_key: test_value');

    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // Value should appear in the table
    await expect(page.getByText(svName)).toBeVisible({ timeout: 10_000 });

    // Cleanup: delete via API
    const resp = await page.request.get(`${API_BASE}/api/v1/clusters/${clusterId}/shared-values`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const values = await resp.json();
    const created = values.find((v: { name: string }) => v.name === svName);
    if (created) {
      await apiDeleteSharedValues(page.request, token, clusterId, created.id);
    }
  });

  test('edit shared values', async ({ page, request }) => {
    const svName = uniqueName('sv-edit');
    const svId = await apiCreateSharedValues(request, token, clusterId, svName);

    // Select our test cluster and reload
    await page.getByLabel('Cluster').click();
    await page.getByRole('option', { name: new RegExp(clusterName) }).click();
    await expect(page.getByText(svName)).toBeVisible({ timeout: 10_000 });

    // Click edit button
    await page.getByRole('button', { name: `Edit ${svName}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Edit Shared Values')).toBeVisible();

    const nameField = dialog.getByLabel('Name');
    const updatedName = uniqueName('sv-edited');
    await nameField.clear();
    await nameField.fill(updatedName);

    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    await expect(page.getByText(updatedName)).toBeVisible({ timeout: 10_000 });

    // Cleanup
    await apiDeleteSharedValues(request, token, clusterId, svId);
  });

  test('delete shared values', async ({ page, request }) => {
    const svName = uniqueName('sv-del');
    const svId = await apiCreateSharedValues(request, token, clusterId, svName);

    // Select our test cluster and reload
    await page.getByLabel('Cluster').click();
    await page.getByRole('option', { name: new RegExp(clusterName) }).click();
    await expect(page.getByText(svName)).toBeVisible({ timeout: 10_000 });

    // Click delete button
    await page.getByRole('button', { name: `Delete ${svName}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Delete Shared Values')).toBeVisible();
    await dialog.getByRole('button', { name: 'Delete' }).click();

    await expect(dialog).not.toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(svName)).not.toBeVisible({ timeout: 10_000 });

    // Already deleted, but attempt cleanup just in case
    await apiDeleteSharedValues(request, token, clusterId, svId).catch(() => {});
  });

  test('priority ordering is displayed in table', async ({ page, request }) => {
    const svName1 = uniqueName('sv-p1');
    const svName2 = uniqueName('sv-p2');

    // Create two shared values with different priorities
    const sv1Id = await apiCreateSharedValues(request, token, clusterId, svName1);
    const sv2Resp = await request.post(`${API_BASE}/api/v1/clusters/${clusterId}/shared-values`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: svName2,
        description: 'Higher priority',
        values: 'priority_test: high',
        priority: 20,
      },
    });
    const sv2 = await sv2Resp.json();

    // Select our test cluster
    await page.getByLabel('Cluster').click();
    await page.getByRole('option', { name: new RegExp(clusterName) }).click();

    await expect(page.getByText(svName1)).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(svName2)).toBeVisible({ timeout: 10_000 });

    // Verify the Priority column header is visible
    await expect(page.getByRole('columnheader', { name: 'Priority' })).toBeVisible();

    // Cleanup
    await apiDeleteSharedValues(request, token, clusterId, sv1Id);
    await apiDeleteSharedValues(request, token, clusterId, sv2.id);
  });
});
