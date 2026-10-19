import { test, expect, request as playwrightRequest } from '@playwright/test';
import type { APIRequestContext, APIResponse, Page } from '@playwright/test';
import {
  API_BASE,
  apiCreateDefinition,
  apiLogin,
  E2E_DB_SKIP_REASON,
  e2eDbConfigured,
  ensureDefaultCluster,
  runE2eSql,
  uniqueName,
} from './helpers';

/**
 * Unique instance names for each cluster (#587, UNIQUE_INSTANCE_NAMES=true;
 * `make test-e2e` starts the backend with it).
 *
 * Create, rename, clone and quick deploy refuse a name that another instance
 * uses on the same cluster: 409 `instance name already exists`, the message
 * `A stack named "x" already exists on this cluster (owner: y)` and free name
 * suggestions (handlers/instance_names.go). The comparison ignores case.
 *
 * Each worker creates its own users (a devops user and a plain user), its own
 * cluster (not the default: other specs move the default flag), one
 * definition and one published template. Every instance names that cluster
 * explicitly, so no other spec can hold or take a name the tests check.
 * The built-in `admin` is only the caller of the setup and the cleanup.
 */

const INSTANCES_URL = `${API_BASE}/api/v1/stack-instances`;
const PASSWORD = 'e2e-unique-names-password';
const NAME_TAKEN = 'instance name already exists';

/** The message of the 409 (instanceNameGuard.check). */
function takenMessage(name: string, owner: string): string {
  return `A stack named "${name}" already exists on this cluster (owner: ${owner})`;
}

/** Suggestions of generateNameSuggestions: <name>-2, -3, -4 (names here are short enough not to be cut). */
function suggestionsFor(name: string): string[] {
  return [`${name}-2`, `${name}-3`, `${name}-4`];
}

/**
 * A short unique name (like uniqueName, with a base-36 time stamp). The
 * namespace stack-<instance>-<owner> is cut at 63 characters (buildNamespace),
 * so with the long uniqueName values `<name>-copy` and `<name>-copy-2` of one
 * owner end up in the same namespace. With at most 26 characters for the
 * instance name (and the `-copy-N` suffix) and 18 for the owner, the
 * namespace is never cut.
 */
