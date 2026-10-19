import { test, expect, request as playwrightRequest } from '@playwright/test';
import type { APIRequestContext, APIResponse, BrowserContext, Page } from '@playwright/test';
import {
  API_BASE,
  E2E_DB_SKIP_REASON,
  apiLogin,
  e2eDbConfigured,
  loginAsAdmin,
  markUserAsSSO,
  uniqueName,
} from './helpers';

/**
 * User role change (#585): PUT /users/:id/role (admin, local users only).
 *
 * A change revokes the access tokens, the refresh tokens and the open
 * WebSocket connections of the user; API keys keep working with the new role.
 * SSO users (409), the own role (403) and an unchanged role (no-op) are the
 * rules around it. The last-admin guard is in last-admin.spec.ts (isolated
 * project: it disables the built-in admin for a moment).
 *
 * Every test creates its own users. The built-in `admin` account is only used
 * as the caller and is never changed.
 */

const USERS_URL = `${API_BASE}/api/v1/users`;
const ME_URL = `${API_BASE}/api/v1/auth/me`;
const REFRESH_URL = `${API_BASE}/api/v1/auth/refresh`;
const AUDIT_URL = `${API_BASE}/api/v1/audit-logs`;
const WS_URL = `${API_BASE.replace(/^http/, 'ws')}/ws`;
const PASSWORD = 'e2e-role-password';

// ---------------------------------------------------------------------------
// Helpers

interface TestUser {
  id: string;
  username: string;
}

function bearer(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}

/** Create a local user through the admin register API. */
async function createUser(
  request: APIRequestContext,
  adminToken: string,
  role: 'user' | 'devops' = 'user',
  prefix = 'e2e-role',
): Promise<TestUser> {
  const username = uniqueName(prefix);
  const res = await request.post(`${API_BASE}/api/v1/auth/register`, {
    headers: bearer(adminToken),
    data: { username, password: PASSWORD, role },
  });
  expect(res.ok(), `register ${username}: ${res.status()}`).toBe(true);
  const body = await res.json();
  expect(body.role).toBe(role);
  return { id: body.id, username };
}

async function deleteUser(request: APIRequestContext, adminToken: string, id: string): Promise<void> {
  const res = await request.delete(`${USERS_URL}/${id}`, { headers: bearer(adminToken) });
  expect([204, 404], `delete user ${id}`).toContain(res.status());
}

async function changeRole(request: APIRequestContext, token: string, id: string, role: string): Promise<APIResponse> {
  return request.put(`${USERS_URL}/${id}/role`, { headers: bearer(token), data: { role } });
}

/** The user row of GET /users (admin). */
async function findUser(
  request: APIRequestContext,
  adminToken: string,
  id: string,
): Promise<{ id: string; role: string; auth_provider: string; disabled: boolean }> {
  const res = await request.get(USERS_URL, { headers: bearer(adminToken) });
  expect(res.status()).toBe(200);
  const users: { id: string; role: string; auth_provider: string; disabled: boolean }[] = await res.json();
  const user = users.find((u) => u.id === id);
  expect(user, `user ${id} not in GET /users`).toBeDefined();
  return user!;
}

interface AuditEntry {
  id: string;
  username: string;
  action: string;
  entity_type: string;
  entity_id: string;
  details: string;
}

/** change_role audit entries of a user (newest first). */
async function roleAuditEntries(request: APIRequestContext, adminToken: string, userId: string): Promise<AuditEntry[]> {
  const res = await request.get(AUDIT_URL, {
    headers: bearer(adminToken),
    params: { action: 'change_role', entity_type: 'user', entity_id: userId, limit: '50' },
  });
  expect(res.status()).toBe(200);
  const body: { data: AuditEntry[] } = await res.json();
  return body.data;
}

/** The audit middleware writes entries asynchronously: poll until `count` entries exist. */
async function waitForRoleAuditEntries(
  request: APIRequestContext,
  adminToken: string,
  userId: string,
  count: number,
): Promise<AuditEntry[]> {
  let entries: AuditEntry[] = [];
  await expect
    .poll(
      async () => {
        entries = await roleAuditEntries(request, adminToken, userId);
        return entries.length;
      },
      { timeout: 10_000, message: `change_role audit entries of ${userId}` },
    )
    .toBe(count);
  return entries;
}

function details(entry: AuditEntry): Record<string, unknown> {
  return JSON.parse(entry.details) as Record<string, unknown>;
}

/**
 * A separate API client for the user under test, so its cookie jar holds
 * only that user's refresh cookie (the `request` fixture logs in as admin).
 */
