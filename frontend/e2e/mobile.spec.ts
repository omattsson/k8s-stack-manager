import { test, expect, type Page } from '@playwright/test';
import { API_BASE, apiAddChart, apiCreateDefinition, apiLogin, loginAsAdmin, uniqueName } from './helpers';

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
      await page.waitForLoadState('networkidle').catch(() => undefined);

      const overflow = await pageOverflow(page);
      expect(overflow, `page is ${overflow}px wider than the screen: ${(await overflowingElements(page)).join('; ')}`).toBeLessThanOrEqual(1);

      // The app bar controls are inside the screen and can be tapped.
      const bell = page.locator('header').getByRole('button', { name: 'Open notifications' });
      const box = await bell.boundingBox();
      expect(box, 'notification bell').not.toBeNull();
      expect(box!.x + box!.width).toBeLessThanOrEqual(PHONE_WIDTH);
      await bell.tap();
      await expect(page.locator('.MuiPopover-paper')).toBeVisible();
      await page.keyboard.press('Escape');
      await page.locator('header').getByRole('button', { name: 'Open navigation' }).tap();
      await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
    });
  }

  test('text inputs use at least 16px, so iOS does not zoom on focus', async ({ page }) => {
    await page.goto('/stack-instances/new');
    const name = page.getByLabel('Instance Name');
    await expect(name).toBeVisible({ timeout: 10_000 });
    const sizes = await page.evaluate(() =>
      Array.from(document.querySelectorAll('main input:not([type=hidden]):not([type=checkbox]):not([type=radio]), main textarea'))
        .filter((e) => e.getBoundingClientRect().width > 0)
        .map((e) => parseFloat(getComputedStyle(e).fontSize)),
    );
    expect(sizes.length).toBeGreaterThan(0);
    expect(Math.min(...sizes)).toBeGreaterThanOrEqual(16);
  });

  test('compare shows a unified diff on a phone', async ({ page }) => {
    await page.goto(`/stack-instances/compare?left=${ids.left}&right=${ids.right}`);
    // A split view has two code columns side by side; a unified view has one.
    const table = page.locator('main table').filter({ has: page.locator('pre') }).first();
    await expect(table).toBeVisible({ timeout: 15_000 });
    const columns = await table.evaluate((t) => {
      const row = Array.from(t.querySelectorAll('tr')).find((r) => r.querySelectorAll('pre').length > 0);
      return row ? row.querySelectorAll('pre').length : 0;
    });
    expect(columns).toBe(1);
  });
});
