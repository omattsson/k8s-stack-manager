import { test, expect, type Page } from '@playwright/test';
import { API_BASE, apiAddChart, apiCreateDefinition, apiLogin, loginAsAdmin, uniqueName } from './helpers';
import { stubHealth } from './stub/client';

// Phone layout (#577, #586). Runs in the Playwright project "mobile"
// (iPhone 13 viewport, 390 x 844, touch). DB-backed, no cluster required.

const PHONE_WIDTH = 390;

/** How far the page is wider than the screen, in CSS pixels (0: no horizontal scroll). */
const pageOverflow = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);

/** Elements that end right of the screen and are not inside a scroll container (for the failure message). */
const overflowingElements = (page: Page) =>
  page.evaluate((width) => {
    const out: string[] = [];
    for (const el of Array.from(document.querySelectorAll('main *'))) {
      const r = el.getBoundingClientRect();
      if (!r.width || r.right <= width + 1) continue;
      let p = el.parentElement;
      let scrolls = false;
      while (p && p.tagName !== 'MAIN') {
        if (getComputedStyle(p).overflowX !== 'visible' && p.getBoundingClientRect().right <= width + 1) {
          scrolls = true;
          break;
        }
        p = p.parentElement;
      }
      if (!scrolls) out.push(`${el.tagName.toLowerCase()} right=${Math.round(r.right)} "${(el.textContent ?? '').trim().slice(0, 40)}"`);
    }
    return out.slice(-5);
  }, PHONE_WIDTH);

