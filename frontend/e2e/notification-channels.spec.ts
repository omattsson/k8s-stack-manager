import { test, expect, APIRequestContext, Page } from '@playwright/test';
import { API_BASE, apiCreateDefinition, apiLogin, ensureDefaultCluster, loginAsUser, uniqueName } from './helpers';
import { STUB_HTTPS_URL, STUB_TLS_TRUSTED, expectedSignature, stubHealth, stubRecords } from './stub/client';

/**
 * Notification channels admin page (#592): /admin/notification-channels
 * (devops) and /api/v1/admin/notification-channels.
 *
 * The backend sends only to https:// webhooks and checks the certificate. The
 * test stub (e2e/stub/server.ts) receives them on https://127.0.0.1:18443 when
 * the backend trusts its certificate: `make test-e2e` on Linux (E2E_STUB_TLS=1,
 * SSL_CERT_DIR). Elsewhere the tests that need a successful delivery skip; the
 * failure paths use a closed port and run everywhere.
 *
 * A real event: instance.created fires when an instance is created (no cluster
 * needed). Each channel filters on the unique name of its own instance, so
 * parallel specs do not reach it.
 */

const PASSWORD = 'e2e-test-password';
/** Nothing listens here: a delivery fails with "Connection failed". */
const CLOSED_URL = 'https://127.0.0.1:18444/webhook/closed';
const TLS_SKIP = 'Needs a backend that trusts the e2e stub certificate (make test-e2e on Linux sets E2E_STUB_TLS=1).';

interface Actor {
  id: string;
  username: string;
  token: string;
}

let api: APIRequestContext;
let adminToken: string;
let devops: Actor;
let definitionId: string;
let tlsReady = false;
const channelIds: string[] = [];
const instanceIds: string[] = [];

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const CHANNELS = `${API_BASE}/api/v1/admin/notification-channels`;

async function registerDevops(): Promise<Actor> {
  const username = uniqueName('e2e-nc-devops');
  const res = await api.post(`${API_BASE}/api/v1/auth/register`, {
    headers: auth(adminToken),
    data: { username, password: PASSWORD, role: 'devops' },
  });
  expect(res.status(), `register ${username}: ${await res.text()}`).toBe(201);
  const { id } = await res.json();
  return { id, username, token: await apiLogin(api, username, PASSWORD) };
}

/** Create a channel through the API and subscribe it to the event types. */
async function createChannel(data: Record<string, unknown>, eventTypes: string[] = []): Promise<string> {
  const res = await api.post(CHANNELS, { headers: auth(devops.token), data });
  expect(res.status(), `create channel: ${await res.text()}`).toBe(201);
  const { id } = await res.json();
  channelIds.push(id);
  if (eventTypes.length > 0) {
    const sub = await api.put(`${CHANNELS}/${id}/subscriptions`, { headers: auth(devops.token), data: { event_types: eventTypes } });
    expect(sub.ok(), `subscriptions: ${await sub.text()}`).toBe(true);
  }
  return id;
}

async function createInstance(name: string): Promise<string> {
  const res = await api.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: auth(adminToken),
    data: { stack_definition_id: definitionId, name, branch: 'main' },
  });
  expect(res.status(), `create instance: ${await res.text()}`).toBe(201);
  const { id } = await res.json();
  instanceIds.push(id);
  return id;
}

interface DeliveryLog {
  event_type: string;
  status: string;
  status_code: number;
  error_message: string;
}

/** Wait until the channel has a delivery log of the event type, and return it. */
async function waitForDelivery(channelId: string, eventType: string): Promise<DeliveryLog> {
  let found: DeliveryLog | undefined;
  await expect.poll(async () => {
    const res = await api.get(`${CHANNELS}/${channelId}/delivery-logs?limit=20`, { headers: auth(devops.token) });
    const logs = ((await res.json()).logs ?? []) as DeliveryLog[];
    found = logs.find((l) => l.event_type === eventType);
    return !!found;
  }, { timeout: 45_000, message: `delivery log ${eventType}` }).toBe(true);
  return found!;
}

