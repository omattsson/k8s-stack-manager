import { test, expect, type APIRequestContext, type Locator, type Page, type Route } from '@playwright/test';
import { API_BASE, apiCreateDefinition, apiLogin, ensureDefaultCluster, loginAsDevops, uniqueName } from './helpers';

const mockNotifications = [
  {
    id: 'n1',
    title: 'Deploy successful',
    message: 'Instance my-app deployed',
    type: 'deployment.success',
    is_read: false,
    entity_type: 'stack_instance',
    entity_id: 'inst1',
    created_at: new Date().toISOString(),
  },
  {
    id: 'n2',
    title: 'Instance stopped',
    message: 'Instance backend-svc was stopped',
    type: 'deployment.stopped',
    is_read: true,
    entity_type: 'stack_instance',
    entity_id: 'inst2',
    created_at: new Date(Date.now() - 3600_000).toISOString(),
  },
  {
    id: 'n3',
    title: 'Deploy failed',
    message: 'Instance broken-app failed to deploy',
    type: 'deployment.error',
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

// ---------------------------------------------------------------------------
// Who did the action (#652): "by alice", "by you", "by expiry",
// "by Cleanup policy <name>"; no line without a trigger.

const LONG_POLICY = 'remove-stopped-stacks-after-fourteen-days-without-a-deploy';

/** The user ID in the JWT of a browser request (the signed-in user). */
function userIdOfRequest(route: Route): string {
  const token = (route.request().headers().authorization ?? '').replace(/^Bearer /, '');
  const payload = token.split('.')[1] ?? '';
  return (JSON.parse(Buffer.from(payload, 'base64url').toString('utf8')) as { user_id?: string }).user_id ?? '';
}

/** Mocked notifications with all trigger forms; "by you" needs the ID of the signed-in user. */
function triggerNotifications(selfId: string, policyName = 'nightly') {
  const base = { message: 'Details', is_read: true, created_at: new Date().toISOString() };
  return [
    { ...base, id: 'tr1', title: 'Stopped by alice', type: 'deployment.stopped', trigger: { type: 'user', id: 'other-user', name: 'alice' } },
    { ...base, id: 'tr2', title: 'Created by me', type: 'instance.created', trigger: { type: 'user', id: selfId, name: 'me' } },
    { ...base, id: 'tr3', title: 'Expired stack', type: 'stack.expired', trigger: { type: 'ttl' } },
    { ...base, id: 'tr4', title: 'Cleaned by policy', type: 'clean.completed', trigger: { type: 'cleanup-policy', id: 'p1', name: policyName } },
    { ...base, id: 'tr5', title: 'Old notification', type: 'deployment.success' },
  ];
}

async function mockTriggerNotifications(page: Page, policyName?: string) {
  await page.route('**/api/v1/notifications?*', async (route) => {
    const notifications = triggerNotifications(userIdOfRequest(route), policyName);
    await route.fulfill({ json: { notifications, total: notifications.length, unread_count: 0 } });
  });
  await page.route('**/api/v1/notifications/count', async (route) => {
    await route.fulfill({ json: { unread_count: 0 } });
  });
}

/** Check the "by" line of each mocked notification inside `list`. */
async function expectTriggerLines(list: Locator, policyName = 'nightly') {
  const item = (title: string) => list.getByRole('listitem').filter({ hasText: title });
  await expect(item('Stopped by alice').getByText('by alice', { exact: true })).toBeVisible({ timeout: 10_000 });
  await expect(item('Created by me').getByText('by you', { exact: true })).toBeVisible();
  await expect(item('Expired stack').getByText('by expiry', { exact: true })).toBeVisible();
  await expect(item('Cleaned by policy').getByText(`by Cleanup policy ${policyName}`, { exact: true })).toBeVisible();
  await expect(item('Old notification')).toBeVisible();
  await expect(item('Old notification').getByText(/^by /)).toHaveCount(0);
}

/** The bell popover (the bell is in the drawer on a desktop and in the app bar on a phone). */
async function openBell(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Open notifications' }).click();
  const popover = page.locator('.MuiPopover-paper').filter({ hasText: 'View all notifications' });
  await expect(popover).toBeVisible();
  return popover;
}

test.describe('Notifications: who did the action (mocked list, #652)', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('the bell menu and the page show all trigger forms and no line without a trigger', async ({ page }) => {
    await mockTriggerNotifications(page);
    await page.goto('/notifications');
    await expect(page.getByRole('heading', { level: 1, name: 'Notifications' })).toBeVisible({ timeout: 10_000 });

    await expectTriggerLines(page.locator('main'));
    await expectTriggerLines(await openBell(page));
  });
});

test.describe('Notifications: who did the action at phone width (#652)', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('the "by" line fits the screen on the page and in the bell menu', async ({ page }) => {
    await mockTriggerNotifications(page, LONG_POLICY);
    await page.goto('/notifications');
    await expect(page.getByRole('heading', { level: 1, name: 'Notifications' })).toBeVisible({ timeout: 10_000 });

    const width = page.viewportSize()?.width ?? 390;
    const fits = async (line: Locator) => {
      await expect(line).toBeVisible();
      const box = await line.boundingBox();
      expect(box, 'bounding box of the "by" line').not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(width);
    };

    const main = page.locator('main');
    await expectTriggerLines(main, LONG_POLICY);
    await fits(main.getByText(`by Cleanup policy ${LONG_POLICY}`, { exact: true }));
    await fits(main.getByText('by alice', { exact: true }));
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow, 'horizontal page scroll').toBeLessThanOrEqual(1);

    const popover = await openBell(page);
    await expectTriggerLines(popover, LONG_POLICY);
    await fits(popover.getByText(`by Cleanup policy ${LONG_POLICY}`, { exact: true }));
    await fits(popover.getByText('by you', { exact: true }));
  });
});