async function userClient(): Promise<APIRequestContext> {
  return playwrightRequest.newContext();
}

/** Open an app page with the token in localStorage (written once, not by an init script). */
async function openApp(context: BrowserContext, token: string): Promise<Page> {
  const page = await context.newPage();
  await page.goto('/login');
  await page.evaluate((t) => localStorage.setItem('token', t), token);
  await page.goto('/stack-definitions');
  await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({ timeout: 10_000 });
  return page;
}

function mainNav(page: Page) {
  return page.getByRole('navigation', { name: 'Main navigation' });
}

/** Open, close events of the app WebSockets that the pages of a context create. */
interface WsEvent {
  socket: string;
  url: string;
  type: 'open' | 'close';
  at: number;
  code?: number;
  reason?: string;
}

/**
 * Record the open and close events (with the close code) of every WebSocket of
 * the context. Playwright's WebSocket `close` event has no close code, so an
 * init script wraps the WebSocket constructor; the sockets still talk to the
 * real server. Same technique as session-lifecycle.spec.ts.
 */
async function recordWebSockets(context: BrowserContext): Promise<WsEvent[]> {
  const events: WsEvent[] = [];
  await context.exposeFunction('__e2eWsEvent', (event: Omit<WsEvent, 'at'>) => {
    events.push({ ...event, at: Date.now() });
  });
  await context.addInitScript(() => {
    type Report = (event: { socket: string; url: string; type: string; code?: number; reason?: string }) => void;
    const report: Report = (event) => {
      const fn = (globalThis as unknown as { __e2eWsEvent?: Report }).__e2eWsEvent;
      if (fn) void fn(event);
    };
    const Native = globalThis.WebSocket;
    const prefix = Math.random().toString(36).slice(2, 8);
    let seq = 0;
    class TrackedWebSocket extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        seq += 1;
        const socket = `${prefix}-${seq}`;
        const plain = String(url).split('?')[0];
        this.addEventListener('open', () => report({ socket, url: plain, type: 'open' }));
        this.addEventListener('close', (e) => report({ socket, url: plain, type: 'close', code: e.code, reason: e.reason }));
      }
    }
    (globalThis as unknown as { WebSocket: typeof WebSocket }).WebSocket = TrackedWebSocket;
  });
  return events;
}

/** App sockets only (the Vite HMR socket on :3000 is another URL). */
function appSocketEvents(events: WsEvent[]): WsEvent[] {
  return events.filter((e) => e.url === WS_URL);
}

function openAppSockets(events: WsEvent[]): string[] {
  const app = appSocketEvents(events);
  const closed = new Set(app.filter((e) => e.type === 'close').map((e) => e.socket));
  return app.filter((e) => e.type === 'open' && !closed.has(e.socket)).map((e) => e.socket);
}

// ---------------------------------------------------------------------------
// Tests

