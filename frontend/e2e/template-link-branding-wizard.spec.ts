import { test, expect, type APIRequestContext, type Page, type Request } from '@playwright/test';
import { ADMIN_PASSWORD, API_BASE, apiAddChart, apiCreateDefinition, apiLogin, mockListAPI, uniqueName } from './helpers';

// Issue #593: e2e coverage of three small UI features.
//   1. Template link (PUT /stack-definitions/:id/template-link, TemplateLinkDialog).
//   2. Export and import of the template link (definition.source_template, warnings).
//   3. Branding from GET /api/v1/ui-config (page.route mocks; cache in localStorage).
//   4. A failed or slow ui-config keeps the built-in defaults.
//   5. Setup wizard on an "empty" database (page.route mocks of the list endpoints).
//
// The shared e2e database is not empty and other specs run against it at the
// same time: every name comes from uniqueName(), the spec uses its own users,
// and the wizard's empty state comes from mocks.

const PASSWORD = 'e2e-593-password';

/** Users of this worker, created in beforeAll. */
const users = { devops: '', user: '' };
/** Rows to remove in afterAll (best effort). */
const cleanup = { definitions: [] as string[], templates: [] as string[] };

function auth(token: string) {
  return { Authorization: `Bearer ${token}` };
}

/** Register a user with the admin token. Returns the username. */
async function registerUser(request: APIRequestContext, adminToken: string, prefix: string, role: string): Promise<string> {
  const username = uniqueName(prefix);
  const res = await request.post(`${API_BASE}/api/v1/auth/register`, {
    headers: auth(adminToken),
    data: { username, password: PASSWORD, role },
  });
  expect(res.ok(), `register ${username}: ${res.status()} ${await res.text()}`).toBe(true);
  return username;
}

/** Log in through the API and put the token into localStorage before any page script runs. */
async function loginPageAs(page: Page, username: string, password = PASSWORD): Promise<string> {
  const token = await apiLogin(page.request, username, password);
  await page.addInitScript((t) => {
    localStorage.setItem('token', t);
  }, token);
  return token;
}

/**
 * Create a template with one chart and publish each version in order
 * (the working copy gets the version before each publish). Returns the ID.
 */
async function createPublishedTemplate(
  request: APIRequestContext,
  token: string,
  name: string,
  versions: string[] = ['1.0.0'],
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/templates`, {
    headers: auth(token),
    data: { name, description: `E2E #593 template ${name}`, category: 'Web', version: versions[0] ?? '1.0.0' },
  });
  expect(res.ok(), `create template: ${res.status()} ${await res.text()}`).toBe(true);
  const id: string = (await res.json()).id;
  cleanup.templates.push(id);
  const chart = await request.post(`${API_BASE}/api/v1/templates/${id}/charts`, {
    headers: auth(token),
    data: { chart_name: 'web', repository_url: 'https://charts.example.com', chart_version: '1.0.0' },
  });
  expect(chart.ok(), `add template chart: ${chart.status()} ${await chart.text()}`).toBe(true);
  for (const [i, version] of versions.entries()) {
    if (i > 0) {
      const upd = await request.put(`${API_BASE}/api/v1/templates/${id}`, {
        headers: auth(token),
        data: { version, description: `E2E #593 template ${name} v${version}` },
      });
      expect(upd.ok(), `update template to ${version}: ${upd.status()} ${await upd.text()}`).toBe(true);
    }
    const pub = await request.post(`${API_BASE}/api/v1/templates/${id}/publish`, {
      headers: auth(token),
      data: { version },
    });
    expect(pub.ok(), `publish ${version}: ${pub.status()} ${await pub.text()}`).toBe(true);
  }
  return id;
}

/** Create an unpublished template (draft only). Returns the ID. */
async function createDraftTemplate(request: APIRequestContext, token: string, name: string): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/templates`, {
    headers: auth(token),
    data: { name, description: `E2E #593 draft ${name}`, category: 'Web', version: '1.0.0' },
  });
  expect(res.ok(), `create draft template: ${res.status()} ${await res.text()}`).toBe(true);
  const id: string = (await res.json()).id;
  cleanup.templates.push(id);
  return id;
}

/** Create a definition with one chart, owned by the token's user. Returns the ID. */
async function createDefinition(request: APIRequestContext, token: string, name: string): Promise<string> {
  const id = await apiCreateDefinition(request, token, name);
  cleanup.definitions.push(id);
  await apiAddChart(request, token, id, { chart_name: 'web' });
  return id;
}

