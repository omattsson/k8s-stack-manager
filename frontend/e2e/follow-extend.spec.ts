import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import {
  API_BASE,
  apiCreateDefinition,
  apiLogin,
  e2eDbConfigured,
  runE2eSql,
  uniqueName,
} from './helpers';

// Follow and unfollow (POST/DELETE /stack-instances/:id/follow) and extend
// TTL (POST /stack-instances/:id/extend), issue #591. DB-backed, no cluster
// required:
// - The instance event that reaches followers without a cluster is the
//   direct delete of a draft: the handler reads the followers before the
//   delete and sends "instance.deleted" to the owner and to them
//   (afterInstanceDeleted). Deploy, stop and clean events need a cluster.
// - An instance gets an expiry at create: a TTL (expires_at = now + TTL) or
//   a fixed stop time (expires_at, ttl_minutes 0).
// Fixed stop time and "Custom..." are covered by ttl-stop-time.spec.ts.

const MINUTE = 60_000;
const DAY = 24 * 60 * MINUTE;
const PASSWORD = 'e2e-follow-password';

interface TestUser {
  id: string;
  username: string;
  token: string;
}

interface Instance {
  id: string;
  name: string;
  status: string;
  ttl_minutes?: number;
  expires_at?: string | null;
  following?: boolean;
  follower_count?: number;
}

interface FollowState {
  following: boolean;
  follower_count: number;
}

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