test.describe('User role change (#585)', () => {
  test('1: an admin changes a local user from user to devops in Admin > Users, and the audit log records change_role', async ({
    page,
    request,
  }) => {
    const adminToken = await apiLogin(request);
    const target = await createUser(request, adminToken);
    try {
      await loginAsAdmin(page);
      await page.goto('/admin/users');
      await expect(page.getByRole('heading', { level: 1, name: 'User Management' })).toBeVisible({ timeout: 10_000 });

      const row = page.getByRole('row').filter({ has: page.getByRole('cell', { name: target.username, exact: true }) });
      await expect(row.getByText('user', { exact: true })).toBeVisible({ timeout: 10_000 });
      await row.getByRole('button', { name: `Edit role of ${target.username}` }).click();

      const dialog = page.getByRole('dialog', { name: 'Edit Role' });
      await expect(dialog).toBeVisible();
      const submit = dialog.getByRole('button', { name: 'Change Role' });
      // Same role: nothing to submit.
      await expect(submit).toBeDisabled();
      await dialog.getByLabel('Role').click();
      await page.getByRole('option', { name: 'devops', exact: true }).click();
      await expect(dialog.getByRole('alert')).toContainText('The role changes from user to devops');
      await expect(dialog.getByRole('alert')).toContainText('The user is signed out and must sign in again');

      const answer = page.waitForResponse(
        (r) => r.request().method() === 'PUT' && r.url() === `${USERS_URL}/${target.id}/role`,
      );
      await submit.click();
      const res = await answer;
      expect(res.status()).toBe(200);
      expect(await res.json()).toEqual({
        id: target.id,
        old_role: 'user',
        new_role: 'devops',
        changed: true,
        message: 'Role changed',
      });
      await expect(dialog).toBeHidden();
      await expect(row.getByText('devops', { exact: true })).toBeVisible();

      // Stored: the role survives a reload and the API agrees.
      await page.reload();
      await expect(row.getByText('devops', { exact: true })).toBeVisible({ timeout: 10_000 });
      expect((await findUser(request, adminToken, target.id)).role).toBe('devops');

      // Audit: one change_role entry by admin with the old and the new role.
      const [entry] = await waitForRoleAuditEntries(request, adminToken, target.id, 1);
      expect(entry).toMatchObject({ action: 'change_role', entity_type: 'user', entity_id: target.id, username: 'admin' });
      expect(details(entry)).toEqual({ old_role: 'user', new_role: 'devops' });
    } finally {
      await deleteUser(request, adminToken, target.id);
    }
  });

  test('2: the old access and refresh tokens get 401, and after a new login the user sees the devops navigation', async ({
    context,
    request,
  }) => {
    const adminToken = await apiLogin(request);
    const target = await createUser(request, adminToken);
    const client = await userClient();
    try {
      // Session of the user: the login token, and a rotated pair from a refresh
      // (proves the refresh cookie worked right before the change).
      const loginToken = await apiLogin(client, target.username, PASSWORD);
      const rotated = await client.post(REFRESH_URL);
      expect(rotated.status(), 'refresh before the change').toBe(200);
      const rotatedToken: string = (await rotated.json()).token;
      for (const token of [loginToken, rotatedToken]) {
        const me = await request.get(ME_URL, { headers: bearer(token) });
        expect(me.status()).toBe(200);
        expect((await me.json()).role).toBe('user');
      }

      // The user's app, before the change: no devops navigation.
      const page = await openApp(context, rotatedToken);
      await expect(mainNav(page).getByRole('link', { name: 'Definitions', exact: true })).toBeVisible();
      await expect(mainNav(page).getByRole('link', { name: 'Audit Log', exact: true })).toHaveCount(0);

      const res = await changeRole(request, adminToken, target.id, 'devops');
      expect(res.status()).toBe(200);
      expect((await res.json()).changed).toBe(true);

      // Every access token issued before the change is refused.
      for (const token of [loginToken, rotatedToken]) {
        const me = await request.get(ME_URL, { headers: bearer(token) });
        expect(me.status(), 'access token issued before the role change').toBe(401);
      }
      // The refresh token is revoked on the server (sent from the jar).
      const refresh = await client.post(REFRESH_URL);
      expect(refresh.status(), 'refresh token issued before the role change').toBe(401);
      expect((await refresh.json()).error).toBe('Invalid refresh token');

      // The open app lands on the login page at its next request.
      await expect(async () => {
        if (!/\/login(\?|$)/.test(page.url())) {
          await mainNav(page).getByRole('link', { name: 'Templates', exact: true }).click({ timeout: 2_000 }).catch(() => {});
        }
        await expect(page).toHaveURL(/\/login(\?|$)/, { timeout: 2_000 });
      }).toPass({ timeout: 20_000 });

      // A new login (through the form) gives a devops token and the devops navigation.
      await page.getByLabel('Username').fill(target.username);
      await page.getByLabel('Password').fill(PASSWORD);
      const login = page.waitForResponse((r) => r.request().method() === 'POST' && r.url() === `${API_BASE}/api/v1/auth/login`);
      await page.getByRole('button', { name: 'Sign In' }).click();
      expect((await login).status()).toBe(200);
      await expect(page).not.toHaveURL(/\/login(\?|$)/, { timeout: 10_000 });
      const auditLink = mainNav(page).getByRole('link', { name: 'Audit Log', exact: true });
      await expect(auditLink).toBeVisible({ timeout: 10_000 });
      const newToken = await page.evaluate(() => localStorage.getItem('token'));
      const me = await request.get(ME_URL, { headers: bearer(newToken!) });
      expect(me.status()).toBe(200);
      expect((await me.json()).role).toBe('devops');

      // The devops page works for the user.
      await auditLink.click();
      await expect(page).toHaveURL(/\/audit-log$/);
      await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({ timeout: 10_000 });
    } finally {
      await client.dispose();
      await deleteUser(request, adminToken, target.id);
    }
  });

  test('3: the open WebSocket of the user closes with 1008 "session revoked" on the role change', async ({
    browser,
    request,
  }) => {
    const adminToken = await apiLogin(request);
    const target = await createUser(request, adminToken);
    const context = await browser.newContext({ baseURL: test.info().project.use.baseURL });
    try {
      const sockets = await recordWebSockets(context);
      const refreshes: { at: number; status: number }[] = [];
      context.on('response', (r) => {
        if (r.request().method() === 'POST' && r.url() === REFRESH_URL) refreshes.push({ at: Date.now(), status: r.status() });
      });

      const token = await apiLogin(context.request, target.username, PASSWORD);
      await openApp(context, token);
      await expect.poll(() => openAppSockets(sockets).length, { timeout: 15_000 }).toBeGreaterThan(0);
      const socket = openAppSockets(sockets)[0];

      const res = await changeRole(request, adminToken, target.id, 'devops');
      expect(res.status()).toBe(200);

      await expect
        .poll(() => sockets.find((e) => e.socket === socket && e.type === 'close'), { timeout: 10_000 })
        .toBeDefined();
      const close = sockets.find((e) => e.socket === socket && e.type === 'close')!;
      expect({ code: close.code, reason: close.reason }).toEqual({ code: 1008, reason: 'session revoked' });

      // After a 1008 the app refreshes before it reconnects; the refresh token
      // is revoked, so it gets 401 and the socket stays closed.
      await expect.poll(() => refreshes.filter((r) => r.at >= close.at).map((r) => r.status), { timeout: 10_000 }).toContain(401);
      await new Promise((resolve) => setTimeout(resolve, 1_000));
      expect(refreshes.filter((r) => r.at >= close.at && r.status === 200)).toEqual([]);
      expect(appSocketEvents(sockets).filter((e) => e.type === 'open' && e.at > close.at)).toEqual([]);
    } finally {
      await context.close();
      await deleteUser(request, adminToken, target.id);
    }
  });

  test('4a: the role of an SSO user cannot be changed (409)', async ({ page, request }) => {
    test.skip(!e2eDbConfigured(), E2E_DB_SKIP_REASON);
    const adminToken = await apiLogin(request);
    const target = await createUser(request, adminToken, 'user', 'e2e-role-sso');
    try {
      await markUserAsSSO(target.username);
      expect((await findUser(request, adminToken, target.id)).auth_provider).toBe('oidc');

      for (const role of ['devops', 'admin', 'user']) {
        const res = await changeRole(request, adminToken, target.id, role);
        expect(res.status(), `SSO user to ${role}`).toBe(409);
        expect(await res.json()).toEqual({ error: 'Role is managed by the identity provider' });
      }
      expect((await findUser(request, adminToken, target.id)).role).toBe('user');
      expect(await roleAuditEntries(request, adminToken, target.id)).toEqual([]);

      // The page shows the role read-only.
      await loginAsAdmin(page);
      await page.goto('/admin/users');
      const row = page.getByRole('row').filter({ has: page.getByRole('cell', { name: target.username, exact: true }) });
      await expect(row.getByText('managed by SSO')).toBeVisible({ timeout: 10_000 });
      await expect(row.getByRole('button', { name: `Edit role of ${target.username}` })).toHaveCount(0);
    } finally {
      await deleteUser(request, adminToken, target.id);
    }
  });

  test('4b: an admin cannot change the own role (403), and the own session stays valid', async ({ request }) => {
    // A fresh admin of this test is the caller, so the built-in admin is never
    // at risk if the guard were missing.
    const adminToken = await apiLogin(request);
    const caller = await createUser(request, adminToken, 'user', 'e2e-role-own');
    try {
      const promote = await changeRole(request, adminToken, caller.id, 'admin');
      expect(promote.status()).toBe(200);
      const callerToken = await apiLogin(request, caller.username, PASSWORD);

      // The path ID in another case is the same account (MySQL compares IDs without case).
      for (const id of [caller.id, caller.id.toUpperCase()]) {
        for (const role of ['user', 'devops', 'admin']) {
          const res = await changeRole(request, callerToken, id, role);
          expect(res.status(), `own role to ${role} (${id})`).toBe(403);
          expect(await res.json()).toEqual({ error: 'Cannot change your own role' });
        }
      }

      expect((await findUser(request, adminToken, caller.id)).role).toBe('admin');
      const me = await request.get(ME_URL, { headers: bearer(callerToken) });
      expect(me.status(), 'a refused change revokes nothing').toBe(200);
      expect((await me.json()).role).toBe('admin');
      expect(await roleAuditEntries(request, adminToken, caller.id)).toHaveLength(1); // the promotion only
    } finally {
      await deleteUser(request, adminToken, caller.id);
    }
  });

  test('4c: an unchanged role is a no-op: 200 with changed=false, and the sessions stay valid', async ({ request }) => {
    const adminToken = await apiLogin(request);
    const target = await createUser(request, adminToken);
    const client = await userClient();
    try {
      const token = await apiLogin(client, target.username, PASSWORD);

      const res = await changeRole(request, adminToken, target.id, 'user');
      expect(res.status()).toBe(200);
      expect(await res.json()).toEqual({
        id: target.id,
        old_role: 'user',
        new_role: 'user',
        changed: false,
        message: 'Role unchanged',
      });

      // Nothing was revoked: the access token and the refresh cookie still work.
      const me = await request.get(ME_URL, { headers: bearer(token) });
      expect(me.status()).toBe(200);
      expect((await me.json()).role).toBe('user');
      const refresh = await client.post(REFRESH_URL);
      expect(refresh.status(), 'refresh after a no-op role change').toBe(200);
      expect((await findUser(request, adminToken, target.id)).role).toBe('user');
    } finally {
      await client.dispose();
      await deleteUser(request, adminToken, target.id);
    }
  });

  // A no-op answers 200 with changed=false. The handler calls
  // middleware.SkipAudit, so it writes no change_role audit entry (#585).
  test('4d: an unchanged role writes no change_role audit entry', async ({ request }) => {
    const adminToken = await apiLogin(request);
    const target = await createUser(request, adminToken);
    try {
      const noop = await changeRole(request, adminToken, target.id, 'user');
      expect((await noop.json()).changed).toBe(false);
      // A real change afterwards: once its entry is there, an entry of the
      // no-op (written earlier, same asynchronous path) would be there too.
      const real = await changeRole(request, adminToken, target.id, 'devops');
      expect((await real.json()).changed).toBe(true);
      await expect
        .poll(async () => (await roleAuditEntries(request, adminToken, target.id)).some((e) => details(e).new_role === 'devops'), {
          timeout: 10_000,
        })
        .toBe(true);
      await new Promise((resolve) => setTimeout(resolve, 500));
      const entries = await roleAuditEntries(request, adminToken, target.id);
      expect(entries.map(details)).toEqual([{ old_role: 'user', new_role: 'devops' }]);
    } finally {
      await deleteUser(request, adminToken, target.id);
    }
  });

  test('6: API keys of the user keep working after a role change and use the new role at once', async ({ request }) => {
    const adminToken = await apiLogin(request);
    const target = await createUser(request, adminToken);
    try {
      const created = await request.post(`${USERS_URL}/${target.id}/api-keys`, {
        headers: bearer(adminToken),
        data: { name: 'e2e-role-change-key', expires_in_days: 1 },
      });
      expect(created.status()).toBe(201);
      const { raw_key: rawKey, id: keyId } = await created.json();
      expect(rawKey).toMatch(/^sk_/);
      const withKey = { 'X-API-Key': rawKey as string };

      // devops-gated routes (RequireDevOps): audit log list and analytics overview.
      const devopsRoutes = [`${AUDIT_URL}?limit=1`, `${API_BASE}/api/v1/analytics/overview`];
      const statuses = async () => Promise.all(devopsRoutes.map(async (url) => (await request.get(url, { headers: withKey })).status()));
      const meRole = async () => {
        const me = await request.get(ME_URL, { headers: withKey });
        expect(me.status()).toBe(200);
        return (await me.json()).role;
      };

      expect(await meRole()).toBe('user');
      expect(await statuses(), 'role user').toEqual([403, 403]);

      expect((await changeRole(request, adminToken, target.id, 'devops')).status()).toBe(200);
      expect(await meRole()).toBe('devops');
      expect(await statuses(), 'role devops').toEqual([200, 200]);
      // Still not admin.
      expect((await request.get(USERS_URL, { headers: withKey })).status()).toBe(403);

      // Back to user: the key follows the stored role in both directions.
      expect((await changeRole(request, adminToken, target.id, 'user')).status()).toBe(200);
      expect(await meRole()).toBe('user');
      expect(await statuses(), 'role user again').toEqual([403, 403]);

      // The role changes kept the key.
      const keys = await request.get(`${USERS_URL}/${target.id}/api-keys`, { headers: bearer(adminToken) });
      expect(keys.status()).toBe(200);
      expect((await keys.json()).map((k: { id: string }) => k.id)).toContain(keyId);
    } finally {
      await deleteUser(request, adminToken, target.id);
    }
  });
});