async function getDefinition(request: APIRequestContext, token: string, id: string) {
  const res = await request.get(`${API_BASE}/api/v1/stack-definitions/${id}`, { headers: auth(token) });
  expect(res.ok(), `get definition: ${res.status()}`).toBe(true);
  return (await res.json()) as { id: string; name: string; source_template_id?: string; source_template_version?: string };
}

async function putTemplateLink(request: APIRequestContext, token: string, definitionId: string, body: unknown) {
  return request.put(`${API_BASE}/api/v1/stack-definitions/${definitionId}/template-link`, {
    headers: auth(token),
    data: body,
  });
}

test.beforeAll(async ({ request }) => {
  const adminToken = await apiLogin(request);
  users.devops = await registerUser(request, adminToken, 'e2e593-devops', 'devops');
  users.user = await registerUser(request, adminToken, 'e2e593-user', 'user');
});

test.afterAll(async ({ request }) => {
  const adminToken = await apiLogin(request);
  // Definitions first: they reference the templates.
  for (const id of cleanup.definitions) {
    await request.delete(`${API_BASE}/api/v1/stack-definitions/${id}`, { headers: auth(adminToken) }).catch(() => undefined);
  }
  for (const id of cleanup.templates) {
    await request.delete(`${API_BASE}/api/v1/templates/${id}`, { headers: auth(adminToken) }).catch(() => undefined);
  }
  const list = await request.get(`${API_BASE}/api/v1/users`, { headers: auth(adminToken) }).catch(() => null);
  if (list?.ok()) {
    const body = (await list.json()) as Array<{ id: string; username: string }> | { data?: Array<{ id: string; username: string }> };
    const all = Array.isArray(body) ? body : body.data ?? [];
    for (const u of all.filter((x) => x.username === users.devops || x.username === users.user)) {
      await request.delete(`${API_BASE}/api/v1/users/${u.id}`, { headers: auth(adminToken) }).catch(() => undefined);
    }
  }
});

