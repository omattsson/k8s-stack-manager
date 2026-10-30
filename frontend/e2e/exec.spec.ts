import { test, expect, APIRequestContext, Page } from '@playwright/test';
import {
  API_BASE,
  E2E_DB_SKIP_REASON,
  apiAddChart,
  apiCreateDefinition,
  apiLogin,
  e2eDbConfigured,
  loginAsAdmin,
  loginAsUser,
  runE2eSql,
  tokenFromPage,
  uniqueName,
} from './helpers';

/**
 * Exec in a stack container (#723), the part that needs no cluster.
 * The e2e backend runs with STACK_EXEC_ENABLED=true and
 * STACK_EXEC_ALLOWED_CONTAINERS=web (Makefile, e2e_start_backend). Every check
 * of the route that runs before the WebSocket upgrade answers a plain JSON
 * error, so a normal HTTP request is enough. The command itself needs a
 * cluster: exec-tunnel.spec.ts (E2E_KUBECONFIG).
 *
 * The spec registers its own cluster (so no other spec sees the exec switch),
 * and every check that needs a deployed instance sets the row directly in the
 * database (E2E_DB_*) and puts it back.
 */

test.describe.configure({ mode: 'serial' });

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

let api: APIRequestContext;
let adminToken: string;
let adminId: string;
let clusterId = '';
let clusterName = '';
let definitionId = '';
let instanceId = '';
let instanceName = '';

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const execPath = (id: string, query = 'chart=e2e-exec-chart') => `${API_BASE}/api/v1/stack-instances/${id}/exec?${query}`;

async function setClusterSwitch(allow: boolean) {
  const res = await api.put(`${API_BASE}/api/v1/clusters/${clusterId}`, {
    headers: auth(adminToken),
    data: { allow_exec: allow },
  });
  expect(res.ok(), `update cluster: ${res.status()} ${await res.text()}`).toBe(true);
  expect((await res.json()).allow_exec).toBe(allow);
}

