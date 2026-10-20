import { test, expect, APIRequestContext, Page, Route } from '@playwright/test';
import { API_BASE, apiCreateDefinition, apiLogin, ensureDefaultCluster, uniqueName } from './helpers';
import { E2E_ACTION_SECRET, expectedSignature, stubHealth, stubRecords } from './stub/client';

/**
 * Custom instance actions (#588): the actions of HOOKS_CONFIG_FILE in the
 * instance "Actions" menu, the parameter form, the result view and the job
 * log dialog.
 *
 * `make test-e2e` gives the backend e2e/fixtures/hooks-e2e.json. Its actions
 * call the test stub (e2e/stub/server.ts, started by global-setup.ts):
 * - e2e-echo: parameters target (string, required), dry_run (bool, default
 *   true), mode (enum fast|full, default fast), token (string, sensitive).
 *   Signed with E2E_ACTION_SECRET. The target "refuse" gives 409.
 * - e2e-job: answers 202 with a job_id; its log is "running" for two polls,
 *   then "succeeded".
 * - e2e-redirect: answers 302 with an HTML body.
 * Without that config (a backend started another way) the specs skip.
 *
 * An action needs no cluster: a draft instance is enough. The labels and the
 * job test IDs also show in the "Action runs" section and its job log dialog
 * (#640), so the locators use the menu or the dialog as their scope.
 */

const PASSWORD = 'e2e-test-password';
const MSG_TRANSPORT = 'action subscriber unreachable or returned transport error';

interface Actor {
  id: string;
  username: string;
  token: string;
}

let api: APIRequestContext;
let adminToken: string;
let owner: Actor;
let viewer: Actor;
let devops: Actor;
let definitionId: string;
let instanceId: string;
/** Instances of single tests; afterAll deletes them. */
const extraInstances: string[] = [];
let skipReason = '';

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const actionsUrl = () => `${API_BASE}/api/v1/stack-instances/${instanceId}/actions`;

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

/** Open the instance page as the given user. */
async function openInstance(page: Page, token: string) {
  await page.addInitScript((t) => localStorage.setItem('token', t), token);
  await page.goto(`/stack-instances/${instanceId}`);
  await expect(page.locator('#instance-actions-button')).toBeVisible({ timeout: 15000 });
}

/** Open the header "Actions" menu and the dialog of one custom action. */
async function openAction(page: Page, label: string) {
  await page.locator('#instance-actions-button').click();
  await page.getByRole('menu', { name: 'Actions' }).getByRole('menuitem', { name: new RegExp(`^${label}`) }).click();
  const dialog = page.getByRole('dialog', { name: label, exact: true });
  await expect(dialog).toBeVisible();
  return dialog;
}

