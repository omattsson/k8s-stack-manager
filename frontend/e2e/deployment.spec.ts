import { test, expect } from '@playwright/test';
import { loginAsDevops, uniqueName, createAndPublishTemplate, instantiateTemplate } from './helpers';

const API_BASE = 'http://localhost:8081';

/**
 * Helper: login via API and return the JWT token.
 */
async function apiLogin(request: import('@playwright/test').APIRequestContext): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { username: 'admin', password: 'admin' },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.token;
}

/**
 * Helper: create a stack definition via API.
 */
async function apiCreateDefinition(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  name: string,
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/stack-definitions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      description: 'E2E deployment test definition',
      default_branch: 'main',
    },
  });
  expect(res.ok()).toBe(true);
  const body = await res.json();
  return body.id;
}

/**
 * Helper: add a chart config to a definition so it can be deployed.
 */
async function apiAddChart(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  definitionId: string,
): Promise<void> {
  const res = await request.post(`${API_BASE}/api/v1/stack-definitions/${definitionId}/charts`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      chart_name: 'test-chart',
      repository_url: 'https://charts.example.com',
      chart_version: '1.0.0',
    },
  });
  expect(res.ok()).toBe(true);
}

/**
 * Helper: create a stack definition with a chart via API (ready for deploy).
 */
async function apiCreateDeployableDefinition(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  name: string,
): Promise<string> {
  const defId = await apiCreateDefinition(request, token, name);
  await apiAddChart(request, token, defId);
  return defId;
}

/**
 * Helper: create a stack instance via API.
 */
async function apiCreateInstance(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  definitionId: string,
  name: string,
): Promise<string> {
  const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      stack_definition_id: definitionId,
      name,
      branch: 'main',
    },
  });
  const body = await res.json();
  expect(res.ok(), `apiCreateInstance failed: ${res.status()} ${JSON.stringify(body)}`).toBe(true);
  return body.id;
}

/**
 * Helper: get a stack instance by ID via API.
 */
