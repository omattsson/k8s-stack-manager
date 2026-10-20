import { test, expect, APIRequestContext, Locator, Page } from '@playwright/test';
import { API_BASE, apiCreateDefinition, apiLogin, ensureDefaultCluster, uniqueName } from './helpers';
import { STUB_HTTP_PORT, stubHealth, stubRecords } from './stub/client';

/**
 * Action run history of the instance page (#640): the "Action runs" section
 * lists the custom action runs of an instance, newest first.
 *
 * Uses the e2e actions of e2e/fixtures/hooks-e2e.json and the stub
 * (e2e/stub/server.ts), as custom-actions.spec.ts does. Without them the
 * specs skip. Each test uses its own draft instance, so the rows are known.
 */

const PASSWORD = 'e2e-test-password';
/** Rows of one page of the "Action runs" list. */
const PAGE_SIZE = 10;
const REDIRECT_LEAKS = ['stub-redirect-secret', `127.0.0.1:${STUB_HTTP_PORT}`];

interface Actor {
  id: string;
  username: string;
  token: string;
}

interface ActionRun {
  id: string;
  instance_id: string;
  action: string;
  action_label: string;
  user_id: string;
  started_by?: string;
  started_at: string;
  finished_at?: string;
  parameters: Record<string, string | boolean>;
  status_code: number;
  message?: string;
  job_id?: string;
  job_status?: string;
  has_job_log: boolean;
  request_id?: string;
}

interface ActionRunPage {
  instance_id: string;
  can_view_logs: boolean;
  data: ActionRun[];
  next_cursor?: string;
}

interface InvokeEnvelope {
  action: string;
  instance_id: string;
  status_code: number;
  result: unknown;
  job_id?: string;
  message?: string;
  run_id?: string;
}

let api: APIRequestContext;
let adminToken: string;
let owner: Actor;
let viewer: Actor;
let definitionId: string;
/** Instances of the tests; afterAll deletes them. */
const instances: string[] = [];
let skipReason = '';

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const instanceUrl = (id: string) => `${API_BASE}/api/v1/stack-instances/${id}`;

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

/** Create a draft instance of the owner. */
async function newInstance(): Promise<string> {
  const res = await api.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: auth(owner.token),
    data: { stack_definition_id: definitionId, name: uniqueName('e2e-ar'), branch: 'main' },
  });
  expect(res.status(), `create instance: ${await res.text()}`).toBe(201);
  const id = (await res.json()).id as string;
  instances.push(id);
  return id;
}

/** Run an action through the API (as the owner) and return the envelope. */
async function invoke(instanceId: string, action: string, parameters: Record<string, unknown> = {}): Promise<InvokeEnvelope> {
  const res = await api.post(`${instanceUrl(instanceId)}/actions/${action}`, {
    headers: auth(owner.token),
    data: { parameters },
  });
  const text = await res.text();
  expect(res.status(), `invoke ${action}: ${text}`).toBe(200);
  const env = JSON.parse(text) as InvokeEnvelope;
  expect(env.run_id, `invoke ${action}: run_id`).toBeTruthy();
  return env;
}

/** GET the action runs of an instance: the raw body and the parsed page. */
async function listRuns(instanceId: string, token = owner.token, query = ''): Promise<{ text: string; runs: ActionRunPage }> {
  const res = await api.get(`${instanceUrl(instanceId)}/action-runs${query}`, { headers: auth(token) });
  const text = await res.text();
  expect(res.status(), `action runs: ${text}`).toBe(200);
  return { text, runs: JSON.parse(text) as ActionRunPage };
}

/** Open the instance page and return the "Action runs" section. */
async function openInstance(page: Page, instanceId: string, token = owner.token): Promise<Locator> {
  await page.addInitScript((t) => localStorage.setItem('token', t), token);
  await page.goto(`/stack-instances/${instanceId}`);
  const section = page.getByTestId('action-runs');
  await expect(section).toBeVisible({ timeout: 15000 });
  await expect(section.getByRole('heading', { name: 'Action runs' })).toBeVisible();
  return section;
}