// Real backend: the notifier stores the trigger of the request (#652). A
// draft delete notifies at once, so no cluster access is needed.
test.describe('Notifications: who did the action (backend, #652)', () => {
  const PASSWORD = 'e2e-trigger-password';

  interface TestUser {
    id: string;
    username: string;
    token: string;
  }

  interface StoredNotification {
    type: string;
    entity_id?: string;
    trigger?: { type: string; id?: string; name?: string };
  }

  const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
  /** Short username: the namespace is stack-<instance>-<owner>, at most 63 characters. */
  const shortUser = (prefix: string) => `${prefix}${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;

  let adminToken: string;
  let definitionId: string;
  let owner: TestUser;
  let devops: TestUser;
  const created: string[] = [];

  async function createUser(request: APIRequestContext, prefix: string, role: 'user' | 'devops'): Promise<TestUser> {
    const username = shortUser(prefix);
    const res = await request.post(`${API_BASE}/api/v1/auth/register`, {
      headers: auth(adminToken),
      data: { username, password: PASSWORD, role },
    });
    const body = await res.json();
    expect(res.status(), `register ${username}: ${JSON.stringify(body)}`).toBe(201);
    return { id: body.id, username, token: await apiLogin(request, username, PASSWORD) };
  }

  async function createOwnerInstance(request: APIRequestContext): Promise<{ id: string; name: string }> {
    const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(owner.token),
      data: { stack_definition_id: definitionId, name: uniqueName('ntr').slice(0, 40), branch: 'main' },
    });
    const body = await res.json();
    expect(res.ok(), `create instance: ${res.status()} ${JSON.stringify(body)}`).toBe(true);
    created.push(body.id);
    return { id: body.id, name: body.name };
  }

  /** The notification of `type` for the instance, as the owner reads it. */
  async function ownerNotification(request: APIRequestContext, instanceId: string, type: string) {
    let found: StoredNotification | undefined;
    await expect.poll(async () => {
      const res = await request.get(`${API_BASE}/api/v1/notifications?limit=100`, { headers: auth(owner.token) });
      expect(res.ok()).toBe(true);
      const { notifications } = (await res.json()) as { notifications: StoredNotification[] };
      found = notifications.find((n) => n.entity_id === instanceId && n.type === type);
      return Boolean(found);
    }, { timeout: 10_000, message: `${type} notification of ${instanceId}` }).toBe(true);
    return found as StoredNotification;
  }

  async function openAsOwner(page: Page, url: string) {
    await page.addInitScript((t) => {
      localStorage.setItem('token', t);
      localStorage.setItem('setupWizardDismissed', 'true');
    }, owner.token);
    await page.goto(url);
  }

  /** Check the line on the page and in the bell menu for the item that has `message`. */
  async function expectLine(page: Page, message: string, line: string) {
    const pageItem = page.locator('main').getByRole('listitem').filter({ hasText: message });
    await expect(pageItem.getByText(line, { exact: true })).toBeVisible({ timeout: 10_000 });
    const bellItem = (await openBell(page)).getByRole('listitem').filter({ hasText: message });
    await expect(bellItem.getByText(line, { exact: true })).toBeVisible({ timeout: 10_000 });
  }

  test.beforeAll(async ({ request }) => {
    adminToken = await apiLogin(request);
    await ensureDefaultCluster(request, adminToken);
    owner = await createUser(request, 'ntro', 'user');
    devops = await createUser(request, 'ntrd', 'devops');
    // Creating a definition needs devops; instances of it can belong to any user.
    definitionId = await apiCreateDefinition(request, adminToken, uniqueName('ntr-def'));
  });

  test.afterAll(async ({ request }) => {
    if (!adminToken) return;
    for (const id of created) {
      await request.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    }
    if (definitionId) {
      await request.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    }
    for (const u of [owner, devops]) {
      if (u) await request.delete(`${API_BASE}/api/v1/users/${u.id}`, { headers: auth(adminToken) });
    }
  });

  test('the owner sees the devops user who deleted the stack', async ({ page, request }) => {
    const inst = await createOwnerInstance(request);
    const del = await request.delete(`${API_BASE}/api/v1/stack-instances/${inst.id}`, { headers: auth(devops.token) });
    expect(del.status(), 'devops deletes the draft of another user').toBe(204);

    const stored = await ownerNotification(request, inst.id, 'instance.deleted');
    expect(stored.trigger).toMatchObject({ type: 'user', id: devops.id, name: devops.username });

    await openAsOwner(page, '/notifications');
    await expectLine(page, `Stack ${inst.name} has been deleted`, `by ${devops.username}`);
  });

  test('the own create shows "by you"', async ({ page, request }) => {
    const inst = await createOwnerInstance(request);

    const stored = await ownerNotification(request, inst.id, 'instance.created');
    expect(stored.trigger).toMatchObject({ type: 'user', id: owner.id });

    await openAsOwner(page, '/notifications');
    await expectLine(page, `Stack ${inst.name} has been created`, 'by you');
  });
});