// ===========================================================================
// Scenario 1: template link dialog and PUT /stack-definitions/:id/template-link
// ===========================================================================
test.describe('Template link', () => {
  test('devops links a definition to a published version in the dialog, the page shows it, then removes it', async ({ page }) => {
    const token = await loginPageAs(page, users.devops);
    const tplName = uniqueName('e2e593-tpl');
    const templateId = await createPublishedTemplate(page.request, token, tplName, ['1.0.0', '1.1.0']);
    const defId = await createDefinition(page.request, token, uniqueName('e2e593-def'));

    await page.goto(`/stack-definitions/${defId}/edit`);
    await expect(page.getByRole('heading', { level: 1, name: 'Edit Stack Definition' })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Derived from template')).toHaveCount(0);

    await page.getByRole('button', { name: 'Link to a Template' }).click();
    const dialog = page.getByRole('dialog', { name: 'Template link' });
    await expect(dialog).toBeVisible();
    // Save is disabled until a published template is selected.
    await expect(dialog.getByRole('button', { name: 'Save link' })).toBeDisabled({ timeout: 10_000 });

    await dialog.getByRole('combobox', { name: 'Template' }).click();
    await page.getByRole('option', { name: tplName, exact: true }).click();
    // The versions load for the template; the newest comes first as "Latest".
    await dialog.getByRole('combobox', { name: 'Version' }).click();
    await expect(page.getByRole('option', { name: 'Latest (1.1.0)' })).toBeVisible({ timeout: 10_000 });
    await page.getByRole('option', { name: '1.0.0', exact: true }).click();

    const put = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith(`/stack-definitions/${defId}/template-link`));
    await dialog.getByRole('button', { name: 'Save link' }).click();
    expect((await put).postDataJSON()).toEqual({ template_id: templateId, version: '1.0.0' });

    await expect(dialog).toBeHidden({ timeout: 10_000 });
    await expect(page.getByText('Template link saved')).toBeVisible();
    await expect(page.getByRole('alert').filter({ hasText: `Derived from template ${tplName} (v1.0.0).` })).toBeVisible({ timeout: 10_000 });

    // Stored, and still shown after a reload.
    const linked = await getDefinition(page.request, token, defId);
    expect(linked.source_template_id).toBe(templateId);
    expect(linked.source_template_version).toBe('1.0.0');
    await page.reload();
    await expect(page.getByRole('alert').filter({ hasText: `Derived from template ${tplName} (v1.0.0).` })).toBeVisible({ timeout: 10_000 });

    // Remove the link through the dialog (sends an empty template_id).
    await page.getByRole('button', { name: 'Change Link' }).click();
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(`Current link: ${tplName} (v1.0.0)`)).toBeVisible({ timeout: 10_000 });
    const removePut = page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith(`/stack-definitions/${defId}/template-link`));
    await dialog.getByRole('button', { name: 'Remove link' }).click();
    expect((await removePut).postDataJSON()).toEqual({ template_id: '', version: '' });
    await expect(page.getByText('Template link removed')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('button', { name: 'Link to a Template' })).toBeVisible();
    await expect(page.getByText('Derived from template')).toHaveCount(0);

    const unlinked = await getDefinition(page.request, token, defId);
    expect(unlinked.source_template_id ?? '').toBe('');
    expect(unlinked.source_template_version ?? '').toBe('');
  });

  test('the dialog shows the 409 when the template is unpublished before the save', async ({ page }) => {
    const token = await loginPageAs(page, users.devops);
    const tplName = uniqueName('e2e593-unpub');
    const templateId = await createPublishedTemplate(page.request, token, tplName);
    const defId = await createDefinition(page.request, token, uniqueName('e2e593-def'));

    await page.goto(`/stack-definitions/${defId}/edit`);
    await page.getByRole('button', { name: 'Link to a Template' }).click({ timeout: 10_000 });
    const dialog = page.getByRole('dialog', { name: 'Template link' });
    await dialog.getByRole('combobox', { name: 'Template' }).click({ timeout: 10_000 });
    await page.getByRole('option', { name: tplName, exact: true }).click();

    // The template is unpublished after the dialog listed it.
    const unpub = await page.request.post(`${API_BASE}/api/v1/templates/${templateId}/unpublish`, { headers: auth(token) });
    expect(unpub.ok(), `unpublish: ${unpub.status()}`).toBe(true);

    const response = page.waitForResponse((r) => r.request().method() === 'PUT' && r.url().endsWith(`/stack-definitions/${defId}/template-link`));
    await dialog.getByRole('button', { name: 'Save link' }).click();
    expect((await response).status()).toBe(409);
    await expect(dialog.getByRole('alert').filter({ hasText: 'Template has no published version' })).toBeVisible();
    await expect(dialog).toBeVisible();

    expect((await getDefinition(page.request, token, defId)).source_template_id ?? '').toBe('');
  });

  test('API: link rules (published, version, unpublished 409, unknown 404, empty template_id removes, role)', async ({ request }) => {
    const token = await apiLogin(request, users.devops, PASSWORD);
    const templateId = await createPublishedTemplate(request, token, uniqueName('e2e593-api'), ['1.0.0', '2.0.0']);
    const draftId = await createDraftTemplate(request, token, uniqueName('e2e593-draft'));
    const defId = await createDefinition(request, token, uniqueName('e2e593-def'));

    // Without a version: the latest published version.
    let res = await putTemplateLink(request, token, defId, { template_id: templateId });
    expect(res.status()).toBe(200);
    let body = await res.json();
    expect(body.source_template_id).toBe(templateId);
    expect(body.source_template_version).toBe('2.0.0');

    // An older published version.
    res = await putTemplateLink(request, token, defId, { template_id: templateId, version: '1.0.0' });
    expect(res.status()).toBe(200);
    expect((await res.json()).source_template_version).toBe('1.0.0');

    // A version that was never published: 400, the link stays.
    res = await putTemplateLink(request, token, defId, { template_id: templateId, version: '9.9.9' });
    expect(res.status()).toBe(400);
    expect((await res.json()).error).toContain('9.9.9');

    // An unpublished (never published) template: 409.
    res = await putTemplateLink(request, token, defId, { template_id: draftId });
    expect(res.status()).toBe(409);
    expect((await res.json()).error).toBe('Template has no published version');

    // An unknown template: 404.
    res = await putTemplateLink(request, token, defId, { template_id: '00000000-0000-0000-0000-000000000593' });
    expect(res.status()).toBe(404);

    // A version without a template: 400.
    res = await putTemplateLink(request, token, defId, { template_id: '', version: '1.0.0' });
    expect(res.status()).toBe(400);

    let stored = await getDefinition(request, token, defId);
    expect(stored.source_template_id).toBe(templateId);
    expect(stored.source_template_version).toBe('1.0.0');

    // The role `user` may not change the link (devops route): 403.
    const userToken = await apiLogin(request, users.user, PASSWORD);
    res = await putTemplateLink(request, userToken, defId, { template_id: '' });
    expect(res.status()).toBe(403);

    // An empty template_id removes the link.
    res = await putTemplateLink(request, token, defId, { template_id: '' });
    expect(res.status()).toBe(200);
    body = await res.json();
    expect(body.source_template_id ?? '').toBe('');
    expect(body.source_template_version ?? '').toBe('');
    stored = await getDefinition(request, token, defId);
    expect(stored.source_template_id ?? '').toBe('');
  });
});

