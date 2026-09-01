import { test, expect } from '@playwright/test';
import type { APIRequestContext } from '@playwright/test';
import {
  loginAsDevops,
  uniqueName,
  API_BASE,
  apiLogin,
  apiCreateDefinition,
  apiAddChart,
  apiCreateInstance,
  tokenFromPage,
} from './helpers';

// Default values so the merged export has content to merge the override into.
const CHART_DEFAULT_VALUES = 'replicaCount: 1\nimage:\n  repository: demo\n';

async function listOverrides(
  request: APIRequestContext,
  token: string,
  instanceId: string,
): Promise<Array<{ chart_config_id: string; values: string }>> {
  const res = await request.get(`${API_BASE}/api/v1/stack-instances/${instanceId}/overrides`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.ok(), `list overrides failed: ${res.status()}`).toBe(true);
  return res.json();
}

async function setOverride(
  request: APIRequestContext,
  token: string,
  instanceId: string,
  chartId: string,
  values: string,
) {
  return request.put(`${API_BASE}/api/v1/stack-instances/${instanceId}/overrides/${chartId}`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { values },
  });
}

// ---------------------------------------------------------------------------
// API-level tests — DB-backed, no cluster required.
// ---------------------------------------------------------------------------
test.describe('Per-chart value overrides (API)', () => {
  let token: string;
  let defId: string;
  let chartId: string;

  test.beforeAll(async ({ request }) => {
    token = await apiLogin(request);
    defId = await apiCreateDefinition(request, token, uniqueName('vo-def'));
    chartId = await apiAddChart(request, token, defId, {
      chart_name: uniqueName('vochart'),
      default_values: CHART_DEFAULT_VALUES,
    });
  });

  test('set a value override and read it back', async ({ request }) => {
    const instId = await apiCreateInstance(request, token, defId, uniqueName('vo-inst'));

    expect(await listOverrides(request, token, instId)).toEqual([]);

    const marker = uniqueName('ov');
    const values = `overrideMarker: "${marker}"\nreplicaCount: 3\n`;
    const putRes = await setOverride(request, token, instId, chartId, values);
    expect(putRes.status(), await putRes.text()).toBe(200);
    const created = await putRes.json();
    expect(created.chart_config_id).toBe(chartId);
    expect(created.values).toBe(values);

    const list = await listOverrides(request, token, instId);
    expect(list).toHaveLength(1);
    expect(list[0].chart_config_id).toBe(chartId);
    expect(list[0].values).toBe(values);
  });

  test('update an existing value override (upsert keeps a single row)', async ({ request }) => {
    const instId = await apiCreateInstance(request, token, defId, uniqueName('vo-inst'));

    const first = `replicaCount: 2\n`;
    const second = `replicaCount: 9\nextra: "${uniqueName('v2')}"\n`;

    expect((await setOverride(request, token, instId, chartId, first)).status()).toBe(200);
    expect((await setOverride(request, token, instId, chartId, second)).status()).toBe(200);

    const list = await listOverrides(request, token, instId);
    expect(list).toHaveLength(1);
    expect(list[0].values).toBe(second);
  });

  test('value override is merged into the exported chart values', async ({ request }) => {
    const instId = await apiCreateInstance(request, token, defId, uniqueName('vo-inst'));

    const marker = uniqueName('mergedov');
    expect(
      (await setOverride(request, token, instId, chartId, `overrideMarker: "${marker}"\n`)).status(),
    ).toBe(200);

    const res = await request.get(
      `${API_BASE}/api/v1/stack-instances/${instId}/values/${chartId}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(res.ok(), `export chart values failed: ${res.status()}`).toBe(true);
    const merged = await res.text();
    // Override value plus a default key both present after the deep merge.
    expect(merged).toContain(marker);
    expect(merged).toContain('replicaCount');
  });

  test('server stores syntactically invalid YAML (validation is client-side only)', async ({
    request,
  }) => {
    // For a chart with no locked template values the backend stores the raw
    // string; the YAML validation lives in the frontend YamlEditor, which shows
    // a hint but does not block the save. This documents that the API accepts
    // the payload rather than rejecting it.
    const instId = await apiCreateInstance(request, token, defId, uniqueName('vo-inst'));

    const invalid = 'foo: [unclosed\n  bar: : :';
    const putRes = await setOverride(request, token, instId, chartId, invalid);
    expect(putRes.status(), await putRes.text()).toBe(200);

    const list = await listOverrides(request, token, instId);
    expect(list).toHaveLength(1);
    expect(list[0].values).toBe(invalid);
  });
});

// ---------------------------------------------------------------------------
// UI-level test — the instance detail page surfaces the per-chart override
// editor. DB-backed, no cluster required. (The editor body is Monaco, loaded
// lazily, so we assert the deterministic MUI scaffolding rather than the
// editor's internal text.)
// ---------------------------------------------------------------------------
test.describe('Per-chart value overrides (UI)', () => {
  test('instance detail shows the per-chart override editor', async ({ page }) => {
    await loginAsDevops(page);
    await page.goto('/');
    const token = await tokenFromPage(page);

    const defId = await apiCreateDefinition(page.request, token, uniqueName('vo-ui-def'));
    const chartName = uniqueName('vouichart');
    const chartId = await apiAddChart(page.request, token, defId, {
      chart_name: chartName,
      default_values: CHART_DEFAULT_VALUES,
    });
    const instId = await apiCreateInstance(page.request, token, defId, uniqueName('vo-ui-inst'));

    // Seed an override so the detail page loads it into the editor.
    const marker = uniqueName('uiov');
    expect(
      (await setOverride(page.request, token, instId, chartId, `overrideMarker: "${marker}"\n`)).status(),
    ).toBe(200);

    await page.goto(`/stack-instances/${instId}`);

    await expect(page.getByRole('tab', { name: chartName })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Default Values')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Your Overrides')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('button', { name: 'Save Changes' })).toBeVisible({ timeout: 10_000 });
  });
});
