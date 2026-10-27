import { test, expect, APIRequestContext } from '@playwright/test';
import { API_BASE, apiCreateDefinition, apiCreateInstance, apiLogin, ensureDefaultCluster, uniqueName } from './helpers';
import { stubHealth, stubRecords } from './stub/client';

/**
 * Actions limited to some templates (applies_to, #720).
 *
 * e2e/fixtures/hooks-e2e.json has two actions that call the echo route of
 * the test stub: e2e-team-a (template category e2e-team-a) and e2e-team-b
 * (category e2e-team-b). A stack of a team A template lists and runs only
 * e2e-team-a; a stack without a template lists neither. Without that config
 * (a backend started another way) the specs skip.
 */

interface ActionList {
  actions: { name: string }[];
  scope_incomplete?: boolean;
}

let api: APIRequestContext;
let adminToken: string;
let skipReason = '';
const cleanup = { instances: [] as string[], definitions: [] as string[], templates: [] as string[] };

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const actionsUrl = (instanceId: string) => `${API_BASE}/api/v1/stack-instances/${instanceId}/actions`;

/** Create a published template of the category, instantiate it, and create a draft stack. Returns the stack ID. */
async function teamStack(category: string): Promise<string> {
  const tpl = await api.post(`${API_BASE}/api/v1/templates`, {
    headers: auth(adminToken),
    data: { name: uniqueName(`e2e-scope-${category}`), description: 'E2E #720', category, version: '1.0.0' },
  });
  expect(tpl.ok(), `create template: ${await tpl.text()}`).toBe(true);
  const templateId: string = (await tpl.json()).id;
  cleanup.templates.push(templateId);
  const chart = await api.post(`${API_BASE}/api/v1/templates/${templateId}/charts`, {
    headers: auth(adminToken),
    data: { chart_name: 'web', repository_url: 'https://charts.example.com', chart_version: '1.0.0' },
  });
  expect(chart.ok(), `add template chart: ${await chart.text()}`).toBe(true);
  const pub = await api.post(`${API_BASE}/api/v1/templates/${templateId}/publish`, { headers: auth(adminToken), data: {} });
  expect(pub.ok(), `publish: ${await pub.text()}`).toBe(true);

  const def = await api.post(`${API_BASE}/api/v1/templates/${templateId}/instantiate`, {
    headers: auth(adminToken),
    data: { name: uniqueName(`e2e-scope-def-${category}`) },
  });
  expect(def.status(), `instantiate: ${await def.text()}`).toBe(201);
  const definitionId: string = (await def.json()).id;
  cleanup.definitions.push(definitionId);

  const instanceId = await apiCreateInstance(api, adminToken, definitionId, uniqueName('e2e-scope'));
  cleanup.instances.push(instanceId);
  return instanceId;
}

async function listedActions(instanceId: string): Promise<ActionList> {
  const res = await api.get(actionsUrl(instanceId), { headers: auth(adminToken) });
  expect(res.status(), await res.text()).toBe(200);
  return res.json();
}

const teamNames = (list: ActionList) => list.actions.map((a) => a.name).filter((n) => n.startsWith('e2e-team-'));

test.describe('Actions limited to templates (applies_to)', () => {
  test.beforeAll(async ({ playwright }) => {
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);
    await ensureDefaultCluster(api, adminToken);

    const probeDef = await apiCreateDefinition(api, adminToken, uniqueName('e2e-scope-probe'));
    cleanup.definitions.push(probeDef);
    const probe = await apiCreateInstance(api, adminToken, probeDef, uniqueName('e2e-scope-probe'));
    cleanup.instances.push(probe);
    const names = (await listedActions(probe)).actions.map((a) => a.name);
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
    for (const id of cleanup.instances) await api.delete(`${API_BASE}/api/v1/stack-instances/${id}`, { headers: auth(adminToken) });
    for (const id of cleanup.definitions) await api.delete(`${API_BASE}/api/v1/stack-definitions/${id}`, { headers: auth(adminToken) });
    for (const id of cleanup.templates) await api.delete(`${API_BASE}/api/v1/templates/${id}`, { headers: auth(adminToken) });
    await api.dispose();
  });

  test('each stack lists only the actions of its template category', async () => {
    const teamA = await teamStack('e2e-team-a');
    const teamB = await teamStack('e2e-team-b');
    const noTemplateDef = await apiCreateDefinition(api, adminToken, uniqueName('e2e-scope-plain'));
    cleanup.definitions.push(noTemplateDef);
    const plain = await apiCreateInstance(api, adminToken, noTemplateDef, uniqueName('e2e-scope-plain'));
    cleanup.instances.push(plain);

    const listA = await listedActions(teamA);
    expect(teamNames(listA)).toEqual(['e2e-team-a']);
    expect(listA.actions.map((a) => a.name)).toContain('e2e-echo');
    expect(listA).not.toHaveProperty('scope_incomplete');
    expect(teamNames(await listedActions(teamB))).toEqual(['e2e-team-b']);
    expect(teamNames(await listedActions(plain))).toEqual([]);
  });

  test('an action of another template answers 404 and is not called', async () => {
    const teamA = await teamStack('e2e-team-a');

    const other = await api.post(`${actionsUrl(teamA)}/e2e-team-b`, { headers: auth(adminToken), data: {} });
    expect(other.status(), await other.text()).toBe(404);
    expect((await other.json()).error).toBe('unknown action "e2e-team-b"');

    const own = await api.post(`${actionsUrl(teamA)}/e2e-team-a`, { headers: auth(adminToken), data: {} });
    expect(own.status(), await own.text()).toBe(200);

    // Only the in-scope action reached the stub; its request names the template.
    const calls = (await stubRecords(api, '/actions/echo'))
      .map((r) => JSON.parse(r.body) as { action: string; instance: { id: string; template_category?: string; template_id?: string } })
      .filter((b) => b.instance?.id === teamA);
    expect(calls.map((b) => b.action)).toEqual(['e2e-team-a']);
    expect(calls[0].instance.template_category).toBe('e2e-team-a');
    expect(calls[0].instance.template_id).toBeTruthy();
  });

  test('the Actions menu of the stack shows only its own team action', async ({ page }) => {
    const teamA = await teamStack('e2e-team-a');
    await page.addInitScript((t) => localStorage.setItem('token', t), adminToken);
    await page.goto(`/stack-instances/${teamA}`);
    await expect(page.locator('#instance-actions-button')).toBeVisible({ timeout: 15000 });
    await page.locator('#instance-actions-button').click();
    const menu = page.getByRole('menu', { name: 'Actions' });
    await expect(menu.getByRole('menuitem', { name: /^E2E team A/ })).toBeVisible();
    await expect(menu.getByRole('menuitem', { name: /^E2E team B/ })).toHaveCount(0);
  });
});