// ===========================================================================
// Scenario 2: export writes definition.source_template; import links or warns
// ===========================================================================
test.describe('Template link in export and import', () => {
  test('UI export writes source_template and UI import of the bundle links the new definition', async ({ page }) => {
    const token = await loginPageAs(page, users.devops);
    const tplName = uniqueName('e2e593-exp');
    const templateId = await createPublishedTemplate(page.request, token, tplName, ['1.0.0', '1.1.0']);
    const defName = uniqueName('e2e593-expdef');
    const defId = await createDefinition(page.request, token, defName);
    expect((await putTemplateLink(page.request, token, defId, { template_id: templateId, version: '1.0.0' })).status()).toBe(200);

    await page.goto(`/stack-definitions/${defId}/edit`);
    await expect(page.getByRole('alert').filter({ hasText: `Derived from template ${tplName} (v1.0.0).` })).toBeVisible({ timeout: 10_000 });
    const downloadPromise = page.waitForEvent('download');
    await page.getByRole('button', { name: 'Export' }).click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe(`${defName}-export.json`);
    const filePath = await download.path();
    const { readFile } = await import('node:fs/promises');
    const bundle = JSON.parse(await readFile(filePath, 'utf8'));
    expect(bundle.definition.source_template).toEqual({ id: templateId, name: tplName, version: '1.0.0' });

    // Import the bundle under a new name: the new definition is linked.
    const importedName = uniqueName('e2e593-imported');
    bundle.definition.name = importedName;
    await page.goto('/stack-definitions');
    await page.getByRole('button', { name: 'Import', exact: true }).click({ timeout: 10_000 });
    const dialog = page.getByRole('dialog', { name: 'Import Stack Definition' });
    await dialog.getByLabel('Select definition file').setInputFiles({
      name: 'linked.json',
      mimeType: 'application/json',
      buffer: Buffer.from(JSON.stringify(bundle)),
    });
    await expect(dialog.getByText(`Name: ${importedName}`)).toBeVisible();
    const importResponse = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith('/stack-definitions/import'));
    await dialog.getByRole('button', { name: 'Import', exact: true }).click();
    const imported = await (await importResponse).json();
    expect(imported.warnings ?? []).toEqual([]);
    expect(imported.source_template_id).toBe(templateId);
    expect(imported.source_template_version).toBe('1.0.0');
    cleanup.definitions.push(imported.id);

    await page.waitForURL(new RegExp(`/stack-definitions/${imported.id}/edit$`), { timeout: 10_000 });
    await expect(page.getByRole('alert').filter({ hasText: `Derived from template ${tplName} (v1.0.0).` })).toBeVisible({ timeout: 10_000 });
  });

  test('UI import of a bundle with an unknown source template warns and creates an unlinked definition', async ({ page }) => {
    const token = await loginPageAs(page, users.devops);
    const missingName = uniqueName('e2e593-missing-tpl');
    const importedName = uniqueName('e2e593-warn');
    const bundle = {
      schema_version: '1.0',
      exported_at: new Date().toISOString(),
      definition: {
        name: importedName,
        description: 'E2E #593 import with an unknown template',
        default_branch: 'main',
        source_template: { name: missingName, version: '1.0.0' },
      },
      charts: [{ chart_name: 'web', repository_url: 'https://charts.example.com', chart_version: '1.0.0', default_values: '' }],
    };

    await page.goto('/stack-definitions');
    await page.getByRole('button', { name: 'Import', exact: true }).click({ timeout: 10_000 });
    const dialog = page.getByRole('dialog', { name: 'Import Stack Definition' });
    await dialog.getByLabel('Select definition file').setInputFiles({
      name: 'unknown-template.json',
      mimeType: 'application/json',
      buffer: Buffer.from(JSON.stringify(bundle)),
    });
    const importResponse = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith('/stack-definitions/import'));
    await dialog.getByRole('button', { name: 'Import', exact: true }).click();
    const response = await importResponse;
    expect(response.status()).toBe(201);
    const imported = await response.json();
    cleanup.definitions.push(imported.id);
    const expected = `The source template "${missingName}" does not exist; the definition is not linked`;
    expect(imported.warnings).toEqual([expected]);

    // The warning toast, and the edit page of the new definition without a link.
    await expect(page.getByText(expected)).toBeVisible({ timeout: 10_000 });
    await page.waitForURL(new RegExp(`/stack-definitions/${imported.id}/edit$`), { timeout: 10_000 });
    await expect(page.getByRole('button', { name: 'Link to a Template' })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Derived from template')).toHaveCount(0);
    expect((await getDefinition(page.request, token, imported.id)).source_template_id ?? '').toBe('');
  });

  test('API: import links by name; an unpublished template or an unknown version gives warnings', async ({ request }) => {
    const token = await apiLogin(request, users.devops, PASSWORD);
    const pubName = uniqueName('e2e593-byname');
    const pubId = await createPublishedTemplate(request, token, pubName, ['1.0.0', '1.2.0']);
    const draftName = uniqueName('e2e593-imp-draft');
    await createDraftTemplate(request, token, draftName);

    const importBundle = async (sourceTemplate: Record<string, string>) => {
      const res = await request.post(`${API_BASE}/api/v1/stack-definitions/import`, {
        headers: auth(token),
        data: {
          schema_version: '1.0',
          exported_at: new Date().toISOString(),
          definition: { name: uniqueName('e2e593-apiimp'), description: '', default_branch: 'main', source_template: sourceTemplate },
          charts: [{ chart_name: 'web', repository_url: 'https://charts.example.com', chart_version: '1.0.0', default_values: '' }],
        },
      });
      expect(res.status(), await res.text()).toBe(201);
      const body = await res.json();
      cleanup.definitions.push(body.id);
      return body as { id: string; source_template_id?: string; source_template_version?: string; warnings?: string[] };
    };

    // By name, no version: the latest published version.
    let body = await importBundle({ name: pubName });
    expect(body.warnings ?? []).toEqual([]);
    expect(body.source_template_id).toBe(pubId);
    expect(body.source_template_version).toBe('1.2.0');

    // By ID with an older version.
    body = await importBundle({ id: pubId, version: '1.0.0' });
    expect(body.warnings ?? []).toEqual([]);
    expect(body.source_template_version).toBe('1.0.0');

    // A version that was never published: not linked, a warning.
    body = await importBundle({ id: pubId, name: pubName, version: '7.7.7' });
    expect(body.source_template_id ?? '').toBe('');
    expect(body.warnings).toHaveLength(1);
    expect(body.warnings?.[0]).toContain(`The definition is not linked to template "${pubName}"`);
    expect(body.warnings?.[0]).toContain('7.7.7');

    // The name of an unpublished template: the lookup by name sees published templates only.
    body = await importBundle({ name: draftName });
    expect(body.source_template_id ?? '').toBe('');
    expect(body.warnings).toEqual([`The source template "${draftName}" does not exist; the definition is not linked`]);

    // The ID of a template that is not published (unpublished now): 409 reason in the warning.
    const unpub = await request.post(`${API_BASE}/api/v1/templates/${pubId}/unpublish`, { headers: auth(token) });
    expect(unpub.ok()).toBe(true);
    body = await importBundle({ id: pubId, name: pubName });
    expect(body.source_template_id ?? '').toBe('');
    expect(body.warnings).toEqual([`The definition is not linked to template "${pubName}": Template has no published version`]);
  });
});