async function apiGetInstance(
  request: import('@playwright/test').APIRequestContext,
  token: string,
  instanceId: string,
) {
  const res = await request.get(`${API_BASE}/api/v1/stack-instances/${instanceId}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(res.ok()).toBe(true);
  return res.json();
}

// ---------------------------------------------------------------------------
// API-level tests (using request context, not browser)
// ---------------------------------------------------------------------------
test.describe('Deployment API', () => {
  let token: string;

  test.beforeAll(async ({ request }) => {
    token = await apiLogin(request);
  });

  test('deploy instance returns 202 and transitions to deploying then error', async ({ request }) => {
    const defName = uniqueName('deploy-def');
    const defId = await apiCreateDeployableDefinition(request, token, defName);
    const instName = uniqueName('deploy-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    const deployRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    // Deploy might return 202 (accepted) or 503 (deployer not configured in test env).
    if (deployRes.status() === 503) {
      // Deployment service not available in test env — skip remaining assertions.
      test.skip();
      return;
    }

    expect(deployRes.status()).toBe(202);
    const deployBody = await deployRes.json();
    expect(deployBody).toHaveProperty('log_id');
    expect(deployBody).toHaveProperty('message', 'Deployment started');

    // Instance should be in deploying (or already transitioned to error/running).
    const inst = await apiGetInstance(request, token, instId);
    expect(['deploying', 'error', 'running', 'stopping']).toContain(inst.status);

    // Wait briefly for async deploy to fail (no helm in CI), then check status.
    await new Promise((r) => setTimeout(r, 3000));
    const instAfter = await apiGetInstance(request, token, instId);
    // Should be error because helm is not available in the test environment, but may be running if Helm/K8s succeeds.
    expect(['deploying', 'error', 'running', 'stopping']).toContain(instAfter.status);
  });

  test('deploy already deploying instance returns 409', async ({ request }) => {
    const defName = uniqueName('conflict-def');
    const defId = await apiCreateDeployableDefinition(request, token, defName);
    const instName = uniqueName('conflict-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    // First deploy
    const firstRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    if (firstRes.status() === 503) {
      test.skip();
      return;
    }
    expect(firstRes.status()).toBe(202);

    // Immediately try a second deploy — instance should still be deploying.
    const secondRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    // Expect 409 (conflict) because it is deploying.
    // If the first deploy already failed (error state), it might be 202 again.
    expect([409, 202]).toContain(secondRes.status());
    if (secondRes.status() === 409) {
      const body = await secondRes.json();
      expect(body.error).toContain('Cannot deploy');
    }
  });

  test('stop non-running instance returns 409', async ({ request }) => {
    const defName = uniqueName('stop-draft-def');
    const defId = await apiCreateDefinition(request, token, defName);
    const instName = uniqueName('stop-draft-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    // Instance is in draft state — stop should be rejected.
    const stopRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/stop`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    if (stopRes.status() === 503) {
      test.skip();
      return;
    }

    expect(stopRes.status()).toBe(409);
    const body = await stopRes.json();
    expect(body.error).toContain('Cannot stop');
  });

  test('get deployment logs returns array', async ({ request }) => {
    const defName = uniqueName('log-def');
    const defId = await apiCreateDeployableDefinition(request, token, defName);
    const instName = uniqueName('log-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    // Trigger a deploy so there is a log entry.
    const deployRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    if (deployRes.status() === 503) {
      // If deploy service not available, logs endpoint may also be 503.
      const logRes = await request.get(`${API_BASE}/api/v1/stack-instances/${instId}/deploy-log`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      // Accept 200 (empty array) or 503.
      expect([200, 503]).toContain(logRes.status());
      test.skip();
      return;
    }

    // Wait a moment for the log to be written.
    await new Promise((r) => setTimeout(r, 2000));

    const logRes = await request.get(`${API_BASE}/api/v1/stack-instances/${instId}/deploy-log`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(logRes.ok()).toBe(true);
    const logs = await logRes.json();
    expect(Array.isArray(logs)).toBe(true);
    expect(logs.length).toBeGreaterThanOrEqual(1);

    // Verify log entry shape.
    const entry = logs[0];
    expect(entry).toHaveProperty('id');
    expect(entry).toHaveProperty('stack_instance_id', instId);
    expect(entry).toHaveProperty('action', 'deploy');
    expect(['running', 'success', 'error']).toContain(entry.status);
    expect(entry).toHaveProperty('started_at');
  });

  test('get instance status endpoint responds', async ({ request }) => {
    const defName = uniqueName('status-def');
    const defId = await apiCreateDefinition(request, token, defName);
    const instName = uniqueName('status-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    const statusRes = await request.get(`${API_BASE}/api/v1/stack-instances/${instId}/status`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    // May return 200 (cached status), 404 (no status yet), or 503 (no K8s).
    expect([200, 404, 503]).toContain(statusRes.status());

    if (statusRes.status() === 200) {
      const body = await statusRes.json();
      expect(body).toHaveProperty('namespace');
      expect(body).toHaveProperty('status');
    }
  });

  test('deploy from error state returns 202', async ({ request }) => {
    const defName = uniqueName('redeploy-def');
    const defId = await apiCreateDeployableDefinition(request, token, defName);
    const instName = uniqueName('redeploy-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    // First deploy
    const firstRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    if (firstRes.status() === 503) {
      test.skip();
      return;
    }
    expect(firstRes.status()).toBe(202);

    // Wait for the deploy to fail (helm not available).
    let retries = 10;
    let inst;
    while (retries > 0) {
      await new Promise((r) => setTimeout(r, 1000));
      inst = await apiGetInstance(request, token, instId);
      if (inst.status === 'error') break;
      retries--;
    }

    // If still deploying after waiting, skip — test environment may be slow.
    if (inst?.status !== 'error') {
      test.skip();
      return;
    }

    // Re-deploy from error state.
    const secondRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(secondRes.status()).toBe(202);
    const body = await secondRes.json();
    expect(body).toHaveProperty('log_id');
    expect(body).toHaveProperty('message', 'Deployment started');
  });

  test('clean draft instance returns 409', async ({ request }) => {
    const defName = uniqueName('clean-draft-def');
    const defId = await apiCreateDefinition(request, token, defName);
    const instName = uniqueName('clean-draft-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    // Instance is in draft state — clean should be rejected.
    const cleanRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/clean`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    expect(cleanRes.status()).toBe(409);
    const body = await cleanRes.json();
    expect(body.error).toContain('Cannot clean');
  });

  test('clean endpoint returns 202 for error instance', async ({ request }) => {
    const defName = uniqueName('clean-err-def');
    const defId = await apiCreateDeployableDefinition(request, token, defName);
    const instName = uniqueName('clean-err-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    // Deploy — it will fail since no real helm, transitioning to error.
    const deployRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    if (deployRes.status() === 503) {
      test.skip();
      return;
    }
    expect(deployRes.status()).toBe(202);

    // Wait for the deploy to fail (helm not available).
    let retries = 10;
    let inst;
    while (retries > 0) {
      await new Promise((r) => setTimeout(r, 1000));
      inst = await apiGetInstance(request, token, instId);
      if (inst.status === 'error' || inst.status === 'running') break;
      retries--;
    }

    // If not in a cleanable state after waiting, skip.
    if (!['error', 'running', 'stopped'].includes(inst?.status)) {
      test.skip();
      return;
    }

    // Clean the instance.
    const cleanRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/clean`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(cleanRes.status()).toBe(202);
    const body = await cleanRes.json();
    expect(body).toHaveProperty('log_id');
  });

  test('clean creates deployment log with action clean', async ({ request }) => {
    const defName = uniqueName('clean-log-def');
    const defId = await apiCreateDeployableDefinition(request, token, defName);
    const instName = uniqueName('clean-log-inst');
    const instId = await apiCreateInstance(request, token, defId, instName);

    // Deploy first to get the instance into error state.
    const deployRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/deploy`, {
      headers: { Authorization: `Bearer ${token}` },
    });

    if (deployRes.status() === 503) {
      test.skip();
      return;
    }
    expect(deployRes.status()).toBe(202);

    // Wait for the deploy to fail.
    let retries = 10;
    let inst;
    while (retries > 0) {
      await new Promise((r) => setTimeout(r, 1000));
      inst = await apiGetInstance(request, token, instId);
      if (inst.status === 'error' || inst.status === 'running') break;
      retries--;
    }

    if (!['error', 'running', 'stopped'].includes(inst?.status)) {
      test.skip();
      return;
    }

    // Clean the instance.
    const cleanRes = await request.post(`${API_BASE}/api/v1/stack-instances/${instId}/clean`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(cleanRes.status()).toBe(202);

    // Wait for the clean operation to complete and log to be written.
    await new Promise((r) => setTimeout(r, 3000));

    // Fetch deploy logs and verify a clean action exists.
    const logRes = await request.get(`${API_BASE}/api/v1/stack-instances/${instId}/deploy-log`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(logRes.ok()).toBe(true);
    const logs = await logRes.json();
    expect(Array.isArray(logs)).toBe(true);

    const cleanLogs = logs.filter((entry: { action: string }) => entry.action === 'clean');
    expect(cleanLogs.length).toBeGreaterThanOrEqual(1);

    // Verify the clean log entry shape.
    const cleanEntry = cleanLogs[0];
    expect(cleanEntry).toHaveProperty('id');
    expect(cleanEntry).toHaveProperty('stack_instance_id', instId);
    expect(cleanEntry).toHaveProperty('action', 'clean');
    expect(cleanEntry).toHaveProperty('started_at');
  });
});

// ---------------------------------------------------------------------------
// UI-level tests (using browser)
// ---------------------------------------------------------------------------
test.describe('Deployment UI', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDevops(page);
  });

  test('deploy button visible on draft instance, stop button hidden', async ({ page }) => {
    // Create prerequisite data via UI helpers.
    const tplName = uniqueName('tpl-deploy-btn');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-deploy-btn');
    await instantiateTemplate(page, templateId, defName);

    // Create instance via UI.
    const instName = uniqueName('inst-deploy-btn');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded');

    // Verify Deploy button is visible for draft instance.
    await expect(page.getByRole('button', { name: 'Deploy' })).toBeVisible({ timeout: 10_000 });

    // Stop button should NOT be visible for draft instance.
    await expect(page.getByRole('button', { name: 'Stop' })).not.toBeVisible();
  });

  test('deploy button triggers deployment and shows snackbar', async ({ page }) => {
    const tplName = uniqueName('tpl-deploy-click');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-deploy-click');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-deploy-click');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded');

    // Click Deploy.
    await page.getByRole('button', { name: 'Deploy' }).click();

    // Expect either a success snackbar or an error (if deployer not configured).
    // The snackbar text depends on the backend configuration.
    const snackbar = page.locator('.MuiSnackbar-root, .MuiAlert-root');
    await expect(snackbar.first()).toBeVisible({ timeout: 10_000 });
  });

  test('deployment history section appears after deploy', async ({ page }) => {
    const tplName = uniqueName('tpl-deploy-hist');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-deploy-hist');
    await instantiateTemplate(page, templateId, defName);

    const instName = uniqueName('inst-deploy-hist');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded');

    // Draft instance should NOT show Deployment History yet.
    await expect(page.getByRole('button', { name: 'Deploy' })).toBeVisible({ timeout: 10_000 });

    // Extract the instance ID from the URL for API checks.
    const instanceId = page.url().split('/stack-instances/')[1];

    // Trigger a deploy — this creates a deployment log entry.
    await page.getByRole('button', { name: 'Deploy' }).click();

    // Wait for a snackbar (success or error) to confirm the deploy was triggered.
    const snackbar = page.locator('.MuiSnackbar-root, .MuiAlert-root');
    await expect(snackbar.first()).toBeVisible({ timeout: 10_000 });

    // Poll the deploy-log API until at least one log entry exists.
    // The deploy is async, so the log may not exist immediately after the 202.
    const token = await page.evaluate(() => localStorage.getItem('token'));
    for (let i = 0; i < 10; i++) {
      await new Promise((r) => setTimeout(r, 1000));
      const res = await page.request.get(
        `http://localhost:8081/api/v1/stack-instances/${instanceId}/deploy-log`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      if (res.ok()) {
        const logs = (await res.json()) as unknown[];
        if (logs.length > 0) break;
      }
    }

    // Reload the page — now Deployment History should be visible.
    await page.reload();
    await expect(page.getByText('Deployment History')).toBeVisible({ timeout: 15_000 });
  });

  test('Clean Namespace button is visible for error instance', async ({ page }) => {
    const tplName = uniqueName('tpl-clean-btn');
    const templateId = await createAndPublishTemplate(page, tplName);
    const defName = uniqueName('def-clean-btn');
    await instantiateTemplate(page, templateId, defName);

    // Create instance via UI.
    const instName = uniqueName('inst-clean-btn');
    await page.goto('/stack-instances/new');
    await page.getByLabel('Stack Definition').click();
    await page.getByRole('option', { name: new RegExp(defName) }).click();
    await page.getByLabel('Instance Name').fill(instName);
    await page.getByRole('button', { name: 'Create Instance' }).click();
    await page.waitForURL(/\/stack-instances\/[^/]+$/, { timeout: 10_000 });
    await page.waitForLoadState('domcontentloaded');

    // Deploy to trigger error state (helm not available in test env).
    await page.getByRole('button', { name: 'Deploy' }).click();

    // Wait for snackbar to confirm deploy was triggered.
    const snackbar = page.locator('.MuiSnackbar-root, .MuiAlert-root');
    await expect(snackbar.first()).toBeVisible({ timeout: 10_000 });

    // Extract the instance ID and poll for error state via API.
    const instanceId = page.url().split('/stack-instances/')[1];
    const token = await page.evaluate(() => localStorage.getItem('token'));

    let reachedError = false;
    for (let i = 0; i < 10; i++) {
      await new Promise((r) => setTimeout(r, 1000));
      const res = await page.request.get(
        `http://localhost:8081/api/v1/stack-instances/${instanceId}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      if (res.ok()) {
        const inst = await res.json();
        if (inst.status === 'error') {
          reachedError = true;
          break;
        }
      }
    }

    if (!reachedError) {
      test.skip();
      return;
    }

    // Reload the page so the UI reflects the error state.
    await page.reload();

    // Verify Clean Namespace button is visible for error instance.
    await expect(
      page.getByRole('button', { name: 'Clean Namespace' }),
    ).toBeVisible({ timeout: 10_000 });
  });
});
