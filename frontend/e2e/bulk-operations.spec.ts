import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate, instantiateTemplate } from './helpers';

const mockInstances = [
  {
    id: 'bulk-inst-001',
    name: 'bulk-test-alpha',
    branch: 'main',
    namespace: 'stack-bulk-test-alpha-user',
    status: 'stopped',
    owner: 'user',
    cluster_id: '',
    definition_id: 'def-001',
    created_at: '2025-06-01T00:00:00Z',
    updated_at: '2025-06-01T00:00:00Z',
  },
  {
    id: 'bulk-inst-002',
    name: 'bulk-test-beta',
    branch: 'develop',
    namespace: 'stack-bulk-test-beta-user',
    status: 'running',
    owner: 'user',
    cluster_id: '',
    definition_id: 'def-002',
    created_at: '2025-06-02T00:00:00Z',
    updated_at: '2025-06-02T00:00:00Z',
  },
  {
    id: 'bulk-inst-003',
    name: 'bulk-test-gamma',
    branch: 'main',
    namespace: 'stack-bulk-test-gamma-user',
    status: 'draft',
    owner: 'user',
    cluster_id: '',
    definition_id: 'def-001',
    created_at: '2025-06-03T00:00:00Z',
    updated_at: '2025-06-03T00:00:00Z',
  },
];

/**
 * Mock APIs needed for the dashboard to render with mock instance data.
 */
async function mockDashboardAPIs(page: import('@playwright/test').Page) {
  await page.route('**/api/v1/stack-instances', (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockInstances),
      });
    }
    return route.continue();
  });

  await page.route('**/api/v1/stack-instances/recent', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    }),
  );

  await page.route('**/api/v1/favorites', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    }),
  );

  await page.route(/\/api\/v1\/clusters$/, (route) => {
    if (route.request().method() === 'GET') {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    }
    return route.continue();
  });

  // Mock bulk operation endpoints
  await page.route('**/api/v1/stack-instances/bulk/deploy', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        total: 2,
        succeeded: 2,
        failed: 0,
        results: [
          { instance_id: 'bulk-inst-001', instance_name: 'bulk-test-alpha', status: 'ok' },
          { instance_id: 'bulk-inst-002', instance_name: 'bulk-test-beta', status: 'ok' },
        ],
      }),
    }),
  );

  await page.route('**/api/v1/stack-instances/bulk/stop', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        total: 2,
        succeeded: 2,
        failed: 0,
        results: [
          { instance_id: 'bulk-inst-001', instance_name: 'bulk-test-alpha', status: 'ok' },
          { instance_id: 'bulk-inst-002', instance_name: 'bulk-test-beta', status: 'ok' },
        ],
      }),
    }),
  );

  await page.route('**/api/v1/stack-instances/bulk/clean', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        total: 2,
        succeeded: 2,
        failed: 0,
        results: [
          { instance_id: 'bulk-inst-001', instance_name: 'bulk-test-alpha', status: 'ok' },
          { instance_id: 'bulk-inst-002', instance_name: 'bulk-test-beta', status: 'ok' },
        ],
      }),
    }),
  );

  await page.route('**/api/v1/stack-instances/bulk/delete', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        total: 2,
        succeeded: 2,
        failed: 0,
        results: [
          { instance_id: 'bulk-inst-001', instance_name: 'bulk-test-alpha', status: 'ok' },
          { instance_id: 'bulk-inst-002', instance_name: 'bulk-test-beta', status: 'ok' },
        ],
      }),
    }),
  );
}