// ===========================================================================
// Scenarios 3 and 4: branding through GET /api/v1/ui-config (mocked)
// ===========================================================================

/** 1x1 transparent PNG. */
const PNG_1X1 = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
  'base64',
);

const UI_CONFIG_ROUTE = '**/api/v1/ui-config';

function isUIConfig(r: Request): boolean {
  return new URL(r.url()).pathname === '/api/v1/ui-config';
}

type UIConfigCounters = { __uiConfigStarted?: number; __uiConfigSettled?: number };

/**
 * Count the ui-config XHRs of the page (axios uses XHR): started at send(),
 * settled one task after loadend. axios rejects a 500 or a timeout in
 * microtasks only, so after that task the .catch of BrandingProvider has run.
 * Call before page.goto.
 */
async function trackUIConfigXHR(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as UIConfigCounters;
    w.__uiConfigStarted = 0;
    w.__uiConfigSettled = 0;
    const open = XMLHttpRequest.prototype.open;
    const send = XMLHttpRequest.prototype.send;
    const urls = new WeakMap<XMLHttpRequest, string>();
    XMLHttpRequest.prototype.open = function (this: XMLHttpRequest, ...args: unknown[]) {
      urls.set(this, String(args[1]));
      return (open as (...a: unknown[]) => void).apply(this, args);
    } as typeof XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.send = function (this: XMLHttpRequest, body?: Document | XMLHttpRequestBodyInit | null) {
      if (/\/api\/v1\/ui-config(\?|$)/.test(urls.get(this) ?? '')) {
        w.__uiConfigStarted = (w.__uiConfigStarted ?? 0) + 1;
        this.addEventListener('loadend', () => {
          setTimeout(() => {
            w.__uiConfigSettled = (w.__uiConfigSettled ?? 0) + 1;
          }, 0);
        });
      }
      return send.call(this, body);
    };
  });
}

