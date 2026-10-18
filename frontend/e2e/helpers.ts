import { Page, expect, APIRequestContext } from '@playwright/test';

export const API_BASE = process.env.API_BASE_URL || 'http://localhost:8081';
export const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD || 'admin';

/**
 * Mock a paged GET list endpoint with fixed items.
 * The UI sends the list requests with `?page=...&pageSize=...`, so the route
 * matches the path with or without a query string. It answers with the paged
 * shape `{data, page, pageSize, total}`. Other methods go to the backend.
 */
export async function mockListAPI(page: Page, path: string, items: unknown[]) {
  const escaped = path.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  await page.route(new RegExp(`${escaped}(\\?.*)?$`), (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: items,
        page: 1,
        pageSize: Math.max(items.length, 25),
        total: items.length,
      }),
    });
  });
}

/** Name of the shared default cluster of the e2e tests. */
const E2E_DEFAULT_CLUSTER = 'e2e-default-cluster';

/**
 * Ensure a default cluster exists. All specs share one cluster named
 * `e2e-default-cluster`, and no spec deletes it: the specs run in parallel
 * workers, and a cluster that one worker deletes in afterAll can still be in
 * use by another worker. When the cluster exists but another spec moved the
 * default flag, the helper makes it the default again.
 * Always returns null, so the deleteCluster calls in afterAll do nothing.
 * Accepts either a Playwright APIRequestContext (for API-level tests)
 * or a Page (for UI tests).
 */
export async function ensureDefaultCluster(
  requestOrPage: APIRequestContext | Page,
  token: string,
): Promise<string | null> {
  const req = 'request' in requestOrPage ? requestOrPage.request : requestOrPage;
  const headers = { Authorization: `Bearer ${token}` };

  for (let attempt = 0; attempt < 3; attempt++) {
    const listRes = await req.get(`${API_BASE}/api/v1/clusters`, { headers });
    expect(listRes.ok(), `Failed to list clusters: ${listRes.status()}`).toBe(true);
    const clusters: { id: string; name: string; is_default: boolean }[] = await listRes.json();
    if (clusters.some((c) => c.is_default)) return null;

    const shared = clusters.find((c) => c.name === E2E_DEFAULT_CLUSTER);
    if (shared) {
      const res = await req.post(`${API_BASE}/api/v1/clusters/${shared.id}/default`, { headers });
      expect(res.ok(), `Failed to make ${E2E_DEFAULT_CLUSTER} the default: ${res.status()}`).toBe(true);
      return null;
    }

    // Create the cluster with in-cluster config. Outside a pod, the backend has
    // no cluster access unless `make test-e2e` runs with E2E_KUBECONFIG.
    const createRes = await req.post(`${API_BASE}/api/v1/clusters`, {
      headers: { ...headers, 'Content-Type': 'application/json' },
      data: {
        name: E2E_DEFAULT_CLUSTER,
        api_server_url: 'https://kubernetes.default.svc',
        use_in_cluster: true,
        is_default: true,
      },
    });
    if (createRes.ok()) return null;
    // 409: another worker created it at the same time. Look again.
    expect(createRes.status(), `Failed to create default cluster: ${createRes.status()}`).toBe(409);
  }
  throw new Error(`No default cluster after 3 attempts`);
}

/**
 * Delete a cluster by ID. No-op if clusterID is null.
 */
