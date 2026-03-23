import { test, expect } from '@playwright/test';
import { loginAsAdmin, uniqueName } from './helpers';

test.describe('Cluster Management', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/admin/clusters');
    await expect(page.getByRole('heading', { level: 1, name: 'Cluster Management' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('page loads and shows heading', async ({ page }) => {
    await expect(page.getByRole('heading', { level: 1, name: 'Cluster Management' })).toBeVisible();
  });

  test('shows Add Cluster button', async ({ page }) => {
    await expect(page.getByRole('button', { name: 'Add Cluster' })).toBeVisible();
  });

  test('cluster table shows correct column headers', async ({ page }) => {
    await expect(page.getByRole('columnheader', { name: 'Name' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Region' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'API Server URL' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Health Status' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Default' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Actions' })).toBeVisible();
  });

  test('create cluster via dialog', async ({ page }) => {
    const name = uniqueName('cluster');
    const apiUrl = 'https://test-create.example.com:6443';

    await page.getByRole('button', { name: 'Add Cluster' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Add Cluster')).toBeVisible();
    await expect(dialog.getByRole('textbox').first()).toBeVisible({ timeout: 10_000 });

    await dialog.getByRole('textbox').first().fill(name);
    await dialog.getByLabel('API Server URL').fill(apiUrl);
    await dialog.getByLabel('Kubeconfig Path', { exact: true }).fill('/tmp/test-kubeconfig');
    await dialog.getByLabel('Region').fill('westeurope');

    await dialog.getByRole('button', { name: 'Create' }).click();

    // Wait for dialog to close and cluster to appear in table
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(apiUrl)).toBeVisible();

    // Cleanup via API
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const response = await page.request.get('http://localhost:8081/api/v1/clusters', {
      headers: { Authorization: `Bearer ${token}` },
    });
    const clusters = await response.json();
    const created = clusters.find((c: { name: string }) => c.name === name);
    if (created) {
      await page.request.delete(`http://localhost:8081/api/v1/clusters/${created.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });

  test('edit cluster via dialog', async ({ page }) => {
    // Create a cluster via API first
    const name = uniqueName('cluster-edit');
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const createResp = await page.request.post('http://localhost:8081/api/v1/clusters', {
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      data: {
        name,
        api_server_url: 'https://edit-test.example.com:6443',
        kubeconfig_path: '/tmp/test-kubeconfig',
        region: 'northeurope',
      },
    });
    const created = await createResp.json();

    // Reload to see the new cluster
    await page.reload();
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });

    // Click edit button on the row containing our cluster
    const row = page.getByRole('row').filter({ hasText: name });
    await row.getByRole('button', { name: `Edit ${name}` }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Edit Cluster')).toBeVisible();

    const updatedName = uniqueName('cluster-edited');
    const nameField = dialog.getByRole('textbox').first();
    await nameField.clear();
    await nameField.fill(updatedName);

    await dialog.getByRole('button', { name: 'Update' }).click();
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // Verify updated name appears
    await expect(page.getByText(updatedName)).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(name)).not.toBeVisible();

    // Cleanup
    await page.request.delete(`http://localhost:8081/api/v1/clusters/${created.id}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test('delete cluster via confirmation dialog', async ({ page }) => {
    // Create a cluster via API first
    const name = uniqueName('cluster-del');
    const token = await page.evaluate(() => localStorage.getItem('token'));
    await page.request.post('http://localhost:8081/api/v1/clusters', {
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      data: {
        name,
        api_server_url: 'https://delete-test.example.com:6443',
        kubeconfig_path: '/tmp/test-kubeconfig',
        region: 'eastus',
      },
    });

    await page.reload();
    await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });

    // Click delete on the row
    const row = page.getByRole('row').filter({ hasText: name });
    await row.getByRole('button', { name: 'Delete' }).click();

    // Confirm deletion dialog
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'Delete Cluster' })).toBeVisible();
    await expect(dialog.getByText(name)).toBeVisible();

    await dialog.getByRole('button', { name: 'Delete' }).click();
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // Verify removed from table
    await expect(page.getByText(name)).not.toBeVisible({ timeout: 10_000 });
  });

  test('create dialog requires name and API Server URL', async ({ page }) => {
    await page.getByRole('button', { name: 'Add Cluster' }).click();

    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Kubeconfig Path', { exact: true }).fill('/tmp/test-kubeconfig');

    // Try to create without name and URL
    await dialog.getByRole('button', { name: 'Create' }).click();

    // Validation error should appear in dialog
    await expect(dialog.getByText('Name and API Server URL are required')).toBeVisible();
  });

  test('create dialog requires kubeconfig', async ({ page }) => {
    await page.getByRole('button', { name: 'Add Cluster' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('textbox').first()).toBeVisible({ timeout: 10_000 });
    await dialog.getByRole('textbox').first().fill('test-cluster');
    await dialog.getByLabel('API Server URL').fill('https://test.example.com:6443');

    // Try to create without kubeconfig
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog.getByText('Either kubeconfig data or kubeconfig path is required when creating a cluster')).toBeVisible();
  });

  test('test cluster connection shows feedback', async ({ page }) => {
    // Create a cluster via API
    const name = uniqueName('cluster-conn');
    const token = await page.evaluate(() => localStorage.getItem('token'));
    const createResp = await page.request.post('http://localhost:8081/api/v1/clusters', {
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      data: {
        name,
        api_server_url: 'https://conn-test.example.com:6443',
        kubeconfig_path: '/tmp/test-kubeconfig',
        region: 'westeurope',
      },
    });
    const created = await createResp.json();

    try {
      await page.reload();
      await expect(page.getByText(name)).toBeVisible({ timeout: 10_000 });

      // Click the Test Connection button in the row
      const row = page.getByRole('row').filter({ hasText: name });
      await row.getByRole('button', { name: `Test connection for ${name}` }).click();

      // The notification should appear as an alert (success or error depending on
      // whether the cluster is actually reachable). Either way it should contain
      // the cluster name, confirming the action was invoked.
      const alert = page.getByRole('alert');
      await expect(alert).toBeVisible({ timeout: 15_000 });
      await expect(alert).toContainText(name);
    } finally {
      // Cleanup
      await page.request.delete(`http://localhost:8081/api/v1/clusters/${created.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });

  test('set default cluster marks it with Default chip', async ({ page }) => {
    // Create two clusters so we can change the default
    const nameA = uniqueName('cluster-std-a');
    const nameB = uniqueName('cluster-std-b');
    const token = await page.evaluate(() => localStorage.getItem('token'));

    const respA = await page.request.post('http://localhost:8081/api/v1/clusters', {
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      data: {
        name: nameA,
        api_server_url: 'https://std-a.example.com:6443',
        kubeconfig_path: '/tmp/test-kubeconfig',
        region: 'westeurope',
      },
    });
    const createdA = await respA.json();

    const respB = await page.request.post('http://localhost:8081/api/v1/clusters', {
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      data: {
        name: nameB,
        api_server_url: 'https://std-b.example.com:6443',
        kubeconfig_path: '/tmp/test-kubeconfig',
        region: 'northeurope',
      },
    });
    const createdB = await respB.json();

    try {
      await page.reload();
      await expect(page.getByText(nameA)).toBeVisible({ timeout: 10_000 });
      await expect(page.getByText(nameB)).toBeVisible({ timeout: 10_000 });

      // Find the row for cluster B and click Set Default
      const rowB = page.getByRole('row').filter({ hasText: nameB });
      await rowB.getByRole('button', { name: `Set ${nameB} as default` }).click();

      // Wait for the success notification
      const alert = page.getByRole('alert');
      await expect(alert).toBeVisible({ timeout: 10_000 });
      await expect(alert).toContainText(`${nameB} set as default`);

      // After refresh, cluster B's row should show the "Default" chip
      // The page auto-refreshes after setDefault, but wait for the chip to appear
      const rowBAfter = page.getByRole('row').filter({ hasText: nameB });
      await expect(rowBAfter.getByText('Default')).toBeVisible({ timeout: 10_000 });

      // Cluster B's row should no longer have the "Set as default" button
      await expect(rowBAfter.getByRole('button', { name: `Set ${nameB} as default` })).not.toBeVisible();
    } finally {
      // Restore the original default cluster before cleanup
      const clustersRes = await page.request.get('http://localhost:8081/api/v1/clusters', {
        headers: { Authorization: `Bearer ${token}` },
      });
      const clusters = await clustersRes.json();
      const original = clusters.find((c: { name: string }) => c.name === 'default');
      if (original) {
        await page.request.post(`http://localhost:8081/api/v1/clusters/${original.id}/default`, {
          headers: { Authorization: `Bearer ${token}` },
        });
      }
      // Cleanup both test clusters
      await page.request.delete(`http://localhost:8081/api/v1/clusters/${createdA.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      await page.request.delete(`http://localhost:8081/api/v1/clusters/${createdB.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });
});