test.describe('Custom instance actions', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);
    owner = await registerActor('user', 'e2e-act-owner');
    viewer = await registerActor('user', 'e2e-act-viewer');
    devops = await registerActor('devops', 'e2e-act-devops');
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-act-def'));
    const inst = await api.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(owner.token),
      data: { stack_definition_id: definitionId, name: uniqueName('e2e-act'), branch: 'main' },
    });
    expect(inst.status(), `create instance: ${await inst.text()}`).toBe(201);
    instanceId = (await inst.json()).id;

    const list = await api.get(actionsUrl(), { headers: auth(adminToken) });
    const names = ((await list.json()).actions as { name: string }[]).map((a) => a.name);
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
    for (const id of [instanceId, ...extraInstances]) {
      if (id) await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    }
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    for (const actor of [owner, viewer, devops]) {
      if (actor) await api.delete(`${API_BASE}/api/v1/users/${actor.id}`, { headers: auth(adminToken) });
    }
    await api.dispose();
  });

  test('the list shows can_invoke for each role and never the URL or the secret', async () => {
    const cases: [string, string, boolean][] = [
      ['viewer (role user, not the owner)', viewer.token, false],
      ['owner (role user)', owner.token, true],
      ['devops', devops.token, true],
      ['admin', adminToken, true],
    ];
    for (const [who, token, canInvoke] of cases) {
      const res = await api.get(actionsUrl(), { headers: auth(token) });
      const text = await res.text();
      expect(res.status(), `${who}: ${text}`).toBe(200);
      const body = JSON.parse(text) as { can_invoke: boolean; actions: Record<string, unknown>[] };
      expect(body.can_invoke, `${who}: can_invoke`).toBe(canInvoke);
      const byName = new Map(body.actions.map((a) => [a.name as string, a]));
      for (const name of ['e2e-echo', 'e2e-job', 'e2e-redirect']) {
        const a = byName.get(name);
        expect(a, `${who}: action ${name}`).toBeTruthy();
        expect(a!.can_invoke, `${who}: ${name}.can_invoke`).toBe(canInvoke);
        expect(a).not.toHaveProperty('url');
        expect(a).not.toHaveProperty('secret_env');
      }
      const echoParams = byName.get('e2e-echo')!.parameters as { name: string; sensitive?: boolean }[];
      expect(echoParams.find((p) => p.name === 'token')?.sensitive, `${who}: token is sensitive`).toBe(true);
      expect(echoParams.find((p) => p.name === 'target'), `${who}: target`).not.toHaveProperty('sensitive');
      expect(text, `${who}: the list must not show the subscriber`).not.toContain('127.0.0.1');
      expect(text).not.toContain(E2E_ACTION_SECRET);
    }

    // The viewer cannot run an action.
    const run = await api.post(`${actionsUrl()}/e2e-echo`, {
      headers: auth(viewer.token),
      data: { parameters: { target: 'x', mode: 'fast' } },
    });
    expect(run.status(), await run.text()).toBe(403);
  });

  test('the viewer sees the actions disabled in the menu', async ({ page }) => {
    await openInstance(page, viewer.token);
    await page.locator('#instance-actions-button').click();
    const item = page.getByRole('menu', { name: 'Actions' }).getByRole('menuitem', { name: /^E2E echo/ });
    await expect(item).toBeVisible();
    await expect(item).toHaveAttribute('aria-disabled', 'true');
    await expect(item).toContainText('Only the owner, an admin or a devops user can run this action');
  });

  test('the parameter form checks the required fields and sends the values', async ({ page }) => {
    // The server checks the parameters too.
    const missing = await api.post(`${actionsUrl()}/e2e-echo`, {
      headers: auth(owner.token),
      data: { parameters: { mode: 'fast' } },
    });
    expect(missing.status(), await missing.text()).toBe(400);
    expect((await missing.json()).error).toContain('parameter "target" is required');
    const badEnum = await api.post(`${actionsUrl()}/e2e-echo`, {
      headers: auth(owner.token),
      data: { parameters: { target: 'x', mode: 'slow' } },
    });
    expect(badEnum.status(), await badEnum.text()).toBe(400);
    expect((await badEnum.json()).error).toContain('must be one of: fast, full');

    await openInstance(page, owner.token);
    const dialog = await openAction(page, 'E2E echo');
    await expect(dialog.getByText('This runs the e2e echo action.')).toBeVisible();

    // Target is required: Run stays disabled until it has a value.
    const run = dialog.getByRole('button', { name: 'Run' });
    await expect(run).toBeDisabled();
    await expect(dialog.getByText('Required')).toBeVisible();

    // The defaults: dry_run on, mode fast.
    const dryRun = dialog.getByRole('switch', { name: 'Dry run' });
    await expect(dryRun).toBeChecked();
    await expect(dialog.getByRole('combobox', { name: /Mode/ })).toHaveText('fast');

    // The sensitive parameter is an optional password field; left empty, it is not sent.
    const token = dialog.getByLabel('Token', { exact: true });
    await expect(token).toHaveAttribute('type', 'password');
    await expect(token).toHaveValue('');
    await expect(dialog.getByText('Not stored in the run history.')).toBeVisible();

    const target = uniqueName('e2e-target');
    await dialog.getByLabel(/Target/).fill(target);
    await dryRun.uncheck();
    await dialog.getByRole('combobox', { name: /Mode/ }).click();
    await page.getByRole('option', { name: 'full' }).click();
    await expect(run).toBeEnabled();
    await run.click();

    await expect(dialog.getByTestId('action-status')).toHaveText('E2E echo returned status 200.');
    await expect(dialog.getByTestId('action-result')).toContainText(target);

    // The subscriber got the values, the instance and a valid signature.
    const recs = (await stubRecords(api, '/actions/echo')).filter((r) => r.body.includes(target));
    expect(recs).toHaveLength(1);
    const envelope = JSON.parse(recs[0].body);
    expect(envelope.action).toBe('e2e-echo');
    expect(envelope.instance.id).toBe(instanceId);
    expect(envelope.parameters).toEqual({ target, dry_run: false, mode: 'full' });
    expect(recs[0].headers['x-stackmanager-signature']).toBe(expectedSignature(recs[0].body, E2E_ACTION_SECRET));
  });

  test('a job result opens the job log, which polls until the job is done', async ({ page }) => {
    await openInstance(page, owner.token);
    const dialog = await openAction(page, 'E2E job');
    await dialog.getByRole('button', { name: 'Run' }).click();

    await expect(dialog.getByTestId('action-status')).toHaveText('E2E job returned status 202.');
    const heading = await dialog.getByText(/^Job job-[0-9a-f]+$/).textContent();
    const jobId = heading!.replace('Job ', '');

    // The stub answers "running" twice, then "succeeded" (X-Job-Status).
    await expect(dialog.getByTestId('job-status')).toHaveText('succeeded', { timeout: 20000 });
    await expect(dialog.getByTestId('job-log')).toContainText('Step 2 of 2 done');
    await expect(dialog.getByLabel('Job running')).toHaveCount(0);
    await expect(dialog.getByRole('button', { name: 'Stop following' })).toHaveCount(0);

    // The polling stopped at done: no more requests after one more interval.
    const polls = await stubRecords(api, `/jobs/${jobId}/log`);
    expect(polls).toHaveLength(3);
    await page.waitForTimeout(4000);
    expect(await stubRecords(api, `/jobs/${jobId}/log`)).toHaveLength(3);

    // Each poll is signed, carries the instance and moves the offset.
    const offsets = polls.map((p) => Number(p.query.offset));
    expect(offsets[0]).toBe(0);
    expect(offsets[1]).toBeGreaterThan(offsets[0]);
    expect(offsets[2]).toBeGreaterThan(offsets[1]);
    for (const p of polls) {
      expect(p.query.instance_id).toBe(instanceId);
      // The backend signs the request URI (path and query).
      expect(p.headers['x-stackmanager-signature']).toBe(expectedSignature(p.url, E2E_ACTION_SECRET));
    }
  });

  test('the job log of another instance is not found', async () => {
    const run = await api.post(`${actionsUrl()}/e2e-job`, { headers: auth(owner.token), data: {} });
    expect(run.status(), await run.text()).toBe(200);
    const jobId = (await run.json()).job_id as string;
    expect(jobId).toMatch(/^job-/);

    const other = await api.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(owner.token),
      data: { stack_definition_id: definitionId, name: uniqueName('e2e-act-other'), branch: 'main' },
    });
    expect(other.status(), await other.text()).toBe(201);
    const otherId = (await other.json()).id as string;
    extraInstances.push(otherId);

    // The subscriber answers 404 for a job of another instance; the backend passes it on.
    const res = await api.get(`${API_BASE}/api/v1/stack-instances/${otherId}/actions/e2e-job/jobs/${jobId}/log`, {
      headers: auth(owner.token),
    });
    expect(res.status(), await res.text()).toBe(404);
    expect((await res.json()).error).toBe('job log not found');
  });

  test('the job log backs off on 429, 5xx and network errors and stops on 404', async ({ page }) => {
    // Answers of the job log, in order. The real invoke starts the job.
    const answers: ((route: Route) => Promise<void>)[] = [
      (r) => r.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"busy"}' }),
      (r) => r.fulfill({ status: 429, contentType: 'application/json', body: '{"error":"slow down"}' }),
      (r) => r.abort('connectionreset'),
      // The page calls the backend cross-origin here; the backend exposes Retry-After (CORS).
      (r) => r.fulfill({
        status: 502,
        headers: { 'Retry-After': '2', 'Access-Control-Expose-Headers': 'Content-Disposition, X-Request-ID, Retry-After' },
        contentType: 'application/json',
        body: '{"error":"gateway"}',
      }),
      (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"job log not found"}' }),
    ];
    let calls = 0;
    await page.route(/\/api\/v1\/stack-instances\/[^/]+\/actions\/e2e-job\/jobs\/[^/]+\/log/, async (route) => {
      const n = calls;
      calls += 1;
      await answers[Math.min(n, answers.length - 1)](route);
    });
    // Page time (the fake clock) of each poll, taken when the page sends it.
    await page.addInitScript(() => {
      const w = window as unknown as { __jobPolls: number[] };
      w.__jobPolls = [];
      const open = XMLHttpRequest.prototype.open as (...args: unknown[]) => void;
      const send = XMLHttpRequest.prototype.send;
      XMLHttpRequest.prototype.open = function (this: XMLHttpRequest & { e2eUrl?: string }, ...args: unknown[]) {
        this.e2eUrl = String(args[1]);
        return open.apply(this, args);
      } as typeof XMLHttpRequest.prototype.open;
      XMLHttpRequest.prototype.send = function (this: XMLHttpRequest & { e2eUrl?: string }, body?: Document | XMLHttpRequestBodyInit | null) {
        if (this.e2eUrl?.includes('/actions/e2e-job/jobs/')) w.__jobPolls.push(Date.now());
        return send.call(this, body);
      };
    });

    await page.clock.install();
    await openInstance(page, owner.token);
    const dialog = await openAction(page, 'E2E job');
    await dialog.getByRole('button', { name: 'Run' }).click();
    await expect(dialog.getByTestId('action-status')).toHaveText('E2E job returned status 202.');

    /** Move the page clock in small steps until the next poll (or until maxMs). */
    const runUntilPoll = async (maxMs: number) => {
      const before = calls;
      for (let t = 0; t < maxMs && calls === before; t += 250) {
        await page.clock.fastForward(250);
        await page.waitForTimeout(25);
      }
      // Let the answer arrive before the clock moves again.
      await page.waitForTimeout(300);
    };

    for (let i = 0; i < answers.length; i++) await runUntilPoll(40_000);
    expect(calls, 'one poll for each answer').toBe(answers.length);
    const pollTimes = await page.evaluate(() => (window as unknown as { __jobPolls: number[] }).__jobPolls);
    expect(pollTimes).toHaveLength(answers.length);
    await expect(dialog.getByText('Failed to read the job log (HTTP 404: job log not found)')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Stop following' })).toHaveCount(0);

    // The delay after each failed poll: doubled from 3 s, or Retry-After.
    const expected = [
      ['after 503 the delay doubles to 6 s', 6000],
      ['after 429 the delay doubles to 12 s', 12000],
      ['after a network error the delay doubles to 24 s', 24000],
      ['Retry-After 2 of the 502 wins over the doubled delay', 2000],
    ] as const;
    expected.forEach(([label, delay], i) => {
      const gap = pollTimes[i + 1] - pollTimes[i];
      expect(gap, label).toBeGreaterThanOrEqual(delay - 50);
      expect(gap, label).toBeLessThan(delay + 1000);
    });

    // The 404 stopped the polling.
    await page.clock.fastForward(60_000);
    await page.waitForTimeout(500);
    expect(calls, 'no poll after 404').toBe(answers.length);

    // The mocked 502 copies the CORS header of the backend: a cross-origin
    // page can read Retry-After only because the backend exposes it.
    const cors = await api.get(actionsUrl(), { headers: { ...auth(owner.token), Origin: 'http://localhost:3000' } });
    expect(cors.headers()['access-control-expose-headers']).toContain('Retry-After');
  });

  test('the job log stops at once on 403', async ({ page }) => {
    let calls = 0;
    await page.route(/\/api\/v1\/stack-instances\/[^/]+\/actions\/e2e-job\/jobs\/[^/]+\/log/, async (route) => {
      calls += 1;
      await route.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"You are not allowed to modify this stack instance"}' });
    });
    await openInstance(page, owner.token);
    const dialog = await openAction(page, 'E2E job');
    await dialog.getByRole('button', { name: 'Run' }).click();
    await expect(dialog.getByText(/Failed to read the job log \(HTTP 403/)).toBeVisible({ timeout: 10000 });
    await page.waitForTimeout(4000);
    expect(calls).toBe(1);
  });

  test('a 3xx answer of the subscriber gives 502 without its URL', async ({ page }) => {
    const res = await api.post(`${actionsUrl()}/e2e-redirect`, { headers: auth(owner.token), data: {} });
    const text = await res.text();
    expect(res.status(), text).toBe(502);
    const body = JSON.parse(text);
    expect(body).toMatchObject({ action: 'e2e-redirect', error: MSG_TRANSPORT });
    expect(Object.keys(body).sort()).toEqual(['action', 'error', 'request_id']);
    for (const leak of ['127.0.0.1', '18090', 'stub-redirect-secret', 'elsewhere']) {
      expect(text).not.toContain(leak);
    }

    await openInstance(page, owner.token);
    const dialog = await openAction(page, 'E2E redirect');
    await dialog.getByRole('button', { name: 'Run' }).click();
    await expect(dialog.getByText(`Failed to run E2E redirect (HTTP 502: ${MSG_TRANSPORT})`)).toBeVisible();
    await expect(page.getByText(/stub-redirect-secret|127\.0\.0\.1:18090/)).toHaveCount(0);

    // The backend did not follow the redirect.
    expect(await stubRecords(api, '/elsewhere')).toHaveLength(0);
  });
});
