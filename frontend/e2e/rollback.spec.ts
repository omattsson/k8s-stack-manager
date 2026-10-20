import fs from 'node:fs';
import path from 'node:path';
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

/**
 * Rollback (#590): POST /stack-instances/:id/rollback and RollbackDialog.tsx.
 *
 * Without a cluster no deploy succeeds, so the UI scenarios mock the instance
 * (status running) and its deploy log list with page.route. The 404 and 409
 * answers come from the real backend: the spec sets the status of a real
 * instance in the database (runE2eSql). A real rollback (scenario 5) needs
 * E2E_KUBECONFIG and deploys e2e/fixtures/charts/e2e-rollback.
 *
 * The dialog has no "previous revision" option: it always sends a
 * target_log_id. Its default target is the successful deploy before the
 * current one.
 */

const MSG_TARGET_NOT_FOUND = 'Rollback target deployment log not found';
const WARNING_DRIFT = 'The stored overrides differ from the running values. The next deploy applies the stored overrides again.';
const ALERT_DRIFT = 'The running values differ from the stored overrides (after a rollback). The next deploy applies the stored overrides.';
const REJECT_REASON = 'pre-rollback hook "e2e-gate" denied the rollback: change freeze until 18:00';

interface MockLog {
  id: string;
  stack_instance_id: string;
  action: 'deploy' | 'rollback';
  status: 'success' | 'error';
  output: string;
  error_message?: string;
  branch?: string;
  target_log_id?: string;
  started_at: string;
  completed_at: string;
}

let api: APIRequestContext;
let adminToken: string;
let definitionId: string;
const createdInstances: string[] = [];

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

async function createInstance(): Promise<{ id: string; name: string }> {
  const name = uniqueName('e2e-rb');
  const res = await api.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: auth(adminToken),
    data: { stack_definition_id: definitionId, name, branch: 'main' },
  });
  expect(res.status(), `create instance: ${await res.text()}`).toBe(201);
  const { id } = await res.json();
  createdInstances.push(id);
  return { id, name };
}

/** Three successful deploys (D1 oldest, D3 current) and a failed one, newest first. */
function deployLogs(instanceId: string): MockLog[] {
  const log = (id: string, status: 'success' | 'error', branch: string, at: string): MockLog => ({
    id,
    stack_instance_id: instanceId,
    action: 'deploy',
    status,
    output: `deploy ${id}\n`,
    branch,
    started_at: at,
    completed_at: at.replace(':00Z', ':45Z'),
  });
  return [
    log(`${instanceId}-d3`, 'success', 'main', '2026-10-19T10:00:00Z'),
    log(`${instanceId}-f2`, 'error', 'broken', '2026-10-19T09:30:00Z'),
    log(`${instanceId}-d2`, 'success', 'feature-x', '2026-10-19T09:00:00Z'),
    log(`${instanceId}-d1`, 'success', 'main', '2026-10-19T08:00:00Z'),
  ];
}

/**
 * Mock the instance (status running, optional fields) and its deploy log list.
 * `state` is read on each request, so a test can change the answers after the
 * rollback request.
 */
