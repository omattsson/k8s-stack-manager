import { test, expect } from '@playwright/test';
import {
  loginAsAdmin,
  loginAsUser,
  uniqueName,
  API_BASE,
  ADMIN_PASSWORD,
  ensureDefaultCluster,
  deleteCluster,
} from './helpers';

async function apiLogin(request: import('@playwright/test').APIRequestContext): Promise<string> {
  let res;
  for (let attempt = 0; attempt < 5; attempt++) {
    res = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { username: 'admin', password: ADMIN_PASSWORD },
    });
    if (res.status() !== 429) break;
    await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)));
  }
  expect(res!.ok(), `apiLogin failed with status ${res!.status()}`).toBe(true);
  return (await res!.json()).token;
}

async function apiCreateDefinition(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  name: string,
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/stack-definitions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, description: 'e2e dashboard widget test', default_branch: 'main' },
  });
  expect(res.ok(), `apiCreateDefinition failed: ${res.status()}`).toBe(true);
  return (await res.json()).id;
}

async function apiCreateInstance(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  definitionId: string,
  name: string,
  ttlMinutes?: number,
): Promise<string> {
  const data: Record<string, unknown> = {
    stack_definition_id: definitionId,
    name,
    branch: 'main',
  };
  if (ttlMinutes != null) data.ttl_minutes = ttlMinutes;
  const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  const body = await res.json();
  expect(res.ok(), `apiCreateInstance failed: ${res.status()} ${JSON.stringify(body)}`).toBe(true);
  return body.id;
}

async function apiDeleteInstance(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  instanceId: string,
): Promise<void> {
  await request.delete(`${API_BASE}/api/v1/stack-instances/${instanceId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function apiDeleteDefinition(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  defId: string,
): Promise<void> {
  await request.delete(`${API_BASE}/api/v1/stack-definitions/${defId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

function accordionHeader(page: import('@playwright/test').Page, label: string) {
  return page.locator('.MuiAccordion-root .MuiAccordionSummary-root', { hasText: label });
}

function accordion(page: import('@playwright/test').Page, label: string) {
  return page.locator('.MuiAccordion-root', { has: page.locator('.MuiAccordionSummary-content', { hasText: label }) });
}

// Use test.describe.configure to run all describes in this file serially
// to avoid hammering the login endpoint with parallel beforeAll calls.
test.describe.configure({ mode: 'serial' });

// ---------------------------------------------------------------------------
// Dashboard widget structure tests
// ---------------------------------------------------------------------------
test.describe('Dashboard Widgets - Structure', () => {
  let clusterId: string | null;

  test.beforeAll(async ({ request }) => {
    const token = await apiLogin(request);
    clusterId = await ensureDefaultCluster(request, token);
  });

  test.afterAll(async ({ request }) => {
    const token = await apiLogin(request);
    await deleteCluster(request, token, clusterId);
  });

  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
  });

  test('all four widget accordions render on the dashboard', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });
    await expect(accordionHeader(page, 'Cluster Health')).toBeVisible({ timeout: 10_000 });
    await expect(accordionHeader(page, 'Recent Deployments')).toBeVisible();
    await expect(accordionHeader(page, 'Expiring Soon')).toBeVisible();
    await expect(accordionHeader(page, 'Failing Instances')).toBeVisible();
  });

  test('cluster health widget shows registered cluster', async ({ page }) => {
    await page.goto('/');

    const widget = accordion(page, 'Cluster Health');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    // Count chip should show at least 1
    const countChip = widget.locator('.MuiAccordionSummary-content .MuiChip-root');
    await expect(countChip).toBeVisible({ timeout: 10_000 });

    // Cluster card with health chip
    await expect(widget.locator('.MuiCard-root').first()).toBeVisible({ timeout: 10_000 });
    await expect(widget.locator('.MuiCard-root .MuiChip-root').first()).toBeVisible();
  });

  test('recent deployments table has correct column headers or empty state', async ({ page }) => {
    await page.goto('/');

    const widget = accordion(page, 'Recent Deployments');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    const hasTable = await widget.locator('table').isVisible().catch(() => false);
    if (hasTable) {
      await expect(page.getByRole('columnheader', { name: 'Instance' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Action' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'Status' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'User' })).toBeVisible();
      await expect(page.getByRole('columnheader', { name: 'When' })).toBeVisible();
    } else {
      await expect(widget.getByText('No recent deployments.')).toBeVisible();
    }
  });

  test('expiring soon widget shows empty state or instance list', async ({ page }) => {
    await page.goto('/');

    const widget = accordion(page, 'Expiring Soon');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    // Either empty state text or at least one Extend TTL button
    const emptyText = widget.getByText('No instances expiring soon.');
    const extendBtn = widget.getByRole('button', { name: 'Extend TTL' }).first();
    await expect(emptyText.or(extendBtn)).toBeVisible({ timeout: 5_000 });
  });

  test('failing instances widget shows empty state or error list', async ({ page }) => {
    await page.goto('/');

    const widget = accordion(page, 'Failing Instances');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    // Either empty state text or at least one failing instance link
    const emptyText = widget.getByText('No failing instances.');
    const anyLink = widget.locator('.MuiAccordionDetails-root a').first();
    await expect(emptyText.or(anyLink)).toBeVisible({ timeout: 5_000 });
  });

  test('widgets render above the instance list', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Stack Instances' })).toBeVisible({
      timeout: 10_000,
    });

    const widgetBox = page.locator('.MuiAccordion-root').first();
    const searchBar = page.getByPlaceholder('Search instances...');
    await expect(widgetBox).toBeVisible({ timeout: 10_000 });
    await expect(searchBar).toBeVisible();

    const widgetY = (await widgetBox.boundingBox())!.y;
    const searchY = (await searchBar.boundingBox())!.y;
    expect(widgetY).toBeLessThan(searchY);
  });
});