export async function deleteCluster(
  requestOrPage: APIRequestContext | Page,
  token: string,
  clusterId: string | null,
): Promise<void> {
  if (!clusterId) return;
  const req = 'request' in requestOrPage ? requestOrPage.request : requestOrPage;
  await req.delete(`${API_BASE}/api/v1/clusters/${clusterId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

/**
 * Log in as admin by obtaining a JWT token via API and injecting it into localStorage.
 * This avoids the UI login flow to reduce API calls and rate-limiter pressure.
 * Does NOT navigate to any page — each test should navigate to its target page.
 * Retries on rate-limit (429) responses with backoff.
 */
export async function loginAsAdmin(page: Page) {
  let res;
  for (let attempt = 0; attempt < 5; attempt++) {
    res = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { username: 'admin', password: ADMIN_PASSWORD },
    });
    if (res.status() !== 429) break;
    await page.waitForTimeout(2000 * (attempt + 1));
  }
  expect(res!.ok(), `Login API failed with status ${res!.status()}`).toBe(true);
  const { token } = await res!.json();
  // Inject token before any page JS runs so AuthContext picks it up on mount
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
}

/**
 * Helper to perform a login API call with retry logic for 429 rate-limit responses.
 * Returns the JWT token string.
 */
async function loginViaAPI(page: Page, username: string, password: string): Promise<string> {
  let res;
  for (let attempt = 0; attempt < 5; attempt++) {
    res = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { username, password },
    });
    if (res.status() !== 429) break;
    await page.waitForTimeout(2000 * (attempt + 1));
  }
  expect(res!.ok(), `Login API failed for ${username} with status ${res!.status()}`).toBe(true);
  const { token } = await res!.json();
  return token;
}

/**
 * Helper to register a user via the API. Uses the admin token for auth.
 * If the user already exists (409), this is a no-op.
 */
async function ensureUserRegistered(
  page: Page,
  adminToken: string,
  username: string,
  password: string,
  role: string,
) {
  let res;
  for (let attempt = 0; attempt < 5; attempt++) {
    res = await page.request.post(`${API_BASE}/api/v1/auth/register`, {
      data: { username, password, role },
      headers: { Authorization: `Bearer ${adminToken}` },
    });
    if (res.status() !== 429) break;
    await page.waitForTimeout(2000 * (attempt + 1));
  }
  const status = res!.status();
  if (status !== 409 && !res!.ok()) {
    throw new Error(`Failed to register user ${username}: status ${status}`);
  }
}

const E2E_USER_USERNAME = `e2e-regular-user-${process.pid}`;
const E2E_DEVOPS_USERNAME = `e2e-devops-user-${process.pid}`;
const E2E_TEST_PASSWORD = 'e2e-test-password';

/**
 * Log in as a regular user (role: "user").
 * Creates the user on first call (or reuses if it already exists).
 * Injects the user's token into localStorage via addInitScript.
 * Returns the username.
 */
export async function loginAsUser(page: Page): Promise<string> {
  const adminToken = await loginViaAPI(page, 'admin', ADMIN_PASSWORD);
  await ensureUserRegistered(page, adminToken, E2E_USER_USERNAME, E2E_TEST_PASSWORD, 'user');
  const token = await loginViaAPI(page, E2E_USER_USERNAME, E2E_TEST_PASSWORD);
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
  return E2E_USER_USERNAME;
}

/**
 * Log in as a devops user (role: "devops").
 * Creates the user on first call (or reuses if it already exists).
 * Injects the user's token into localStorage via addInitScript.
 * Returns the username.
 */
export async function loginAsDevops(page: Page): Promise<string> {
  const adminToken = await loginViaAPI(page, 'admin', ADMIN_PASSWORD);
  await ensureUserRegistered(page, adminToken, E2E_DEVOPS_USERNAME, E2E_TEST_PASSWORD, 'devops');
  const token = await loginViaAPI(page, E2E_DEVOPS_USERNAME, E2E_TEST_PASSWORD);
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
  return E2E_DEVOPS_USERNAME;
}

/**
 * Generate a unique name with a prefix to avoid cross-test collisions.
 */
export function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
}

/**
 * Log in via the API and return the JWT token. Retries on rate-limit (429).
 * Defaults to the admin user. Accepts a Playwright APIRequestContext (the
 * `request` fixture or `page.request`).
 */
export async function apiLogin(
  request: APIRequestContext,
  username: string = 'admin',
  password: string = ADMIN_PASSWORD,
): Promise<string> {
  let res;
  for (let attempt = 0; attempt < 5; attempt++) {
    res = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { username, password },
    });
    if (res.status() !== 429) break;
    await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)));
  }
  expect(res!.ok(), `Login API failed for ${username} with status ${res!.status()}`).toBe(true);
  const { token } = await res!.json();
  return token;
}

/**
 * Read the JWT token that a loginAs* helper injected into browser localStorage.
 * The caller must navigate first (e.g. `await page.goto('/')`) so the init
 * script has run.
 */
export async function tokenFromPage(page: Page): Promise<string> {
  const token = await page.evaluate(() => localStorage.getItem('token'));
  expect(token, 'No token in localStorage — call a loginAs* helper and navigate first').toBeTruthy();
  return token as string;
}

/**
 * Create a stack definition via the API. Returns the definition ID.
 */
export async function apiCreateDefinition(
  request: APIRequestContext,
  token: string,
  name: string,
  defaultBranch: string = 'main',
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/stack-definitions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, description: `E2E override test definition ${name}`, default_branch: defaultBranch },
  });
  const body = await res.json();
  expect(res.ok(), `apiCreateDefinition failed: ${res.status()} ${JSON.stringify(body)}`).toBe(true);
  return body.id;
}

/**
 * Add a chart config to a definition via the API. Returns the chart config ID.
 * `default_values` may contain Helm template vars such as `{{.Branch}}`.
 */
export async function apiAddChart(
  request: APIRequestContext,
  token: string,
  definitionId: string,
  chart: {
    chart_name: string;
    repository_url?: string;
    chart_version?: string;
    default_values?: string;
  },
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/stack-definitions/${definitionId}/charts`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      repository_url: 'https://charts.example.com',
      chart_version: '1.0.0',
      ...chart,
    },
  });
  const body = await res.json();
  expect(res.ok(), `apiAddChart failed: ${res.status()} ${JSON.stringify(body)}`).toBe(true);
  return body.id;
}