function shortName(prefix: string): string {
  const name = `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
  expect(name.length, `name ${name} too long for the namespace`).toBeLessThanOrEqual(prefix.length + 14);
  return name;
}

/** A unique RFC 1123 instance name of at most 26 characters. */
function instanceName(prefix: string): string {
  expect(prefix.length).toBeLessThanOrEqual(12);
  return shortName(prefix);
}

function bearer(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}

interface TestUser {
  id: string;
  username: string;
  token: string;
}

interface Fixture {
  api: APIRequestContext;
  adminToken: string;
  devops: TestUser;
  user: TestUser;
  clusterId: string;
  clusterName: string;
  definitionId: string;
  definitionName: string;
  templateId: string;
  templateName: string;
  /** Instances to delete in afterAll (any owner; admin deletes them). */
  instanceIds: string[];
}

let fx: Fixture;

async function createUser(api: APIRequestContext, adminToken: string, role: 'user' | 'devops'): Promise<TestUser> {
  const username = shortName(role === 'devops' ? 'un-dev' : 'un-usr');
  const res = await api.post(`${API_BASE}/api/v1/auth/register`, {
    headers: bearer(adminToken),
    data: { username, password: PASSWORD, role },
  });
  expect(res.ok(), `register ${username}: ${res.status()} ${await res.text()}`).toBe(true);
  const body = await res.json();
  return { id: body.id, username, token: await apiLogin(api, username, PASSWORD) };
}

/** POST /stack-instances on the cluster of the fixture (or clusterId) as the token owner. */
async function postInstance(token: string, name: string, clusterId: string = fx.clusterId): Promise<APIResponse> {
  return fx.api.post(INSTANCES_URL, {
    headers: bearer(token),
    data: { stack_definition_id: fx.definitionId, name, branch: 'main', cluster_id: clusterId },
  });
}

/** Create an instance that must succeed; it is deleted in afterAll. */
async function createInstance(token: string, name: string, clusterId: string = fx.clusterId): Promise<string> {
  const res = await postInstance(token, name, clusterId);
  expect(res.status(), `create instance ${name}: ${await res.text()}`).toBe(201);
  const body = await res.json();
  fx.instanceIds.push(body.id);
  return body.id;
}

async function getInstance(id: string): Promise<{ id: string; name: string; cluster_id: string; namespace: string }> {
  const res = await fx.api.get(`${INSTANCES_URL}/${id}`, { headers: bearer(fx.adminToken) });
  expect(res.status()).toBe(200);
  return res.json();
}

/** An API response or a page response (both have status, text and json). */
type AnyResponse = Pick<APIResponse, 'status' | 'text' | 'json'>;

/** Check a 409 name conflict body (NamespaceConflictResponse). */
async function expectNameTaken(res: AnyResponse, holderName: string, holderOwner: string, suggestions: string[]) {
  expect(res.status(), await res.text()).toBe(409);
  expect(await res.json()).toEqual({
    error: NAME_TAKEN,
    message: takenMessage(holderName, holderOwner),
    suggestions,
  });
}

/** Open the app as the owner of token (the token goes to localStorage before the app runs). */
async function loginWithToken(page: Page, token: string): Promise<void> {
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
}

// ---------------------------------------------------------------------------
// Direct database change: an instance name with upper case letters. The API
// refuses such a name since the RFC 1123 rule; instances created before it
// keep theirs, and the uniqueness check must compare them without case.


/** Set the stored name of an instance (the uuid and the name are checked before they go into SQL). */
async function setInstanceNameInDb(instanceId: string, name: string): Promise<void> {
  if (!/^[0-9a-f-]{36}$/.test(instanceId)) throw new Error(`unexpected instance id ${instanceId}`);
  if (!/^[A-Za-z0-9-]{1,50}$/.test(name)) throw new Error(`unexpected instance name ${name}`);
  const stdout = await runE2eSql(`UPDATE stack_instances SET name='${name}' WHERE id='${instanceId}'; SELECT ROW_COUNT();`);
  expect(stdout.trim().split('\n').pop(), `rows changed by renaming ${instanceId} in the database`).toBe('1');
}

// ---------------------------------------------------------------------------
// Probe: does the backend run with UNIQUE_INSTANCE_NAMES=true?

const NEEDS_UNIQUE_NAMES =
  'Needs a backend with UNIQUE_INSTANCE_NAMES=true, as make test-e2e sets (the backend allowed one name twice on one cluster)';

/**
 * Read the second create of the probe (the same name on the same cluster, as
 * another user, so the namespaces differ). 409 "instance name already exists":
 * the rule is on. 201: the rule is off. Any other answer is a real failure.
 */
// probe-decision-start
function uniqueNamesEnforced(status: number, body: { error?: unknown }): boolean {
  if (status === 409 && body.error === NAME_TAKEN) return true;
  if (status === 201) return false;
  throw new Error(`UNIQUE_INSTANCE_NAMES probe: unexpected answer ${status} ${JSON.stringify(body)}`);
}
// probe-decision-end

/**
 * Create one name twice on the cluster (devops, then user) and delete what
 * the probe created. Returns true when the backend refused the second create.
 */
async function probeUniqueNames(
  api: APIRequestContext,
  adminToken: string,
  definitionId: string,
  clusterId: string,
  tokens: [string, string],
): Promise<boolean> {
  const name = shortName('un-probe');
  const created: string[] = [];
  try {
    const answers: { status: number; body: { id?: string; error?: unknown } }[] = [];
    for (const token of tokens) {
      const res = await api.post(INSTANCES_URL, {
        headers: bearer(token),
        data: { stack_definition_id: definitionId, name, branch: 'main', cluster_id: clusterId },
      });
      const body = await res.json();
      if (res.status() === 201) created.push(body.id);
      answers.push({ status: res.status(), body });
    }
    expect(answers[0].status, `probe create ${name}: ${JSON.stringify(answers[0].body)}`).toBe(201);
    return uniqueNamesEnforced(answers[1].status, answers[1].body);
  } finally {
    for (const id of created) {
      const res = await api.delete(`${INSTANCES_URL}/${id}`, { headers: bearer(adminToken) });
      expect([202, 204, 404], `delete probe instance ${id}`).toContain(res.status());
    }
  }
}

// ---------------------------------------------------------------------------

test.describe('Unique instance names per cluster (#587)', () => {
  test.beforeAll(async () => {
    const api = await playwrightRequest.newContext();
    const adminToken = await apiLogin(api);
    // A default cluster must exist, else a new cluster becomes the default.
    await ensureDefaultCluster(api, adminToken);

    const clusterName = uniqueName('e2e-un-cluster');
    const clusterRes = await api.post(`${API_BASE}/api/v1/clusters`, {
      headers: bearer(adminToken),
      data: { name: clusterName, api_server_url: 'https://kubernetes.default.svc', use_in_cluster: true },
    });
    expect(clusterRes.status(), await clusterRes.text()).toBe(201);
    const clusterId: string = (await clusterRes.json()).id;

    const devops = await createUser(api, adminToken, 'devops');
    const user = await createUser(api, adminToken, 'user');

    const definitionName = uniqueName('e2e-un-def');
    const definitionId = await apiCreateDefinition(api, devops.token, definitionName);

    // Without UNIQUE_INSTANCE_NAMES=true (a dev backend, for example) every
    // test would fail for the wrong reason: clean up and skip them all.
    if (!(await probeUniqueNames(api, adminToken, definitionId, clusterId, [devops.token, user.token]))) {
      await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: bearer(adminToken) });
      await api.delete(`${API_BASE}/api/v1/clusters/${clusterId}`, { headers: bearer(adminToken) });
      for (const u of [devops, user]) {
        await api.delete(`${API_BASE}/api/v1/users/${u.id}`, { headers: bearer(adminToken) });
      }
      await api.dispose();
      test.skip(true, NEEDS_UNIQUE_NAMES);
      return;
    }

    // A published template with one chart for Quick Deploy.
    const templateName = uniqueName('e2e-un-tpl');
    const tplRes = await api.post(`${API_BASE}/api/v1/templates`, {
      headers: bearer(devops.token),
      data: { name: templateName, description: 'Unique instance names', category: 'Web', version: '1.0.0' },
    });
    expect(tplRes.ok(), await tplRes.text()).toBe(true);
    const templateId: string = (await tplRes.json()).id;
    const chartRes = await api.post(`${API_BASE}/api/v1/templates/${templateId}/charts`, {
      headers: bearer(devops.token),
      data: { chart_name: 'web', repository_url: 'https://charts.example.com', chart_version: '1.0.0' },
    });
    expect(chartRes.ok(), await chartRes.text()).toBe(true);
    const pubRes = await api.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, { headers: bearer(devops.token) });
    expect(pubRes.ok(), await pubRes.text()).toBe(true);

    fx = {
      api, adminToken, devops, user, clusterId, clusterName,
      definitionId, definitionName, templateId, templateName, instanceIds: [],
    };
  });

  test.afterAll(async () => {
    if (!fx) return;
    const { api } = fx;
    const headers = bearer(await apiLogin(api));
    for (const id of fx.instanceIds) {
      const res = await api.delete(`${INSTANCES_URL}/${id}`, { headers });
      expect([202, 204, 404], `delete instance ${id}`).toContain(res.status());
    }
    await api.delete(`${API_BASE}/api/v1/templates/${fx.templateId}`, { headers });
    await api.delete(`${API_BASE}/api/v1/stack-definitions/${fx.definitionId}`, { headers });
    const clusterRes = await api.delete(`${API_BASE}/api/v1/clusters/${fx.clusterId}`, { headers });
    expect([204, 404], `delete cluster ${fx.clusterName}: ${await clusterRes.text()}`).toContain(clusterRes.status());
    for (const u of [fx.devops, fx.user]) {
      await api.delete(`${API_BASE}/api/v1/users/${u.id}`, { headers });
    }
    await api.dispose();
  });

  // Scenario 1
  test('create form: a taken name shows the owner and suggestions, a suggestion fills the name', async ({ page }) => {
    const taken = instanceName('un-create');
    await createInstance(fx.user.token, taken);

    await loginWithToken(page, fx.devops.token);
    await page.goto(`/stack-instances/new?definition=${fx.definitionId}`);
    await expect(page.getByRole('heading', { level: 1, name: 'Create Stack Instance' })).toBeVisible({ timeout: 10_000 });
    // ?definition= preselects the definition once the list has loaded.
    await expect(page.getByLabel('Stack Definition')).toContainText(fx.definitionName, { timeout: 10_000 });

    await page.getByLabel('Instance Name').fill(taken);
    await page.getByLabel('Cluster').click();
    await page.getByRole('option', { name: new RegExp(`^${fx.clusterName}`) }).click();

    const conflict = page.waitForResponse(
      (r) => r.url() === INSTANCES_URL && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await expectNameTaken(await conflict, taken, fx.user.username, suggestionsFor(taken));

    const alert = page.getByRole('alert');
    await expect(alert).toContainText(takenMessage(taken, fx.user.username), { timeout: 10_000 });
    for (const s of suggestionsFor(taken)) {
      await expect(alert.getByRole('button', { name: s, exact: true })).toBeVisible();
    }
    // The form stays on the page (no instance was created).
    await expect(page).toHaveURL(/\/stack-instances\/new/);

    // A click on a suggestion puts it into the name field and clears the error.
    const pick = `${taken}-3`;
    await alert.getByRole('button', { name: pick, exact: true }).click();
    await expect(page.getByLabel('Instance Name')).toHaveValue(pick);
    await expect(page.getByRole('alert')).toHaveCount(0);

    // The suggestion is free: the create succeeds and opens the new instance.
    const created = page.waitForResponse(
      (r) => r.url() === INSTANCES_URL && r.request().method() === 'POST',
    );
    await page.getByRole('button', { name: 'Create Instance' }).click();
    const createdRes = await created;
    expect(createdRes.status()).toBe(201);
    const createdBody = await createdRes.json();
    fx.instanceIds.push(createdBody.id);
    expect(createdBody).toMatchObject({ name: pick, cluster_id: fx.clusterId });
    await page.waitForURL(new RegExp(`/stack-instances/${createdBody.id}$`), { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: pick })).toBeVisible({ timeout: 10_000 });
  });

  test('create API: the name is unique for each cluster, not across clusters', async () => {
    const taken = instanceName('un-api');
    await createInstance(fx.user.token, taken);

    // Same cluster, another user: 409 with the holder and free suggestions.
    await expectNameTaken(await postInstance(fx.devops.token, taken), taken, fx.user.username, suggestionsFor(taken));

    // A suggestion that another instance holds on the cluster is left out.
    await createInstance(fx.user.token, `${taken}-2`);
    await expectNameTaken(
      await postInstance(fx.devops.token, taken),
      taken,
      fx.user.username,
      [`${taken}-3`, `${taken}-4`],
    );

    // Another cluster (the default cluster): the same name is free.
    const defaultClusters: { id: string; is_default: boolean }[] = await (
      await fx.api.get(`${API_BASE}/api/v1/clusters`, { headers: bearer(fx.adminToken) })
    ).json();
    const other = defaultClusters.find((c) => c.is_default && c.id !== fx.clusterId);
    expect(other, 'a default cluster other than the spec cluster').toBeDefined();
    const id = await createInstance(fx.devops.token, taken, other!.id);
    expect((await getInstance(id)).cluster_id).toBe(other!.id);
  });

  // Scenario 2
  test('the comparison ignores case (Web-Dev against web-dev)', async () => {
    test.skip(!e2eDbConfigured(), `Stores an upper case name, which the API refuses. ${E2E_DB_SKIP_REASON}`);
    const lower = instanceName('web-dev');
    const mixed = lower.replace(/^web-dev/, 'Web-Dev');
    const legacyId = await createInstance(fx.user.token, lower);
    await setInstanceNameInDb(legacyId, mixed);
    expect((await getInstance(legacyId)).name).toBe(mixed);

    // Create: the lower case name collides with the stored "Web-Dev-...".
    await expectNameTaken(await postInstance(fx.devops.token, lower), mixed, fx.user.username, suggestionsFor(lower));

    // Rename: the same rule.
    const own = await createInstance(fx.devops.token, instanceName('un-case'));
    const rename = await fx.api.put(`${INSTANCES_URL}/${own}`, {
      headers: bearer(fx.devops.token),
      data: { name: lower },
    });
    await expectNameTaken(rename, mixed, fx.user.username, suggestionsFor(lower));
  });

  // Scenario 3
  test('clone without a name skips the <name>-copy names taken on the cluster', async () => {
    const source = instanceName('un-clone');
    const sourceId = await createInstance(fx.user.token, source);
    // The devops user holds <source>-copy and <source>-copy-2 on the cluster.
    // Their namespaces are stack-<name>-<devops>, so the namespaces of the
    // clone (stack-<name>-<user>) are free: only the name rule skips them.
    await createInstance(fx.devops.token, `${source}-copy`);
    await createInstance(fx.devops.token, `${source}-copy-2`);

    const res = await fx.api.post(`${INSTANCES_URL}/${sourceId}/clone`, { headers: bearer(fx.user.token) });
    expect(res.status(), await res.text()).toBe(201);
    const clone = await res.json();
    fx.instanceIds.push(clone.id);
    expect(clone).toMatchObject({
      name: `${source}-copy-3`,
      cluster_id: fx.clusterId,
      namespace: `stack-${source}-copy-3-${fx.user.username}`,
    });

    // An explicit taken clone name gives the 409 of the name rule. The
    // suggestions <name>-copy-2 (devops) and <name>-copy-3 (the clone) are
    // taken on the cluster, so only <name>-copy-4 is left.
    const named = await fx.api.post(`${INSTANCES_URL}/${sourceId}/clone`, {
      headers: bearer(fx.user.token),
      data: { name: `${source}-copy` },
    });
    await expectNameTaken(named, `${source}-copy`, fx.devops.username, [`${source}-copy-4`]);
  });

  // Scenario 4
  test('quick deploy: a taken name shows the same message and the free names', async ({ page }) => {
    const taken = instanceName('un-quick');
    await createInstance(fx.user.token, taken);

    await loginWithToken(page, fx.devops.token);
    await page.goto('/templates');
    await expect(page.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({ timeout: 10_000 });
    await page.getByPlaceholder('Search templates...').fill(fx.templateName);
    await expect(page.getByRole('heading', { name: fx.templateName })).toBeVisible({ timeout: 10_000 });
    const card = page.locator('.MuiCard-root').filter({ hasText: fx.templateName });
    await card.getByRole('button', { name: 'Quick Deploy' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText(`Quick Deploy: ${fx.templateName}`)).toBeVisible({ timeout: 5_000 });
    await dialog.getByLabel('Instance Name').fill(taken);
    // The shared database has more than one cluster, so the dialog shows the select.
    await dialog.getByLabel('Cluster').click();
    await page.getByRole('option', { name: fx.clusterName, exact: true }).click();

    const conflict = page.waitForResponse(
      (r) => r.url() === `${API_BASE}/api/v1/templates/${fx.templateId}/quick-deploy` && r.request().method() === 'POST',
    );
    await dialog.getByRole('button', { name: 'Deploy' }).click();
    await expectNameTaken(await conflict, taken, fx.user.username, suggestionsFor(taken));

    // describeNameConflict: '<message>. Free names: a, b, c.'
    await expect(dialog.getByRole('alert')).toHaveText(
      `${takenMessage(taken, fx.user.username)}. Free names: ${suggestionsFor(taken).join(', ')}.`,
      { timeout: 10_000 },
    );
    await expect(dialog.getByRole('button', { name: 'Retry' })).toBeVisible();

    // Nothing was created: the name still has one holder on the cluster.
    const list = await fx.api.get(INSTANCES_URL, {
      headers: bearer(fx.adminToken),
      params: { name: taken, cluster_id: fx.clusterId },
    });
    expect(list.status()).toBe(200);
    const names = ((await list.json()).data as { name: string }[]).map((i) => i.name);
    expect(names.filter((n) => n === taken)).toHaveLength(1);
  });

  // Scenario 5
  test('rename: a taken name gives 409, the own name and a free name pass', async () => {
    const taken = instanceName('un-ren-b');
    await createInstance(fx.user.token, taken);
    const ownName = instanceName('un-ren-a');
    const ownId = await createInstance(fx.devops.token, ownName);
    const put = (name: string) =>
      fx.api.put(`${INSTANCES_URL}/${ownId}`, { headers: bearer(fx.devops.token), data: { name } });

    // A rename keeps the namespace, so the suggestions are only checked
    // against the names on the cluster.
    await expectNameTaken(await put(taken), taken, fx.user.username, suggestionsFor(taken));
    expect((await getInstance(ownId)).name).toBe(ownName);

    // The own name is not a conflict (the instance itself is excluded).
    const same = await put(ownName);
    expect(same.status(), await same.text()).toBe(200);
    expect((await same.json()).name).toBe(ownName);

    // A name that is taken only on another cluster is free on this one.
    const defaultClusters: { id: string; is_default: boolean }[] = await (
      await fx.api.get(`${API_BASE}/api/v1/clusters`, { headers: bearer(fx.adminToken) })
    ).json();
    const other = defaultClusters.find((c) => c.is_default && c.id !== fx.clusterId);
    expect(other, 'a default cluster other than the spec cluster').toBeDefined();
    const elsewhere = instanceName('un-ren-c');
    await createInstance(fx.user.token, elsewhere, other!.id);
    const moved = await put(elsewhere);
    expect(moved.status(), await moved.text()).toBe(200);
    expect(await getInstance(ownId)).toMatchObject({
      name: elsewhere,
      cluster_id: fx.clusterId,
      namespace: `stack-${ownName}-${fx.devops.username}`,
    });
  });
});
