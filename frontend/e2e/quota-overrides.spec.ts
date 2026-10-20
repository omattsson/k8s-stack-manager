import { test, expect, APIRequestContext, APIResponse, Locator, Page } from '@playwright/test';
import {
  API_BASE,
  apiCreateDefinition,
  apiLogin,
  ensureDefaultCluster,
  loginAsAdmin,
  skipSetupWizard,
  uniqueName,
} from './helpers';

/**
 * Per-instance quota overrides (#589): PUT, GET and DELETE
 * /api/v1/stack-instances/:id/quota-overrides.
 *
 * Every test registers its own cluster (a kubeconfig path that does not
 * exist, so the backend never reaches a cluster) with a known cluster quota,
 * and creates a draft instance on it. A draft has no namespace, so the
 * background quota write is skipped (handlers/instance_quota_overrides.go,
 * shouldApplyQuota) and the API answers without a cluster. The shared default
 * cluster and its quota are never touched.
 *
 * Rules and messages pinned here:
 * - handlers/instance_quota_overrides.go: owner, admin or devops; 403 with
 *   "<check error>; only admin or devops can set a higher quota" for a role
 *   `user` above the cluster quota.
 * - models/quota_merge.go: CheckOverrideWithinClusterQuota (403 text),
 *   ValidateEffectiveQuota (400 text with the source of each value).
 * - models/validation.go: validateQuotaQuantities (400 "<a> must not exceed <b>").
 * - handlers/clusters.go: checkOverrideConflicts (a cluster quota save checks
 *   the stored overrides).
 * - GET /stack-instances/:id/quota and components/InstanceQuota: the
 *   "Resource quota" section of the instance page (#629, #635).
 */

const PASSWORD = 'e2e-test-password';
const ABOVE_SUFFIX = '; only admin or devops can set a higher quota';
const MSG_MODIFY_FORBIDDEN = 'You are not allowed to modify this stack instance';
const MSG_OVERRIDE_NOT_FOUND = 'Instance quota override not found';

/** Cluster quota of every test cluster. All container defaults are below the namespace totals. */
const CLUSTER_QUOTA = {
  cpu_request: '1',
  cpu_limit: '2',
  memory_request: '1Gi',
  memory_limit: '2Gi',
  storage_limit: '10Gi',
  ephemeral_storage_limit: '4Gi',
  pod_limit: 10,
  container_cpu_request: '100m',
  container_cpu_limit: '500m',
  container_cpu_max: '1',
  container_memory_request: '128Mi',
  container_memory_limit: '512Mi',
  container_memory_max: '1Gi',
  container_ephemeral_storage_request: '128Mi',
  container_ephemeral_storage_limit: '1Gi',
} as const;

type QuotaBody = Record<string, string | number | null>;

interface Actor {
  id: string;
  username: string;
  token: string;
}

interface Scenario {
  clusterId: string;
  clusterName: string;
  instanceId: string;
  instanceName: string;
}

// Worker state (fullyParallel: each worker runs beforeAll once).
let api: APIRequestContext;
let adminToken: string;
let owner: Actor;
let otherUser: Actor;
let devops: Actor;
let definitionId: string;
/** Clusters and instances of the current test; afterEach deletes them. */
let created: { clusterIds: string[]; instanceIds: string[] } = { clusterIds: [], instanceIds: [] };

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const overrideUrl = (instanceId: string) => `${API_BASE}/api/v1/stack-instances/${instanceId}/quota-overrides`;

async function registerActor(role: 'user' | 'devops', prefix: string): Promise<Actor> {
  const username = uniqueName(prefix);
  const res = await api.post(`${API_BASE}/api/v1/auth/register`, {
    headers: auth(adminToken),
    data: { username, password: PASSWORD, role },
  });
  expect(res.status(), `register ${username}: ${await res.text()}`).toBe(201);
  const { id } = await res.json();
  return { id, username, token: await apiLogin(api, username, PASSWORD) };
}

/**
 * Register a cluster that the backend cannot reach. ensureDefaultCluster runs
 * first: when no default cluster exists, the backend makes a new cluster the
 * default (CreateCluster auto-default), and other specs would then use this one.
 */
