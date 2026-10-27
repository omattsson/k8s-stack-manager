import { test, expect, APIRequestContext } from '@playwright/test';
import { API_BASE, apiCreateDefinition, apiCreateInstance, apiLogin, ensureDefaultCluster, uniqueName } from './helpers';
import { stubHealth, stubRecords } from './stub/client';

/**
 * Two teams, two database extensions (applies_to, #720).
 *
 * e2e/fixtures/hooks-e2e.json has two actions with the label "Refresh
 * database" and two post-instance-create subscriptions. The Postgres
 * extension applies to templates of the category e2e-postgres-team, the MySQL
 * extension to e2e-mysql-team. Both run on the test stub (/ext/<name>/...).
 * Each team stack must see and call only its own extension; a stack without a
 * template calls neither. Without that config, or without the stub, the specs skip.
 */

type Team = 'postgres' | 'mysql';
const CATEGORY: Record<Team, string> = { postgres: 'e2e-postgres-team', mysql: 'e2e-mysql-team' };
const ACTION: Record<Team, string> = { postgres: 'refresh-db-postgres', mysql: 'refresh-db-mysql' };
const other = (t: Team): Team => (t === 'postgres' ? 'mysql' : 'postgres');

interface Stack {
  id: string;
  templateId?: string;
}
interface Envelope {
  event?: string;
  action?: string;
  instance?: { id: string; template_id?: string; template_name?: string; template_category?: string };
}

let api: APIRequestContext;
let adminToken: string;
let skipReason = '';
const stacks = {} as Record<Team | 'plain', Stack>;
const cleanup = { instances: [] as string[], definitions: [] as string[], templates: [] as string[] };

const auth = () => ({ Authorization: `Bearer ${adminToken}` });
const actionsUrl = (id: string) => `${API_BASE}/api/v1/stack-instances/${id}/actions`;

async function teamStack(team: Team): Promise<Stack> {
  const tpl = await api.post(`${API_BASE}/api/v1/templates`, {
    headers: auth(),
    data: { name: uniqueName(`e2e-ext-${team}`), description: 'E2E #720', category: CATEGORY[team], version: '1.0.0' },
  });
  expect(tpl.ok(), `create template: ${await tpl.text()}`).toBe(true);
  const templateId: string = (await tpl.json()).id;
  cleanup.templates.push(templateId);
  const chart = await api.post(`${API_BASE}/api/v1/templates/${templateId}/charts`, {
    headers: auth(),
    data: { chart_name: 'web', repository_url: 'https://charts.example.com', chart_version: '1.0.0' },
  });
  expect(chart.ok(), `add chart: ${await chart.text()}`).toBe(true);
  const pub = await api.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, { headers: auth(), data: {} });
  expect(pub.ok(), `publish: ${await pub.text()}`).toBe(true);
  const def = await api.post(`${API_BASE}/api/v1/templates/${templateId}/instantiate`, {
    headers: auth(),
    data: { name: uniqueName(`e2e-ext-def-${team}`) },
  });
  expect(def.status(), `instantiate: ${await def.text()}`).toBe(201);
  const definitionId: string = (await def.json()).id;
  cleanup.definitions.push(definitionId);
  const id = await apiCreateInstance(api, adminToken, definitionId, uniqueName(`e2e-ext-${team}`));
  cleanup.instances.push(id);
  return { id, templateId };
}

async function plainStack(): Promise<Stack> {
  const def = await apiCreateDefinition(api, adminToken, uniqueName('e2e-ext-plain'));
  cleanup.definitions.push(def);
  const id = await apiCreateInstance(api, adminToken, def, uniqueName('e2e-ext-plain'));
  cleanup.instances.push(id);
  return { id };
}

/** Stub records under the prefix whose envelope names the instance. */
async function callsFor(prefix: string, instanceId: string): Promise<Envelope[]> {
  return (await stubRecords(api, prefix))
    .map((r) => { try { return JSON.parse(r.body) as Envelope; } catch { return {} as Envelope; } })
    .filter((b) => b.instance?.id === instanceId);
}

async function listedNames(instanceId: string): Promise<string[]> {
  const res = await api.get(actionsUrl(instanceId), { headers: auth() });
  expect(res.status(), await res.text()).toBe(200);
  return ((await res.json()).actions as { name: string }[]).map((a) => a.name);
}

const refreshNames = (names: string[]) => names.filter((n) => n.startsWith('refresh-db-'));