test.describe('Bulk Operations', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
    await mockDashboardAPIs(page);
    await page.goto('/');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });
  });

  test('dashboard shows checkboxes on instance cards', async ({ page }) => {
    // Each instance card should have a checkbox for selection
    await expect(page.getByRole('checkbox', { name: /Select bulk-test-alpha/ })).toBeVisible({
      timeout: 10_000,
    });
    await expect(page.getByRole('checkbox', { name: /Select bulk-test-beta/ })).toBeVisible();
    await expect(page.getByRole('checkbox', { name: /Select bulk-test-gamma/ })).toBeVisible();
  });

  test('select all checkbox is visible', async ({ page }) => {
    await expect(page.getByRole('checkbox', { name: 'Select all instances' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('selecting instances shows bulk action toolbar with count', async ({ page }) => {
    // Select first instance
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();

    // Bulk toolbar should appear
    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });
    await expect(toolbar.getByText('1 selected')).toBeVisible();

    // Select second instance
    await page.getByRole('checkbox', { name: /Select bulk-test-beta/ }).click();
    await expect(toolbar.getByText('2 selected')).toBeVisible();
  });

  test('select all checkbox selects all filtered instances', async ({ page }) => {
    await page.getByRole('checkbox', { name: 'Select all instances' }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });
    await expect(toolbar.getByText('3 selected')).toBeVisible();
  });

  test('bulk action buttons are present in toolbar', async ({ page }) => {
    // Select an instance to trigger toolbar
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await expect(toolbar.getByRole('button', { name: 'Deploy' })).toBeVisible();
    await expect(toolbar.getByRole('button', { name: 'Stop' })).toBeVisible();
    await expect(toolbar.getByRole('button', { name: 'Clean' })).toBeVisible();
    await expect(toolbar.getByRole('button', { name: 'Delete' })).toBeVisible();
  });

  test('clicking bulk action shows confirmation dialog with instance names', async ({ page }) => {
    // Select two instances
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();
    await page.getByRole('checkbox', { name: /Select bulk-test-beta/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Click Deploy
    await toolbar.getByRole('button', { name: 'Deploy' }).click();

    // Confirmation dialog should appear with title and instance names
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Confirm Bulk Deploy')).toBeVisible({ timeout: 5_000 });
    await expect(dialog.getByText('bulk-test-alpha')).toBeVisible();
    await expect(dialog.getByText('bulk-test-beta')).toBeVisible();
    await expect(dialog.getByText(/Deploy 2 instances/)).toBeVisible();
  });

  test('delete confirmation shows warning alert', async ({ page }) => {
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Click Delete
    await toolbar.getByRole('button', { name: 'Delete' }).click();

    // Confirmation dialog should show a warning
    await expect(page.getByText('Confirm Bulk Delete')).toBeVisible({ timeout: 5_000 });
    await expect(
      page.getByText('This action cannot be undone. Selected instances will be permanently deleted.'),
    ).toBeVisible();
  });

  test('cancel dismisses confirmation dialog', async ({ page }) => {
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    await toolbar.getByRole('button', { name: 'Stop' }).click();
    await expect(page.getByText('Confirm Bulk Stop')).toBeVisible({ timeout: 5_000 });

    // Cancel the dialog
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.getByText('Confirm Bulk Stop')).not.toBeVisible({ timeout: 5_000 });

    // Toolbar should still be visible (selection not cleared)
    await expect(toolbar).toBeVisible();
  });

  test('clear selection button removes all selections', async ({ page }) => {
    // Select two instances
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();
    await page.getByRole('checkbox', { name: /Select bulk-test-beta/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });
    await expect(toolbar.getByText('2 selected')).toBeVisible();

    // Click "Clear Selection"
    await toolbar.getByRole('button', { name: 'Clear Selection' }).click();

    // Toolbar should disappear
    await expect(toolbar).not.toBeVisible({ timeout: 5_000 });

    // Checkboxes should be unchecked
    await expect(page.getByRole('checkbox', { name: /Select bulk-test-alpha/ })).not.toBeChecked();
    await expect(page.getByRole('checkbox', { name: /Select bulk-test-beta/ })).not.toBeChecked();
  });

  test('deselect all via select-all checkbox clears toolbar', async ({ page }) => {
    // Select all
    await page.getByRole('checkbox', { name: 'Select all instances' }).click();
    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Deselect all
    await page.getByRole('checkbox', { name: 'Select all instances' }).click();
    await expect(toolbar).not.toBeVisible({ timeout: 5_000 });
  });

  test('executing bulk deploy shows results dialog with success counts', async ({ page }) => {
    // Select two instances
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();
    await page.getByRole('checkbox', { name: /Select bulk-test-beta/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Click Deploy in toolbar to open confirmation dialog
    await toolbar.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByText('Confirm Bulk Deploy')).toBeVisible({ timeout: 5_000 });

    // Click the Confirm (Deploy) button in the dialog
    await page.getByRole('dialog').getByRole('button', { name: 'Deploy' }).click();

    // Results dialog appears with success counts and instance names
    const resultsDialog = page.getByRole('dialog');
    await expect(resultsDialog.getByText('Bulk Operation Results')).toBeVisible({ timeout: 5_000 });
    await expect(resultsDialog.getByText('2 succeeded', { exact: true })).toBeVisible();
    await expect(resultsDialog.getByText('bulk-test-alpha')).toBeVisible();
    await expect(resultsDialog.getByText('bulk-test-beta')).toBeVisible();
  });

  test('executing bulk stop completes and shows results', async ({ page }) => {
    // Select two instances
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();
    await page.getByRole('checkbox', { name: /Select bulk-test-beta/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Click Stop in toolbar
    await toolbar.getByRole('button', { name: 'Stop' }).click();
    await expect(page.getByText('Confirm Bulk Stop')).toBeVisible({ timeout: 5_000 });

    // Click the Confirm (Stop) button in the dialog
    await page.getByRole('dialog').getByRole('button', { name: 'Stop' }).click();

    // Results dialog appears with success counts
    const resultsDialog = page.getByRole('dialog');
    await expect(resultsDialog.getByText('Bulk Operation Results')).toBeVisible({ timeout: 5_000 });
    await expect(resultsDialog.getByText('2 succeeded', { exact: true })).toBeVisible();
  });

  test('executing bulk delete with warning completes successfully', async ({ page }) => {
    // Select one instance
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Click Delete in toolbar
    await toolbar.getByRole('button', { name: 'Delete' }).click();
    await expect(page.getByText('Confirm Bulk Delete')).toBeVisible({ timeout: 5_000 });

    // Warning text must be visible before confirming
    await expect(
      page.getByText('This action cannot be undone. Selected instances will be permanently deleted.'),
    ).toBeVisible();

    // Click the Confirm (Delete) button in the dialog
    await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();

    // Results dialog appears with a success alert
    const resultsDialog = page.getByRole('dialog');
    await expect(resultsDialog.getByText('Bulk Operation Results')).toBeVisible({ timeout: 5_000 });
    await expect(resultsDialog.getByRole('alert').filter({ hasText: /succeeded/ })).toBeVisible();
  });

  test('bulk operation with partial failure shows errors in results', async ({ page }) => {
    // Override deploy mock with a partial failure response (LIFO — this handler runs first)
    await page.route('**/api/v1/stack-instances/bulk/deploy', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          total: 2,
          succeeded: 1,
          failed: 1,
          results: [
            { instance_id: 'bulk-inst-001', instance_name: 'bulk-test-alpha', status: 'ok' },
            {
              instance_id: 'bulk-inst-002',
              instance_name: 'bulk-test-beta',
              status: 'error',
              error: 'instance status is not valid for deploy',
            },
          ],
        }),
      }),
    );

    // Select two instances
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();
    await page.getByRole('checkbox', { name: /Select bulk-test-beta/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Click Deploy in toolbar
    await toolbar.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByText('Confirm Bulk Deploy')).toBeVisible({ timeout: 5_000 });

    // Confirm the operation
    await page.getByRole('dialog').getByRole('button', { name: 'Deploy' }).click();

    // Results dialog shows both success and failure counts
    const resultsDialog = page.getByRole('dialog');
    await expect(resultsDialog.getByText('Bulk Operation Results')).toBeVisible({ timeout: 5_000 });
    await expect(resultsDialog.getByText('1 succeeded', { exact: true })).toBeVisible();
    await expect(resultsDialog.getByText('1 failed', { exact: true })).toBeVisible();
    await expect(resultsDialog.getByText('instance status is not valid for deploy')).toBeVisible();
  });

  test('results dialog close clears selection and hides toolbar', async ({ page }) => {
    // Select two instances
    await page.getByRole('checkbox', { name: /Select bulk-test-alpha/ }).click();
    await page.getByRole('checkbox', { name: /Select bulk-test-beta/ }).click();

    const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
    await expect(toolbar).toBeVisible({ timeout: 5_000 });

    // Execute bulk deploy through confirm dialog
    await toolbar.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByText('Confirm Bulk Deploy')).toBeVisible({ timeout: 5_000 });
    await page.getByRole('dialog').getByRole('button', { name: 'Deploy' }).click();

    // Results dialog appears
    const resultsDialog = page.getByRole('dialog');
    await expect(resultsDialog.getByText('Bulk Operation Results')).toBeVisible({ timeout: 5_000 });

    // Close the results dialog
    await page.getByRole('dialog').getByRole('button', { name: 'Close' }).click();

    // Selection is cleared — toolbar must disappear
    await expect(toolbar).not.toBeVisible({ timeout: 5_000 });

    // Individual checkboxes must be unchecked
    await expect(page.getByRole('checkbox', { name: /Select bulk-test-alpha/ })).not.toBeChecked();
    await expect(page.getByRole('checkbox', { name: /Select bulk-test-beta/ })).not.toBeChecked();
  });
});
