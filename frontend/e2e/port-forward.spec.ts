import { test, expect, APIRequestContext, Page } from '@playwright/test';
import {
  API_BASE,
  apiAddChart,
  apiCreateDefinition,
  apiCreateInstance,
  apiLogin,
  ensureDefaultCluster,
  loginAsAdmin,
  loginAsUser,
  tokenFromPage,
  uniqueName,
} from './helpers';

/**
 * Port-forward to a stack service (#722), the part that needs no cluster.
 * The e2e backend runs with STACK_PORT_FORWARD_ENABLED=true (Makefile,
 * e2e_start_backend). Every check of the route that runs before the WebSocket
 * upgrade answers a plain JSON error, so a normal HTTP request is enough:
 * 401, 400 (invalid_service, invalid_port), 403 (forbidden,
 * cluster_port_forward_disabled, api_key_not_allowed), 409
 * (instance_not_deployed). The tunnel itself needs a cluster:
 * port-forward-tunnel.spec.ts (E2E_KUBECONFIG).
 *
 * The specs toggle allow_port_forward on the default e2e cluster, so they run
 * serially and restore the setting afterwards. No other spec reads it.
 */

test.describe.configure({ mode: 'serial' });

let api: APIRequestContext;
let adminToken: string;
let adminId: string;
let clusterId: string;
let clusterWasAllowed = false;
let definitionId: string;
let instanceId: string;
let instanceName: string;

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const pfPath = (id: string, query = 'service=web&port=80') => `${API_BASE}/api/v1/stack-instances/${id}/port-forward?${query}`;

async function setClusterSwitch(allow: boolean) {
  const res = await api.put(`${API_BASE}/api/v1/clusters/${clusterId}`, {
    headers: auth(adminToken),
    data: { allow_port_forward: allow },
  });
  expect(res.ok(), `update cluster: ${res.status()} ${await res.text()}`).toBe(true);
  expect((await res.json()).allow_port_forward).toBe(allow);
}

