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

// A chart whose default values reference the {{.Branch}} template var. The
// merged/exported values substitute this, so a per-chart branch override is
// observable end-to-end without a Kubernetes cluster.
const CHART_DEFAULT_VALUES = 'image:\n  ref: "img-{{.Branch}}"\n';

/**
 * Fetch the merged/exported values for a single chart as YAML text.
 * This is DB-backed (pure Helm value merge + template substitution) and works
 * without a reachable cluster.
 */
async function getMergedChartValues(
  request: APIRequestContext,
  token: string,
  instanceId: string,
  chartId: string,
): Promise<string> {
  const res = await request.get(
    `${API_BASE}/api/v1/stack-instances/${instanceId}/values/${chartId}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(res.ok(), `export chart values failed: ${res.status()}`).toBe(true);
  return res.text();
}

async function listBranchOverrides(
  request: APIRequestContext,
  token: string,
  instanceId: string,
): Promise<Array<{ chart_config_id: string; branch: string }>> {
  const res = await request.get(`${API_BASE}/api/v1/stack-instances/${instanceId}/branches`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.ok(), `list branch overrides failed: ${res.status()}`).toBe(true);
  return res.json();
}

// ---------------------------------------------------------------------------
// API-level tests — DB-backed, no cluster required.
// ---------------------------------------------------------------------------
test.describe('Per-chart branch overrides (API)', () => {
  let token: string;
  let defId: string;
  let chartId: string;

  test.beforeAll(async ({ request }) => {
    token = await apiLogin(request);
    defId = await apiCreateDefinition(request, token, uniqueName('bo-def'));
    chartId = await apiAddChart(request, token, defId, {
      chart_name: uniqueName('bochart'),
      default_values: CHART_DEFAULT_VALUES,
    });
  });

  test('set a branch override and read it back', async ({ request }) => {
    const instId = await apiCreateInstance(request, token, defId, uniqueName('bo-inst'), 'main');

    // No overrides initially.
    expect(await listBranchOverrides(request, token, instId)).toEqual([]);

    const overrideBranch = uniqueName('feat');
    const putRes = await request.put(
      `${API_BASE}/api/v1/stack-instances/${instId}/branches/${chartId}`,
      { headers: { Authorization: `Bearer ${token}` }, data: { branch: overrideBranch } },
    );
    expect(putRes.status(), await putRes.text()).toBe(200);
    const created = await putRes.json();
    expect(created.chart_config_id).toBe(chartId);
    expect(created.branch).toBe(overrideBranch);

    // Persisted — read back via GET.
    const list = await listBranchOverrides(request, token, instId);
    expect(list).toHaveLength(1);
    expect(list[0].chart_config_id).toBe(chartId);
    expect(list[0].branch).toBe(overrideBranch);
  });

  test('branch override is substituted into merged values, then clears/resets', async ({
    request,
  }) => {
    const instId = await apiCreateInstance(request, token, defId, uniqueName('bo-inst'), 'main');

    // Baseline: merged values use the instance branch.
    const before = await getMergedChartValues(request, token, instId, chartId);
    expect(before).toContain('img-main');

    // Set the per-chart override (no slash keeps the assertion unambiguous).
    const overrideBranch = uniqueName('override');
    const putRes = await request.put(
      `${API_BASE}/api/v1/stack-instances/${instId}/branches/${chartId}`,
      { headers: { Authorization: `Bearer ${token}` }, data: { branch: overrideBranch } },
    );
    expect(putRes.status()).toBe(200);

    // Merged values now reflect the override, not the instance branch.
    const after = await getMergedChartValues(request, token, instId, chartId);
    expect(after).toContain(`img-${overrideBranch}`);
    expect(after).not.toContain('img-main');

    // Clear the override.
    const delRes = await request.delete(
      `${API_BASE}/api/v1/stack-instances/${instId}/branches/${chartId}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(delRes.status()).toBe(204);
    expect(await listBranchOverrides(request, token, instId)).toEqual([]);

    // Merged values fall back to the instance branch again.
    const reset = await getMergedChartValues(request, token, instId, chartId);
    expect(reset).toContain('img-main');
    expect(reset).not.toContain(`img-${overrideBranch}`);
  });

  test('empty branch is rejected with 400', async ({ request }) => {
    const instId = await apiCreateInstance(request, token, defId, uniqueName('bo-inst'), 'main');

    const putRes = await request.put(
      `${API_BASE}/api/v1/stack-instances/${instId}/branches/${chartId}`,
      { headers: { Authorization: `Bearer ${token}` }, data: { branch: '' } },
    );
    expect(putRes.status()).toBe(400);
    const body = await putRes.json();
    expect(body.error).toContain('Branch is required');
  });
});

// ---------------------------------------------------------------------------
// UI-level test — drives the per-chart "Chart Branch" selector on the instance
// detail page. DB-backed, no cluster required.
// ---------------------------------------------------------------------------
test.describe('Per-chart branch overrides (UI)', () => {
  test('set and reset a chart branch override via the instance detail page', async ({ page }) => {
    await loginAsDevops(page);
    await page.goto('/');
    const token = await tokenFromPage(page);

    // Prerequisite data owned by the devops user (so the UI, which acts as that
    // user, may mutate the override).
    const defId = await apiCreateDefinition(page.request, token, uniqueName('bo-ui-def'));
    const chartName = uniqueName('bouichart');
    const chartId = await apiAddChart(page.request, token, defId, {
      chart_name: chartName,
      default_values: CHART_DEFAULT_VALUES,
    });
    const instId = await apiCreateInstance(page.request, token, defId, uniqueName('bo-ui-inst'), 'main');

    await page.goto(`/stack-instances/${instId}`);

    // The per-chart section renders once the chart tab is available.
    await expect(page.getByRole('tab', { name: chartName })).toBeVisible({ timeout: 10_000 });
    await expect(page.getByText('Using instance branch')).toBeVisible({ timeout: 10_000 });

    // Type a new branch into the per-chart selector.
    const overrideBranch = uniqueName('uioverride');
    const branchInput = page.getByLabel('Chart Branch');
    await branchInput.click();
    await branchInput.fill('');
    await branchInput.pressSequentially(overrideBranch, { delay: 20 });
    // Close any autocomplete popup without clearing the typed value.
    await page.keyboard.press('Escape');

    // The chip reflects the override immediately (local state).
    await expect(page.getByText(`Override: ${overrideBranch}`)).toBeVisible({ timeout: 10_000 });

    // The debounced save persists it — confirm via the API.
    await expect
      .poll(
        async () => {
          const list = await listBranchOverrides(page.request, token, instId);
          return list.find((o) => o.chart_config_id === chartId)?.branch ?? null;
        },
        { timeout: 15_000 },
      )
      .toBe(overrideBranch);

    // Reset via the chip delete icon ("Reset to instance branch").
    await page
      .locator('.MuiChip-root')
      .filter({ hasText: `Override: ${overrideBranch}` })
      .locator('.MuiChip-deleteIcon')
      .click();

    await expect(page.getByText('Using instance branch')).toBeVisible({ timeout: 10_000 });

    // The debounced delete removes it — confirm via the API.
    await expect
      .poll(
        async () => (await listBranchOverrides(page.request, token, instId)).length,
        { timeout: 15_000 },
      )
      .toBe(0);
  });
});