/** A short unique username: the namespace is stack-<instance>-<owner>, at most 63 characters. */
const shortUser = (prefix: string) => `${prefix}${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;

/** Register a user with the role `user` (admin token) and log in. */
async function createUser(request: APIRequestContext, adminToken: string, prefix: string): Promise<TestUser> {
  const username = shortUser(prefix);
  const res = await request.post(`${API_BASE}/api/v1/auth/register`, {
    headers: auth(adminToken),
    data: { username, password: PASSWORD, role: 'user' },
  });
  const body = await res.json();
  expect(res.status(), `register ${username}: ${JSON.stringify(body)}`).toBe(201);
  const token = await apiLogin(request, username, PASSWORD);
  return { id: body.id, username, token };
}

async function createInstance(
  request: APIRequestContext,
  token: string,
  definitionId: string,
  body: Record<string, unknown> = {},
): Promise<Instance> {
  const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: auth(token),
    data: { stack_definition_id: definitionId, name: uniqueName('fx'), branch: 'main', ...body },
  });
  const created = await res.json();
  expect(res.ok(), `create instance: ${res.status()} ${JSON.stringify(created)}`).toBe(true);
  return created;
}

async function getInstance(request: APIRequestContext, token: string, id: string): Promise<Instance> {
  const res = await request.get(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(token) });
  expect(res.ok(), `GET instance ${id}: ${res.status()}`).toBe(true);
  return res.json();
}

async function setFollow(
  request: APIRequestContext,
  token: string,
  id: string,
  follow: boolean,
): Promise<{ status: number; body: FollowState & { error?: string } }> {
  const url = `${API_BASE}/api/v1/stack-instances/${id}/follow`;
  const res = follow
    ? await request.post(url, { headers: auth(token) })
    : await request.delete(url, { headers: auth(token) });
  return { status: res.status(), body: await res.json() };
}

async function deleteInstance(request: APIRequestContext, token: string, id: string): Promise<number> {
  const res = await request.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(token) });
  return res.status();
}

/** POST /extend. `data` undefined sends no body (Content-Length 0). */
async function extend(
  request: APIRequestContext,
  token: string,
  id: string,
  data?: Record<string, unknown>,
): Promise<{ status: number; body: Instance & { error?: string } }> {
  const res = await request.post(`${API_BASE}/api/v1/stack-instances/${id}/extend`, {
    headers: auth(token),
    ...(data === undefined ? {} : { data }),
  });
  return { status: res.status(), body: await res.json() };
}

const ms = (iso: string | null | undefined) => {
  expect(iso, 'expires_at is set').toBeTruthy();
  return new Date(iso as string).getTime();
};

/** A whole minute `offsetMs` from now (the stop time the API stores exactly). */
const wholeMinuteFromNow = (offsetMs: number) => new Date(Math.floor((Date.now() + offsetMs) / MINUTE) * MINUTE);

/** Open the app with `token` in localStorage (no UI login). */
async function openAs(page: Page, token: string, url: string) {
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
  await page.goto(url);
}

// ---------------------------------------------------------------------------
// Read-only database check (instance_followers has no list API).


/** Number of instance_followers rows of the instance (read-only, through runE2eSql). */
async function followerRowsInDb(instanceId: string): Promise<number> {
  if (!/^[0-9a-f-]{36}$/.test(instanceId)) throw new Error(`unexpected instance ID ${instanceId}`);
  const out = await runE2eSql(`SELECT COUNT(*) FROM instance_followers WHERE instance_id='${instanceId}'`);
  const n = parseInt(out.trim(), 10);
  if (!Number.isFinite(n)) throw new Error(`unexpected mysql output ${JSON.stringify(out)}`);
  return n;
}

// ---------------------------------------------------------------------------

test.describe('Follow and extend TTL', () => {
  let adminToken: string;
  let definitionId: string;
  let maxDays: number;
  let owner: TestUser;
  let alice: TestUser;
  let bob: TestUser;
  const created: string[] = [];

  test.beforeAll(async ({ request }) => {
    adminToken = await apiLogin(request);
    const cfg = await (await request.get(`${API_BASE}/api/v1/ui-config`)).json();
    maxDays = cfg.max_instance_ttl_days;
    expect(maxDays, 'MAX_INSTANCE_TTL_DAYS').toBeGreaterThanOrEqual(1);
    owner = await createUser(request, adminToken, 'fxo');
    alice = await createUser(request, adminToken, 'fxa');
    bob = await createUser(request, adminToken, 'fxb');
    // Creating a definition needs devops; instances of it can belong to any user.
    definitionId = await apiCreateDefinition(request, adminToken, uniqueName('fx-def'));
  });

  test.afterAll(async ({ request }) => {
    for (const id of created) await deleteInstance(request, adminToken, id);
    await request.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    for (const u of [owner, alice, bob]) {
      if (u) await request.delete(`${API_BASE}/api/v1/users/${u.id}`, { headers: auth(adminToken) });
    }
  });

  /** A draft owned by `owner`, deleted in afterAll unless the test deletes it. */
  async function ownerInstance(request: APIRequestContext, body: Record<string, unknown> = {}): Promise<Instance> {
    const inst = await createInstance(request, owner.token, definitionId, body);
    created.push(inst.id);
    return inst;
  }

  // Scenario 1
  test('a user who can view an instance follows and unfollows it; both calls are idempotent', async ({ page, request }) => {
    const inst = await ownerInstance(request);

    const before = await getInstance(request, alice.token, inst.id);
    expect(before.following).toBe(false);
    expect(before.follower_count).toBe(0);

    // Follow through the Follow button on the instance page (alice is not the owner).
    await openAs(page, alice.token, `/stack-instances/${inst.id}`);
    await expect(page.getByRole('heading', { level: 1, name: inst.name })).toBeVisible({ timeout: 10_000 });
    const followButton = page.getByRole('button', { name: 'Follow stack' });
    await expect(followButton).toHaveAttribute('aria-pressed', 'false');
    await followButton.click();
    const unfollowButton = page.getByRole('button', { name: 'Unfollow stack' });
    await expect(unfollowButton).toHaveAttribute('aria-pressed', 'true', { timeout: 10_000 });
    await expect(unfollowButton).toHaveText('Following (1)');

    // The state is per caller; the count is shared.
    const asAlice = await getInstance(request, alice.token, inst.id);
    expect(asAlice.following).toBe(true);
    expect(asAlice.follower_count).toBe(1);
    const asOwner = await getInstance(request, owner.token, inst.id);
    expect(asOwner.following).toBe(false);
    expect(asOwner.follower_count).toBe(1);

    // A second follow is no error and adds no row.
    expect(await setFollow(request, alice.token, inst.id, true)).toEqual({
      status: 200, body: { following: true, follower_count: 1 },
    });
    expect(await setFollow(request, bob.token, inst.id, true)).toEqual({
      status: 200, body: { following: true, follower_count: 2 },
    });

    // The page shows the new count after a reload; unfollow through the button.
    await page.reload();
    await expect(page.getByRole('button', { name: 'Unfollow stack' })).toHaveText('Following (2)', { timeout: 10_000 });
    await page.getByRole('button', { name: 'Unfollow stack' }).click();
    await expect(page.getByRole('button', { name: 'Follow stack' })).toHaveText('Follow (1)', { timeout: 10_000 });

    // A second unfollow is no error and removes nothing else.
    expect(await setFollow(request, alice.token, inst.id, false)).toEqual({
      status: 200, body: { following: false, follower_count: 1 },
    });
    expect(await setFollow(request, alice.token, inst.id, false)).toEqual({
      status: 200, body: { following: false, follower_count: 1 },
    });
    const asBob = await getInstance(request, bob.token, inst.id);
    expect(asBob.following).toBe(true);
    expect(asBob.follower_count).toBe(1);

    // Unknown instance: 404.
    const missing = await setFollow(request, alice.token, '00000000-0000-0000-0000-000000000000', true);
    expect(missing.status).toBe(404);
  });

  // Scenarios 2 and 3
  test('a follower gets the notification of an instance delete; follow and unfollow are audited; the delete removes the followers', async ({ page, request }) => {
    const inst = await ownerInstance(request);

    expect((await setFollow(request, alice.token, inst.id, true)).status).toBe(200);
    expect((await setFollow(request, bob.token, inst.id, true)).status).toBe(200);
    expect((await setFollow(request, bob.token, inst.id, false)).body).toEqual({ following: false, follower_count: 1 });
    if (e2eDbConfigured()) expect(await followerRowsInDb(inst.id)).toBe(1);

    // Audit log (admin): follow by alice and bob, unfollow by bob, for this instance.
    await expect.poll(async () => {
      const res = await request.get(
        `${API_BASE}/api/v1/audit-logs?entity_type=stack_instance&entity_id=${inst.id}&limit=100`,
        { headers: auth(adminToken) },
      );
      const { data } = (await res.json()) as { data: { action: string; user_id: string }[] };
      return data
        .filter((e) => e.action === 'follow' || e.action === 'unfollow')
        .map((e) => `${e.action}:${e.user_id}`)
        .sort();
    }, { timeout: 10_000 }).toEqual([`follow:${alice.id}`, `follow:${bob.id}`, `unfollow:${bob.id}`].sort());

    // The owner deletes the draft: no cluster needed, the handler notifies at once.
    expect(await deleteInstance(request, owner.token, inst.id)).toBe(204);

    const deletedNotifications = async (user: TestUser) => {
      const res = await request.get(`${API_BASE}/api/v1/notifications?limit=100`, { headers: auth(user.token) });
      expect(res.ok()).toBe(true);
      const { notifications } = (await res.json()) as {
        notifications: { type: string; entity_id?: string; message: string }[];
      };
      return notifications.filter((n) => n.entity_id === inst.id && n.type === 'instance.deleted');
    };
    await expect.poll(async () => (await deletedNotifications(alice)).length, { timeout: 10_000 }).toBe(1);
    expect((await deletedNotifications(alice))[0].message).toBe(`Stack ${inst.name} has been deleted`);
    expect((await deletedNotifications(owner)).length).toBe(1);
    // bob unfollowed before the delete: no notification.
    expect((await deletedNotifications(bob)).length).toBe(0);

    // The follower sees it on the notifications page.
    await openAs(page, alice.token, '/notifications');
    await expect(page.getByText(`Stack ${inst.name} has been deleted`)).toBeVisible({ timeout: 10_000 });

    // Scenario 3: the follow is gone with the instance. The follow state is
    // no longer reachable through the API (404) ...
    expect((await setFollow(request, alice.token, inst.id, true)).status).toBe(404);
    expect((await setFollow(request, alice.token, inst.id, false)).status).toBe(404);
    // ... and the follower rows are deleted (direct read, when E2E_DB_* are set).
    if (e2eDbConfigured()) {
      expect(await followerRowsInDb(inst.id)).toBe(0);
    } else {
      test.info().annotations.push({ type: 'note', description: 'instance_followers row check skipped: E2E_DB_* not set' });
    }
  });

  // Scenario 4
  test('extend by minutes adds to the current expiry, never shortens it and keeps ttl_minutes', async ({ request }) => {
    // Fixed stop time two days out: +90 minutes must give stop + 90 min (not now + 90 min).
    const stop = wholeMinuteFromNow(2 * DAY);
    const fixed = await ownerInstance(request, { expires_at: stop.toISOString() });
    const r1 = await extend(request, owner.token, fixed.id, { minutes: 90 });
    expect(r1.status, JSON.stringify(r1.body)).toBe(200);
    expect(ms(r1.body.expires_at)).toBe(stop.getTime() + 90 * MINUTE);
    expect(r1.body.ttl_minutes ?? 0).toBe(0);
    const fixedAfter = await getInstance(request, owner.token, fixed.id);
    expect(ms(fixedAfter.expires_at)).toBe(stop.getTime() + 90 * MINUTE);
    expect(fixedAfter.ttl_minutes ?? 0).toBe(0);

    // TTL instance: the expiry moves by exactly N minutes each time, the TTL stays.
    const ttl = await ownerInstance(request, { ttl_minutes: 120 });
    const start = ms((await getInstance(request, owner.token, ttl.id)).expires_at);
    expect(Math.abs(start - (Date.now() + 120 * MINUTE))).toBeLessThan(2 * MINUTE);
    const r2 = await extend(request, owner.token, ttl.id, { minutes: 30 });
    expect(r2.status).toBe(200);
    expect(ms(r2.body.expires_at)).toBe(start + 30 * MINUTE);
    expect(r2.body.ttl_minutes).toBe(120);
    const r3 = await extend(request, owner.token, ttl.id, { minutes: 45 });
    expect(ms(r3.body.expires_at)).toBe(start + 75 * MINUTE);
    expect((await getInstance(request, owner.token, ttl.id)).ttl_minutes).toBe(120);

    // Zero or negative minutes cannot shorten the expiry: 400, nothing changes.
    for (const minutes of [0, -60]) {
      const bad = await extend(request, owner.token, ttl.id, { minutes });
      expect(bad.status).toBe(400);
      expect(bad.body.error).toBe('minutes must be greater than 0');
    }
    expect(ms((await getInstance(request, owner.token, ttl.id)).expires_at)).toBe(start + 75 * MINUTE);

    // Without an expiry there is nothing to extend.
    const noExpiry = await ownerInstance(request, { ttl_minutes: 0 });
    expect(noExpiry.expires_at ?? null).toBeNull();
    const none = await extend(request, owner.token, noExpiry.id, { minutes: 60 });
    expect(none.status).toBe(400);
    expect(none.body.error).toBe('Instance has no expiry; nothing to extend');

    // Only the owner, devops or admin may extend: a follower cannot.
    expect((await setFollow(request, alice.token, ttl.id, true)).status).toBe(200);
    expect((await extend(request, alice.token, ttl.id, { minutes: 60 })).status).toBe(403);
    expect(ms((await getInstance(request, owner.token, ttl.id)).expires_at)).toBe(start + 75 * MINUTE);
  });

  // Scenario 5
  test('extend caps the expiry at now plus MAX_INSTANCE_TTL_DAYS', async ({ request }) => {
    const limitFromNow = () => Date.now() + maxDays * DAY;

    // A stop time 2 hours before the limit, extended by 24 hours, ends at the limit.
    const stop = wholeMinuteFromNow(maxDays * DAY - 2 * 60 * MINUTE);
    const nearLimit = await ownerInstance(request, { expires_at: stop.toISOString() });
    const lo1 = Date.now();
    const r1 = await extend(request, owner.token, nearLimit.id, { minutes: 24 * 60 });
    const hi1 = Date.now();
    expect(r1.status, JSON.stringify(r1.body)).toBe(200);
    const capped1 = ms(r1.body.expires_at);
    expect(capped1).toBeGreaterThanOrEqual(lo1 + maxDays * DAY - 1000);
    expect(capped1).toBeLessThanOrEqual(hi1 + maxDays * DAY + 1000);
    expect(capped1).toBeLessThan(stop.getTime() + 24 * 60 * MINUTE);
    expect(capped1).toBeGreaterThan(stop.getTime());

    // More minutes than the limit (a year) on a short TTL: also at the limit.
    const ttl = await ownerInstance(request, { ttl_minutes: 60 });
    const lo2 = Date.now();
    const r2 = await extend(request, owner.token, ttl.id, { minutes: 365 * 24 * 60 });
    const hi2 = Date.now();
    expect(r2.status).toBe(200);
    const capped2 = ms(r2.body.expires_at);
    expect(capped2).toBeGreaterThanOrEqual(lo2 + maxDays * DAY - 1000);
    expect(capped2).toBeLessThanOrEqual(hi2 + maxDays * DAY + 1000);
    expect(r2.body.ttl_minutes).toBe(60);

    // At the limit, a further extend keeps the expiry at the (moving) limit; it
    // never goes back before the stored expiry.
    const r3 = await extend(request, owner.token, ttl.id, { minutes: 60 });
    expect(r3.status).toBe(200);
    expect(ms(r3.body.expires_at)).toBeGreaterThanOrEqual(capped2);
    expect(ms(r3.body.expires_at)).toBeLessThanOrEqual(limitFromNow() + 1000);
  });

  // Scenario 6
  test('an empty body adds one TTL', async ({ request }) => {
    const ttl = await ownerInstance(request, { ttl_minutes: 180 });
    const start = ms((await getInstance(request, owner.token, ttl.id)).expires_at);

    const r1 = await extend(request, owner.token, ttl.id);
    expect(r1.status, JSON.stringify(r1.body)).toBe(200);
    expect(ms(r1.body.expires_at)).toBe(start + 180 * MINUTE);
    expect(r1.body.ttl_minutes).toBe(180);
    // An empty JSON object is an empty body too.
    const r2 = await extend(request, owner.token, ttl.id, {});
    expect(r2.status).toBe(200);
    expect(ms(r2.body.expires_at)).toBe(start + 360 * MINUTE);
    const after = await getInstance(request, owner.token, ttl.id);
    expect(ms(after.expires_at)).toBe(start + 360 * MINUTE);
    expect(after.ttl_minutes).toBe(180);

    // A fixed stop time has no TTL to add.
    const fixed = await ownerInstance(request, { expires_at: wholeMinuteFromNow(DAY).toISOString() });
    const none = await extend(request, owner.token, fixed.id);
    expect(none.status).toBe(400);
    expect(none.body.error).toBe('No TTL configured for this instance');
  });

  test('the Extend menu adds time and shows the new expiry', async ({ page, request }) => {
    // The instance page shows the Extend menu for a running stack or a fixed
    // stop time; without a cluster only the fixed stop time is possible.
    const stop = wholeMinuteFromNow(2 * DAY);
    const inst = await ownerInstance(request, { expires_at: stop.toISOString() });
    const expected = stop.getTime() + 4 * 60 * MINUTE;

    await openAs(page, owner.token, `/stack-instances/${inst.id}`);
    const chip = page.getByText(/^Stops at /);
    await expect(chip).toBeVisible({ timeout: 10_000 });

    // Text of the new time as the page formats it (browser locale and time zone).
    const when = (t: number) => page.evaluate((v) => new Date(v).toLocaleString([], {
      weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    }), t);
    await expect(chip).toContainText(await when(stop.getTime()));

    await page.getByRole('button', { name: 'Extend', exact: true }).click();
    const menu = page.getByRole('menu', { name: 'Extend' });
    await expect(menu.getByRole('menuitem')).toHaveText(['+1 h', '+4 h', '+24 h', 'Custom...']);
    const extendCall = page.waitForResponse((r) => r.url().endsWith(`/stack-instances/${inst.id}/extend`)
      && r.request().method() === 'POST');
    await menu.getByRole('menuitem', { name: '+4 h' }).click();
    const res = await extendCall;
    expect(res.status()).toBe(200);
    expect(res.request().postDataJSON()).toEqual({ minutes: 240 });

    // Success message with the new expiry, and the chip shows the new stop time.
    const expiryDay = await page.evaluate((v) => new Date(v).toLocaleString([], {
      month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    }), expected);
    await expect(page.getByText(`Extended by 4 h. Expires ${expiryDay}`, { exact: false })).toBeVisible({ timeout: 10_000 });
    await expect(chip).toContainText(await when(expected));

    const after = await getInstance(request, owner.token, inst.id);
    expect(ms(after.expires_at)).toBe(expected);
    expect(after.ttl_minutes ?? 0).toBe(0);
  });
});