test.describe('Port-forward (no cluster)', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);
    const clusters: { id: string; is_default: boolean; allow_port_forward?: boolean }[] = await (
      await api.get(`${API_BASE}/api/v1/clusters`, { headers: auth(adminToken) })
    ).json();
    const def = clusters.find((c) => c.is_default);
    expect(def, 'a default cluster').toBeTruthy();
    clusterId = def!.id;
    clusterWasAllowed = def!.allow_port_forward === true;

    const me = await (await api.get(`${API_BASE}/api/v1/auth/me`, { headers: auth(adminToken) })).json();
    adminId = me.id;

    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-pf-def'));
    await apiAddChart(api, adminToken, definitionId, { chart_name: 'e2e-pf-chart' });
    instanceName = uniqueName('e2e-pf');
    // The instance stays a draft: no cluster, no deploy.
    instanceId = await apiCreateInstance(api, adminToken, definitionId, instanceName);
    await setClusterSwitch(false);
  });

  test.afterAll(async () => {
    if (!api) return;
    await setClusterSwitch(clusterWasAllowed).catch(() => undefined);
    if (instanceId) await api.delete(`${API_BASE}/api/v1/stack-instances/${instanceId}`, { headers: auth(adminToken) });
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    await api.dispose();
  });

  test('ui-config reports the feature as enabled', async () => {
    const res = await api.get(`${API_BASE}/api/v1/ui-config`);
    expect(res.ok()).toBe(true);
    expect((await res.json()).port_forward_enabled).toBe(true);
  });

  test('a request without credentials is 401', async () => {
    const res = await api.get(pfPath(instanceId));
    expect(res.status()).toBe(401);
  });

  test('?token= alone does not authenticate', async () => {
    const res = await api.get(pfPath(instanceId, `service=web&port=80&token=${adminToken}`));
    expect(res.status()).toBe(401);
  });

  test('a cluster without the switch is 403 cluster_port_forward_disabled', async () => {
    const res = await api.get(pfPath(instanceId), { headers: auth(adminToken) });
    expect(res.status()).toBe(403);
    expect((await res.json()).code).toBe('cluster_port_forward_disabled');
  });

  test('an API key is refused by default (403 api_key_not_allowed)', async () => {
    const created = await api.post(`${API_BASE}/api/v1/users/${adminId}/api-keys`, {
      headers: auth(adminToken),
      data: { name: uniqueName('e2e-pf-key'), expires_in_days: 1 },
    });
    expect(created.ok(), `create key: ${created.status()}`).toBe(true);
    const key = await created.json();
    try {
      const res = await api.get(pfPath(instanceId), { headers: { 'X-API-Key': key.raw_key } });
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
      const res = await page.request.get(pfPath(instanceId), { headers: auth(userToken) });
      expect(res.status()).toBe(403);
      expect((await res.json()).code).toBe('forbidden');
    });

    test('an instance that is not deployed is 409 instance_not_deployed', async () => {
      const res = await api.get(pfPath(instanceId), { headers: auth(adminToken) });
      expect(res.status()).toBe(409);
      expect((await res.json()).code).toBe('instance_not_deployed');
    });

    for (const [name, query, code] of [
      ['a missing service', 'port=80', 'invalid_service'],
      ['a service that is not a DNS-1035 label', 'service=Bad_Name&port=80', 'invalid_service'],
      ['a missing port', 'service=web', 'invalid_port'],
      ['port 0', 'service=web&port=0', 'invalid_port'],
      ['port 65536', 'service=web&port=65536', 'invalid_port'],
    ] as const) {
      test(`${name} is 400 ${code}`, async () => {
        const res = await api.get(pfPath(instanceId, query), { headers: auth(adminToken) });
        expect(res.status()).toBe(400);
        expect((await res.json()).code).toBe(code);
      });
    }

    test('an unknown instance is 404 instance_not_found', async () => {
      const res = await api.get(pfPath('does-not-exist'), { headers: auth(adminToken) });
      expect(res.status()).toBe(404);
      expect((await res.json()).code).toBe('instance_not_found');
    });
  });

  test('the audit log filter offers port_forward and sends it to the API', async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/audit-log');
    await expect(page.getByRole('heading', { level: 1, name: 'Audit Log' })).toBeVisible({ timeout: 10_000 });

    await page.getByLabel('Action').click();
    await expect(page.getByRole('option', { name: 'port_forward_end' })).toBeVisible();
    const request = page.waitForRequest((r) => r.url().includes('/api/v1/audit-logs') && r.url().includes('action=port_forward'));
    await page.getByRole('option', { name: 'port_forward', exact: true }).click();
    await page.getByRole('button', { name: 'Filter' }).click();
    await request;
  });

  test.describe('Access URLs hint', () => {
    const namespaceStatus = {
      namespace: 'e2e-pf',
      status: 'healthy',
      charts: [{
        chart_name: 'e2e-pf-chart', release_name: 'e2e-pf-chart', status: 'healthy', deployments: [],
        services: [{ name: 'web', type: 'ClusterIP', cluster_ip: '10.0.0.1', ports: ['80/TCP', '9090/TCP'] }],
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
      // A RegExp, not a glob: the page asks for /pods?exec_hints=true.
      await page.route(new RegExp(`/api/v1/stack-instances/${instanceId}/pods(\\?.*)?$`), (route) => route.fulfill({ json: namespaceStatus }));
    }

    test.afterEach(async ({ page }) => {
      await page.unrouteAll({ behavior: 'ignoreErrors' });
    });

    test('shows the stackctl command for each ClusterIP port', async ({ page }) => {
      await loginAsAdmin(page);
      await mockRunning(page);
      await page.goto(`/stack-instances/${instanceId}`);
      await expect(page.getByText(`stackctl stack port-forward ${instanceName} web 80`, { exact: true })).toBeVisible({ timeout: 10_000 });
      await expect(page.getByText(`stackctl stack port-forward ${instanceName} web 9090`, { exact: true })).toBeVisible();
      // The kubectl hint stays for devops and admin.
      await expect(page.getByText(/kubectl port-forward svc\/web/)).toBeVisible();
    });

    test('hides the stackctl command when ui-config turns the feature off', async ({ page }) => {
      await loginAsAdmin(page);
      await mockRunning(page);
      await page.route('**/api/v1/ui-config', async (route) => {
        const res = await route.fetch();
        return route.fulfill({ response: res, json: { ...(await res.json()), port_forward_enabled: false } });
      });
      await page.goto(`/stack-instances/${instanceId}`);
      await expect(page.getByText(/kubectl port-forward svc\/web/)).toBeVisible({ timeout: 10_000 });
      await expect(page.getByText(/stackctl stack port-forward/)).toHaveCount(0);
    });
  });
});

// The tunnel itself (needs a cluster, E2E_KUBECONFIG): port-forward-tunnel.spec.ts.