async function mockInstance(
  page: Page,
  instanceId: string,
  state: { logs: MockLog[]; patch: Record<string, unknown> },
) {
  await page.route(new RegExp(`/api/v1/stack-instances/${instanceId}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    const body = { ...(await res.json()), status: 'running', ...state.patch };
    return route.fulfill({ response: res, json: body });
  });
  await page.route(new RegExp(`/api/v1/stack-instances/${instanceId}/deploy-log(\\?.*)?$`), (route) =>
    route.fulfill({ json: { data: state.logs, total: state.logs.length } }),
  );
}

async function openRollbackDialog(page: Page, instanceId: string) {
  await page.goto(`/stack-instances/${instanceId}`);
  await page.getByRole('button', { name: 'Rollback', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Roll Back Instance' });
  await expect(dialog).toBeVisible();
  return dialog;
}

test.describe('Rollback', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-rb-def'));
    // The rollback checks need a chart (400 "No charts configured" otherwise).
    const chart = await api.post(`${API_BASE}/api/v1/stack-definitions/${definitionId}/charts`, {
      headers: auth(adminToken),
      data: { chart_name: 'e2e-rb-chart', repository_url: 'https://charts.example.com', chart_version: '1.0.0' },
    });
    expect(chart.status(), await chart.text()).toBe(201);
  });

  // The page can still refetch through a mocked route when a test ends.
  test.afterEach(async ({ page }) => {
    await page.unrouteAll({ behavior: 'ignoreErrors' });
  });

  test.afterAll(async () => {
    if (!api) return;
    for (const id of createdInstances) {
      // A running instance would start a clean (no cluster): make it a draft first.
      if (e2eDbConfigured()) await runE2eSql(`UPDATE stack_instances SET status='draft' WHERE id='${id}'`);
      await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    }
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    await api.dispose();
  });

  test('the dialog lists the successful deploys; the default is the deploy before the current one', async ({ page }) => {
    const inst = await createInstance();
    const state = { logs: deployLogs(inst.id), patch: {} as Record<string, unknown> };
    await loginAsAdmin(page);
    await mockInstance(page, inst.id, state);
    let posted: Request | undefined;
    await page.route(`**/api/v1/stack-instances/${inst.id}/rollback`, (route) => {
      posted = route.request();
      return route.fulfill({
        status: 202,
        json: { log_id: `${inst.id}-r1`, message: 'Rollback started', target_log_id: `${inst.id}-d1` },
      });
    });

    const dialog = await openRollbackDialog(page, inst.id);
    // Only the three successful deploys are targets, newest first.
    const radios = dialog.getByRole('radio');
    await expect(radios).toHaveCount(3);
    await expect(radios.nth(0)).toHaveAccessibleName(/\(branch main\).*current/);
    await expect(radios.nth(1)).toHaveAccessibleName(/\(branch feature-x\)/);
    await expect(radios.nth(2)).toHaveAccessibleName(/\(branch main\)/);
    await expect(radios.nth(0)).toBeDisabled();
    await expect(radios.nth(1)).toBeChecked();
    await expect(dialog.getByText(new RegExp(`Roll back "${inst.name}" to the deploy of .*\\(branch feature-x\\)\\?`))).toBeVisible();
    await expect(dialog.getByText('The rollback does not change the stored overrides. The next deploy applies them again.')).toBeVisible();

    // Pick the oldest deploy and confirm.
    await radios.nth(2).check();
    await dialog.getByRole('button', { name: 'Roll Back' }).click();
    await expect(page.getByText('Rollback started. Follow the deployment log.')).toBeVisible();
    expect(posted?.postDataJSON()).toEqual({ target_log_id: `${inst.id}-d1` });
    await expect(page.getByText(ALERT_DRIFT)).toHaveCount(0);
  });

  test('"Roll back to this deploy" in the history opens the dialog with that target', async ({ page }) => {
    const inst = await createInstance();
    await loginAsAdmin(page);
    await mockInstance(page, inst.id, { logs: deployLogs(inst.id), patch: {} });
    await page.goto(`/stack-instances/${inst.id}`);

    // Open the row of the oldest successful deploy (branch main, 08:00).
    const rows = page.getByRole('button', { name: /deploy\s*success/i });
    await expect(rows).toHaveCount(3);
    await rows.nth(2).click();
    await page.getByRole('button', { name: 'Roll back to this deploy' }).click();
    const dialog = page.getByRole('dialog', { name: 'Roll Back Instance' });
    await expect(dialog.getByRole('radio').nth(2)).toBeChecked();
  });

  test('a rollback with values drift shows the warning and the drift alert', async ({ page }) => {
    const inst = await createInstance();
    const state = { logs: deployLogs(inst.id), patch: {} as Record<string, unknown> };
    await loginAsAdmin(page);
    await mockInstance(page, inst.id, state);
    await page.route(`**/api/v1/stack-instances/${inst.id}/rollback`, (route) => {
      // After the rollback the API reports the drift on the instance too.
      state.patch = { values_drift: true };
      return route.fulfill({
        status: 202,
        json: {
          log_id: `${inst.id}-r1`,
          message: 'Rollback started',
          target_log_id: `${inst.id}-d2`,
          values_drift: true,
          warning: WARNING_DRIFT,
        },
      });
    });

    const dialog = await openRollbackDialog(page, inst.id);
    await dialog.getByRole('button', { name: 'Roll Back' }).click();
    await expect(page.getByText(`Rollback started. Follow the deployment log. ${WARNING_DRIFT}`)).toBeVisible();
    await expect(page.getByText(ALERT_DRIFT)).toBeVisible();
  });

  test('a deleted or unknown target gives 404, a draft gives 409 (real backend)', async ({ page }) => {
    test.skip(!e2eDbConfigured(), E2E_DB_SKIP_REASON);
    const inst = await createInstance();
    const rollback = (target: string) =>
      api.post(`${API_BASE}/api/v1/stack-instances/${inst.id}/rollback`, {
        headers: auth(adminToken),
        data: { target_log_id: target },
      });

    // A draft cannot roll back.
    const draft = await rollback(`${inst.id}-d2`);
    expect(draft.status(), await draft.text()).toBe(409);
    expect((await draft.json()).error).toBe('Cannot rollback: instance is in state draft');

    // A running instance with a target that does not exist (deleted by the log retention, for example).
    await runE2eSql(`UPDATE stack_instances SET status='running' WHERE id='${inst.id}'`);
    const gone = await rollback('00000000-0000-4000-8000-000000000000');
    expect(gone.status(), await gone.text()).toBe(404);
    expect(await gone.json()).toEqual({ error: MSG_TARGET_NOT_FOUND });

    // The UI shows the message of the server. Only the list of targets is mocked.
    await loginAsAdmin(page);
    await page.route(new RegExp(`/api/v1/stack-instances/${inst.id}/deploy-log(\\?.*)?$`), (route) =>
      route.fulfill({ json: { data: deployLogs(inst.id), total: 4 } }),
    );
    const dialog = await openRollbackDialog(page, inst.id);
    await dialog.getByRole('button', { name: 'Roll Back' }).click();
    await expect(page.getByText(`Failed to start rollback (HTTP 404: ${MSG_TARGET_NOT_FOUND})`)).toBeVisible();
  });

  test('a rollback that a pre-rollback hook rejects shows the hook reason', async ({ page }) => {
    const inst = await createInstance();
    const state = { logs: deployLogs(inst.id), patch: {} as Record<string, unknown> };
    await loginAsAdmin(page);
    await mockInstance(page, inst.id, state);
    await page.route(`**/api/v1/stack-instances/${inst.id}/rollback`, (route) => {
      // The hook runs after the 202: the rollback log ends with the reason, and
      // the instance keeps its status (running).
      state.logs = [
        {
          id: `${inst.id}-r1`,
          stack_instance_id: inst.id,
          action: 'rollback',
          status: 'error',
          output: `Running pre-rollback step "e2e-gate" ...\nERROR: ${REJECT_REASON}\n`,
          error_message: REJECT_REASON,
          target_log_id: `${inst.id}-d2`,
          started_at: '2026-10-19T10:30:00Z',
          completed_at: '2026-10-19T10:30:05Z',
        },
        ...deployLogs(inst.id),
      ];
      return route.fulfill({ status: 202, json: { log_id: `${inst.id}-r1`, message: 'Rollback started', target_log_id: `${inst.id}-d2` } });
    });

    const dialog = await openRollbackDialog(page, inst.id);
    await dialog.getByRole('button', { name: 'Roll Back' }).click();
    await expect(page.getByText('Rollback started. Follow the deployment log.')).toBeVisible();
    await expect(page.getByText('Deployment History (5)')).toBeVisible();
    // The history keeps the row that was open (the current deploy). Open the
    // rejected rollback: it shows the reason of the hook.
    await page.getByRole('button', { name: /rollback\s*error/i }).click();
    await expect(page.getByText(`Error: ${REJECT_REASON}`)).toBeVisible();
  });

  test('a real rollback restores the values snapshot of the target (E2E_KUBECONFIG)', async () => {
    test.skip(!process.env.E2E_KUBECONFIG, 'Needs a local cluster: set E2E_KUBECONFIG (see make test-e2e)');
    test.setTimeout(600_000);

    // A relative E2E_KUBECONFIG is relative to the repository root (as in the Makefile).
    const kc = process.env.E2E_KUBECONFIG!;
    const kubeconfig = fs.readFileSync(path.isAbsolute(kc) ? kc : path.resolve('..', kc), 'utf8');
    const server = /server:\s*(\S+)/.exec(kubeconfig)?.[1] ?? 'https://127.0.0.1:6443';
    const clusterRes = await api.post(`${API_BASE}/api/v1/clusters`, {
      headers: auth(adminToken),
      data: { name: uniqueName('e2e-rb-cluster'), api_server_url: server, kubeconfig_data: kubeconfig, is_default: false },
    });
    expect(clusterRes.status(), await clusterRes.text()).toBe(201);
    const clusterId = (await clusterRes.json()).id;

    const defId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-rb-real-def'));
    const chartRes = await api.post(`${API_BASE}/api/v1/stack-definitions/${defId}/charts`, {
      headers: auth(adminToken),
      data: {
        chart_name: 'e2e-rollback',
        repository_url: '',
        chart_path: path.resolve('e2e/fixtures/charts/e2e-rollback'),
        chart_version: '',
        default_values: 'value: base\n',
      },
    });
    expect(chartRes.status(), await chartRes.text()).toBe(201);
    const chartId = (await chartRes.json()).id;

    const instRes = await api.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(adminToken),
      data: { stack_definition_id: defId, name: uniqueName('e2e-rb-real'), branch: 'main', cluster_id: clusterId },
    });
    expect(instRes.status(), await instRes.text()).toBe(201);
    const instanceId = (await instRes.json()).id;
    const instUrl = `${API_BASE}/api/v1/stack-instances/${instanceId}`;

    const waitForStatus = async (want: string) => {
      await expect.poll(async () => (await (await api.get(instUrl, { headers: auth(adminToken) })).json()).status, {
        timeout: 240_000,
        intervals: [2000],
      }).toBe(want);
    };
    const deployWith = async (value: string) => {
      const ov = await api.put(`${instUrl}/overrides/${chartId}`, { headers: auth(adminToken), data: { values: `value: ${value}\n` } });
      expect(ov.ok(), await ov.text()).toBe(true);
      const dep = await api.post(`${instUrl}/deploy`, { headers: auth(adminToken) });
      expect(dep.status(), await dep.text()).toBe(202);
      await waitForStatus('running');
      return (await dep.json()).log_id as string;
    };
    const snapshot = async (logId: string) =>
      (await (await api.get(`${instUrl}/deploy-log/${logId}/values`, { headers: auth(adminToken) })).json()).values;

    try {
      const first = await deployWith('one');
      await deployWith('two');

      const rb = await api.post(`${instUrl}/rollback`, { headers: auth(adminToken), data: { target_log_id: first } });
      expect(rb.status(), await rb.text()).toBe(202);
      const rollbackLogId = (await rb.json()).log_id as string;
      await waitForStatus('running');
      await expect.poll(async () => {
        const logs = (await (await api.get(`${instUrl}/deploy-log`, { headers: auth(adminToken) })).json()).data as MockLog[];
        return logs.find((l) => l.id === rollbackLogId)?.status;
      }, { timeout: 120_000 }).toBe('success');

      // The rollback ran the values of the first deploy; the stored override ("two") now differs.
      expect(await snapshot(rollbackLogId)).toEqual(await snapshot(first));
      expect(JSON.stringify(await snapshot(rollbackLogId))).toContain('one');
      const inst = await (await api.get(instUrl, { headers: auth(adminToken) })).json();
      expect(inst.values_drift).toBe(true);
    } finally {
      await api.delete(instUrl, { headers: auth(adminToken) });
      await expect.poll(async () => (await api.get(instUrl, { headers: auth(adminToken) })).status(), { timeout: 240_000 }).toBe(404);
      await api.delete(`${API_BASE}/api/v1/stack-definitions/${defId}`, { headers: auth(adminToken) });
      await api.delete(`${API_BASE}/api/v1/clusters/${clusterId}`, { headers: auth(adminToken) });
    }
  });
});