/** Wait until every ui-config XHR that the page started has settled (see trackUIConfigXHR). */
async function waitForUIConfigSettled(page: Page) {
  await page.waitForFunction(() => {
    const w = window as unknown as UIConfigCounters;
    return (w.__uiConfigStarted ?? 0) > 0 && w.__uiConfigSettled === w.__uiConfigStarted;
  }, undefined, { timeout: 15_000 });
}

/** Branding values that the page shows: document title, favicon link, logo src and the cached ui-config. */
async function readBranding(page: Page) {
  return page.evaluate(() => {
    const link = document.querySelector<HTMLLinkElement>('link[rel~="icon"]');
    const logo = document.querySelector<HTMLImageElement>('[data-testid="brand-logo"]');
    return {
      title: document.title,
      favicon: link?.getAttribute('href') ?? null,
      faviconType: link?.getAttribute('type') ?? null,
      logo: logo?.getAttribute('src') ?? null,
      cached: localStorage.getItem('ui-config'),
    };
  });
}

/** Link to "/" in the open sidebar: logo plus product name. */
function homeBrandLink(page: Page) {
  return page.getByRole('link').filter({ has: page.getByTestId('brand-logo') }).first();
}

test.describe('Branding (ui-config)', () => {
  test('title, logo and favicon from ui-config change the app bar, document.title and the favicon, and are cached', async ({ page }) => {
    const suffix = Math.random().toString(36).slice(2, 7);
    const config = {
      title: `Acme Stacks ${suffix}`,
      logo_url: `https://branding.e2e.test/${suffix}/logo.png`,
      favicon_url: `https://branding.e2e.test/${suffix}/favicon.png`,
      default_instance_ttl_minutes: 120,
      max_instance_ttl_days: 14,
    };
    let served = 0;
    await page.route(UI_CONFIG_ROUTE, (route) => {
      served += 1;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(config) });
    });
    await page.route('https://branding.e2e.test/**', (route) =>
      route.fulfill({ status: 200, contentType: 'image/png', body: PNG_1X1 }),
    );
    await loginPageAs(page, users.devops);

    await page.goto('/');
    await expect(page).toHaveTitle(config.title, { timeout: 10_000 });
    await expect(homeBrandLink(page)).toContainText(config.title);
    const logo = homeBrandLink(page).getByTestId('brand-logo');
    await expect(logo).toHaveAttribute('src', config.logo_url);
    // The image loaded (no fallback to the built-in logo).
    await expect.poll(() => logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);

    let shown = await readBranding(page);
    expect(shown.favicon).toBe(config.favicon_url);
    // Not an SVG: the type attribute of index.html is removed.
    expect(shown.faviconType).toBeNull();
    expect(served).toBeGreaterThan(0);
    // Only the known fields are cached.
    expect(JSON.parse(shown.cached ?? 'null')).toEqual(config);

    // The next start shows the cached values, also when ui-config then fails.
    await page.unroute(UI_CONFIG_ROUTE);
    await page.route(UI_CONFIG_ROUTE, (route) => route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"Internal server error"}' }));
    const failed = page.waitForResponse((r) => isUIConfig(r.request()));
    await page.reload();
    expect((await failed).status()).toBe(500);
    await expect(homeBrandLink(page)).toContainText(config.title, { timeout: 10_000 });
    shown = await readBranding(page);
    expect(shown.title).toBe(config.title);
    expect(shown.favicon).toBe(config.favicon_url);
    expect(shown.logo).toBe(config.logo_url);
  });

  test('a failed ui-config keeps the built-in defaults and caches nothing', async ({ page }) => {
    await page.route(UI_CONFIG_ROUTE, (route) => route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"Internal server error"}' }));
    await trackUIConfigXHR(page);
    await loginPageAs(page, users.devops);

    const failed = page.waitForResponse((r) => isUIConfig(r.request()));
    await page.goto('/');
    expect((await failed).status()).toBe(500);
    await expect(homeBrandLink(page)).toContainText('K8s Stack Manager', { timeout: 10_000 });
    // Every ui-config request has settled and its rejection was handled.
    await waitForUIConfigSettled(page);
    const shown = await readBranding(page);
    expect(shown).toEqual({
      title: 'K8s Stack Manager',
      favicon: '/favicon.svg',
      faviconType: 'image/svg+xml',
      logo: '/logo.svg',
      cached: null,
    });
  });

  test('a ui-config slower than the 5 s timeout keeps the built-in defaults', async ({ page }) => {
    test.setTimeout(60_000);
    const SLOW_MS = 7_000;
    const slow = { title: 'Too Slow Stacks', logo_url: '/slow-logo.png', favicon_url: '/slow-favicon.png' };
    // Entry time of the route handler for each request, and one promise per
    // handler that resolves after its late answer (StrictMode sends two requests).
    const enteredAt = new Map<Request, number>();
    const lateAnswers: Promise<void>[] = [];
    let answered = false;
    await page.route(UI_CONFIG_ROUTE, (route) => {
      enteredAt.set(route.request(), Date.now());
      const done = (async () => {
        await new Promise((r) => setTimeout(r, SLOW_MS));
        answered = true;
        // The page aborted the request at 5 s; fulfilling it then fails.
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(slow) }).catch(() => undefined);
      })();
      lateAnswers.push(done);
      return done;
    });
    await trackUIConfigXHR(page);
    await loginPageAs(page, users.devops);

    const failedPromise = page.waitForEvent('requestfailed', { predicate: isUIConfig, timeout: 30_000 });
    await page.goto('/');
    const failed = await failedPromise;
    const failedAt = Date.now();
    // The client aborted it (UI_CONFIG_TIMEOUT_MS = 5000) before the mock answered.
    expect(answered, 'the request failed only after the mock answered').toBe(false);
    expect(failed.failure()?.errorText).toMatch(/ERR_ABORTED|abort/i);
    const entered = enteredAt.get(failed);
    expect(entered, 'route handler entry time of the failed request').toBeDefined();
    // Measured from the request itself, so page load time does not count.
    // The route sees the request a little after the client timer started.
    expect(failedAt - (entered as number)).toBeGreaterThanOrEqual(4_000);
    expect(failedAt - (entered as number)).toBeLessThan(SLOW_MS);

    // Wait until each late answer was attempted and every ui-config request
    // has settled in the page; nothing can change the branding after that.
    await Promise.all(lateAnswers);
    await waitForUIConfigSettled(page);
    await expect(homeBrandLink(page)).toContainText('K8s Stack Manager');
    const shown = await readBranding(page);
    expect(shown).toEqual({
      title: 'K8s Stack Manager',
      favicon: '/favicon.svg',
      faviconType: 'image/svg+xml',
      logo: '/logo.svg',
      cached: null,
    });
  });
});

