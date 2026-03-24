import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate, instantiateTemplate } from './helpers';

const mockInstances = [
  {
    id: 'inst-001',
    name: 'frontend-app-dev',
    branch: 'main',
    namespace: 'stack-frontend-app-dev-alice',
    status: 'running',
    owner: 'alice',
    cluster_id: '',
    definition_id: 'def-001',
    created_at: '2025-06-01T00:00:00Z',
    updated_at: '2025-06-01T00:00:00Z',
  },
  {
    id: 'inst-002',
    name: 'frontend-app-staging',
    branch: 'release/1.0',
    namespace: 'stack-frontend-app-staging-bob',
    status: 'running',
    owner: 'bob',
    cluster_id: '',
    definition_id: 'def-001',
    created_at: '2025-06-02T00:00:00Z',
    updated_at: '2025-06-02T00:00:00Z',
  },
];

const mockCompareResult = {
  left: {
    name: 'frontend-app-dev',
    definition_name: 'frontend-def',
    branch: 'main',
    owner: 'alice',
  },
  right: {
    name: 'frontend-app-staging',
    definition_name: 'frontend-def',
    branch: 'release/1.0',
    owner: 'bob',
  },
  charts: [
    {
      chart_name: 'nginx',
      has_differences: true,
      left_values: 'replicas: 1\nimage: nginx:latest',
      right_values: 'replicas: 3\nimage: nginx:1.25',
    },
    {
      chart_name: 'redis',
      has_differences: false,
      left_values: 'maxmemory: 256mb',
      right_values: 'maxmemory: 256mb',
    },
  ],
};

const mockCompareIdentical = {
  left: {
    name: 'frontend-app-dev',
    definition_name: 'frontend-def',
    branch: 'main',
    owner: 'alice',
  },
  right: {
    name: 'frontend-app-staging',
    definition_name: 'frontend-def',
    branch: 'main',
    owner: 'alice',
  },
  charts: [],
};

/**
 * Mock the instance list and compare API endpoints for deterministic tests.
 */
async function mockCompareAPIs(
  page: import('@playwright/test').Page,
  compareResult = mockCompareResult,
) {
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

  // Also mock the recent endpoint so Dashboard doesn't interfere
  await page.route('**/api/v1/stack-instances/recent', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    }),
  );

  await page.route('**/api/v1/stack-instances/compare*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(compareResult),
    }),
  );

  // Mock favorites and clusters so Dashboard loads cleanly
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
}

test.describe('Stack Instance Comparison', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('compare page loads with correct heading', async ({ page }) => {
    await mockCompareAPIs(page);
    await page.goto('/stack-instances/compare');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });
  });

  test('two instance selectors are visible', async ({ page }) => {
    await mockCompareAPIs(page);
    await page.goto('/stack-instances/compare');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    await expect(page.getByLabel('Left instance')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByLabel('Right instance')).toBeVisible({ timeout: 10_000 });
  });

  test('compare button is present and initially disabled', async ({ page }) => {
    await mockCompareAPIs(page);
    await page.goto('/stack-instances/compare');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    const compareButton = page.getByRole('button', { name: 'Compare' });
    await expect(compareButton).toBeVisible();
    await expect(compareButton).toBeDisabled();
  });

  test('info message shown when no comparison started', async ({ page }) => {
    await mockCompareAPIs(page);
    await page.goto('/stack-instances/compare');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    await expect(
      page.getByText('Select two instances above and click Compare to see their differences.'),
    ).toBeVisible();
  });

  test('compare button from dashboard navigates to compare page', async ({ page }) => {
    await mockCompareAPIs(page);
    await page.goto('/');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    await page.getByRole('button', { name: 'Compare' }).click();
    await expect(page).toHaveURL(/\/stack-instances\/compare/, { timeout: 10_000 });
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });
  });

  test('shows comparison results with chart tabs', async ({ page }) => {
    await mockCompareAPIs(page, mockCompareResult);
    await page.goto('/stack-instances/compare?left=inst-001&right=inst-002');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    // Wait for comparison results to load (auto-triggered by URL params)
    await expect(page.getByRole('heading', { name: 'frontend-app-dev' })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('heading', { name: 'frontend-app-staging' })).toBeVisible();

    // Verify chart tabs are rendered
    await expect(page.getByRole('tab', { name: /nginx/ })).toBeVisible();
    await expect(page.getByRole('tab', { name: /redis/ })).toBeVisible();
  });

  test('shows "No differences" chip for identical chart values', async ({ page }) => {
    await mockCompareAPIs(page, mockCompareResult);
    await page.goto('/stack-instances/compare?left=inst-001&right=inst-002');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    // Wait for results
    await expect(page.getByRole('tab', { name: /redis/ })).toBeVisible({ timeout: 10_000 });

    // Click the redis tab (which has no differences)
    await page.getByRole('tab', { name: /redis/ }).click();

    // Verify "No differences" chip is shown
    await expect(page.getByText('No differences')).toBeVisible({ timeout: 5_000 });
  });

  test('shows "No charts found" when comparison has empty charts', async ({ page }) => {
    await mockCompareAPIs(page, mockCompareIdentical);
    await page.goto('/stack-instances/compare?left=inst-001&right=inst-002');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    await expect(page.getByText('No charts found for comparison.')).toBeVisible({
      timeout: 10_000,
    });
  });

  test('warning shown when same instance selected for both sides', async ({ page }) => {
    await mockCompareAPIs(page);
    await page.goto('/stack-instances/compare');
    await expect(
      page.getByRole('heading', { level: 1, name: 'Compare Stack Instances' }),
    ).toBeVisible({ timeout: 10_000 });

    // Select same instance for left
    const leftInput = page.getByLabel('Left instance');
    await leftInput.click();
    await page.getByRole('option', { name: /frontend-app-dev/ }).click();

    // Select same instance for right
    const rightInput = page.getByLabel('Right instance');
    await rightInput.click();
    await page.getByRole('option', { name: /frontend-app-dev/ }).click();

    // Warning should appear
    await expect(
      page.getByText('Please select two different instances to compare.'),
    ).toBeVisible({ timeout: 5_000 });

    // Compare button should remain disabled
    await expect(page.getByRole('button', { name: 'Compare' })).toBeDisabled();
  });
});