async function openPage(page: Page) {
  await page.addInitScript((t) => localStorage.setItem('token', t), devops.token);
  await page.goto('/admin/notification-channels');
  await expect(page.getByRole('heading', { name: 'Notification Channels' })).toBeVisible();
}

const rowOf = (page: Page, name: string) => page.getByRole('row').filter({ hasText: name });

test.describe('Notification channels', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);
    devops = await registerDevops();
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-nc-def'));
    const httpsUp = !!(await stubHealth(api))?.https;
    // In CI the HTTPS tests must run: a missing stub is an error, not a skip.
    if (process.env.CI && STUB_TLS_TRUSTED && !httpsUp) {
      throw new Error('E2E_STUB_TLS=1, but the HTTPS stub does not run (see the stub warning and backend/tmp/e2e-stub/openssl.log).');
    }
    tlsReady = STUB_TLS_TRUSTED && httpsUp;
  });

  test.afterAll(async () => {
    if (!api) return;
    for (const id of channelIds) await api.delete(`${CHANNELS}/${id}`, { headers: auth(adminToken) });
    for (const id of instanceIds) await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    if (devops) await api.delete(`${API_BASE}/api/v1/users/${devops.id}`, { headers: auth(adminToken) });
    await api.dispose();
  });

  test('create a channel with subscriptions, edit it and delete it', async ({ page }) => {
    const name = uniqueName('e2e-nc-crud');
    await openPage(page);

    await page.getByRole('button', { name: 'Create Channel' }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Notification Channel' });
    await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill(name);
    // The client checks the scheme before the server does.
    await dialog.getByLabel('Webhook URL').fill('http://hooks.example.com/x');
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog.getByText('Webhook URL must start with https://')).toBeVisible();
    await dialog.getByLabel('Webhook URL').fill(CLOSED_URL);
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(page.getByText('Channel created')).toBeVisible();
    const row = rowOf(page, name);
    await expect(row).toBeVisible();
    await expect(row.getByTestId(/^channel-filters-/)).toHaveText('All instances');
    const id = (await row.getByTestId(/^channel-filters-/).getAttribute('data-testid'))!.replace('channel-filters-', '');
    channelIds.push(id);

    // Subscriptions: the count chip opens the dialog. The channel has no
    // filter and a closed URL, so it takes events that no spec fires: a failed
    // delivery would delay the deliveries of the other specs.
    await row.getByRole('button', { name: '0', exact: true }).click();
    const subs = page.getByRole('dialog', { name: `Subscriptions: ${name}` });
    await subs.getByRole('checkbox', { name: 'quota.warning' }).check();
    await subs.getByRole('checkbox', { name: 'secret.expiring' }).check();
    await subs.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByText('Subscriptions updated')).toBeVisible();
    await expect(row.getByRole('button', { name: '2', exact: true })).toBeVisible();
    const stored = await api.get(`${CHANNELS}/${id}/subscriptions`, { headers: auth(devops.token) });
    expect(JSON.stringify(await stored.json())).toContain('quota.warning');

    // Edit: rename.
    const renamed = `${name}-renamed`;
    await page.getByRole('button', { name: `Edit ${name}` }).click();
    const edit = page.getByRole('dialog', { name: 'Edit Channel' });
    await edit.getByRole('textbox', { name: 'Name', exact: true }).fill(renamed);
    await edit.getByRole('button', { name: 'Update' }).click();
    await expect(page.getByText('Channel updated')).toBeVisible();
    await expect(rowOf(page, renamed)).toBeVisible();

    // Delete.
    await page.getByRole('button', { name: `Delete ${renamed}` }).click();
    const confirm = page.getByRole('dialog', { name: 'Delete Channel' });
    await confirm.getByRole('button', { name: 'Delete' }).click();
    await expect(page.getByText('Channel deleted')).toBeVisible();
    await expect(rowOf(page, renamed)).toHaveCount(0);
    expect((await api.get(`${CHANNELS}/${id}`, { headers: auth(devops.token) })).status()).toBe(404);
  });

  test('filters: a bad glob gives 400, unknown IDs give warnings', async ({ page }) => {
    // API: unknown IDs are stored, with a warning for each.
    const res = await api.post(CHANNELS, {
      headers: auth(devops.token),
      data: {
        name: uniqueName('e2e-nc-warn'),
        webhook_url: CLOSED_URL,
        filters: { owner_ids: ['no-such-user'], definition_ids: ['no-such-def'], cluster_ids: ['no-such-cluster'] },
      },
    });
    const body = await res.json();
    expect(res.status(), JSON.stringify(body)).toBe(201);
    channelIds.push(body.id);
    expect(body.warnings).toEqual(expect.arrayContaining([
      expect.stringContaining('owner_ids: user no-such-user does not exist'),
      expect.stringContaining('definition_ids: stack definition no-such-def does not exist'),
      expect.stringContaining('cluster_ids: cluster no-such-cluster does not exist'),
    ]));

    // API: a bad glob is refused.
    const bad = await api.post(CHANNELS, {
      headers: auth(devops.token),
      data: { name: uniqueName('e2e-nc-glob'), webhook_url: CLOSED_URL, filters: { instance_name_patterns: ['['] } },
    });
    expect(bad.status(), await bad.text()).toBe(400);
    expect((await bad.json()).error).toBe('Invalid filters: instance_name_patterns: "[" is not a valid pattern');

    // UI: the dialog shows the server message for the bad glob.
    await openPage(page);
    await page.getByRole('button', { name: 'Create Channel' }).click();
    const dialog = page.getByRole('dialog', { name: 'Create Notification Channel' });
    await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill(uniqueName('e2e-nc-glob-ui'));
    await dialog.getByLabel('Webhook URL').fill(CLOSED_URL);
    const patterns = dialog.getByLabel('Instance name patterns');
    await patterns.fill('[');
    await patterns.press('Enter');
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog.getByText('Invalid filters: instance_name_patterns: "[" is not a valid pattern')).toBeVisible();

    // UI: the warnings of a save show as a warning.
    await page.route(/\/api\/v1\/admin\/notification-channels$/, async (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      const resp = await route.fetch();
      const created = await resp.json();
      if (created.id) channelIds.push(created.id);
      return route.fulfill({ response: resp, json: { ...created, warnings: ['owner_ids: user ghost does not exist'] } });
    });
    await patterns.focus();
    await page.keyboard.press('Backspace');
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(page.getByText('Saved with filter warnings: owner_ids: user ghost does not exist')).toBeVisible();
  });

  test('an unknown format gives 400', async () => {
    const res = await api.post(CHANNELS, {
      headers: auth(devops.token),
      data: { name: uniqueName('e2e-nc-fmt'), webhook_url: CLOSED_URL, format: 'slack' },
    });
    expect(res.status(), await res.text()).toBe(400);
    expect((await res.json()).error).toBe('Invalid format: format must be one of: generic, teams');
  });

  test('a failed test send and a failed event show in the delivery logs', async ({ page }) => {
    const instanceName = uniqueName('e2e-nc-fail');
    const name = uniqueName('e2e-nc-closed');
    const id = await createChannel(
      { name, webhook_url: CLOSED_URL, filters: { instance_name_patterns: [instanceName] } },
      ['instance.created'],
    );
    await createInstance(instanceName);
    const event = await waitForDelivery(id, 'instance.created');
    expect(event.status).toBe('failed');
    // The cause only: the webhook URL (it can hold a token) is never in the log.
    expect(event.error_message).toContain('connection refused');
    expect(event.error_message).not.toContain('https://');
    expect(event.error_message).not.toContain('/webhook/closed');

    await openPage(page);
    await page.getByRole('button', { name: `Test ${name}` }).click();
    await expect(page.getByRole('alert').filter({ hasText: 'Connection failed' })).toBeVisible();

    await rowOf(page, name).getByRole('button', { name: 'Expand delivery logs' }).click();
    await expect(page.getByText('Delivery Logs (last 20)')).toBeVisible();
    await expect(page.getByRole('row', { name: /^test failed / })).toBeVisible();
    await expect(page.getByRole('row', { name: /^instance\.created failed / })).toBeVisible();
  });

  test('a test send reaches the webhook with the signature of the secret', async ({ page }) => {
    test.skip(!tlsReady, TLS_SKIP);
    const key = uniqueName('sig');
    const secret = uniqueName('s3cret');
    const name = uniqueName('e2e-nc-sig');
    await createChannel({ name, webhook_url: `${STUB_HTTPS_URL}/webhook/${key}`, secret });

    await openPage(page);
    await page.getByRole('button', { name: `Test ${name}` }).click();
    await expect(page.getByRole('alert').filter({ hasText: 'Test notification sent successfully' })).toBeVisible();

    const recs = await stubRecords(api, `/webhook/${key}`);
    expect(recs).toHaveLength(1);
    expect(recs[0].headers['x-stackmanager-event']).toBe('test');
    expect(recs[0].headers['x-stackmanager-signature']).toBe(expectedSignature(recs[0].body, secret));
    expect(JSON.parse(recs[0].body).event_type).toBe('test');
  });

  test('format teams sends an Adaptive Card envelope', async () => {
    test.skip(!tlsReady, TLS_SKIP);
    const key = uniqueName('teams');
    const id = await createChannel({ name: uniqueName('e2e-nc-teams'), webhook_url: `${STUB_HTTPS_URL}/webhook/${key}`, format: 'teams' });
    const res = await api.post(`${CHANNELS}/${id}/test`, { headers: auth(devops.token) });
    expect(await res.json()).toMatchObject({ success: true });

    const recs = await stubRecords(api, `/webhook/${key}`);
    expect(recs).toHaveLength(1);
    const card = JSON.parse(recs[0].body);
    expect(card.type).toBe('message');
    expect(card.attachments[0].contentType).toBe('application/vnd.microsoft.card.adaptive');
    expect(card.attachments[0].content.type).toBe('AdaptiveCard');
    // No signature without a secret.
    expect(recs[0].headers['x-stackmanager-signature']).toBeUndefined();
  });

  test('the delivery logs show the test send and a real event', async ({ page }) => {
    test.skip(!tlsReady, TLS_SKIP);
    const key = uniqueName('event');
    const instanceName = uniqueName('e2e-nc-event');
    const name = uniqueName('e2e-nc-event-ch');
    const id = await createChannel(
      { name, webhook_url: `${STUB_HTTPS_URL}/webhook/${key}`, filters: { instance_name_patterns: [instanceName] } },
      ['instance.created'],
    );
    await createInstance(instanceName);
    // Another instance (no match of the filter) does not reach the channel.
    await createInstance(uniqueName('e2e-nc-other'));
    const event = await waitForDelivery(id, 'instance.created');
    expect(event.status).toBe('success');
    expect(event.status_code).toBe(200);

    const recs = (await stubRecords(api, `/webhook/${key}`)).filter((r) => r.headers['x-stackmanager-event'] === 'instance.created');
    expect(recs).toHaveLength(1);
    expect(JSON.parse(recs[0].body).event_type).toBe('instance.created');
    expect(recs[0].body).toContain(instanceName);

    await openPage(page);
    await page.getByRole('button', { name: `Test ${name}` }).click();
    await expect(page.getByRole('alert').filter({ hasText: 'Test notification sent successfully' })).toBeVisible();
    await rowOf(page, name).getByRole('button', { name: 'Expand delivery logs' }).click();
    await expect(page.getByRole('cell', { name: 'test', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'instance.created', exact: true })).toBeVisible();
    // The log row only: the outer row of the channel holds the whole log table.
    await expect(page.getByRole('row', { name: /^instance\.created success 200 / })).toBeVisible();
  });

  test('a user without the devops role cannot open the page or the API', async ({ page }) => {
    await loginAsUser(page);
    await page.goto('/admin/notification-channels');
    await expect(page.getByText('You do not have permission to access this page.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create Channel' })).toHaveCount(0);

    const userToken = await page.evaluate(() => localStorage.getItem('token'));
    const res = await api.get(CHANNELS, { headers: auth(userToken!) });
    expect(res.status()).toBe(403);
  });
});
