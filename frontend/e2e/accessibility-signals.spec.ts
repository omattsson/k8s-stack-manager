import { test, expect, type Page, type WebSocketRoute } from '@playwright/test';
import type { PaginatedAuditLogs } from '../src/api/client';
import type {
  Cluster,
  ClusterNamespaceInfo,
  ClusterSummary,
  ClusterUtilization,
  NamespaceResourceUsage,
  NamespaceStatus,
  NodeStatusInfo,
  Notification,
  NotificationListResponse,
  StackInstance,
  TemplateStats,
  UserFavorite,
} from '../src/types';
import {
  AA_NON_TEXT,
  MODES,
  checkPage,
  createSeed,
  deleteSeed,
  focusRingSweep,
  nonTextRatio,
  pageList,
  tooltipRatio,
  type SweepOptions,
  type PageSetup,
  type Seed,
} from './a11y';

// What axe does not judge (#758). Focus rings: computed outline or border colour
// against what is behind it, 3:1, and not cut off by a parent. Colour as the only
// signal: each state must also differ in shape, weight or text. Bars: the value is
// also text next to the bar. Tooltips are translucent, so tooltipRatio measures
// them over white and over black.
//
// Not covered here, inspected by hand: whether a shape reads as its meaning.

test.describe('Accessibility: focus rings, tooltips, bars and colour as the only signal', () => {
  test.use({ viewport: { width: 1400, height: 1000 } });

  const seed = {} as Seed;

  test.beforeAll(async ({ request }) => {
    Object.assign(seed, await createSeed(request));
  });

  test.afterAll(async ({ request }) => {
    await deleteSeed(request, seed);
  });

  const setup = (name: string): PageSetup => pageList(seed).find((p) => p.name === name)?.spec as PageSetup;

  const HEALTH = ['healthy', 'progressing', 'degraded', 'error', 'not_found'];

  /** A running row per pod health. */
  const mockHealthRows = async (page: Page) => {
    const rows = HEALTH.map((status): StackInstance => ({
      id: `a11y-health-${status}`,
      name: `health-${status}`,
      namespace: `stack-${status}`,
      branch: 'main',
      status: 'running',
      stack_definition_id: seed.definitionId,
      owner_id: 'a11y-owner',
      created_at: '2026-10-19T09:00:00Z',
      updated_at: '2026-10-19T09:00:00Z',
    }));
    await page.route(/\/api\/v1\/stack-instances(\?.*)?$/, (route) => (
      route.request().method() !== 'GET'
        ? route.continue()
        : route.fulfill({ json: { data: rows, page: 1, pageSize: 25, total: rows.length } })
    ));
    await page.route(/\/api\/v1\/stack-instances\/a11y-health-[^/]+\/status$/, (route) => {
      const status = /a11y-health-([^/]+)\/status/.exec(route.request().url())?.[1];
      return route.fulfill({ json: { namespace: 'a11y', status: status as NamespaceStatus['status'], charts: [], last_checked: '2026-10-19T09:00:00Z' } satisfies NamespaceStatus });
    });
  };

  const notifications: Notification[] = [
    { id: 'a11y-unread', user_id: 'u', type: 'deployment.error', title: 'Unread notification', message: 'Forced', is_read: false, created_at: '2026-10-19T10:00:00Z' },
    { id: 'a11y-read', user_id: 'u', type: 'deployment.success', title: 'Read notification', message: 'Forced', is_read: true, created_at: '2026-10-19T09:00:00Z' },
  ];

  const mockNotifications = (page: Page) => page.route(/\/api\/v1\/notifications(\?.*)?$/, (route) => (
    route.request().method() !== 'GET'
      ? route.continue()
      : route.fulfill({ json: { notifications, total: 2, unread_count: 1 } satisfies NotificationListResponse })
  ));

  // A row differs from a read row in more than its tint.
  const expectUnreadSignal = async (page: Page) => {
    const unread = page.getByRole('button', { name: /Unread notification\s*,\s*unread/ });
    const read = page.getByRole('button', { name: /^Read notification/ });
    await expect(unread, 'an unread row says so to a screen reader').toBeVisible();
    await expect(read, 'a read row does not').toBeVisible();
    const weight = (row: typeof read) => row.locator('.MuiListItemText-primary')
      .evaluate((el) => Number(getComputedStyle(el).fontWeight));
    expect(await weight(unread), 'an unread title is bolder than a read title').toBeGreaterThan(await weight(read));
  };

  /** Open tooltips cannot go through axe (see tooltipRatio), so measure them directly. */
  const expectTooltipContrast = async (page: Page, what: string) => {
    const tooltip = page.locator('[role=tooltip] .MuiTooltip-tooltip');
    await expect(tooltip, `${what}: the tooltip did not open`).toBeVisible();
    const ratio = await tooltipRatio(tooltip);
    test.info().annotations.push({ type: 'explicit', description: `${what}: lowest ${ratio.toFixed(2)}` });
    expect(ratio, `${what}: tooltip text over white and over black`).toBeGreaterThanOrEqual(4.5);
  };

  // ---------------------------------------------------------------------
  // Monaco keeps the Tab key until its tab-focus toggle is pressed (#760).

  const EDITOR_PAGES: [string, () => PageSetup, string?, string?][] = [
    ['new stack definition', () => setup('new stack definition'), 'Add Chart'],
    ['edit stack definition', () => setup('edit stack definition')],
    ['template builder', () => setup('template builder'), 'Add Chart'],
    ['template instantiate', () => setup('template instantiate')],
    // The first editor here is read-only, and a read-only editor does not keep Tab.
    ['stack instance', () => setup('stack instance'), undefined, 'Your Overrides'],
  ];
  for (const [name, getSetup, addButton, label] of EDITOR_PAGES) {
    test(`the code editor gives Tab back: ${name}`, async ({ page }) => {
      await checkPage(page, 'light', {
        ...getSetup(),
        ready: async (p) => {
          if (addButton) await p.getByRole('button', { name: addButton, exact: true }).click();
          const editor = label
            ? p.getByText(label, { exact: true }).first().locator('xpath=following-sibling::*[1]').locator('.monaco-editor')
            : p.locator('.monaco-editor:visible').first();
          await expect(editor, 'no code editor on the page').toBeVisible({ timeout: 15_000 });
          // Not "any editor": the next editor on the page also holds the focus.
          await editor.evaluate((el) => el.setAttribute('data-a11y-target', 'true'));
          const inEditor = () => p.evaluate(() => Boolean(document.activeElement?.closest('[data-a11y-target]')));
          await editor.locator('.view-lines').click();
          await expect.poll(inEditor, { message: 'the click did not focus the editor' }).toBe(true);

          await p.keyboard.press('Tab');
          expect(await inEditor(), 'without the toggle, Tab stays in the editor').toBe(true);

          // Monaco: Ctrl+M, and Ctrl+Shift+M on macOS. It reads the user agent, which the
          // test browser sets apart from navigator.platform.
          const mac = await p.evaluate(() => /Macintosh|Mac OS X/.test(navigator.userAgent));
          await p.keyboard.press(mac ? 'Control+Shift+M' : 'Control+M');
          await p.keyboard.press('Tab');
          const described = await p.evaluate(() => { const a = document.activeElement; return `${a?.tagName}.${a?.className} in ${a?.closest('.monaco-editor')?.className}`; });
          expect(await inEditor(), `with the toggle, Tab leaves the editor (focus: ${described})`).toBe(false);
          const where = await p.evaluate(() => document.activeElement?.tagName ?? 'none');
          expect(where, 'the focus is on an element').not.toBe('BODY');
          test.info().annotations.push({ type: 'focus', description: `${name}: Tab after the toggle lands on <${where.toLowerCase()}>` });
        },
      });
    });
  }

  // ---------------------------------------------------------------------
  // Pod health: one summary for a screen reader, not a live region per card (#760).

  test('pod health changes are announced in one summary', async ({ page }) => {
    let socket: WebSocketRoute | undefined;
    const rows = ['alpha', 'beta'].map((n): StackInstance => ({
      id: `a11y-announce-${n}`,
      name: `announce-${n}`,
      namespace: `stack-${n}`,
      branch: 'main',
      status: 'running',
      stack_definition_id: seed.definitionId,
      owner_id: 'a11y-owner',
      created_at: '2026-10-19T09:00:00Z',
      updated_at: '2026-10-19T09:00:00Z',
    }));
    await checkPage(page, 'light', {
      path: '/',
      heading: 'Stack Instances',
      onSocket: (ws) => { socket = ws; },
      before: async (p) => {
        await p.route(/\/api\/v1\/stack-instances(\?.*)?$/, (route) => (
          route.request().method() !== 'GET'
            ? route.continue()
            : route.fulfill({ json: { data: rows, page: 1, pageSize: 25, total: rows.length } })
        ));
        await p.route(/\/api\/v1\/stack-instances\/a11y-announce-[^/]+\/status$/, (route) => route.fulfill({
          json: { namespace: 'a11y', status: 'healthy', charts: [], last_checked: '2026-10-19T09:00:00Z' } satisfies NamespaceStatus,
        }));
      },
      ready: async (p) => {
        await expect(p.getByRole('img', { name: 'Pod health: healthy' }), 'the first health').toHaveCount(2);
        await p.waitForTimeout(1500);
        await expect(p.getByText(/changed health/), 'the first load is silent').toHaveCount(0);
        await expect(p.locator('main .MuiGrid-container[aria-live]'), 'the card grid is not a live region').toHaveCount(0);

        const push = (id: string, status: string) => socket?.send(JSON.stringify({
          type: 'instance.status',
          payload: { instance_id: id, namespace_status: { status } },
        }));
        push('a11y-announce-alpha', 'degraded');
        push('a11y-announce-beta', 'error');
        await expect(
          p.getByRole('status').filter({ hasText: 'changed health' }),
          'one summary of both changes',
        ).toHaveText('2 stacks changed health: announce-alpha degraded, announce-beta error');
      },
    });
  });

  for (const mode of MODES) {
    // ---------------------------------------------------------------------
    // Focus rings

    // Lists that grow with what other specs create need a higher cap.
    const SWEEPS: [string, SweepOptions][] = [
      ['login', {}],
      ['dashboard', { maxStops: 800 }],
      ['templates', { maxStops: 800 }],
      ['stack instance', { skipEditors: 'the values editor of a chart: the Monaco tab-focus toggle gives Tab back (tested in "the code editor gives Tab back")' }],
      ['new stack definition', {}],
      ['users', { maxStops: 300 }],
      ['profile', {}],
      ['notification channels', { maxStops: 300 }],
      ['audit log', { maxStops: 200 }],
    ];
    for (const [name, options] of SWEEPS) {
      test(`focus rings: ${name} (${mode})`, async ({ page }) => {
        test.setTimeout(120_000);
        await checkPage(page, mode, { ...setup(name), ready: (p) => focusRingSweep(p, `${name} (${mode})`, options) });
      });
    }

    // The cards of My Favorites and Recent Stacks are links, in rows that scroll sideways.
    const favorite = (): UserFavorite => ({
      id: 'a11y-favorite', user_id: 'u', entity_type: 'instance', entity_id: seed.instanceId, created_at: '2026-10-19T09:00:00Z',
    });
    const mockFavorites = (p: Page) => p.route(/\/api\/v1\/favorites(\?.*)?$/, (route) => (
      route.request().method() === 'GET' ? route.fulfill({ json: [favorite()] }) : route.continue()
    ));
    const favoriteLink = (p: Page) => p.getByRole('link', { name: new RegExp(seed.instanceName) }).first();

    // More recent stacks than fit: the row must scroll, whatever other specs created.
    const RECENT_STACKS = 8;
    const mockRecentStacks = (p: Page) => p.route(/\/api\/v1\/stack-instances\/recent(\?.*)?$/, (route) => route.fulfill({
      json: Array.from({ length: RECENT_STACKS }, (_, i): StackInstance => ({
        id: `a11y-recent-${i}`,
        stack_definition_id: seed.definitionId,
        name: `a11y-recent-${i}`,
        definition_name: 'a11y-definition',
        namespace: `stack-recent-${i}`,
        owner_id: 'a11y-owner',
        branch: 'main',
        status: 'draft',
        created_at: '2026-10-19T09:00:00Z',
        updated_at: '2026-10-19T09:00:00Z',
      })),
    }));

    test(`focus rings: favorite and recent cards, in a row that scrolls (${mode})`, async ({ page }) => {
      test.setTimeout(120_000);
      await checkPage(page, mode, {
        ...setup('dashboard'),
        before: async (p) => {
          await mockFavorites(p);
          await mockRecentStacks(p);
        },
        ready: async (p) => {
          await expect(p.getByText('My Favorites')).toBeVisible();
          await expect(favoriteLink(p), 'the favorite card is a link').toBeVisible();
          const recent = p.getByRole('link', { name: /^a11y-recent-/ });
          await expect(recent, 'the recent stacks are links').toHaveCount(RECENT_STACKS);
          const row = p.locator('.MuiBox-root').filter({ has: recent.first() }).last();
          expect(await row.evaluate((el) => el.scrollWidth > el.clientWidth), 'the row of recent stacks must scroll').toBe(true);
          await focusRingSweep(p, `favorite and recent cards (${mode})`, { maxStops: 800 });
        },
      });
    });

    test(`focus rings: recently used template cards (${mode})`, async ({ page }) => {
      test.setTimeout(120_000);
      await checkPage(page, mode, {
        ...setup('templates'),
        before: (p) => p.addInitScript(({ id, name }) => {
          localStorage.setItem('recentTemplates', JSON.stringify([{ id, name, usedAt: new Date().toISOString() }]));
        }, { id: seed.publishedTemplateId, name: seed.publishedTemplateName }),
        ready: async (p) => {
          await expect(p.getByText('Recently Used')).toBeVisible();
          await expect(p.getByRole('button', { name: `Open template ${seed.publishedTemplateName}` })).toBeVisible();
          await focusRingSweep(p, `recently used templates (${mode})`, { maxStops: 800 });
        },
      });
    });

    test(`favorite card: opens with the keyboard, and the star does not navigate (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('dashboard'),
        before: async (p) => {
          await mockFavorites(p);
          await p.route(/\/api\/v1\/favorites\/instance\/[^/]+$/, (route) => route.fulfill({ status: 204 }));
        },
        ready: async (p) => {
          const link = favoriteLink(p);
          await expect(link).toBeVisible();
          const card = p.locator('.MuiCard-root').filter({ has: link }).first();
          const star = card.getByRole('button', { name: /favorites/i });
          expect(await link.locator('button').count(), 'a link cannot hold a button').toBe(0);

          await star.click();
          await expect(card.getByRole('button', { name: /favorites/i }), 'the star changed').toBeVisible();
          expect(new URL(p.url()).pathname, 'the star does not navigate').toBe('/');

          await link.focus();
          await p.keyboard.press('Enter');
          await expect.poll(() => new URL(p.url()).pathname).toBe(`/stack-instances/${seed.instanceId}`);
        },
      });
    });

    test(`focus rings: selection toolbar (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('dashboard'),
        ready: async (p) => {
          await p.getByRole('checkbox', { name: 'Select all instances' }).check();
          const toolbar = p.getByRole('toolbar', { name: 'Bulk actions' });
          await expect(toolbar).toBeVisible();
          // The toolbar is dark in light mode, so the primary ring does not show on it.
          await toolbar.getByRole('button', { name: 'Deploy', exact: true }).focus();
          await focusRingSweep(p, `selection toolbar (${mode})`, {
            partial: { stops: 4, reason: 'Stop, Clean, Delete and Clear Selection; the page sweep covers what follows' },
          });
        },
      });
    });

    // The table scrolls inside the page. A link that Tab reaches at its edge is
    // scrolled into view, and its ring must come along.
    test(`focus rings: links in a long scrolling table (${mode})`, async ({ page }) => {
      const logs = Array.from({ length: 50 }, (_, i) => ({
        id: `a11y-log-${i}`,
        user_id: 'u',
        username: 'admin',
        action: 'create',
        entity_type: 'stack_instance',
        entity_id: `a11y-entity-${i}`,
        details: '{}',
        timestamp: '2026-10-19T09:00:00Z',
      }));
      await checkPage(page, mode, {
        ...setup('audit log'),
        before: (p) => p.route(/\/api\/v1\/audit-logs(\?.*)?$/, (route) => route.fulfill({
          json: { data: logs, total: logs.length, limit: 50, offset: 0 } satisfies PaginatedAuditLogs,
        })),
        ready: (p) => focusRingSweep(p, `long audit log (${mode})`, { maxStops: 90 }),
      });
    });

    test(`focus rings: menu items, with the arrow keys (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('stack instance'),
        ready: async (p) => {
          await p.getByRole('button', { name: 'Actions', exact: true }).focus();
          await p.keyboard.press('Enter');
          await expect(p.getByRole('menu'), 'the Actions menu did not open').toBeVisible();
          await focusRingSweep(p, `Actions menu (${mode})`, { key: 'ArrowDown' });
        },
      });
    });

    test(`focus rings: inside a dialog (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('notification channels'),
        ready: async (p) => {
          await p.getByRole('button', { name: 'Create Channel' }).click();
          await expect(p.getByRole('dialog')).toBeVisible();
          await focusRingSweep(p, `dialog (${mode})`);
        },
      });
    });

    // ---------------------------------------------------------------------
    // Tooltips

    test(`tooltip on the page and on a control (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('dashboard'),
        ready: async (p) => {
          await p.getByRole('button', { name: /^Open notifications/ }).first().hover();
          await expectTooltipContrast(p, `bell tooltip (${mode})`);
        },
      });
    });

    test(`tooltip of a pod health icon (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/',
        heading: 'Stack Instances',
        before: mockHealthRows,
        ready: async (p) => {
          await p.getByRole('img', { name: 'Pod health: degraded' }).hover();
          await expect(p.getByRole('tooltip')).toHaveText('Health: degraded');
          await expectTooltipContrast(p, `pod health tooltip (${mode})`);
        },
      });
    });

    // ---------------------------------------------------------------------
    // Colour as the only signal

    test(`pod health: a shape and a name per state (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/',
        heading: 'Stack Instances',
        before: mockHealthRows,
        ready: async (p) => {
          const shapes = new Set<string>();
          for (const status of HEALTH) {
            const icon = p.getByRole('img', { name: `Pod health: ${status}` });
            await expect(icon, `pod health ${status} has a name`).toBeVisible();
            shapes.add(await icon.locator('path').first().getAttribute('d') ?? '');
            const ratio = await nonTextRatio(icon, 'color');
            test.info().annotations.push({ type: 'explicit', description: `pod health ${status} (${mode}): ${ratio.toFixed(2)}` });
            expect(ratio, `pod health ${status}: icon against its card`).toBeGreaterThanOrEqual(AA_NON_TEXT);
          }
          expect(shapes.size, 'each state has its own shape').toBe(HEALTH.length);
        },
      });
    });

    test(`current page: more than a tint (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('audit log'),
        ready: async (p) => {
          const current = p.getByRole('navigation', { name: 'Main navigation' }).locator('[aria-current=page]');
          await expect(current, 'the current page is marked for a screen reader').toHaveCount(1);
          const { bar, background } = await current.evaluate((el) => ({
            bar: getComputedStyle(el).boxShadow,
            background: getComputedStyle(el.parentElement as Element).backgroundColor,
          }));
          expect(bar, `the current page has a bar (${background})`).toMatch(/inset/);
        },
      });
    });

    // Only the exact page is the current page. Its parent in the sidebar is marked too,
    // but not as the page itself.
    test(`current page: a child route marks its parent as current, not as the page (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('edit stack definition'),
        ready: async (p) => {
          const nav = p.getByRole('navigation', { name: 'Main navigation' });
          await expect(nav.locator('[aria-current=page]'), 'no sidebar link is this page').toHaveCount(0);
          await expect(nav.getByRole('link', { name: 'Definitions' })).toHaveAttribute('aria-current', 'true');
        },
      });
    });

    // The hidden "unread" text is out of the flow. After a title too long for its row,
    // it must not make the list of the bell scroll sideways.
    test(`unread notifications: a long stack name does not scroll the bell list sideways (${mode})`, async ({ page }) => {
      const longName = `stack-${'a'.repeat(70)}`;
      await checkPage(page, mode, {
        ...setup('dashboard'),
        before: (p) => p.route(/\/api\/v1\/notifications(\?.*)?$/, (route) => (
          route.request().method() !== 'GET' ? route.continue() : route.fulfill({
            json: {
              notifications: [{
                id: 'a11y-long', user_id: 'u', type: 'deployment.error', title: `Deploy of ${longName} failed`,
                message: 'Forced', is_read: false, created_at: '2026-10-19T10:00:00Z',
              }],
              total: 1,
              unread_count: 1,
            } satisfies NotificationListResponse,
          })
        )),
        ready: async (p) => {
          await p.getByRole('button', { name: /^Open notifications/ }).first().click();
          const list = p.locator('.MuiPopover-paper ul');
          await expect(list.getByRole('button', { name: /, unread/ })).toBeVisible();
          const overflow = await list.evaluate((el) => el.scrollWidth - el.clientWidth);
          expect(overflow, 'how far the list scrolls sideways').toBeLessThanOrEqual(0);
        },
      });
    });

    test(`unread notifications: more than a tint, in the bell (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('dashboard'),
        before: mockNotifications,
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: /^Open notifications/ }).first().click();
          const popover = p.locator('.MuiPopover-paper');
          await expect(popover).toBeVisible();
          await expectUnreadSignal(p);
          await runAxe({ selector: '.MuiPopover-paper', checked: popover.getByRole('heading', { name: 'Notifications' }) });
        },
      });
    });

    test(`unread notifications: more than a tint, on the page (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('notifications'),
        before: mockNotifications,
        ready: async (p, runAxe) => {
          await expectUnreadSignal(p);
          await runAxe({ selector: 'main', checked: p.getByRole('button', { name: /Unread notification\s*,\s*unread/ }) });
        },
      });
    });

    // ---------------------------------------------------------------------
    // Bars. The only charts in the app: a value in words next to a coloured bar.

    test(`bars: analytics success rate (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...setup('analytics'),
        before: (p) => p.route(/\/api\/v1\/analytics\/templates$/, (route) => route.fulfill({
          json: [
            { template_id: 't1', template_name: 'High rate', category: 'Web', is_published: true, definition_count: 1, instance_count: 1, deploy_count: 10, success_count: 10, error_count: 0, success_rate: 100 },
            { template_id: 't2', template_name: 'Middle rate', category: 'Web', is_published: true, definition_count: 1, instance_count: 1, deploy_count: 10, success_count: 7, error_count: 3, success_rate: 70 },
            { template_id: 't3', template_name: 'Low rate', category: 'Web', is_published: true, definition_count: 1, instance_count: 1, deploy_count: 10, success_count: 2, error_count: 8, success_rate: 20 },
          ] satisfies TemplateStats[],
        })),
        ready: async (p, runAxe) => {
          for (const [template, percent] of [['High rate', '100%'], ['Middle rate', '70%'], ['Low rate', '20%']]) {
            const bar = p.getByRole('progressbar', { name: `Success rate ${template}` });
            await expect(bar).toBeVisible();
            // The value is in words, so the colour of the bar is not the only signal.
            await expect(p.getByRole('row', { name: new RegExp(template) }), `${template}: the value is text`).toContainText(percent);
            const ratio = await nonTextRatio(bar.locator('.MuiLinearProgress-bar').first(), 'backgroundColor', bar);
            test.info().annotations.push({ type: 'explicit', description: `analytics bar ${template} (${mode}): bar against track ${ratio.toFixed(2)}` });
          }
          await runAxe({ selector: 'main', checked: p.getByRole('row', { name: /Low rate/ }) });
        },
      });
    });

    test(`bars: cluster health usage, all colours (${mode})`, async ({ page }) => {
      const usage = (name: string, used: string, pods: number): NamespaceResourceUsage => ({
        namespace: name, cpu_used: used, cpu_limit: '1000m', memory_used: '1Gi', memory_limit: '2Gi', pod_count: pods, pod_limit: 100,
      });
      await checkPage(page, mode, {
        path: '/admin/cluster-health',
        heading: 'Cluster Health',
        before: async (p) => {
          const json = (path: RegExp, body: unknown) => p.route(path, (route) => route.fulfill({ json: body }));
          // The page reads only these fields of a cluster.
          await json(/\/api\/v1\/clusters$/, [{ id: 'a11y-cluster', name: 'a11y-cluster', is_default: true }] satisfies Pick<Cluster, 'id' | 'name' | 'is_default'>[]);
          await json(/\/api\/v1\/clusters\/a11y-cluster\/health\/summary$/, {
            node_count: 1, ready_node_count: 1, total_cpu: '8000m', allocatable_cpu: '6000m', requested_cpu: '5700m',
            total_memory: '16Gi', allocatable_memory: '12Gi', requested_memory: '9Gi', namespace_count: 3,
          } satisfies ClusterSummary);
          await json(/\/api\/v1\/clusters\/a11y-cluster\/health\/nodes$/, [{
            name: 'a11y-node',
            status: 'Ready',
            capacity: { cpu: '4000m', memory: '8Gi' },
            allocatable: { cpu: '3000m', memory: '6Gi' },
            pod_count: 10,
            conditions: [{ type: 'Ready', status: 'True' }],
          }] satisfies NodeStatusInfo[]);
          await json(/\/api\/v1\/clusters\/a11y-cluster\/namespaces$/, [{ name: 'a11y-namespace', phase: 'Active', created_at: '2026-01-01T00:00:00Z' }] satisfies ClusterNamespaceInfo[]);
          await json(/\/api\/v1\/clusters\/a11y-cluster\/utilization$/, {
            namespaces: [usage('stack-low', '500m', 50), usage('stack-mid', '800m', 80), usage('stack-high', '950m', 95)],
          } satisfies ClusterUtilization);
        },
        ready: async (p, runAxe) => {
          // Each mock reached the page.
          await expect(p.getByRole('progressbar', { name: /^CPU requested 95%/ }), 'the summary').toBeVisible();
          await expect(p.getByText('a11y-node'), 'the nodes').toBeVisible();
          for (const [namespace, percent] of [['stack-low', '50%'], ['stack-mid', '80%'], ['stack-high', '95%']]) {
            const row = p.getByRole('row', { name: new RegExp(namespace) });
            // The value is in words, so the colour of the bar is not the only signal.
            await expect(row, `${namespace}: the value is text`).toContainText(percent);
            // Each bar says what it measures: a screen reader can list them all.
            await expect(row.getByRole('progressbar', { name: `${namespace} memory 50% used` })).toBeVisible();
            await expect(row.getByRole('progressbar', { name: new RegExp(`^${namespace} pods `) })).toBeVisible();
            const bar = row.getByRole('progressbar', { name: `${namespace} CPU ${percent} used` });
            await expect(bar, `${namespace}: the bar has a name`).toBeVisible();
            const ratio = await nonTextRatio(bar.locator('.MuiLinearProgress-bar').first(), 'backgroundColor', bar);
            test.info().annotations.push({ type: 'explicit', description: `cluster health bar ${percent} (${mode}): bar against track ${ratio.toFixed(2)}` });
          }
          await runAxe({ selector: 'main', checked: p.getByRole('row', { name: /stack-high/ }) });
        },
      });
    });
  }
});