test.describe('Two teams, two database extensions (applies_to)', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);

    // The stack without a template comes first: its hooks are done before the team hooks.
    stacks.plain = await plainStack();
    const names = await listedNames(stacks.plain.id);
    if (!names.includes('e2e-echo')) {
      skipReason = 'The backend has no e2e actions: start it with HOOKS_CONFIG_FILE=frontend/e2e/fixtures/hooks-e2e.json (make test-e2e does).';
    } else if (!(await stubHealth(api))) {
      skipReason = 'The e2e stub does not run on 127.0.0.1:18090 (see e2e/stub/server.ts).';
    } else {
      stacks.postgres = await teamStack('postgres');
      stacks.mysql = await teamStack('mysql');
    }
  });

  test.beforeEach(() => {
    test.skip(!!skipReason, skipReason);
  });

  test.afterAll(async () => {
    if (!api) return;
    for (const id of cleanup.instances) await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth() });
    for (const id of cleanup.definitions) await api.delete(`${API_BASE}/api/v1/stack-definitions/${id}`, { headers: auth() });
    for (const id of cleanup.templates) await api.delete(`${API_BASE}/api/v1/templates/${id}`, { headers: auth() });
    await api.dispose();
  });

  for (const team of ['postgres', 'mysql'] as Team[]) {
    test(`creating a ${team} stack calls only the ${team} hook, with the template in the envelope`, async () => {
      const { id, templateId } = stacks[team];
      await expect.poll(async () => (await callsFor(`/ext/${team}/hooks/event`, id)).length, { timeout: 10000 }).toBe(1);

      const [env] = await callsFor(`/ext/${team}/hooks/event`, id);
      expect(env.event).toBe('post-instance-create');
      expect(env.instance?.template_category).toBe(CATEGORY[team]);
      expect(env.instance?.template_id).toBe(templateId);
      expect(env.instance?.template_name).toBeTruthy();

      expect(await callsFor(`/ext/${other(team)}/hooks/event`, id)).toHaveLength(0);
    });

    test(`the ${team} stack lists one Refresh database action, its own`, async () => {
      expect(refreshNames(await listedNames(stacks[team].id))).toEqual([ACTION[team]]);
    });

    test(`the ${team} stack runs Refresh database from the UI on the ${team} extension only`, async ({ page }) => {
      const { id, templateId } = stacks[team];
      await page.addInitScript((t) => localStorage.setItem('token', t), adminToken);
      await page.goto(`/stack-instances/${id}`);
      await expect(page.locator('#instance-actions-button')).toBeVisible({ timeout: 15000 });
      await page.locator('#instance-actions-button').click();
      const menu = page.getByRole('menu', { name: 'Actions' });
      await expect(menu.getByRole('menuitem', { name: /^Refresh database/ })).toHaveCount(1);
      await menu.getByRole('menuitem', { name: /^Refresh database/ }).click();
      const dialog = page.getByRole('dialog', { name: 'Refresh database', exact: true });
      await expect(dialog).toBeVisible();
      await dialog.getByRole('button', { name: 'Run' }).click();
      await expect(dialog.getByTestId('action-status')).toHaveText('Refresh database returned status 200.', { timeout: 10000 });
      await expect(dialog.getByTestId('action-result')).toContainText(team);

      const calls = await callsFor(`/ext/${team}/actions/refresh-db`, id);
      expect(calls).toHaveLength(1);
      expect(calls[0].action).toBe(ACTION[team]);
      expect(calls[0].instance?.template_category).toBe(CATEGORY[team]);
      expect(calls[0].instance?.template_id).toBe(templateId);
      expect(await callsFor(`/ext/${other(team)}/actions/refresh-db`, id)).toHaveLength(0);
    });

    test(`the ${other(team)} action answers 404 on the ${team} stack and is not called`, async () => {
      const { id } = stacks[team];
      const res = await api.post(`${actionsUrl(id)}/${ACTION[other(team)]}`, { headers: auth(), data: {} });
      expect(res.status(), await res.text()).toBe(404);
      expect((await res.json()).error).toBe(`unknown action "${ACTION[other(team)]}"`);
      expect(await callsFor(`/ext/${other(team)}/actions/refresh-db`, id)).toHaveLength(0);
    });
  }

  test('a stack without a template sees neither action and triggers neither hook', async ({ page }) => {
    const { id } = stacks.plain;
    expect(refreshNames(await listedNames(id))).toEqual([]);

    for (const team of ['postgres', 'mysql'] as Team[]) {
      const res = await api.post(`${actionsUrl(id)}/${ACTION[team]}`, { headers: auth(), data: {} });
      expect(res.status(), await res.text()).toBe(404);
    }

    // The team hooks of the later team stacks have arrived, so a hook of this stack would have too.
    await expect.poll(async () => (await callsFor('/ext/postgres/hooks/event', stacks.postgres.id)).length, { timeout: 10000 }).toBe(1);
    await expect.poll(async () => (await callsFor('/ext/mysql/hooks/event', stacks.mysql.id)).length, { timeout: 10000 }).toBe(1);
    for (const team of ['postgres', 'mysql'] as Team[]) {
      expect(await callsFor(`/ext/${team}/hooks/event`, id)).toHaveLength(0);
      expect(await callsFor(`/ext/${team}/actions/refresh-db`, id)).toHaveLength(0);
    }

    await page.addInitScript((t) => localStorage.setItem('token', t), adminToken);
    await page.goto(`/stack-instances/${id}`);
    await expect(page.locator('#instance-actions-button')).toBeVisible({ timeout: 15000 });
    await page.locator('#instance-actions-button').click();
    await expect(page.getByRole('menu', { name: 'Actions' }).getByRole('menuitem', { name: /^Refresh database/ })).toHaveCount(0);
  });
});