test.describe('Exec (no cluster)', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    const me = await (await api.get(`${API_BASE}/api/v1/auth/me`, { headers: auth(adminToken) })).json();
    adminId = me.id;

    clusterName = uniqueName('e2e-exec-cluster');
    const clusterRes = await api.post(`${API_BASE}/api/v1/clusters`, {
      headers: auth(adminToken),
      data: {
        name: clusterName,
        api_server_url: 'https://e2e-exec.example.com:6443',
        kubeconfig_path: '/tmp/test-kubeconfig',
        is_default: false,
      },
    });
    expect(clusterRes.status(), await clusterRes.text()).toBe(201);
    const cluster = await clusterRes.json();
    clusterId = cluster.id;
    expect(cluster.allow_exec, 'allow_exec defaults to off').toBe(false);

    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-exec-def'));
    await apiAddChart(api, adminToken, definitionId, { chart_name: 'e2e-exec-chart' });
    instanceName = uniqueName('e2e-exec');
    // The instance stays a draft: no deploy.
    const res = await api.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(adminToken),
      data: { stack_definition_id: definitionId, name: instanceName, branch: 'main', cluster_id: clusterId },
    });
    expect(res.status(), await res.text()).toBe(201);
    instanceId = (await res.json()).id;
  });

  test.afterAll(async () => {
    if (!api) return;
    if (instanceId) await api.delete(`${API_BASE}/api/v1/stack-instances/${instanceId}`, { headers: auth(adminToken) });
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    if (clusterId) await api.delete(`${API_BASE}/api/v1/clusters/${clusterId}`, { headers: auth(adminToken) });
    await api.dispose();
  });

  test('ui-config reports the feature as enabled', async () => {
    const res = await api.get(`${API_BASE}/api/v1/ui-config`);
    expect(res.ok()).toBe(true);
    expect((await res.json()).exec_enabled).toBe(true);
  });

  test('a request without credentials is 401', async () => {
    expect((await api.get(execPath(instanceId))).status()).toBe(401);
  });

  test('?token= alone does not authenticate', async () => {
    const res = await api.get(execPath(instanceId, `chart=e2e-exec-chart&token=${adminToken}`));
    expect(res.status()).toBe(401);
  });

  test('a cluster without the switch is 403 cluster_exec_disabled', async () => {
    const res = await api.get(execPath(instanceId), { headers: auth(adminToken) });
    expect(res.status()).toBe(403);
    expect((await res.json()).code).toBe('cluster_exec_disabled');
  });

  test('an API key is refused by default (403 api_key_not_allowed)', async () => {
    const created = await api.post(`${API_BASE}/api/v1/users/${adminId}/api-keys`, {
      headers: auth(adminToken),
      data: { name: uniqueName('e2e-exec-key'), expires_in_days: 1 },
    });
    expect(created.ok(), `create key: ${created.status()}`).toBe(true);
    const key = await created.json();
    try {
      const res = await api.get(execPath(instanceId), { headers: { 'X-API-Key': key.raw_key } });
      expect(res.status()).toBe(403);
      expect((await res.json()).code).toBe('api_key_not_allowed');
    } finally {
      await api.delete(`${API_BASE}/api/v1/users/${adminId}/api-keys/${key.id}`, { headers: auth(adminToken) });
    }
  });

  test.describe('with the cluster switch on', () => {
    test.beforeAll(async () => {
      await setClusterSwitch(true);
    });

    test('another user gets 403 forbidden', async ({ page }) => {
      await loginAsUser(page);
      await page.goto('/');
      const userToken = await tokenFromPage(page);
      const res = await page.request.get(execPath(instanceId), { headers: auth(userToken) });
      expect(res.status()).toBe(403);
      expect((await res.json()).code).toBe('forbidden');
    });

    test('an instance that is not deployed is 409 instance_not_deployed', async () => {
      const res = await api.get(execPath(instanceId), { headers: auth(adminToken) });
      expect(res.status()).toBe(409);
      expect((await res.json()).code).toBe('instance_not_deployed');
    });

    for (const [name, query, code] of [
      ['a missing chart', 'container=web', 'invalid_chart'],
      ['a chart that is not a release name', 'chart=Bad_Name', 'invalid_chart'],
      ['a chart longer than 53 characters', `chart=${'a'.repeat(54)}`, 'invalid_chart'],
      ['a container that is not a DNS label', 'chart=e2e-exec-chart&container=Bad_Name', 'invalid_container'],
      ['an empty container', 'chart=e2e-exec-chart&container=', 'invalid_container'],
    ] as const) {
      test(`${name} is 400 ${code}`, async () => {
        const res = await api.get(execPath(instanceId, query), { headers: auth(adminToken) });
        expect(res.status()).toBe(400);
        expect((await res.json()).code).toBe(code);
      });
    }

    test('an unknown instance is 404 instance_not_found', async () => {
      const res = await api.get(execPath('does-not-exist'), { headers: auth(adminToken) });
      expect(res.status()).toBe(404);
      expect((await res.json()).code).toBe('instance_not_found');
    });
  });

  // The checks behind "deployed" need a running row with a namespace. The row
  // is set directly in the database and put back. The namespace guard asks the
  // cluster, and the e2e cluster has none, so the checks behind the guard
  // (chart, container, upgrade) are in exec-tunnel.spec.ts.
  test('a running row on a cluster that cannot be asked fails closed with a generic 500', async () => {
    test.skip(!e2eDbConfigured(), E2E_DB_SKIP_REASON);
    expect(instanceId).toMatch(UUID);
    await setClusterSwitch(true);
    await runE2eSql(`UPDATE stack_instances SET status = 'running', namespace = '${uniqueName('stack-e2e-exec')}' WHERE id = '${instanceId}'`);
    try {
      const res = await api.get(execPath(instanceId), { headers: auth(adminToken) });
      // The namespace guard cannot ask the cluster: the request fails closed
      // with the generic error, never an upgrade.
      expect(res.status()).toBe(500);
      expect(await res.json()).toEqual({ error: 'Internal server error', code: 'internal' });
    } finally {
      await runE2eSql(`UPDATE stack_instances SET status = 'draft', namespace = '' WHERE id = '${instanceId}'`);
    }
  });

  test('the audit log filter offers exec and exec_end and sends the choice to the API', async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({ timeout: 10_000 });

    await page.getByLabel('Action').click();
    await expect(page.getByRole('option', { name: 'exec_end' })).toBeVisible();
    const request = page.waitForRequest((r) => r.url().includes('/api/v1/audit-logs') && r.url().includes('action=exec'));
    await page.getByRole('option', { name: 'exec', exact: true }).click();
    await page.getByRole('button', { name: 'Filter' }).click();
    await request;
  });

  test('the cluster form switch "Allow exec" is saved and read back', async ({ page }) => {
    await setClusterSwitch(false);
    await loginAsAdmin(page);
    await page.goto('/admin/clusters');
    await expect(page.getByRole('heading', { level: 1, name: 'Cluster Management' })).toBeVisible({ timeout: 10_000 });

    const row = page.getByRole('row').filter({ hasText: clusterName });
    await row.getByRole('button', { name: `Edit ${clusterName}` }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('Edit Cluster')).toBeVisible();
    const sw = dialog.getByLabel('Allow exec');
    await expect(sw).not.toBeChecked();
    await expect(dialog.getByText(/environment of the container, including secrets/)).toBeVisible();
    await sw.check();
    await dialog.getByRole('button', { name: /Update|Save/ }).click();
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    const stored = await (await api.get(`${API_BASE}/api/v1/clusters/${clusterId}`, { headers: auth(adminToken) })).json();
    expect(stored.allow_exec).toBe(true);
    expect(stored.allow_port_forward, 'the other switch is untouched').toBe(false);

    await row.getByRole('button', { name: `Edit ${clusterName}` }).click();
    await expect(page.getByRole('dialog').getByLabel('Allow exec')).toBeChecked();
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();
  });

  test.describe('Access URLs hint', () => {
    const namespaceStatus = {
      namespace: 'e2e-exec',
      status: 'healthy',
      charts: [{
        chart_name: 'e2e-exec-chart', release_name: 'e2e-exec-chart', status: 'healthy', deployments: [],
        services: [{ name: 'web', type: 'ClusterIP', cluster_ip: '10.0.0.1', ports: ['80/TCP'] }],
      }],
      ingresses: [],
      last_checked: '2026-10-19T10:00:00Z',
    };

    async function mockRunning(page: Page) {
      await page.route(new RegExp(`/api/v1/stack-instances/${instanceId}(\\?.*)?$`), async (route) => {
        if (route.request().method() !== 'GET') return route.continue();
        const res = await route.fetch();
        if (!res.ok()) return route.fulfill({ response: res });
        return route.fulfill({ response: res, json: { ...(await res.json()), status: 'running' } });
      });
      await page.route(`**/api/v1/stack-instances/${instanceId}/pods`, (route) => route.fulfill({ json: namespaceStatus }));
    }

    test.afterEach(async ({ page }) => {
      await page.unrouteAll({ behavior: 'ignoreErrors' });
    });

    test('shows the stackctl exec command for each chart', async ({ page }) => {
      await setClusterSwitch(true);
      await loginAsAdmin(page);
      await mockRunning(page);
      await page.goto(`/stack-instances/${instanceId}`);
      await expect(
        page.getByText(`stackctl stack exec ${instanceName} e2e-exec-chart -- <command>`, { exact: true }),
      ).toBeVisible({ timeout: 10_000 });
    });

    test('hides the command when the cluster does not allow exec', async ({ page }) => {
      await setClusterSwitch(false);
      await loginAsAdmin(page);
      await mockRunning(page);
      await page.goto(`/stack-instances/${instanceId}`);
      await expect(page.getByText('web', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
      await expect(page.getByText(/stackctl stack exec/)).toHaveCount(0);
    });

    test('hides the command when ui-config turns the feature off', async ({ page }) => {
      await setClusterSwitch(true);
      await loginAsAdmin(page);
      await mockRunning(page);
      await page.route('**/api/v1/ui-config', async (route) => {
        const res = await route.fetch();
        return route.fulfill({ response: res, json: { ...(await res.json()), exec_enabled: false } });
      });
      await page.goto(`/stack-instances/${instanceId}`);
      await expect(page.getByText('web', { exact: false }).first()).toBeVisible({ timeout: 10_000 });
      await expect(page.getByText(/stackctl stack exec/)).toHaveCount(0);
    });
  });
});

// The command itself (needs a cluster, E2E_KUBECONFIG): exec-tunnel.spec.ts.