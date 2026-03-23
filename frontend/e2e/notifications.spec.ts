import { test, expect } from '@playwright/test';
import { loginAsDevops } from './helpers';

const mockNotifications = [
  {
    id: 'n1',
    title: 'Deploy successful',
    message: 'Instance my-app deployed',
    type: 'deploy_success',
    is_read: false,
    entity_type: 'stack_instance',
    entity_id: 'inst1',
    created_at: new Date().toISOString(),
  },
  {
    id: 'n2',
    title: 'Instance stopped',
    message: 'Instance backend-svc was stopped',
    type: 'deploy_stopped',
    is_read: true,
    entity_type: 'stack_instance',
    entity_id: 'inst2',
    created_at: new Date(Date.now() - 3600_000).toISOString(),
  },
  {
    id: 'n3',
    title: 'Deploy failed',
    message: 'Instance broken-app failed to deploy',
    type: 'deploy_error',
    is_read: false,
    entity_type: 'stack_instance',
    entity_id: 'inst3',
    created_at: new Date(Date.now() - 7200_000).toISOString(),
  },
];

test.describe('Notifications', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('page loads with heading', async ({ page }) => {
    await page.route('**/api/v1/notifications?*', async (route) => {
      await route.fulfill({ json: { notifications: [], total: 0, unread_count: 0 } });
    });
    await page.route('**/api/v1/notifications/count', async (route) => {
      await route.fulfill({ json: { unread_count: 0 } });
    });

    await page.goto('/notifications');
    await expect(page.getByRole('heading', { level: 1, name: 'Notifications' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('shows empty state when no notifications exist', async ({ page }) => {
    await page.route('**/api/v1/notifications?*', async (route) => {
      await route.fulfill({ json: { notifications: [], total: 0, unread_count: 0 } });
    });
    await page.route('**/api/v1/notifications/count', async (route) => {
      await route.fulfill({ json: { unread_count: 0 } });
    });

    await page.goto('/notifications');
    await expect(page.getByText('No notifications yet')).toBeVisible({ timeout: 10_000 });
  });

  test('shows notification list with titles and messages', async ({ page }) => {
    await page.route('**/api/v1/notifications?*', async (route) => {
      await route.fulfill({
        json: { notifications: mockNotifications, total: 3, unread_count: 2 },
      });
    });
    await page.route('**/api/v1/notifications/count', async (route) => {
      await route.fulfill({ json: { unread_count: 2 } });
    });

    await page.goto('/notifications');
    await expect(page.getByText('Deploy successful')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Instance my-app deployed')).toBeVisible();
    await expect(page.getByText('Instance stopped')).toBeVisible();
    await expect(page.getByText('Deploy failed')).toBeVisible();
  });

  test('shows Unread toggle button', async ({ page }) => {
    await page.route('**/api/v1/notifications?*', async (route) => {
      await route.fulfill({ json: { notifications: [], total: 0, unread_count: 0 } });
    });
    await page.route('**/api/v1/notifications/count', async (route) => {
      await route.fulfill({ json: { unread_count: 0 } });
    });

    await page.goto('/notifications');
    await expect(page.getByRole('button', { name: /Unread/ })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('button', { name: 'All' })).toBeVisible();
  });

  test('Mark all read button visible when there are unread notifications', async ({ page }) => {
    await page.route('**/api/v1/notifications?*', async (route) => {
      await route.fulfill({
        json: { notifications: mockNotifications, total: 3, unread_count: 2 },
      });
    });
    await page.route('**/api/v1/notifications/count', async (route) => {
      await route.fulfill({ json: { unread_count: 2 } });
    });

    await page.goto('/notifications');
    await expect(page.getByRole('button', { name: 'Mark all read' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('shows No unread notifications when switching to Unread filter with none unread', async ({
    page,
  }) => {
    // First request (all) returns notifications
    let requestCount = 0;
    await page.route('**/api/v1/notifications?*', async (route) => {
      const url = route.request().url();
      requestCount++;
      if (url.includes('unread_only=true') || requestCount > 1) {
        await route.fulfill({ json: { notifications: [], total: 0, unread_count: 0 } });
      } else {
        await route.fulfill({
          json: {
            notifications: [{ ...mockNotifications[1] }], // only the read one
            total: 1,
            unread_count: 0,
          },
        });
      }
    });
    await page.route('**/api/v1/notifications/count', async (route) => {
      await route.fulfill({ json: { unread_count: 0 } });
    });

    await page.goto('/notifications');
    await expect(page.getByRole('heading', { level: 1, name: 'Notifications' })).toBeVisible({
      timeout: 10_000,
    });

    // Click Unread filter
    await page.getByRole('button', { name: /Unread/ }).click();
    await expect(page.getByText('No unread notifications')).toBeVisible({ timeout: 10_000 });
  });

  test('clicking a notification calls mark-as-read endpoint', async ({ page }) => {
    const unreadNotification = {
      id: 'n-unread-1',
      title: 'New deploy',
      message: 'Something deployed',
      type: 'deploy_success',
      is_read: false,
      entity_type: 'stack_instance',
      entity_id: 'inst-click',
      created_at: new Date().toISOString(),
    };

    await page.route('**/api/v1/notifications?*', async (route) => {
      await route.fulfill({
        json: { notifications: [unreadNotification], total: 1, unread_count: 1 },
      });
    });
    await page.route('**/api/v1/notifications/count', async (route) => {
      await route.fulfill({ json: { unread_count: 1 } });
    });

    let markReadCalled = false;
    await page.route('**/api/v1/notifications/n-unread-1/read', async (route) => {
      markReadCalled = true;
      await route.fulfill({ status: 200, json: {} });
    });

    // Also mock the navigation target so the page doesn't fail
    await page.route('**/api/v1/stack-instances/inst-click', async (route) => {
      await route.fulfill({
        status: 200,
        json: { id: 'inst-click', name: 'test', status: 'stopped' },
      });
    });

    await page.goto('/notifications');
    await expect(page.getByText('New deploy')).toBeVisible({ timeout: 10_000 });

    await page.getByText('New deploy').click();

    // The mark-as-read call should have been made
    await expect(async () => {
      expect(markReadCalled).toBe(true);
    }).toPass({ timeout: 5_000 });
  });
});
