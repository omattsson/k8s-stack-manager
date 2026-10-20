import { randomUUID } from 'node:crypto';
import { test, expect, APIRequestContext, Page, Request } from '@playwright/test';
import {
  API_BASE,
  E2E_DB_SKIP_REASON,
  apiCreateDefinition,
  apiLogin,
  e2eDbConfigured,
  ensureDefaultCluster,
  loginAsAdmin,
  runE2eSql,
  uniqueName,
} from './helpers';
import { mockDeployLog, MockDeployLog } from './deployLogMock';

/**
 * Deployment History (#626): DeploymentHistory and useDeployHistory on the
 * instance page, and GET /stack-instances/:id/deploy-log?view=summary.
 *
 * Without a cluster no deploy succeeds, so the UI scenarios mock the instance
 * (status running) and a history of 60 operations (deployLogMock.ts answers as
 * the backend does). The last scenario writes rows into the real database and
 * checks the real API.
 */

let api: APIRequestContext;
let adminToken: string;
let definitionId: string;
const createdInstances: string[] = [];

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

async function createInstance(): Promise<string> {
  const res = await api.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: auth(adminToken),
    data: { stack_definition_id: definitionId, name: uniqueName('e2e-dh'), branch: 'main' },
  });
  expect(res.status(), `create instance: ${await res.text()}`).toBe(201);
  const { id } = await res.json();
  createdInstances.push(id);
  return id;
}

/**
 * 60 operations, newest first, 5 minutes apart:
 * - a failed deploy (newer than the current deploy: "Last failure");
 * - the current deploy (branch main);
 * - 12 more successful deploys of branch main (one folded group);
 * - a stop;
 * - 9 times: deploy feature-a, stop, deploy feature-b, clean, failed deploy feature-c.
 */
function history(instanceId: string): MockDeployLog[] {
  const start = Date.parse('2026-10-19T12:00:00Z');
  const logs: MockDeployLog[] = [];
  const add = (action: MockDeployLog['action'], status: MockDeployLog['status'], branch?: string) => {
    const i = logs.length;
    const at = new Date(start - i * 5 * 60_000);
    logs.push({
      id: `${instanceId}-op${String(i).padStart(2, '0')}`,
      stack_instance_id: instanceId,
      action,
      status,
      branch,
      output: `${action} op${i}\n`,
      ...(status === 'error' && { error_message: `op${i} failed` }),
      started_by: 'admin',
      started_at: at.toISOString(),
      completed_at: new Date(at.getTime() + 60_000).toISOString(),
    });
  };
  add('deploy', 'error', 'feature-y');
  add('deploy', 'success', 'main');
  for (let i = 0; i < 12; i++) add('deploy', 'success', 'main');
  add('stop', 'success');
  for (let i = 0; i < 9; i++) {
    add('deploy', 'success', 'feature-a');
    add('stop', 'success');
    add('deploy', 'success', 'feature-b');
    add('clean', 'success');
    add('deploy', 'error', 'feature-c');
  }
  return logs;
}

