import { Page, expect, APIRequestContext, APIResponse } from '@playwright/test';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const API_BASE = process.env.API_BASE_URL || 'http://localhost:8081';
export const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD || 'admin';

// ---------------------------------------------------------------------------
// Direct database changes (no API exists for them)
//
// `make test-e2e` and `make test-e2e-isolated` set E2E_DB_NAME, E2E_DB_USER and
// E2E_DB_PASSWORD (the database of the e2e backend in the docker compose
// `mysql` service). A manual `npx playwright test` usually has none of them:
// specs that need the database skip with E2E_DB_SKIP_REASON.

/** Repository root (docker compose runs there). This file is frontend/e2e/helpers.ts. */
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

/** Values that go into SQL: lowercase letters, digits and dashes only (uniqueName output). */
const SAFE_SQL_VALUE = /^[a-z0-9-]{1,100}$/;
/** Database name and user: passed as separate arguments, never into SQL text. */
const SAFE_DB_IDENTIFIER = /^[A-Za-z0-9_]{1,64}$/;

/** True when the specs may change database rows directly (E2E_DB_* are set). */
export function e2eDbConfigured(): boolean {
  return Boolean(process.env.E2E_DB_NAME && process.env.E2E_DB_USER && process.env.E2E_DB_PASSWORD !== undefined);
}

export const E2E_DB_SKIP_REASON =
  'Needs direct database access: set E2E_DB_NAME, E2E_DB_USER and E2E_DB_PASSWORD (make test-e2e sets them)';

function assertSafeSqlValue(name: string, value: string): void {
  if (!SAFE_SQL_VALUE.test(value)) {
    throw new Error(`${name} must match ${SAFE_SQL_VALUE} (got ${JSON.stringify(value)})`);
  }
}

/**
 * Run SQL in the e2e database through `docker compose exec -T mysql mysql ...`
 * (execFile, no shell) and return the raw output (`--batch`,
 * `--skip-column-names`). Use it for test setup and checks that have no API.
 * Callers build `sql` only from values checked with strict patterns (for
 * example assertSafeSqlValue, or a UUID pattern), never from free text.
 * Throws when the E2E_DB_* variables are not set (see e2eDbConfigured).
 */
export async function runE2eSql(sql: string): Promise<string> {
  const db = process.env.E2E_DB_NAME ?? '';
  const user = process.env.E2E_DB_USER ?? '';
  const password = process.env.E2E_DB_PASSWORD ?? '';
  if (!e2eDbConfigured()) throw new Error(E2E_DB_SKIP_REASON);
  if (!SAFE_DB_IDENTIFIER.test(db) || !SAFE_DB_IDENTIFIER.test(user)) {
    throw new Error('E2E_DB_NAME and E2E_DB_USER may contain only letters, digits and underscores');
  }
  // The password goes to the client through MYSQL_PWD (docker compose exec
  // -e MYSQL_PWD copies it from this process environment), never as an
  // argument: an argument would be visible in `ps` on the host.
  const args = [
    'compose', 'exec', '-T', '-e', 'MYSQL_PWD', 'mysql',
    'mysql', `-u${user}`, '--batch', '--skip-column-names', db,
    '-e', sql,
  ];
  return new Promise<string>((resolve, reject) => {
    execFile(
      'docker',
      args,
      { cwd: REPO_ROOT, timeout: 30_000, env: { ...process.env, MYSQL_PWD: password } },
      (error, out, err) => {
        if (error) {
          if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
            reject(new Error('docker compose is not available (ENOENT): install Docker or run the spec through make test-e2e'));
            return;
          }
          reject(new Error(`SQL in ${db} failed (exit ${error.code ?? '?'}): ${String(err).trim()}`));
          return;
        }
        resolve(String(out));
      },
    );
  });
}

/**
 * Run one SQL statement with runE2eSql and return the output of the
 * `SELECT ROW_COUNT()` that follows it: the number of changed rows.
 */
async function execE2eSql(sql: string): Promise<number> {
  const stdout = await runE2eSql(`${sql}; SELECT ROW_COUNT();`);
  const rows = parseInt(stdout.trim().split('\n').pop() ?? '', 10);
  if (!Number.isFinite(rows)) throw new Error(`Unexpected mysql output: ${JSON.stringify(stdout)}`);
  return rows;
}