// ===========================================================================
// Scenario 5: setup wizard (empty lists come from mocks)
// ===========================================================================

const NOW = new Date().toISOString();
const MOCK_CLUSTER = {
  id: 'e2e593-cluster',
  name: 'e2e593-mock-cluster',
  description: '',
  api_server_url: 'https://k8s.e2e.test',
  region: '',
  health_status: 'healthy',
  max_namespaces: 0,
  max_instances_per_user: 0,
  is_default: true,
  created_at: NOW,
  updated_at: NOW,
};
const MOCK_TEMPLATE = {
  id: 'e2e593-template',
  name: 'e2e593-mock-template',
  description: '',
  category: 'Web',
  version: '1.0.0',
  owner_id: 'e2e593',
  default_branch: 'main',
  is_published: true,
  created_at: NOW,
  updated_at: NOW,
};
const MOCK_INSTANCE = {
  id: 'e2e593-instance',
  stack_definition_id: 'e2e593-definition',
  name: 'e2e593-mock-instance',
  namespace: 'stack-e2e593-mock-instance-admin',
  owner_id: 'e2e593',
  branch: 'main',
  status: 'stopped',
  created_at: NOW,
  updated_at: NOW,
};

/** Mock the three lists that the Dashboard reads for the wizard. */
async function mockWizardLists(page: Page, lists: { clusters: unknown[]; templates: unknown[]; instances: unknown[] }) {
  await mockListAPI(page, '/api/v1/stack-instances', lists.instances);
  await mockListAPI(page, '/api/v1/templates', lists.templates);
  // GET /clusters is not paged: a plain array.
  await page.route(/\/api\/v1\/clusters(\?.*)?$/, (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(lists.clusters) });
  });
}

