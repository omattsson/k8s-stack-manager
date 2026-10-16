import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { API_BASE, apiCreateDefinition, loginAsDevops, tokenFromPage, uniqueName } from './helpers';

// Fixed stop time (#574): "Custom stop time" in the TTL field, "Custom..." in
// the Extend menu and "Stops at" on the instance page. DB-backed, no cluster
// required.

const DAY = 24 * 60 * 60 * 1000;

/** Value of a datetime-local input (local time, minute precision). */
const toLocalInput = (d: Date) => {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

/** A time `days` from now at 10:30 local time. */
const daysFromNowAt1030 = (days: number) => {
  const d = new Date(Date.now() + days * DAY);
  d.setHours(10, 30, 0, 0);
  return d;
};

async function getInstance(request: APIRequestContext, token: string, id: string) {
  const res = await request.get(`${API_BASE}/api/v1/stack-instances/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.ok()).toBe(true);
  return res.json() as Promise<{ ttl_minutes?: number; expires_at?: string }>;
}

async function createInstance(page: Page, token: string, body: Record<string, unknown>): Promise<string> {
  const defId = await apiCreateDefinition(page.request, token, uniqueName('ttl-def'));
  const res = await page.request.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { stack_definition_id: defId, name: uniqueName('ttl'), branch: 'main', ...body },
  });
  const created = await res.json();
  expect(res.ok(), `create failed: ${res.status()} ${JSON.stringify(created)}`).toBe(true);
  return created.id;
}

test.describe('TTL stop time', () => {
  let maxDays: number;

  test.beforeAll(async ({ request }) => {
    const res = await request.get(`${API_BASE}/api/v1/ui-config`);
    maxDays = (await res.json()).max_instance_ttl_days;
    expect(maxDays).toBeGreaterThanOrEqual(3);
  });

  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
    await page.goto('/');
  });

  test('create an instance with a custom stop time', async ({ page }) => {
    const token = await tokenFromPage(page);
    const defId = await apiCreateDefinition(page.request, token, uniqueName('ttl-def'));

    await page.goto(`/stack-instances/new?definition=${defId}`);
    await page.getByLabel('Instance Name').fill(uniqueName('stop').slice(0, 40));
    await page.getByRole('combobox', { name: 'TTL (Time to Live)' }).click();
    await page.getByRole('option', { name: 'Custom stop time' }).click();

    const picker = page.getByLabel('Stop at');
    await expect(picker).toHaveAttribute('type', 'datetime-local');
    const max = new Date(await picker.getAttribute('max') as string).getTime();
    expect(Math.abs(max - (Date.now() + maxDays * DAY))).toBeLessThan(5 * 60_000);

    const stop = daysFromNowAt1030(2);
    await picker.fill(toLocalInput(stop));
    await expect(page.getByText(/\(in \d+d \d+h\)/)).toBeVisible();
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });

    // The instance page shows the fixed stop time, also before the deploy.
    await expect(page.getByText(/^Stops at /)).toBeVisible({ timeout: 10_000 });
    const id = page.url().split('/stack-instances/')[1];
    const inst = await getInstance(page.request, token, id);
    expect(inst.ttl_minutes ?? 0).toBe(0);
    expect(new Date(inst.expires_at as string).getTime()).toBe(stop.getTime());
  });

  test('a stop time after the limit blocks Create', async ({ page }) => {
    const token = await tokenFromPage(page);
    const defId = await apiCreateDefinition(page.request, token, uniqueName('ttl-def'));

    await page.goto(`/stack-instances/new?definition=${defId}`);
    await page.getByLabel('Instance Name').fill(uniqueName('late').slice(0, 40));
    await page.getByRole('combobox', { name: 'TTL (Time to Live)' }).click();
    await page.getByRole('option', { name: 'Custom stop time' }).click();
    await page.getByLabel('Stop at').fill(toLocalInput(daysFromNowAt1030(maxDays + 1)));

    await expect(page.getByText(/^Choose a time before /)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Create Instance' })).toBeDisabled();
  });

  test('extend a fixed stop time with Custom...', async ({ page }) => {
    const token = await tokenFromPage(page);
    const current = daysFromNowAt1030(1);
    const id = await createInstance(page, token, { expires_at: current.toISOString() });

    await page.goto(`/stack-instances/${id}`);
    await expect(page.getByText(/^Stops at /)).toBeVisible({ timeout: 10_000 });
    await page.getByRole('button', { name: 'Extend', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Custom...' }).click();

    const dialog = page.getByRole('dialog', { name: 'Extend until' });
    const picker = dialog.getByLabel('New expiry');
    // Not before the current expiry
    await picker.fill(toLocalInput(new Date(current.getTime() - 60 * 60_000)));
    await expect(dialog.getByText(/^Choose a time after /)).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Extend' })).toBeDisabled();

    const until = daysFromNowAt1030(3);
    await picker.fill(toLocalInput(until));
    await dialog.getByRole('button', { name: 'Extend' }).click();
    await expect(page.getByText(/^Extended\. Expires /)).toBeVisible({ timeout: 10_000 });

    const inst = await getInstance(page.request, token, id);
    expect(new Date(inst.expires_at as string).getTime()).toBe(until.getTime());
    expect(inst.ttl_minutes ?? 0).toBe(0);
  });

  test('set a stop time on the instance page', async ({ page }) => {
    const token = await tokenFromPage(page);
    const id = await createInstance(page, token, { ttl_minutes: 240 });

    await page.goto(`/stack-instances/${id}`);
    await page.getByRole('combobox', { name: 'TTL (Time to Live)' }).click();
    await page.getByRole('option', { name: 'Custom stop time' }).click();
    const stop = daysFromNowAt1030(2);
    await page.getByLabel('Stop at').fill(toLocalInput(stop));
    await page.getByRole('button', { name: 'Save stop time' }).click();
    await expect(page.getByText(/^Stop time set: /)).toBeVisible({ timeout: 10_000 });

    const inst = await getInstance(page.request, token, id);
    expect(inst.ttl_minutes ?? 0).toBe(0);
    expect(new Date(inst.expires_at as string).getTime()).toBe(stop.getTime());
    await expect(page.getByText(/^Stops at /)).toBeVisible();
  });
});