/**
 * Create a stack instance via the API. Returns the instance ID.
 * The instance owner is the user identified by the token, so pass the same
 * token the browser uses when the UI must later mutate the instance.
 */
export async function apiCreateInstance(
  request: APIRequestContext,
  token: string,
  definitionId: string,
  name: string,
  branch: string = 'main',
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { stack_definition_id: definitionId, name, branch },
  });
  const body = await res.json();
  expect(res.ok(), `apiCreateInstance failed: ${res.status()} ${JSON.stringify(body)}`).toBe(true);
  return body.id;
}

/**
 * Create a template via the UI and return its name.
 * Navigates to /templates/new, fills the form, saves, and waits for the preview page.
 */
export async function createTemplate(page: Page, name: string): Promise<string> {
  await page.goto('/templates/new');
  await page.getByRole('textbox', { name: 'Name' }).fill(name);
  await page.getByRole('textbox', { name: 'Description' }).fill(`E2e test template ${name}`);
  // Pick a category
  await page.getByLabel('Category').click();
  await page.getByRole('option', { name: 'Web' }).click();
  await page.getByLabel('Version', { exact: true }).fill('1.0.0');
  await page.getByRole('button', { name: 'Save Template' }).click();
  // Wait for navigation to the preview page
  await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });
  await expect(page.getByRole('heading', { level: 1, name })).toBeVisible({ timeout: 10_000 });
  return name;
}

/**
 * Publish a template from its preview page. Assumes the page is already on /templates/:id.
 */
export async function publishTemplate(page: Page, templateId: string) {
  const token = await page.evaluate(() => localStorage.getItem('token'));
  await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

/**
 * Create a template with a chart, publish it, and return its ID.
 * Useful as a prerequisite for definition/instance tests.
 */
export async function createAndPublishTemplate(page: Page, name: string): Promise<string> {
  // Create template
  await page.goto('/templates/new');
  await page.getByRole('textbox', { name: 'Name' }).fill(name);
  await page.getByRole('textbox', { name: 'Description' }).fill(`E2e test template ${name}`);
  await page.getByLabel('Category').click();
  await page.getByRole('option', { name: 'Web' }).click();
  await page.getByLabel('Version', { exact: true }).fill('1.0.0');

  // Add a chart
  await page.getByRole('button', { name: 'Add Chart' }).click();
  await page.getByLabel('Chart Name').fill('my-chart');
  await page.getByLabel('Repository URL').fill('https://charts.example.com');

  await page.getByRole('button', { name: 'Save Template' }).click();
  // Wait for redirect to preview page (UUID in URL, not /templates/new)
  await page.waitForURL(/\/templates\/(?!new)[^/]+$/, { timeout: 10_000 });
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible({ timeout: 10_000 });

  // Extract template ID from URL
  const url = page.url();
  const templateId = url.split('/templates/')[1];

  // Publish via API (more reliable than UI toggle for test setup)
  const token = await page.evaluate(() => localStorage.getItem('token'));
  const response = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok()) {
    throw new Error(`Failed to publish template: ${response.status()}`);
  }

  return templateId;
}

/**
 * Instantiate a published template into a stack definition via the UI.
 * Returns the definition name.
 */
export async function instantiateTemplate(
  page: Page,
  templateId: string,
  defName: string,
): Promise<string> {
  await page.goto(`/templates/${templateId}/use`);
  await expect(page.getByRole('heading', { level: 1, name: /Use Template/ })).toBeVisible({
    timeout: 10_000,
  });

  // Clear the pre-filled name and set our own
  const nameField = page.getByLabel('Definition Name');
  await nameField.clear();
  await nameField.fill(defName);

  await page.getByRole('button', { name: 'Create Stack Definition' }).click();
  await page.waitForURL(/\/stack-definitions\/[^/]+\/edit/, { timeout: 10_000 });

  return defName;
}