const WIZARD_HEADING = /^Welcome to /;

test.describe('Setup wizard', () => {
  test('empty lists: admin sees the three steps, step 1 is active and opens the clusters page', async ({ page }) => {
    await mockWizardLists(page, { clusters: [], templates: [], instances: [] });
    // The cluster step has a button for admins only.
    await loginPageAs(page, 'admin', ADMIN_PASSWORD);

    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toBeVisible({ timeout: 10_000 });
    for (const label of ['Register a Cluster', 'Create a Template', 'Deploy an Instance']) {
      await expect(page.locator('.MuiStepLabel-label', { hasText: label })).toBeVisible();
    }
    await expect(page.locator('.MuiStepLabel-label.Mui-completed')).toHaveCount(0);
    await expect(page.getByText('Step 1 of 3')).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toHaveCount(0);

    await page.getByRole('button', { name: 'Register a Cluster' }).click();
    await page.waitForURL(/\/admin\/clusters$/, { timeout: 10_000 });
  });

  test('empty lists: the role user gets a hint instead of the cluster button', async ({ page }) => {
    await mockWizardLists(page, { clusters: [], templates: [], instances: [] });
    await loginPageAs(page, users.user);

    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Only administrators can register clusters.', { exact: false })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Register a Cluster' })).toHaveCount(0);
  });

  test('a cluster but no template: step 1 is complete and step 2 is active', async ({ page }) => {
    await mockWizardLists(page, { clusters: [MOCK_CLUSTER], templates: [], instances: [] });
    await loginPageAs(page, users.devops);

    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.MuiStepLabel-label.Mui-completed')).toHaveText(['Register a Cluster']);
    await expect(page.getByText('Step 2 of 3')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create a Template' })).toBeVisible();
  });

  test('cluster, template and instance exist: no wizard, the stack list shows', async ({ page }) => {
    await mockWizardLists(page, { clusters: [MOCK_CLUSTER], templates: [MOCK_TEMPLATE], instances: [MOCK_INSTANCE] });
    await loginPageAs(page, users.devops);

    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText(MOCK_INSTANCE.name).first()).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toHaveCount(0);
  });

  test('a cluster and a template without an instance: no wizard (Dashboard rule)', async ({ page }) => {
    // Dashboard.tsx shows the wizard only while a cluster or a template is
    // missing (and no instance exists); the "Deploy an Instance" step is never
    // the active step. This test pins that rule.
    await mockWizardLists(page, { clusters: [MOCK_CLUSTER], templates: [MOCK_TEMPLATE], instances: [] });
    await loginPageAs(page, users.devops);

    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toHaveCount(0);
  });

  test('Skip setup hides the wizard and stores the flag; it stays hidden after a reload', async ({ page }) => {
    await mockWizardLists(page, { clusters: [], templates: [], instances: [] });
    await loginPageAs(page, users.devops);

    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toBeVisible({ timeout: 10_000 });
    expect(await page.evaluate(() => localStorage.getItem('setupWizardDismissed'))).toBeNull();

    await page.getByRole('button', { name: 'Skip setup' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem('setupWizardDismissed'))).toBe('true');

    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toHaveCount(0);

    // Without the flag, the same empty lists show the wizard again.
    await page.evaluate(() => localStorage.removeItem('setupWizardDismissed'));
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: WIZARD_HEADING })).toBeVisible({ timeout: 10_000 });
  });
});