async function createCluster(): Promise<{ id: string; name: string }> {
  await ensureDefaultCluster(api, adminToken);
  const name = uniqueName('e2e-quota-cluster');
  const res = await api.post(`${API_BASE}/api/v1/clusters`, {
    headers: auth(adminToken),
    data: {
      name,
      api_server_url: 'https://quota-override-e2e.invalid:6443',
      kubeconfig_path: '/tmp/e2e-quota-override-missing-kubeconfig',
      is_default: false,
    },
  });
  expect(res.status(), `create cluster: ${await res.text()}`).toBe(201);
  const cluster = await res.json();
  created.clusterIds.push(cluster.id);
  return { id: cluster.id, name };
}

/** A cluster with CLUSTER_QUOTA and a draft instance of `owner` on it. */
async function setupScenario(): Promise<Scenario> {
  const cluster = await createCluster();
  const quotaRes = await api.put(`${API_BASE}/api/v1/clusters/${cluster.id}/quotas`, {
    headers: auth(adminToken),
    data: CLUSTER_QUOTA,
  });
  expect(quotaRes.status(), `set cluster quota: ${await quotaRes.text()}`).toBe(200);

  const instanceName = uniqueName('e2e-qo');
  const instRes = await api.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: auth(owner.token),
    data: { stack_definition_id: definitionId, name: instanceName, branch: 'main', cluster_id: cluster.id },
  });
  expect(instRes.status(), `create instance: ${await instRes.text()}`).toBe(201);
  const inst = await instRes.json();
  created.instanceIds.push(inst.id);
  expect(inst.status).toBe('draft');
  expect(inst.cluster_id).toBe(cluster.id);
  return { clusterId: cluster.id, clusterName: cluster.name, instanceId: inst.id, instanceName };
}

const putOverride = (instanceId: string, token: string, body: QuotaBody) =>
  api.put(overrideUrl(instanceId), { headers: auth(token), data: body });
const getOverride = (instanceId: string, token: string) => api.get(overrideUrl(instanceId), { headers: auth(token) });
const deleteOverride = (instanceId: string, token: string) =>
  api.delete(overrideUrl(instanceId), { headers: auth(token) });

async function expectError(res: APIResponse, status: number, message: string): Promise<void> {
  const text = await res.text();
  expect(res.status(), text).toBe(status);
  expect(JSON.parse(text)).toEqual({ error: message });
}

/** The stored override has exactly the given values (all other quantities empty). */
async function expectStoredOverride(instanceId: string, token: string, values: QuotaBody): Promise<void> {
  const res = await getOverride(instanceId, token);
  expect(res.status(), await res.text()).toBe(200);
  const body = await res.json();
  expect(body.stack_instance_id).toBe(instanceId);
  for (const field of Object.keys(CLUSTER_QUOTA)) {
    const want = field in values ? values[field] : field === 'pod_limit' ? null : '';
    expect(body[field], field).toBe(want);
  }
}

async function expectNoOverride(instanceId: string): Promise<void> {
  await expectError(await getOverride(instanceId, owner.token), 404, MSG_OVERRIDE_NOT_FOUND);
}

const getQuota = (instanceId: string, token: string) =>
  api.get(`${API_BASE}/api/v1/stack-instances/${instanceId}/quota`, { headers: auth(token) });

/** Use the token of an actor of this spec in the web UI. */
async function loginWithToken(page: Page, token: string): Promise<void> {
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
  await skipSetupWizard(page);
}

/** Open the instance page and return its "Resource quota" section. */
async function openQuotaSection(page: Page, s: Scenario): Promise<Locator> {
  await page.goto(`/stack-instances/${s.instanceId}`);
  await expect(page.getByRole('heading', { level: 1, name: s.instanceName })).toBeVisible({ timeout: 10_000 });
  const section = page.getByRole('region', { name: 'Resource quota' });
  await expect(section).toBeVisible({ timeout: 10_000 });
  return section;
}