/** Mock the instance (status running) and its history. Returns the list requests. */
async function openHistory(page: Page, instanceId: string, logs: MockDeployLog[]): Promise<Request[]> {
  await page.route(new RegExp(`/api/v1/stack-instances/${instanceId}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    return route.fulfill({ response: res, json: { ...(await res.json()), status: 'running' } });
  });
  const requests: Request[] = [];
  await mockDeployLog(page, instanceId, () => logs, requests);
  await page.goto(`/stack-instances/${instanceId}`);
  await expect(page.getByRole('heading', { name: 'Deployment History (60)' })).toBeVisible();
  return requests;
}

const historyRequests = (requests: Request[]) =>
  requests.map((r) => new URL(r.url()).searchParams).filter((p) => p.get('action') !== 'deploy' || p.get('status') !== 'success');

test.describe('Deployment History', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-dh-def'));
  });

  // The page can still refetch through a mocked route when a test ends.
  test.afterEach(async ({ page }) => {
    await page.unrouteAll({ behavior: 'ignoreErrors' });
  });

  test.afterAll(async () => {
    if (!api) return;
    for (const id of createdInstances) {
      await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    }
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    await api.dispose();
  });

  test('a long history shows pinned rows, 10 operations, a folded group and "Show more"', async ({ page }) => {
    const instanceId = await createInstance();
    const logs = history(instanceId);
    await loginAsAdmin(page);
    const requests = await openHistory(page, instanceId, logs);

    // The list asks for the summary view, 10 rows.
    const first = historyRequests(requests)[0];
    expect(first.get('view')).toBe('summary');
    expect(first.get('limit')).toBe('10');

    // The current deploy and the newer failure are pinned at the top of the section.
    const pinned = page.getByRole('list', { name: 'Pinned operations' });
    const current = pinned.getByTestId(`pinned-log-row-${logs[1].id}`);
    const failure = pinned.getByTestId(`pinned-log-row-${logs[0].id}`);
    // Put the heading at the top of the screen: the pinned rows must follow it
    // without a scroll (sections above it can push it to the bottom).
    await page.getByRole('heading', { name: 'Deployment History (60)' }).evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await expect(current).toBeInViewport();
    await expect(failure).toBeInViewport();
    await expect(pinned.getByText('Current', { exact: true })).toBeVisible();
    await expect(pinned.getByText('Last failure', { exact: true })).toBeVisible();

    // Page 1: the failure, the current deploy and 8 deploys of main in one group.
    const list = page.getByRole('list', { name: 'Operations', exact: true });
    await expect(page.getByText('Showing 10 of 60')).toBeVisible();
    await expect(list.getByRole('button', { name: /^8 deploys of branch main/ })).toBeVisible();

    // "Show more" loads the next page through the cursor. The group grows to 12.
    await page.getByRole('button', { name: 'Show more' }).click();
    await expect(page.getByText('Showing 20 of 60')).toBeVisible();
    const group = list.getByRole('button', { name: /^12 deploys of branch main/ });
    await expect(group).toBeVisible();
    expect(historyRequests(requests).some((p) => p.get('cursor') === '10')).toBe(true);

    // The group opens to show each deploy. A row loads its output once.
    await group.click();
    const nested = list.getByRole('list', { name: /^12 deploys of branch main/ });
    await expect(nested.getByRole('button', { name: /^deploy success/ })).toHaveCount(12);
    const target = logs[5];
    const entryCalls: string[] = [];
    page.on('request', (r) => {
      if (r.url().includes(`/deploy-log/${target.id}`)) entryCalls.push(r.url());
    });
    const row = nested.getByTestId(`deploy-log-row-${target.id}`);
    await row.click();
    await expect(page.getByTestId(`deploy-log-output-${target.id}`)).toContainText('deploy op5');
    // React StrictMode (dev server) can load it twice on the first open. A
    // second open uses the cache.
    const loads = entryCalls.length;
    expect(loads).toBeGreaterThan(0);
    await row.click();
    await row.click();
    await expect(page.getByTestId(`deploy-log-output-${target.id}`)).toContainText('deploy op5');
    expect(entryCalls).toHaveLength(loads);
  });

  test('the filters ask the API for the matching operations', async ({ page }) => {
    const instanceId = await createInstance();
    await loginAsAdmin(page);
    const requests = await openHistory(page, instanceId, history(instanceId));

    await page.getByRole('combobox', { name: 'Status' }).click();
    await page.getByRole('option', { name: 'Failed' }).click();
    await expect(page.getByRole('heading', { name: 'Deployment History (10 matching)' })).toBeVisible();
    expect(historyRequests(requests).at(-1)?.get('status')).toBe('error');
    const list = page.getByRole('list', { name: 'Operations', exact: true });
    await expect(list.getByRole('button', { name: /^deploy error/ })).toHaveCount(10);
    // The pinned rows stay: the highlights ignore the filters.
    await expect(page.getByRole('list', { name: 'Pinned operations' }).getByText('Current', { exact: true })).toBeVisible();

    await page.getByRole('combobox', { name: 'Action' }).click();
    await page.getByRole('option', { name: 'Stop' }).click();
    await expect(page.getByText('No operations match the filters.')).toBeVisible();
    const last = historyRequests(requests).at(-1);
    expect(last?.get('action')).toBe('stop');
    expect(last?.get('status')).toBe('error');

    await page.getByRole('combobox', { name: 'Status' }).click();
    await page.getByRole('option', { name: 'All' }).click();
    await page.getByRole('combobox', { name: 'Action' }).click();
    await page.getByRole('option', { name: 'All' }).click();
    await expect(page.getByRole('heading', { name: 'Deployment History (60)' })).toBeVisible();
  });

  test('the history fits a phone screen', async ({ page }) => {
    const instanceId = await createInstance();
    await page.setViewportSize({ width: 390, height: 844 });
    await loginAsAdmin(page);
    await openHistory(page, instanceId, history(instanceId));
    await page.getByRole('heading', { name: 'Deployment History (60)' }).scrollIntoViewIfNeeded();
    await expect(page.getByRole('list', { name: 'Pinned operations' }).getByText('Current', { exact: true })).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });

  test('the API: the summary view has no output, an entry has it, the default view keeps it', async () => {
    test.skip(!e2eDbConfigured(), E2E_DB_SKIP_REASON);
    const instanceId = await createInstance();
    const me = await (await api.get(`${API_BASE}/api/v1/auth/me`, { headers: auth(adminToken) })).json();
    // deployment_logs.id has 36 characters.
    const ids = { d1: randomUUID(), f1: randomUUID(), s1: randomUUID() };
    const row = (id: keyof typeof ids, action: string, status: string, at: string) =>
      `('${ids[id]}', '${instanceId}', '${action}', '${status}', 'output of ${id}', 'main', '${me.id}', '${at}', '${at}')`;
    await runE2eSql(
      'INSERT INTO deployment_logs (id, stack_instance_id, action, status, output, branch, user_id, started_at, completed_at) VALUES '
      + [
        row('d1', 'deploy', 'success', '2026-10-19 08:00:00'),
        row('f1', 'deploy', 'error', '2026-10-19 09:00:00'),
        row('s1', 'stop', 'success', '2026-10-19 10:00:00'),
      ].join(', '),
    );
    const url = `${API_BASE}/api/v1/stack-instances/${instanceId}/deploy-log`;

    const summary = await api.get(`${url}?view=summary&limit=2`, { headers: auth(adminToken) });
    expect(summary.status(), await summary.text()).toBe(200);
    const page1 = await summary.json();
    expect(page1.total).toBe(3);
    expect(page1.data.map((l: MockDeployLog) => l.id)).toEqual([ids.s1, ids.f1]);
    for (const l of page1.data) expect(l).not.toHaveProperty('output');
    expect(page1.data[0].started_by).toBe(me.username);
    expect(page1.highlights.current.id).toBe(ids.d1);
    expect(page1.highlights.latest_failure.id).toBe(ids.f1);
    expect(page1.next_cursor).toBeTruthy();

    const page2 = await (await api.get(`${url}?view=summary&limit=2&cursor=${page1.next_cursor}`, { headers: auth(adminToken) })).json();
    expect(page2.data.map((l: MockDeployLog) => l.id)).toEqual([ids.d1]);
    expect(page2).not.toHaveProperty('highlights');

    const failed = await (await api.get(`${url}?view=summary&status=error`, { headers: auth(adminToken) })).json();
    expect(failed.total).toBe(1);

    const entry = await (await api.get(`${url}/${ids.f1}`, { headers: auth(adminToken) })).json();
    expect(entry.output).toBe('output of f1');

    // stackctl reads the output of the default view.
    const full = await (await api.get(url, { headers: auth(adminToken) })).json();
    expect(full.data[0].output).toBe('output of s1');

    const bad = await api.get(`${url}?action=deploy,restart`, { headers: auth(adminToken) });
    expect(bad.status()).toBe(400);
    expect(await bad.json()).toEqual({ error: 'Invalid action parameter' });

    // An instance delete keeps its deploy logs (analytics). Remove the test rows.
    await runE2eSql(`DELETE FROM deployment_logs WHERE id IN ('${ids.d1}', '${ids.f1}', '${ids.s1}')`);
  });
});