// ---------------------------------------------------------------------------
// Collapse persistence
// ---------------------------------------------------------------------------
test.describe('Dashboard Widgets - Collapse Persistence', () => {
  let clusterId: string | null;

  test.beforeAll(async ({ request }) => {
    const token = await apiLogin(request);
    clusterId = await ensureDefaultCluster(request, token);
  });

  test.afterAll(async ({ request }) => {
    const token = await apiLogin(request);
    await deleteCluster(request, token, clusterId);
  });

  test('collapsed state persists across page navigation', async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/');

    const widget = accordion(page, 'Cluster Health');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    // Collapse the Cluster Health accordion
    await widget.locator('.MuiAccordionSummary-root').click();
    await expect(widget.locator('.MuiAccordionDetails-root')).toBeHidden({ timeout: 5_000 });

    // Navigate away and back
    await page.goto('/about');
    await page.goto('/');
    await expect(accordion(page, 'Cluster Health')).toBeVisible({ timeout: 10_000 });

    // Cluster Health should still be collapsed
    await expect(
      accordion(page, 'Cluster Health').locator('.MuiAccordionDetails-root'),
    ).toBeHidden();

    // Recent Deployments should still be expanded
    await expect(
      accordion(page, 'Recent Deployments').locator('.MuiAccordionDetails-root'),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Expiring soon widget — create an instance with short TTL
// ---------------------------------------------------------------------------
test.describe('Dashboard Widgets - Expiring Soon', () => {
  let token: string;
  let clusterId: string | null;
  let defId: string;
  let instanceId: string;

  test.beforeAll(async ({ request }) => {
    token = await apiLogin(request);
    clusterId = await ensureDefaultCluster(request, token);

    const defName = uniqueName('e2e-dash-exp-def');
    defId = await apiCreateDefinition(request, token, defName);

    const instName = uniqueName('e2e-expiring');
    instanceId = await apiCreateInstance(request, token, defId, instName, 60);

    // Deploy so it transitions to a running state with TTL active.
    // Without deployer/charts configured this returns 503/400 and the test will skip.
    const deployRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instanceId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (deployRes.status() === 202) {
      await new Promise((r) => setTimeout(r, 2000));
    }
  });

  test.afterAll(async ({ request }) => {
    await apiDeleteInstance(request, token, instanceId);
    await apiDeleteDefinition(request, token, defId);
    await deleteCluster(request, token, clusterId);
  });

  test('expiring instance appears with Extend TTL button', async ({ page }) => {
    await loginAsAdmin(page);

    // ListExpiringSoon only returns running/partial instances.
    // Without real k8s/Helm the deploy fails and the instance stays in error/draft.
    const instRes = await page.request.get(`${API_BASE}/api/v1/stack-instances/${instanceId}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const inst = await instRes.json();
    if (inst.status !== 'running' && inst.status !== 'partial') {
      test.skip(true, `Instance is "${inst.status}", not running/partial — needs real k8s`);
      return;
    }

    await page.goto('/');

    const widget = accordion(page, 'Expiring Soon');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    await expect(async () => {
      await page.reload();
      await expect(widget.getByText(/e2e-expiring/)).toBeVisible({ timeout: 5_000 });
    }).toPass({ timeout: 30_000 });

    await expect(page.getByRole('button', { name: 'Extend TTL' }).first()).toBeVisible();
  });

  test('Extend TTL button works without error', async ({ page }) => {
    await loginAsAdmin(page);

    const instRes = await page.request.get(`${API_BASE}/api/v1/stack-instances/${instanceId}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const inst = await instRes.json();
    if (inst.status !== 'running' && inst.status !== 'partial') {
      test.skip(true, `Instance is "${inst.status}", not running/partial — needs real k8s`);
      return;
    }

    await page.goto('/');

    const widget = accordion(page, 'Expiring Soon');

    await expect(async () => {
      await page.reload();
      await expect(widget.getByText(/e2e-expiring/)).toBeVisible({ timeout: 5_000 });
    }).toPass({ timeout: 30_000 });

    await page.getByRole('button', { name: 'Extend TTL' }).first().click();

    // No error toast should appear
    await expect(page.getByText(/Failed to extend TTL/)).not.toBeVisible({ timeout: 5_000 });
  });
});

// ---------------------------------------------------------------------------
// Failing instances widget — deploy to trigger an error state
// ---------------------------------------------------------------------------
test.describe('Dashboard Widgets - Failing Instances', () => {
  let token: string;
  let clusterId: string | null;
  let defId: string;
  let instanceId: string;

  test.beforeAll(async ({ request }) => {
    token = await apiLogin(request);
    clusterId = await ensureDefaultCluster(request, token);

    const defName = uniqueName('e2e-dash-fail-def');
    defId = await apiCreateDefinition(request, token, defName);

    const instName = uniqueName('e2e-failing');
    instanceId = await apiCreateInstance(request, token, defId, instName);

    // Deploy — without real k8s/Helm this should fail and put instance in error state.
    // If deployer isn't configured (503/400), no state change occurs and the test will skip.
    const deployRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instanceId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (deployRes.status() === 202) {
      await new Promise((r) => setTimeout(r, 5000));
    }
  });

  test.afterAll(async ({ request }) => {
    await apiDeleteInstance(request, token, instanceId);
    await apiDeleteDefinition(request, token, defId);
    await deleteCluster(request, token, clusterId);
  });

  test('failing instance appears in widget with error badge', async ({ page }) => {
    await loginAsAdmin(page);

    // Check if instance actually ended up in error state
    const instRes = await page.request.get(`${API_BASE}/api/v1/stack-instances/${instanceId}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const inst = await instRes.json();
    if (inst.status !== 'error') {
      test.skip(true, 'Instance did not reach error state');
      return;
    }

    await page.goto('/');
    const widget = accordion(page, 'Failing Instances');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    await expect(async () => {
      await page.reload();
      await expect(widget.getByText(/e2e-failing/)).toBeVisible({ timeout: 5_000 });
    }).toPass({ timeout: 30_000 });

    // Link to instance detail
    const link = widget.getByRole('link', { name: /e2e-failing/ });
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute('href', `/stack-instances/${instanceId}`);

    // Error-colored count badge
    const badge = widget
      .locator('.MuiAccordionSummary-content')
      .locator('.MuiChip-colorError');
    await expect(badge).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Recent deployments widget — trigger a deploy and verify it appears
// ---------------------------------------------------------------------------
test.describe('Dashboard Widgets - Recent Deployments', () => {
  let token: string;
  let clusterId: string | null;
  let defId: string;
  let instanceId: string;
  let deployAccepted = false;

  test.beforeAll(async ({ request }) => {
    token = await apiLogin(request);
    clusterId = await ensureDefaultCluster(request, token);

    const defName = uniqueName('e2e-dash-deploy-def');
    defId = await apiCreateDefinition(request, token, defName);

    const instName = uniqueName('e2e-deployed');
    instanceId = await apiCreateInstance(request, token, defId, instName);

    // Trigger a deploy so a deployment log entry exists.
    // Returns 503 if deployer not configured, 400 if no charts — in those cases
    // no log entry is created and the tests should skip.
    const deployRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instanceId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    deployAccepted = deployRes.status() === 202;
    if (deployAccepted) {
      await new Promise((r) => setTimeout(r, 3000));
    }
  });

  test.afterAll(async ({ request }) => {
    await apiDeleteInstance(request, token, instanceId);
    await apiDeleteDefinition(request, token, defId);
    await deleteCluster(request, token, clusterId);
  });

  test('deployment entry appears with correct columns', async ({ page }) => {
    if (!deployAccepted) {
      test.skip(true, 'Deploy returned non-202 — no deployment log created');
      return;
    }

    await loginAsAdmin(page);
    await page.goto('/');

    const widget = accordion(page, 'Recent Deployments');
    await expect(widget).toBeVisible({ timeout: 10_000 });

    await expect(async () => {
      await page.reload();
      await expect(widget.locator('table')).toBeVisible({ timeout: 5_000 });
      await expect(widget.getByText(/e2e-deployed/)).toBeVisible({ timeout: 3_000 });
    }).toPass({ timeout: 30_000 });

    // Table headers
    await expect(page.getByRole('columnheader', { name: 'Instance' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Action' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Status' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'User' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'When' })).toBeVisible();

    // Instance link
    const instLink = widget.getByRole('link', { name: /e2e-deployed/ });
    await expect(instLink).toBeVisible();
    await expect(instLink).toHaveAttribute('href', `/stack-instances/${instanceId}`);

    // Action chip
    await expect(widget.locator('.MuiChip-outlined', { hasText: 'deploy' })).toBeVisible();

    // Status chip (success or error depending on env)
    const row = widget.locator('table tbody tr', { hasText: /e2e-deployed/ });
    await expect(row.locator('.MuiChip-filled')).toBeVisible();
  });

  test('deployment instance link navigates to detail page', async ({ page }) => {
    if (!deployAccepted) {
      test.skip(true, 'Deploy returned non-202 — no deployment log created');
      return;
    }

    await loginAsAdmin(page);
    await page.goto('/');

    const widget = accordion(page, 'Recent Deployments');
    await expect(async () => {
      await page.reload();
      await expect(widget.getByText(/e2e-deployed/)).toBeVisible({ timeout: 5_000 });
    }).toPass({ timeout: 30_000 });

    await widget.getByRole('link', { name: /e2e-deployed/ }).click();
    await page.waitForURL(`/stack-instances/${instanceId}`, { timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1, name: /e2e-deployed/ })).toBeVisible({
      timeout: 10_000,
    });
  });
});

// ---------------------------------------------------------------------------
// Role-based visibility
// ---------------------------------------------------------------------------
test.describe('Dashboard Widgets - Role-Based Visibility', () => {
  let clusterId: string | null;

  test.beforeAll(async ({ request }) => {
    const token = await apiLogin(request);
    clusterId = await ensureDefaultCluster(request, token);
  });

  test.afterAll(async ({ request }) => {
    const token = await apiLogin(request);
    await deleteCluster(request, token, clusterId);
  });

  test('admin sees cluster card with health chip', async ({ page }) => {
    await loginAsAdmin(page);
    await page.goto('/');

    const widget = accordion(page, 'Cluster Health');
    const card = widget.locator('.MuiCard-root').first();
    await expect(card).toBeVisible({ timeout: 10_000 });

    // Admin should see health chip (healthy/degraded/unreachable/unknown)
    await expect(card.locator('.MuiChip-root')).toBeVisible();

    // Node metrics are only populated when the cluster is reachable.
    // Verify they're present if the cluster is reachable, skip assertion otherwise.
    const nodesText = card.getByText(/Nodes:/);
    const nodesVisible = await nodesText.isVisible().catch(() => false);
    if (nodesVisible) {
      await expect(card.getByText(/CPU:/)).toBeVisible();
      await expect(card.getByText(/Memory:/)).toBeVisible();
    }
  });

  test('regular user sees cluster card without node metrics', async ({ page }) => {
    await loginAsUser(page);
    await page.goto('/');

    const widget = accordion(page, 'Cluster Health');
    const card = widget.locator('.MuiCard-root').first();
    await expect(card).toBeVisible({ timeout: 10_000 });

    // Regular user should see the health chip showing "unknown" (backend strips health_status)
    await expect(card.locator('.MuiChip-root')).toBeVisible();

    // Regular user should NOT see node metrics (backend strips them for non-privileged)
    await expect(card.getByText(/Nodes:/)).toBeHidden();
  });
});