/** Open the header "Actions" menu and the dialog of one custom action. */
async function openAction(page: Page, label: string): Promise<Locator> {
  await page.locator('#instance-actions-button').click();
  await page.getByRole('menu', { name: 'Actions' }).getByRole('menuitem', { name: new RegExp(`^${label}`) }).click();
  const dialog = page.getByRole('dialog', { name: label, exact: true });
  await expect(dialog).toBeVisible();
  return dialog;
}

/** Click Run in an action dialog and return the invoke envelope. */
async function runInDialog(page: Page, dialog: Locator, instanceId: string, action: string): Promise<InvokeEnvelope> {
  const response = page.waitForResponse((r) =>
    r.request().method() === 'POST' && r.url().endsWith(`/stack-instances/${instanceId}/actions/${action}`));
  await dialog.getByRole('button', { name: 'Run' }).click();
  const res = await response;
  expect(res.status(), `invoke ${action}`).toBe(200);
  const env = (await res.json()) as InvokeEnvelope;
  expect(env.run_id, `invoke ${action}: run_id`).toBeTruthy();
  return env;
}

/** The rows of the list, in page order. */
const listRows = (section: Locator) => section.getByTestId('action-runs-list').getByTestId(/^action-run-row-/);

/** Mark the page; a reload removes the mark. */
const markPage = (page: Page) =>
  page.evaluate(() => { (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload = true; });

const expectNoReload = async (page: Page) => {
  const marked = await page.evaluate(() => (window as unknown as { __e2eNoReload?: boolean }).__e2eNoReload === true);
  expect(marked, 'the page did not reload').toBe(true);
};

test.describe('Action run history', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);
    owner = await registerActor('user', 'e2e-ar-owner');
    viewer = await registerActor('user', 'e2e-ar-viewer');
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-ar-def'));

    const probe = await newInstance();
    const list = await api.get(`${instanceUrl(probe)}/actions`, { headers: auth(adminToken) });
    const names = (((await list.json()).actions ?? []) as { name: string }[]).map((a) => a.name);
    if (!names.includes('e2e-echo')) {
      skipReason = 'The backend has no e2e actions: start it with HOOKS_CONFIG_FILE=frontend/e2e/fixtures/hooks-e2e.json (make test-e2e does).';
    } else if (!(await stubHealth(api))) {
      skipReason = 'The e2e stub does not run on 127.0.0.1:18090 (see e2e/stub/server.ts).';
    }
  });

  test.beforeEach(() => {
    test.skip(!!skipReason, skipReason);
  });

  test.afterAll(async () => {
    if (!api) return;
    for (const id of instances) {
      await api.delete(instanceUrl(id), { headers: auth(adminToken) });
    }
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    for (const actor of [owner, viewer]) {
      if (actor) await api.delete(`${API_BASE}/api/v1/users/${actor.id}`, { headers: auth(adminToken) });
    }
    await api.dispose();
  });

  test('a run from the Actions menu shows on top of the list without a reload', async ({ page }) => {
    const instanceId = await newInstance();
    const older = await invoke(instanceId, 'e2e-echo', { target: uniqueName('e2e-ar-older'), mode: 'fast' });
    const section = await openInstance(page, instanceId);
    await expect(listRows(section)).toHaveCount(1);
    await markPage(page);

    const target = uniqueName('e2e-ar-target');
    const dialog = await openAction(page, 'E2E echo');
    await dialog.getByLabel(/Target/).fill(target);
    await dialog.getByRole('switch', { name: 'Dry run' }).uncheck();
    await dialog.getByRole('combobox', { name: /Mode/ }).click();
    await page.getByRole('option', { name: 'full' }).click();
    const env = await runInDialog(page, dialog, instanceId, 'e2e-echo');
    await expect(dialog.getByTestId('action-status')).toHaveText('E2E echo returned status 200.');
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();

    const id = env.run_id!;
    await expect(listRows(section)).toHaveCount(2);
    await expect(listRows(section).first()).toHaveAttribute('data-testid', `action-run-row-${id}`);
    await expect(listRows(section).last()).toHaveAttribute('data-testid', `action-run-row-${older.run_id}`);
    const row = section.getByTestId(`action-run-row-${id}`);
    await expect(row).toContainText('E2E echo');
    await expect(row).toContainText(owner.username);
    await expect(row).toContainText(/just now|\d+m ago/);
    const params = section.getByTestId(`action-run-params-${id}`);
    await expect(params).toContainText(`target: ${target}`);
    await expect(params).toContainText('dry_run: false');
    await expect(params).toContainText('mode: full');
    await expect(section.getByTestId(`action-run-status-${id}`)).toHaveText('OK (200)');
    await expect(section.getByTestId(`action-run-show-log-${id}`)).toHaveCount(0);
    await expectNoReload(page);
  });

  test('undeclared parameters are never stored', async ({ page }) => {
    const instanceId = await newInstance();
    const target = uniqueName('e2e-ar-target');
    const extra = uniqueName('e2e-undeclared');
    const env = await invoke(instanceId, 'e2e-echo', { target, dry_run: true, mode: 'fast', extra });
    expect(env.status_code).toBe(200);

    // The subscriber gets the undeclared parameter; only the history drops it.
    const recs = (await stubRecords(api, '/actions/echo')).filter((r) => r.body.includes(target));
    expect(recs).toHaveLength(1);
    expect(JSON.parse(recs[0].body).parameters.extra).toBe(extra);

    const { text, runs } = await listRuns(instanceId);
    expect(text).not.toContain(extra);
    const run = runs.data.find((r) => r.id === env.run_id);
    expect(run, 'the run of the invoke').toBeTruthy();
    expect(run!.parameters).toEqual({ target, dry_run: true, mode: 'fast' });

    const section = await openInstance(page, instanceId);
    const params = section.getByTestId(`action-run-params-${env.run_id}`);
    await expect(params).toContainText(`target: ${target}`);
    await expect(params).not.toContainText('extra');
    expect(await page.content()).not.toContain(extra);
  });

  test('a sensitive parameter reaches the subscriber but the history shows [redacted]', async ({ page }) => {
    const instanceId = await newInstance();
    const section = await openInstance(page, instanceId);
    const target = uniqueName('e2e-ar-target');
    const secret = uniqueName('e2e-secret');

    const dialog = await openAction(page, 'E2E echo');
    const token = dialog.getByLabel('Token', { exact: true });
    await expect(token).toHaveAttribute('type', 'password');
    await dialog.getByLabel(/Target/).fill(target);
    await token.fill(secret);
    const env = await runInDialog(page, dialog, instanceId, 'e2e-echo');
    await expect(dialog.getByTestId('action-status')).toHaveText('E2E echo returned status 200.');
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();

    const recs = (await stubRecords(api, '/actions/echo')).filter((r) => r.body.includes(target));
    expect(recs).toHaveLength(1);
    expect(JSON.parse(recs[0].body).parameters.token).toBe(secret);

    const id = env.run_id!;
    await expect(section.getByTestId(`action-run-params-${id}`)).toContainText('token: [redacted]');
    expect(await page.content()).not.toContain(secret);

    const { text, runs } = await listRuns(instanceId);
    expect(text).not.toContain(secret);
    expect(runs.data.find((r) => r.id === id)?.parameters.token).toBe('[redacted]');

    // The stored row, after a reload.
    await page.reload();
    await expect(section.getByTestId(`action-run-params-${id}`)).toContainText('token: [redacted]', { timeout: 15000 });
    expect(await page.content()).not.toContain(secret);
  });

  test('a refused run shows the status and the message of the subscriber', async ({ page }) => {
    const instanceId = await newInstance();
    const env = await invoke(instanceId, 'e2e-echo', { target: 'refuse', mode: 'fast' });
    expect(env.status_code).toBe(409);
    expect(env.message).toBe('Refused by the e2e stub');

    const { runs } = await listRuns(instanceId);
    expect(runs.data).toHaveLength(1);
    expect(runs.data[0]).toMatchObject({
      id: env.run_id,
      action: 'e2e-echo',
      status_code: 409,
      message: 'Refused by the e2e stub',
      parameters: { target: 'refuse', mode: 'fast' },
      has_job_log: false,
    });
    expect(runs.data[0].finished_at).toBeTruthy();

    const section = await openInstance(page, instanceId);
    await expect(section.getByTestId(`action-run-status-${env.run_id}`)).toHaveText('Failed (409)');
    await expect(section.getByTestId(`action-run-message-${env.run_id}`)).toHaveText('Refused by the e2e stub');
  });

  test('a 3xx answer shows as Failed (302) and never shows the redirect URL', async ({ page }) => {
    const instanceId = await newInstance();
    const res = await api.post(`${instanceUrl(instanceId)}/actions/e2e-redirect`, { headers: auth(owner.token), data: {} });
    expect(res.status(), await res.text()).toBe(502);

    const { text, runs } = await listRuns(instanceId);
    expect(runs.data).toHaveLength(1);
    const run = runs.data[0];
    expect(run).toMatchObject({ action: 'e2e-redirect', action_label: 'E2E redirect', status_code: 302, has_job_log: false });
    for (const leak of REDIRECT_LEAKS) expect(text).not.toContain(leak);

    const section = await openInstance(page, instanceId);
    await expect(section.getByTestId(`action-run-status-${run.id}`)).toHaveText('Failed (302)');
    const html = await page.content();
    for (const leak of REDIRECT_LEAKS) expect(html).not.toContain(leak);
  });

  test('a job followed in the action dialog shows its final status in the list', async ({ page }) => {
    const instanceId = await newInstance();
    const section = await openInstance(page, instanceId);
    await expect(section.getByText('No action runs yet.')).toBeVisible();
    await markPage(page);

    const dialog = await openAction(page, 'E2E job');
    const env = await runInDialog(page, dialog, instanceId, 'e2e-job');
    await expect(dialog.getByTestId('action-status')).toHaveText('E2E job returned status 202.');
    await expect(dialog.getByTestId('job-status')).toHaveText('succeeded', { timeout: 20000 });
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();

    const id = env.run_id!;
    await expect(section.getByText('No action runs yet.')).toHaveCount(0);
    await expect(section.getByTestId(`action-run-status-${id}`)).toHaveText('succeeded');
    await expect(section.getByTestId(`action-run-show-log-${id}`)).toHaveText('Show log');
    await expectNoReload(page);

    const stored = async () => (await listRuns(instanceId)).runs.data.find((r) => r.id === id);
    await expect.poll(async () => (await stored())?.job_status).toBe('succeeded');
    const run = await stored();
    expect(run).toMatchObject({
      action: 'e2e-job',
      action_label: 'E2E job',
      status_code: 202,
      job_id: env.job_id,
      job_status: 'succeeded',
      has_job_log: true,
    });
    expect(run!.finished_at, 'finished_at').toBeTruthy();
    expect(Date.parse(run!.finished_at!)).toBeGreaterThanOrEqual(Date.parse(run!.started_at));
  });

  test('Show log follows a job that nobody polled and updates the row', async ({ page }) => {
    const instanceId = await newInstance();
    const env = await invoke(instanceId, 'e2e-job');
    expect(env.status_code).toBe(202);
    const jobId = env.job_id!;
    const id = env.run_id!;
    const before = (await listRuns(instanceId)).runs.data[0];
    expect(before).toMatchObject({ id, job_id: jobId, has_job_log: true });
    expect(before.job_status).toBeUndefined();
    expect(before.finished_at).toBeUndefined();

    const section = await openInstance(page, instanceId);
    await markPage(page);
    const chip = section.getByTestId(`action-run-status-${id}`);
    await expect(chip).toHaveText('Job started');
    await section.getByTestId(`action-run-show-log-${id}`).click();

    const dialog = page.getByRole('dialog', { name: 'Job log: E2E job' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(`Job ${jobId}`, { exact: true })).toBeVisible();
    // The stub answers "running" twice, then "succeeded".
    await expect(dialog.getByTestId('job-log')).toContainText('Step 2 of 2 done', { timeout: 20000 });
    await expect(dialog.getByTestId('job-status')).toHaveText('succeeded', { timeout: 20000 });
    await expect(dialog.getByRole('button', { name: 'Stop following' })).toHaveCount(0);
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();

    await expect(chip).toHaveText('succeeded');
    await expectNoReload(page);
    await expect.poll(async () => (await listRuns(instanceId)).runs.data[0].job_status).toBe('succeeded');
    const after = (await listRuns(instanceId)).runs.data[0];
    expect(after).toMatchObject({ id, job_status: 'succeeded' });
    expect(after.finished_at, 'finished_at').toBeTruthy();
  });

  test('a viewer sees the runs and who started them, but no job log', async ({ page }) => {
    const instanceId = await newInstance();
    const job = await invoke(instanceId, 'e2e-job');
    const echo = await invoke(instanceId, 'e2e-echo', { target: uniqueName('e2e-ar-target'), mode: 'fast' });

    const { runs: forViewer } = await listRuns(instanceId, viewer.token);
    expect(forViewer.can_view_logs).toBe(false);
    expect(forViewer.data.map((r) => r.id)).toEqual([echo.run_id, job.run_id]);
    for (const r of forViewer.data) expect(r.started_by).toBe(owner.username);
    expect(forViewer.data[1].has_job_log).toBe(true);
    expect((await listRuns(instanceId, owner.token)).runs.can_view_logs).toBe(true);

    const section = await openInstance(page, instanceId, viewer.token);
    for (const env of [job, echo]) {
      await expect(section.getByTestId(`action-run-row-${env.run_id}`)).toContainText(owner.username);
    }
    await expect(section.getByTestId(`action-run-status-${job.run_id}`)).toHaveText('Job started');
    await expect(section.getByTestId(`action-run-show-log-${job.run_id}`)).toHaveCount(0);
    await expect(section.getByRole('button', { name: 'Show log' })).toHaveCount(0);
  });

  test('Show more loads the older runs', async ({ page }) => {
    const instanceId = await newInstance();
    const runIds: string[] = [];
    for (let i = 0; i <= PAGE_SIZE; i++) {
      runIds.push((await invoke(instanceId, 'e2e-echo', { target: `e2e-ar-run-${i}`, mode: 'fast' })).run_id!);
    }

    // The API pages newest first.
    const { runs: first } = await listRuns(instanceId, owner.token, `?limit=${PAGE_SIZE}`);
    expect(first.data.map((r) => r.id)).toEqual(runIds.slice(1).reverse());
    expect(first.next_cursor).toBeTruthy();
    const { runs: second } = await listRuns(instanceId, owner.token, `?limit=${PAGE_SIZE}&cursor=${encodeURIComponent(first.next_cursor!)}`);
    expect(second.data.map((r) => r.id)).toEqual([runIds[0]]);
    expect(second.next_cursor).toBeUndefined();

    const section = await openInstance(page, instanceId);
    const rows = listRows(section);
    await expect(rows).toHaveCount(PAGE_SIZE);
    await expect(rows.first()).toHaveAttribute('data-testid', `action-run-row-${runIds[PAGE_SIZE]}`);
    await expect(section.getByTestId(`action-run-row-${runIds[0]}`)).toHaveCount(0);
    const more = section.getByTestId('action-runs-show-more');
    await expect(more).toHaveText(/Show more/);
    await more.click();
    await expect(rows).toHaveCount(PAGE_SIZE + 1);
    await expect(rows.last()).toHaveAttribute('data-testid', `action-run-row-${runIds[0]}`);
    await expect(more).toHaveCount(0);
  });

  test('Show log of a job that the subscriber forgot says that the log is gone', async ({ page }) => {
    const instanceId = await newInstance();
    const env = await invoke(instanceId, 'e2e-job');
    let calls = 0;
    await page.route(/\/api\/v1\/stack-instances\/[^/]+\/actions\/e2e-job\/jobs\/[^/]+\/log/, async (route) => {
      calls += 1;
      await route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"job log not found"}' });
    });

    const section = await openInstance(page, instanceId);
    await section.getByTestId(`action-run-show-log-${env.run_id}`).click();
    const dialog = page.getByRole('dialog', { name: 'Job log: E2E job' });
    await expect(dialog.getByText('The job log is no longer available.')).toBeVisible({ timeout: 10000 });
    expect(calls, 'the dialog read the job log').toBeGreaterThanOrEqual(1);
    await expect(dialog.getByRole('button', { name: 'Stop following' })).toHaveCount(0);
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();
    await expect(section.getByTestId(`action-run-status-${env.run_id}`)).toHaveText('Job started');
  });
});