/** One value of the quota section: the readable value and the source chip ("Override" or "Cluster"). */
async function expectQuotaValue(section: Locator, label: string, value: string, source: 'Override' | 'Cluster'): Promise<void> {
  const item = section.getByRole('group', { name: label, exact: true });
  await expect(item).toContainText(value, { timeout: 10_000 });
  await expect(item).toContainText(source);
}

test.describe('Per-instance quota overrides', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    owner = await registerActor('user', 'e2e-qo-owner');
    otherUser = await registerActor('user', 'e2e-qo-other');
    devops = await registerActor('devops', 'e2e-qo-devops');
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-qo-def'));
  });

  test.afterEach(async () => {
    // Instances first: a cluster with instances cannot be deleted (409).
    for (const id of created.instanceIds) {
      await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    }
    for (const id of created.clusterIds) {
      await api.delete(`${API_BASE}/api/v1/clusters/${id}`, { headers: auth(adminToken) });
    }
    created = { clusterIds: [], instanceIds: [] };
  });

  test.afterAll(async () => {
    if (!api) return;
    if (definitionId) {
      await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    }
    for (const actor of [owner, otherUser, devops]) {
      if (actor) await api.delete(`${API_BASE}/api/v1/users/${actor.id}`, { headers: auth(adminToken) });
    }
    await api.dispose();
  });

  // Scenario 1 -------------------------------------------------------------

  test('owner sets an override at or below each cluster quota value', async () => {
    const s = await setupScenario();
    await expectNoOverride(s.instanceId);

    // Every field below the cluster value; the values are trimmed.
    const below: QuotaBody = {
      cpu_request: '500m',
      cpu_limit: ' 1 ',
      memory_request: '512Mi',
      memory_limit: '1Gi',
      storage_limit: '5Gi',
      ephemeral_storage_limit: '2Gi',
      pod_limit: 5,
      container_cpu_request: '50m',
      container_cpu_limit: '250m',
      container_cpu_max: '500m',
      container_memory_request: '64Mi',
      container_memory_limit: '256Mi',
      container_memory_max: '512Mi',
      container_ephemeral_storage_request: '64Mi',
      container_ephemeral_storage_limit: '512Mi',
    };
    const res = await putOverride(s.instanceId, owner.token, below);
    expect(res.status(), await res.text()).toBe(200);
    expect((await res.json()).cpu_limit).toBe('1');
    await expectStoredOverride(s.instanceId, owner.token, { ...below, cpu_limit: '1' });

    // Every field equal to the cluster value, partly in other units (compared
    // as Kubernetes quantities: 2000m == 2, 2048Mi == 2Gi): passes.
    const equal: QuotaBody = { ...CLUSTER_QUOTA, cpu_limit: '2000m', memory_limit: '2048Mi' };
    const resEqual = await putOverride(s.instanceId, owner.token, equal);
    expect(resEqual.status(), await resEqual.text()).toBe(200);
    await expectStoredOverride(s.instanceId, owner.token, equal);

    // PUT replaces all fields: a field that is not sent is cleared.
    const resPartial = await putOverride(s.instanceId, owner.token, { memory_limit: '1536Mi' });
    expect(resPartial.status(), await resPartial.text()).toBe(200);
    await expectStoredOverride(s.instanceId, owner.token, { memory_limit: '1536Mi' });

    // The cluster quota itself does not change.
    const clusterQuota = await api.get(`${API_BASE}/api/v1/clusters/${s.clusterId}/quotas`, { headers: auth(adminToken) });
    expect(clusterQuota.status()).toBe(200);
    expect(await clusterQuota.json()).toMatchObject(CLUSTER_QUOTA);
  });

  test('another user without the admin or devops role cannot read, set or delete the override', async () => {
    const s = await setupScenario();
    const ok = await putOverride(s.instanceId, owner.token, { cpu_limit: '1' });
    expect(ok.status(), await ok.text()).toBe(200);

    await expectError(await getOverride(s.instanceId, otherUser.token), 403, MSG_MODIFY_FORBIDDEN);
    await expectError(await putOverride(s.instanceId, otherUser.token, { cpu_limit: '500m' }), 403, MSG_MODIFY_FORBIDDEN);
    await expectError(await deleteOverride(s.instanceId, otherUser.token), 403, MSG_MODIFY_FORBIDDEN);
    await expectStoredOverride(s.instanceId, owner.token, { cpu_limit: '1' });
  });

  test('the admin quota dialog shows the cluster quota that bounds the overrides', async ({ page }) => {
    const s = await setupScenario();
    await loginAsAdmin(page);
    await page.goto('/admin/clusters');
    await page.getByRole('button', { name: `Resource quotas for ${s.clusterName}` }).click({ timeout: 10_000 });

    const dialog = page.getByRole('dialog', { name: `Resource Quotas for ${s.clusterName}` });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByLabel('CPU Request', { exact: true })).toHaveValue('1', { timeout: 10_000 });
    await expect(dialog.getByLabel('CPU Limit', { exact: true })).toHaveValue('2');
    await expect(dialog.getByLabel('Memory Limit', { exact: true })).toHaveValue('2Gi');
    await expect(dialog.getByLabel('Pod Limit', { exact: true })).toHaveValue('10');
    await expect(dialog.getByLabel('Container CPU Max', { exact: true })).toHaveValue('1');
    await expect(dialog.getByLabel('Container Memory Max', { exact: true })).toHaveValue('1Gi');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
  });

  // Instance page "Resource quota" section (#629, #635): GET
  // /stack-instances/:id/quota and components/InstanceQuota.

  test('the instance detail page shows the effective quota values', async ({ page }) => {
    const s = await setupScenario();
    const res = await putOverride(s.instanceId, owner.token, { cpu_limit: '1500m' });
    expect(res.status(), await res.text()).toBe(200);

    // Any user who can view the instance reads the effective quota.
    const expected = {
      cluster_id: s.clusterId,
      effective: { ...CLUSTER_QUOTA, cpu_limit: '1500m' },
      sources: { cpu_limit: 'override', cpu_request: 'cluster', memory_limit: 'cluster', pod_limit: 'cluster' },
      cluster: CLUSTER_QUOTA,
      override: { stack_instance_id: s.instanceId, cpu_limit: '1500m', pod_limit: null },
    };
    for (const [actor, canModify] of [[otherUser, false], [owner, true]] as const) {
      const quotaRes = await getQuota(s.instanceId, actor.token);
      expect(quotaRes.status(), await quotaRes.text()).toBe(200);
      expect(await quotaRes.json()).toMatchObject({ ...expected, can_modify: canModify });
    }

    await loginAsAdmin(page);
    const section = await openQuotaSection(page, s);
    await expectQuotaValue(section, 'CPU Limit', '1.5 cores (1500m)', 'Override');
    await expectQuotaValue(section, 'CPU Request', '1 core', 'Cluster');
    await expectQuotaValue(section, 'Memory Limit', '2 GiB', 'Cluster');
    await expectQuotaValue(section, 'Pod Limit', '10', 'Cluster');
    await expectQuotaValue(section, 'Container CPU Max', '1 core', 'Cluster');
    await expect(section.getByRole('button', { name: 'Edit override' })).toBeVisible();
    await expect(section.getByRole('button', { name: 'Remove override' })).toBeVisible();

    // Without the override, every value comes from the cluster quota again.
    const del = await deleteOverride(s.instanceId, owner.token);
    expect(del.status(), await del.text()).toBe(204);
    await page.reload();
    await expectQuotaValue(section, 'CPU Limit', '2 cores', 'Cluster');
    await expect(section.getByRole('button', { name: 'Remove override' })).toHaveCount(0);
  });

  test('a user who cannot modify the instance sees the quota read-only', async ({ page }) => {
    const s = await setupScenario();
    const res = await putOverride(s.instanceId, owner.token, { cpu_limit: '1500m' });
    expect(res.status(), await res.text()).toBe(200);

    await loginWithToken(page, otherUser.token);
    const section = await openQuotaSection(page, s);
    await expectQuotaValue(section, 'CPU Limit', '1.5 cores (1500m)', 'Override');
    await expectQuotaValue(section, 'Memory Limit', '2 GiB', 'Cluster');
    await expect(section.getByRole('button', { name: 'Edit override' })).toHaveCount(0);
    await expect(section.getByRole('button', { name: 'Remove override' })).toHaveCount(0);
  });

  test('the owner sets and removes the override on the instance page', async ({ page }) => {
    const s = await setupScenario();
    await loginWithToken(page, owner.token);
    const section = await openQuotaSection(page, s);
    await expectQuotaValue(section, 'CPU Limit', '2 cores', 'Cluster');
    await expect(section.getByRole('button', { name: 'Remove override' })).toHaveCount(0);

    await section.getByRole('button', { name: 'Edit override' }).click();
    const dialog = page.getByRole('dialog', { name: 'Quota override' });
    await expect(dialog).toBeVisible();
    const cpuLimit = dialog.getByLabel('CPU Limit', { exact: true });
    const cpuRequest = dialog.getByLabel('CPU Request', { exact: true });
    await expect(cpuLimit).toBeEnabled({ timeout: 10_000 });

    // The role user sees the cluster value as the upper limit of each field.
    await expect(dialog.getByText('At most 2 (cluster quota)')).toBeVisible();
    await expect(dialog.getByText('At most 2Gi (cluster quota)')).toBeVisible();
    await expect(dialog.getByText('At most 10 (cluster quota)')).toBeVisible();

    // Above the cluster quota: 403.
    await cpuLimit.fill('3');
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog.getByRole('alert')).toHaveText(
      'Failed to save the quota override (HTTP 403: cpu_limit 3 exceeds the cluster quota 2' + ABOVE_SUFFIX + ')',
      { timeout: 10_000 },
    );

    // An order rule of the effective quota: 400.
    await cpuLimit.fill('');
    await cpuRequest.fill('3');
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog.getByRole('alert')).toHaveText(
      'Failed to save the quota override (HTTP 400: cpu_request 3 (instance override) exceeds the effective cpu_limit 2 (cluster quota))',
      { timeout: 10_000 },
    );
    await expectNoOverride(s.instanceId);

    await cpuRequest.fill('');
    await cpuLimit.fill('1500m');
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog).toBeHidden({ timeout: 10_000 });
    await expect(page.getByText('Quota override saved')).toBeVisible();
    await expectQuotaValue(section, 'CPU Limit', '1.5 cores (1500m)', 'Override');
    await expectQuotaValue(section, 'CPU Request', '1 core', 'Cluster');
    await expectStoredOverride(s.instanceId, owner.token, { cpu_limit: '1500m' });

    await section.getByRole('button', { name: 'Remove override' }).click();
    const confirm = page.getByRole('dialog', { name: 'Remove the quota override' });
    await confirm.getByRole('button', { name: 'Remove' }).click();
    await expect(confirm).toBeHidden();
    await expect(page.getByText('Quota override removed')).toBeVisible();
    await expectQuotaValue(section, 'CPU Limit', '2 cores', 'Cluster');
    await expect(section.getByRole('button', { name: 'Remove override' })).toHaveCount(0);
    await expectNoOverride(s.instanceId);
  });

  // Scenario 2 -------------------------------------------------------------

  test('owner without the admin or devops role cannot set a value above the cluster quota (403)', async () => {
    const s = await setupScenario();
    const cases: { body: QuotaBody; message: string }[] = [
      { body: { cpu_limit: '3' }, message: 'cpu_limit 3 exceeds the cluster quota 2' },
      // Compared as quantities: one millicore above 2.
      { body: { cpu_limit: '2001m' }, message: 'cpu_limit 2001m exceeds the cluster quota 2' },
      { body: { cpu_request: '1500m' }, message: 'cpu_request 1500m exceeds the cluster quota 1' },
      { body: { memory_limit: '3Gi' }, message: 'memory_limit 3Gi exceeds the cluster quota 2Gi' },
      { body: { storage_limit: '20Gi' }, message: 'storage_limit 20Gi exceeds the cluster quota 10Gi' },
      { body: { ephemeral_storage_limit: '8Gi' }, message: 'ephemeral_storage_limit 8Gi exceeds the cluster quota 4Gi' },
      // Per-container fields.
      { body: { container_cpu_request: '200m' }, message: 'container_cpu_request 200m exceeds the cluster quota 100m' },
      { body: { container_cpu_limit: '750m' }, message: 'container_cpu_limit 750m exceeds the cluster quota 500m' },
      { body: { container_cpu_max: '2' }, message: 'container_cpu_max 2 exceeds the cluster quota 1' },
      { body: { container_memory_request: '256Mi' }, message: 'container_memory_request 256Mi exceeds the cluster quota 128Mi' },
      { body: { container_memory_max: '2Gi' }, message: 'container_memory_max 2Gi exceeds the cluster quota 1Gi' },
      {
        body: { container_ephemeral_storage_limit: '2Gi' },
        message: 'container_ephemeral_storage_limit 2Gi exceeds the cluster quota 1Gi',
      },
      { body: { pod_limit: 11 }, message: 'pod_limit 11 exceeds the cluster quota 10' },
      // pod_limit 0 means no limit, so it is above any cluster pod_limit.
      { body: { pod_limit: 0 }, message: 'pod_limit 0 (no limit) exceeds the cluster quota 10' },
    ];
    for (const { body, message } of cases) {
      await test.step(JSON.stringify(body), async () => {
        await expectError(await putOverride(s.instanceId, owner.token, body), 403, message + ABOVE_SUFFIX);
      });
    }
    // Nothing was stored.
    await expectNoOverride(s.instanceId);
  });

  test('owner may resend a value equal to the stored override, also above the cluster quota', async () => {
    const s = await setupScenario();
    // devops grants values above the cluster quota (pod_limit 0 = no limit).
    const grant: QuotaBody = { cpu_limit: '4', container_cpu_max: '2', pod_limit: 0 };
    const granted = await putOverride(s.instanceId, devops.token, grant);
    expect(granted.status(), await granted.text()).toBe(200);

    // The owner changes another field and sends the granted values back
    // unchanged (cpu_limit in another unit): passes.
    const keep: QuotaBody = { cpu_limit: '4000m', container_cpu_max: '2', pod_limit: 0, memory_limit: '1Gi' };
    const kept = await putOverride(s.instanceId, owner.token, keep);
    expect(kept.status(), await kept.text()).toBe(200);
    await expectStoredOverride(s.instanceId, owner.token, keep);

    // A raise above the stored value and the cluster quota fails.
    await expectError(
      await putOverride(s.instanceId, owner.token, { ...keep, cpu_limit: '5' }),
      403,
      'cpu_limit 5 exceeds the cluster quota 2' + ABOVE_SUFFIX,
    );
    await expectError(
      await putOverride(s.instanceId, owner.token, { ...keep, container_cpu_max: '3' }),
      403,
      'container_cpu_max 3 exceeds the cluster quota 1' + ABOVE_SUFFIX,
    );
    // Stored pod_limit is 0 (no limit); a new value above the cluster quota fails.
    await expectError(
      await putOverride(s.instanceId, owner.token, { ...keep, pod_limit: 12 }),
      403,
      'pod_limit 12 exceeds the cluster quota 10' + ABOVE_SUFFIX,
    );
    await expectStoredOverride(s.instanceId, owner.token, keep);

    // The owner lowers cpu_limit to the cluster value; the grant is gone, so
    // the old value is now a raise.
    const lowered: QuotaBody = { ...keep, cpu_limit: '2' };
    const lowerRes = await putOverride(s.instanceId, owner.token, lowered);
    expect(lowerRes.status(), await lowerRes.text()).toBe(200);
    await expectError(
      await putOverride(s.instanceId, owner.token, { ...lowered, cpu_limit: '4' }),
      403,
      'cpu_limit 4 exceeds the cluster quota 2' + ABOVE_SUFFIX,
    );
    await expectStoredOverride(s.instanceId, owner.token, lowered);
  });

  // Scenario 3 -------------------------------------------------------------

  for (const role of ['devops', 'admin'] as const) {
    test(`${role} can set values above the cluster quota on another user's instance`, async () => {
      const s = await setupScenario();
      const token = role === 'devops' ? devops.token : adminToken;
      const above: QuotaBody = {
        cpu_request: '4',
        cpu_limit: '8',
        memory_request: '4Gi',
        memory_limit: '16Gi',
        storage_limit: '100Gi',
        ephemeral_storage_limit: '20Gi',
        pod_limit: 0,
        container_cpu_request: '1',
        container_cpu_limit: '2',
        container_cpu_max: '4',
        container_memory_request: '1Gi',
        container_memory_limit: '2Gi',
        container_memory_max: '8Gi',
        container_ephemeral_storage_request: '1Gi',
        container_ephemeral_storage_limit: '4Gi',
      };
      const res = await putOverride(s.instanceId, token, above);
      expect(res.status(), await res.text()).toBe(200);
      await expectStoredOverride(s.instanceId, token, above);
      // The owner sees the grant.
      await expectStoredOverride(s.instanceId, owner.token, above);

      // The role may also delete it.
      const del = await deleteOverride(s.instanceId, token);
      expect(del.status(), await del.text()).toBe(204);
      await expectNoOverride(s.instanceId);
    });
  }

  // Scenario 4 -------------------------------------------------------------

  test('deleting the override restores the cluster quota values', async ({ page }) => {
    const s = await setupScenario();
    const res = await putOverride(s.instanceId, owner.token, { cpu_limit: '1500m' });
    expect(res.status(), await res.text()).toBe(200);

    // While the override exists, a cluster quota save is checked against the
    // effective quota (cluster quota merged with the override): a cluster
    // cpu_request of 1800m is above the override cpu_limit 1500m.
    await loginAsAdmin(page);
    await page.goto('/admin/clusters');
    const openDialog = async () => {
      await page.getByRole('button', { name: `Resource quotas for ${s.clusterName}` }).click({ timeout: 10_000 });
      const dialog = page.getByRole('dialog', { name: `Resource Quotas for ${s.clusterName}` });
      await expect(dialog.getByLabel('CPU Request', { exact: true })).toHaveValue('1', { timeout: 10_000 });
      return dialog;
    };
    let dialog = await openDialog();
    await dialog.getByLabel('CPU Request', { exact: true }).fill('1800m');
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog.getByRole('alert')).toHaveText(
      'Failed to save quota configuration (HTTP 400: the quota conflicts with the quota override of 1 instance(s): ' +
        `${s.instanceName}; first conflict (${s.instanceName}): ` +
        'cpu_request 1800m (cluster quota) exceeds the effective cpu_limit 1500m (instance override))',
      { timeout: 10_000 },
    );
    // The error toast (8 s) shows at the top of the screen, above the dialog,
    // and does not cover the dialog buttons (#630). Cancel takes the click at
    // once while the toast shows. A short click timeout makes a covered button
    // fail the test: Playwright waits while another element takes the click.
    // The open dialog sets aria-hidden on the page root, so the toast query
    // includes hidden elements. Only the toast has a Close button.
    const errorToast = page
      .getByRole('alert', { includeHidden: true })
      .filter({ hasText: /^Failed to save quota configuration/ })
      .filter({ has: page.getByRole('button', { name: 'Close', includeHidden: true }) });
    await expect(errorToast).toBeVisible();
    await dialog.getByRole('button', { name: 'Cancel' }).click({ timeout: 2_000 });
    await expect(dialog).toBeHidden();
    await expect(errorToast).toBeVisible();

    // The owner deletes the override; a second delete finds nothing.
    const del = await deleteOverride(s.instanceId, owner.token);
    expect(del.status(), await del.text()).toBe(204);
    await expectNoOverride(s.instanceId);
    await expectError(await deleteOverride(s.instanceId, owner.token), 404, MSG_OVERRIDE_NOT_FOUND);

    // The cluster quota applies again: the same save now passes.
    dialog = await openDialog();
    await dialog.getByLabel('CPU Request', { exact: true }).fill('1800m');
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog).toBeHidden({ timeout: 10_000 });
    await expect(page.getByText('Resource quotas saved')).toBeVisible();

    const clusterQuota = await api.get(`${API_BASE}/api/v1/clusters/${s.clusterId}/quotas`, { headers: auth(adminToken) });
    expect(clusterQuota.status()).toBe(200);
    expect(await clusterQuota.json()).toMatchObject({ ...CLUSTER_QUOTA, cpu_request: '1800m' });

    // Without the override, the owner is bounded by the cluster values again:
    // the old override value 1500m is below cpu_request 1800m now.
    await expectError(
      await putOverride(s.instanceId, owner.token, { cpu_limit: '1500m' }),
      400,
      'cpu_request 1800m (cluster quota) exceeds the effective cpu_limit 1500m (instance override)',
    );
  });

  // Scenario 5 -------------------------------------------------------------

  test('invalid overrides give 400 with the rule that failed', async () => {
    const s = await setupScenario();
    const cases: { body: QuotaBody; message: string; token?: string }[] = [
      // Order rules inside the override (models.quotaOrderRules).
      { body: { cpu_request: '2', cpu_limit: '1' }, message: 'cpu_request must not exceed cpu_limit' },
      { body: { memory_request: '2Gi', memory_limit: '1Gi' }, message: 'memory_request must not exceed memory_limit' },
      {
        body: { container_cpu_request: '400m', container_cpu_limit: '300m' },
        message: 'container_cpu_request must not exceed container_cpu_limit',
      },
      {
        body: { container_cpu_limit: '900m', container_cpu_max: '800m' },
        message: 'container_cpu_limit must not exceed container_cpu_max',
      },
      {
        body: { container_cpu_request: '900m', container_cpu_max: '800m' },
        message: 'container_cpu_request must not exceed container_cpu_max',
      },
      {
        body: { container_memory_request: '300Mi', container_memory_limit: '200Mi' },
        message: 'container_memory_request must not exceed container_memory_limit',
      },
      {
        body: { container_memory_limit: '900Mi', container_memory_max: '800Mi' },
        message: 'container_memory_limit must not exceed container_memory_max',
      },
      {
        body: { container_memory_request: '900Mi', container_memory_max: '800Mi' },
        message: 'container_memory_request must not exceed container_memory_max',
      },
      {
        body: { container_ephemeral_storage_request: '900Mi', container_ephemeral_storage_limit: '800Mi' },
        message: 'container_ephemeral_storage_request must not exceed container_ephemeral_storage_limit',
      },
      // A container default above the namespace total.
      { body: { container_cpu_limit: '600m', cpu_limit: '500m' }, message: 'container_cpu_limit must not exceed cpu_limit' },
      // The admin or devops role does not skip validation.
      {
        body: { container_memory_limit: '4Gi', container_memory_max: '2Gi' },
        message: 'container_memory_limit must not exceed container_memory_max',
        token: 'devops',
      },
      // The effective quota (cluster quota merged with the override) follows
      // the same rules; the message names the source of each value. 400 comes
      // before the 403 check.
      {
        body: { container_cpu_limit: '2' },
        message: 'container_cpu_limit 2 (instance override) exceeds the effective container_cpu_max 1 (cluster quota)',
      },
      {
        body: { cpu_request: '3' },
        message: 'cpu_request 3 (instance override) exceeds the effective cpu_limit 2 (cluster quota)',
      },
      {
        body: { container_memory_limit: '4Gi' },
        message: 'container_memory_limit 4Gi (instance override) exceeds the effective container_memory_max 1Gi (cluster quota)',
        token: 'devops',
      },
      // Quantities and pod_limit.
      { body: { cpu_limit: 'two' }, message: 'cpu_limit: invalid quantity (examples: 500m, 2, 512Mi, 10Gi)' },
      { body: { memory_limit: '-1Gi' }, message: 'memory_limit: must not be negative' },
      { body: { pod_limit: -1 }, message: 'pod_limit must be non-negative' },
    ];
    for (const { body, message, token } of cases) {
      await test.step(`${token ?? 'owner'} ${JSON.stringify(body)}`, async () => {
        const res = await putOverride(s.instanceId, token === 'devops' ? devops.token : owner.token, body);
        await expectError(res, 400, message);
      });
    }
    // Nothing was stored.
    await expectNoOverride(s.instanceId);
  });
});