test.describe('Mobile layout', () => {
  const ids: Record<string, string> = {};

  test.beforeAll(async ({ request }) => {
    const token = await apiLogin(request);
    const headers = { Authorization: `Bearer ${token}` };
    // Two definitions with different charts, so that compare shows a diff.
    ids.definition = await apiCreateDefinition(request, token, uniqueName('mob-def'));
    await apiAddChart(request, token, ids.definition, { chart_name: 'web', default_values: 'replicaCount: 1\nimage:\n  tag: main\n' });
    ids.definition2 = await apiCreateDefinition(request, token, uniqueName('mob-def2'));
    await apiAddChart(request, token, ids.definition2, { chart_name: 'web', default_values: 'replicaCount: 3\nimage:\n  tag: develop\n' });
    for (const [key, def] of [['left', ids.definition], ['right', ids.definition2]]) {
      const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
        headers,
        data: { stack_definition_id: def, name: uniqueName(`mob-${key}`).slice(0, 40), branch: 'main' },
      });
      expect(res.ok(), await res.text()).toBe(true);
      ids[key] = (await res.json()).id;
    }
    const tpl = await request.post(`${API_BASE}/api/v1/templates`, {
      headers,
      data: { name: uniqueName('mob-tpl'), description: 'Mobile layout check', category: 'Web', version: '1.0.0' },
    });
    expect(tpl.ok(), await tpl.text()).toBe(true);
    ids.template = (await tpl.json()).id;
  });

  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
  });

  const routes: Array<[string, () => string]> = [
    ['dashboard', () => '/'],
    ['templates', () => '/templates'],
    ['template builder (new)', () => '/templates/new'],
    ['template builder (edit)', () => `/templates/${ids.template}/edit`],
    ['definitions', () => '/stack-definitions'],
    ['definition edit', () => `/stack-definitions/${ids.definition}/edit`],
    ['create instance', () => '/stack-instances/new'],
    ['instance detail', () => `/stack-instances/${ids.left}`],
    ['compare', () => `/stack-instances/compare?left=${ids.left}&right=${ids.right}`],
    ['audit log', () => '/audit-log'],
    ['users', () => '/admin/users'],
    ['clusters', () => '/admin/clusters'],
    ['cluster health', () => '/admin/cluster-health'],
    ['shared values', () => '/admin/shared-values'],
    ['cleanup policies', () => '/admin/cleanup-policies'],
    ['notification channels', () => '/admin/notification-channels'],
    ['profile', () => '/profile'],
    ['notifications', () => '/notifications'],
  ];

  for (const [name, path] of routes) {
    test(`${name}: no horizontal page scroll, app bar can be tapped`, async ({ page }) => {
      await page.goto(path());
      await expect(page.locator('main')).toBeVisible({ timeout: 10_000 });
      await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => undefined);

      const overflow = await pageOverflow(page);
      expect(overflow, `page is ${overflow}px wider than the screen: ${(await overflowingElements(page)).join('; ')}`).toBeLessThanOrEqual(1);

      // The app bar controls are inside the screen and can be tapped.
      const bell = page.locator('header').getByRole('button', { name: 'Open notifications' });
      const box = await bell.boundingBox();
      expect(box, 'notification bell').not.toBeNull();
      expect(box!.x + box!.width).toBeLessThanOrEqual(PHONE_WIDTH);
      await bell.tap();
      const popover = page.locator('.MuiPopover-paper').filter({ hasText: 'Notifications' });
      await expect(popover).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(popover).toBeHidden();
      await page.locator('header').getByRole('button', { name: 'Open navigation' }).tap();
      await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
    });
  }

  test('text inputs use at least 16px, so iOS does not zoom on focus', async ({ page }) => {
    await page.goto('/stack-instances/new');
    const name = page.getByLabel('Instance Name');
    await expect(name).toBeVisible({ timeout: 10_000 });
    // Only fields that can get focus: the hidden native input of a MUI Select
    // (aria-hidden, tabindex -1) never gets focus, so iOS does not zoom on it.
    const sizes = await page.evaluate(() =>
      Array.from(document.querySelectorAll('main input:not([type=hidden]):not([type=checkbox]):not([type=radio]), main textarea'))
        .filter((e) => e.getBoundingClientRect().width > 0)
        .filter((e) => e.getAttribute('aria-hidden') !== 'true' && e.getAttribute('tabindex') !== '-1')
        .map((e) => parseFloat(getComputedStyle(e).fontSize)),
    );
    expect(sizes.length).toBeGreaterThan(0);
    expect(Math.min(...sizes)).toBeGreaterThanOrEqual(16);
  });

  test('the quota override dialog fits a phone', async ({ page }) => {
    await page.goto(`/stack-instances/${ids.left}`);
    const section = page.getByRole('region', { name: 'Resource quota' });
    await section.getByRole('button', { name: 'Edit override' }).tap({ timeout: 15_000 });
    const dialog = page.getByRole('dialog', { name: 'Quota override' });
    await expect(dialog.getByLabel('CPU Limit', { exact: true })).toBeVisible({ timeout: 10_000 });

    const overflow = await pageOverflow(page);
    expect(overflow, `page is ${overflow}px wider than the screen`).toBeLessThanOrEqual(1);
    const box = await dialog.boundingBox();
    expect(box, 'dialog').not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(PHONE_WIDTH);
    // The actions stay below the scrolling fields.
    await expect(dialog.getByRole('button', { name: 'Save' })).toBeInViewport();
    await dialog.getByRole('button', { name: 'Cancel' }).tap();
    await expect(dialog).toBeHidden();
  });

  test('instance page with action runs: no horizontal page scroll, Show log can be tapped', async ({ page, request }) => {
    const token = await apiLogin(request);
    const headers = { Authorization: `Bearer ${token}` };
    const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
      headers,
      data: { stack_definition_id: ids.definition, name: uniqueName('mob-runs').slice(0, 40), branch: 'main' },
    });
    expect(res.ok(), await res.text()).toBe(true);
    const instanceId = (await res.json()).id as string;
    try {
      const actionsUrl = `${API_BASE}/api/v1/stack-instances/${instanceId}/actions`;
      const list = await request.get(actionsUrl, { headers });
      const names = (((await list.json()).actions ?? []) as { name: string }[]).map((a) => a.name);
      test.skip(!names.includes('e2e-job') || !(await stubHealth(request)),
        'Needs the e2e actions and the e2e stub (make test-e2e starts both).');

      // A long value without spaces must wrap, not widen the page (#640).
      const echo = await request.post(`${actionsUrl}/e2e-echo`, {
        headers,
        data: { parameters: { target: `mobile-${'x'.repeat(100)}`, mode: 'full' } },
      });
      expect(echo.status(), await echo.text()).toBe(200);
      const job = await request.post(`${actionsUrl}/e2e-job`, { headers, data: {} });
      expect(job.status(), await job.text()).toBe(200);
      const runId = (await job.json()).run_id as string;
      expect(runId, 'run_id').toBeTruthy();

      await page.goto(`/stack-instances/${instanceId}`);
      const showLog = page.getByTestId('action-runs').getByTestId(`action-run-show-log-${runId}`);
      await expect(showLog).toBeVisible({ timeout: 15_000 });
      await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => undefined);

      const overflow = await pageOverflow(page);
      expect(overflow, `page is ${overflow}px wider than the screen: ${(await overflowingElements(page)).join('; ')}`).toBeLessThanOrEqual(1);

      await showLog.scrollIntoViewIfNeeded();
      const box = await showLog.boundingBox();
      expect(box, 'Show log button').not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(PHONE_WIDTH);
      await showLog.tap();
      const dialog = page.getByRole('dialog', { name: 'Job log: E2E job' });
      await expect(dialog.getByTestId('job-log')).toBeVisible();
      const dialogBox = await dialog.boundingBox();
      expect(dialogBox, 'dialog').not.toBeNull();
      expect(dialogBox!.x).toBeGreaterThanOrEqual(0);
      expect(dialogBox!.x + dialogBox!.width).toBeLessThanOrEqual(PHONE_WIDTH);
      await dialog.getByRole('button', { name: 'Close' }).tap();
      await expect(dialog).toBeHidden();
    } finally {
      await request.delete(`${API_BASE}/api/v1/stack-instances/${instanceId}`, { headers });
    }
  });

  test('compare shows a unified diff on a phone', async ({ page }) => {
    await page.goto(`/stack-instances/compare?left=${ids.left}&right=${ids.right}`);
    // react-diff-viewer marks the table of a split view with the class label
    // "split-view"; a unified view has only "diff-container".
    const table = page.locator('main table[class*="diff-container"]').first();
    await expect(table).toBeVisible({ timeout: 15_000 });
    await expect(table).not.toHaveClass(/split-view/);
  });
});