/**
 * Mark a local user as an SSO (OIDC) user. The backend treats a user as SSO
 * when `auth_provider` is neither empty nor "local" (handlers/users.go); the
 * OIDC login writes "oidc". There is no API to create an SSO user, so create a
 * local user through the API first (with a uniqueName username) and convert it.
 * `externalId` defaults to `e2e-sso-<username>`.
 * Throws unless exactly one row changed.
 */
export async function markUserAsSSO(username: string, externalId: string = `e2e-sso-${username}`): Promise<void> {
  assertSafeSqlValue('username', username);
  assertSafeSqlValue('externalId', externalId);
  const rows = await execE2eSql(
    `UPDATE users SET auth_provider='oidc', external_id='${externalId}' WHERE username='${username}'`,
  );
  if (rows !== 1) throw new Error(`markUserAsSSO(${username}): ${rows} rows changed, want 1`);
}

/**
 * Enable a user directly in the database. Last-resort cleanup only (for
 * example to bring back `admin` when the API path to enable it failed); a
 * test should enable users through PUT /users/:id/enable.
 * Returns the number of changed rows (0 when the user was enabled already).
 */
export async function enableUserInDb(username: string): Promise<number> {
  assertSafeSqlValue('username', username);
  return execE2eSql(`UPDATE users SET disabled=0 WHERE username='${username}'`);
}

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
 * Assert that a login response succeeded. On failure the message has the
 * status and the response body (for example 401 "Session revoked" versus
 * "Invalid username or password"), so a flaky login shows its cause (#619).
 */
export async function expectLoginOk(res: APIResponse, username: string): Promise<void> {
  if (res.ok()) return;
  const body = await res.text().catch(() => '<unreadable body>');
  expect(res.ok(), `Login API failed for ${username} with status ${res.status()}: ${body.slice(0, 500)}`).toBe(true);
}

/**
 * Hide the setup wizard of the Dashboard for this page. The specs share one
 * database, and the Dashboard shows the wizard instead of the stack list
 * until the first instance exists (#628). A spec that checks the stack list
 * must not depend on the order of the specs. The login helpers call this.
 * The wizard spec logs in with its own helper and tests the wizard.
 */
export async function skipSetupWizard(page: Page): Promise<void> {
  await page.addInitScript(() => {
    localStorage.setItem('setupWizardDismissed', 'true');
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
  await expectLoginOk(res!, 'admin');
  const { token } = await res!.json();
  // Inject token before any page JS runs so AuthContext picks it up on mount
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
  await skipSetupWizard(page);
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
  await expectLoginOk(res!, username);
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
  await skipSetupWizard(page);
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
  await skipSetupWizard(page);
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
  await expectLoginOk(res!, username);
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

/**
 * Make sure the built-in `admin` can log in. last-admin.spec.ts disables
 * `admin` for a moment and enables it again in afterAll; a worker that dies
 * before its afterAll (SIGKILL, OOM) leaves `admin` disabled, and every later
 * spec would fail with 403 "Account disabled" for that reason. On a 403
 * "Account disabled", enable `admin` directly in the database (when E2E_DB_*
 * are set) and return a warning for the test output; else return null.
 * Throws when `admin` stays unusable.
 */
export async function ensureAdminEnabled(request: APIRequestContext): Promise<string | null> {
  const login = () => request.post(`${API_BASE}/api/v1/auth/login`, { data: { username: 'admin', password: ADMIN_PASSWORD } });
  let res = await login();
  for (let attempt = 0; res.status() === 429 && attempt < 5; attempt++) {
    await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)));
    res = await login();
  }
  if (res.status() === 200) return null;
  const body = (await res.json().catch(() => ({}))) as { error?: string };
  if (res.status() !== 403 || body.error !== 'Account disabled') {
    throw new Error(`admin login failed: ${res.status()} ${JSON.stringify(body)}`);
  }
  if (!e2eDbConfigured()) {
    throw new Error(`The built-in admin is disabled (an earlier last-admin run did not clean up?) and ${E2E_DB_SKIP_REASON}`);
  }
  const rows = await enableUserInDb('admin');
  const after = await login();
  expect(after.status(), 'admin login after enabling it in the database').toBe(200);
  return `WARNING: the built-in admin was disabled (an earlier last-admin.spec.ts run did not reach its afterAll?); enabled it in the database (${rows} row changed)`;
}
