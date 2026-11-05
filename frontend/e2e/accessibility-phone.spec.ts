import { test, expect, devices, type Page } from '@playwright/test';
import { MODES, checkPage, createSeed, deleteSeed, expectTableCellsReachable, focusRingSweep, pageList, type PageSetup } from './a11y';
import { DIALOGS, admin, checkDialog, createAdminSeed, deleteAdminSeed, seed } from './a11yDialogs';
import type { NotificationListResponse } from '../src/types';

// The phone layout (#758): all pages, the open navigation, a dialog and a toast
// at the iPhone 13 viewport with touch, in both colour modes. A page reflows on a
// phone: other surfaces are on screen, and the app bar replaces the sidebar. This
// runs in the chromium project: `defaultBrowserType` cannot change inside a file.
const { defaultBrowserType: _browser, ...iPhone } = devices['iPhone 13'];

test.describe('Accessibility: phone layout', () => {
  // The phone width with a tall screen: axe cannot read what a scroll container or
  // the bottom of the screen cuts off. The width decides the layout, not the height.
  test.use({ ...iPhone, viewport: { width: iPhone.viewport.width, height: 2400 } });

  test.beforeAll(async ({ request }) => {
    Object.assign(seed, await createSeed(request));
    Object.assign(admin, await createAdminSeed(request));
  });

  test.afterAll(async ({ request }) => {
    await deleteAdminSeed(request);
    await deleteSeed(request, seed);
  });

  const PAGE_NAMES = pageList({
    token: '', definitionId: '', definition2Id: '', instanceId: '', instanceName: '', instance2Id: '', instance2Name: '',
    templateId: '', templateName: '', publishedTemplateId: '', publishedTemplateName: '',
  }).map((p) => p.name);

  const dashboard = (): PageSetup => pageList(seed).find((p) => p.name === 'dashboard')?.spec as PageSetup;

  const openNavigation = async (page: Page) => {
    await page.locator('header').getByRole('button', { name: 'Open navigation' }).tap();
    const nav = page.getByRole('navigation', { name: 'Main navigation' });
    await expect(nav, 'the navigation did not open').toBeVisible();
    return nav;
  };

  // KNOWN_UNDECIDED accepts the table cells that a sideways scroll cuts off, because
  // these tests prove that each one is reachable by scroll and keyboard (#760).
  for (const name of PAGE_NAMES) {
    test(`table scroll: ${name} (phone)`, async ({ page }) => {
      const spec = pageList(seed).find((p) => p.name === name)?.spec as PageSetup;
      let stats = { tables: 0, scrolling: 0, cutOff: 0 };
      await checkPage(page, 'light', {
        ...spec,
        phone: spec.sidebar !== false,
        ready: async (p) => { stats = await expectTableCellsReachable(p, `${name} (phone)`); },
      });
      // These tables are wider than the phone, so they must scroll sideways.
      if (['users', 'audit log'].includes(name)) {
        expect(stats.scrolling, `${name}: the table scrolls sideways on a phone`).toBeGreaterThan(0);
      }
      if (name === 'users') expect(stats.cutOff, 'users: a cell is cut off, so that the check has something to focus').toBeGreaterThan(0);
    });
  }

  for (const mode of MODES) {
    for (const name of PAGE_NAMES) {
      test(`${name} (${mode}, phone)`, async ({ page }) => {
        const spec = pageList(seed).find((p) => p.name === name)?.spec as PageSetup;
        await checkPage(page, mode, { ...spec, phone: spec.sidebar !== false });
      });
    }

    test(`open navigation (${mode}, phone)`, async ({ page }) => {
      await checkPage(page, mode, {
        ...dashboard(),
        phone: true,
        ready: async (p, runAxe) => {
          const nav = await openNavigation(p);
          await runAxe({ selector: '.MuiDrawer-paper', checked: nav.getByRole('link').first() });
        },
      });
    });

    test(`selected row (${mode}, phone)`, async ({ page }) => {
      await checkPage(page, mode, {
        ...dashboard(),
        phone: true,
        ready: async (p, runAxe) => {
          await p.getByRole('checkbox', { name: 'Select all instances' }).check();
          const toolbar = p.getByRole('toolbar', { name: 'Bulk actions' });
          await expect(toolbar).toBeVisible();
          await runAxe({ selector: '[role=toolbar]', checked: toolbar.getByText(/\d+ selected/) });
        },
      });
    });

    // The list of dialogs of the desktop spec: a dialog on a phone has its own width and margins.
    for (const state of DIALOGS) {
      test(`dialog ${state.name} (${mode}, phone)`, ({ page }) => checkDialog(page, mode, state, true));
    }

    test(`dialog (${mode}, phone)`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/admin/notification-channels',
        heading: 'Notification Channels',
        phone: true,
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: 'Create Channel' }).tap();
          const dialog = p.getByRole('dialog');
          await expect(dialog).toBeVisible();
          await runAxe({ selector: '[role=dialog]', checked: dialog.locator('h2') });
        },
      });
    });

    // The toasts have their own layout on a phone: below the app bar, across the screen.
    for (const [severity, status] of [['Success', 200], ['Error', 400]] as const) {
      test(`toast ${severity} (${mode}, phone)`, async ({ page }) => {
        await checkPage(page, mode, {
          path: '/profile',
          heading: 'My Profile',
          phone: true,
          before: (p) => p.route(/\/api\/v1\/notifications\/preferences$/, (route) => (
            route.request().method() === 'PUT' ? route.fulfill({ status, json: {} }) : route.continue()
          )),
          ready: async (p, runAxe) => {
            await p.getByRole('button', { name: 'Save', exact: true }).tap();
            const toast = p.locator(`[data-testid=toast-stack] .MuiAlert-color${severity}`);
            await expect(toast, `the ${severity} toast did not appear`).toBeVisible();
            await expect(toast.locator('.MuiAlert-message')).toHaveText(
              status === 200 ? 'Notification preferences saved' : 'Failed to save notification preferences',
            );
            // The pointer on a toast pauses its timer while axe runs.
            await toast.hover();
            await runAxe({ selector: '[data-testid=toast-stack]', checked: toast.locator('.MuiAlert-message') });
          },
        });
      });
    }

    test(`notification popover (${mode}, phone)`, async ({ page }) => {
      await checkPage(page, mode, {
        ...dashboard(),
        phone: true,
        ready: async (p, runAxe) => {
          await p.locator('header').getByRole('button', { name: /^Open notifications/ }).tap();
          const popover = p.locator('.MuiPopover-paper');
          await expect(popover).toBeVisible();
          await runAxe({ selector: '.MuiPopover-paper', checked: popover.getByRole('heading', { name: 'Notifications' }) });
        },
      });
    });

    // Something wider than the screen must not widen the layout: a phone then pushes
    // the app bar off the screen, with no scroll bar to show it.
    const WIDE_NOTIFICATIONS: [string, { name: string; unread: number; isRead: boolean }][] = [
      ['a long stack name', { name: `stack-${'a'.repeat(70)}`, unread: 0, isRead: true }],
      ['the Mark all read button', { name: 'stack-short', unread: 44, isRead: true }],
      // The text that says "unread" to a screen reader must take no room.
      ['an unread row', { name: 'stack-short', unread: 1, isRead: false }],
    ];
    for (const [what, { name, unread, isRead }] of WIDE_NOTIFICATIONS) {
      test(`notifications with ${what} fit the screen (${mode}, phone)`, async ({ page }) => {
        await checkPage(page, mode, {
          path: '/notifications',
          heading: 'Notifications',
          phone: true,
          before: (p) => p.route(/\/api\/v1\/notifications(\?.*)?$/, (route) => (
            route.request().method() !== 'GET' ? route.continue() : route.fulfill({
              json: {
                notifications: [{
                  id: 'a11y-wide', user_id: 'u', type: 'deployment.success', title: `Deployed ${name}`,
                  message: `Stack ${name} has been created`, is_read: isRead, created_at: '2026-10-19T10:00:00Z',
                }],
                total: 1,
                unread_count: unread,
              } satisfies NotificationListResponse,
            })
          )),
          ready: async (p, runAxe) => {
            await expect(p.getByText(`Stack ${name} has been created`)).toBeVisible();
            if (unread) await expect(p.getByRole('button', { name: 'Mark all read' })).toBeVisible();
            expect(await p.evaluate(() => window.innerWidth), 'the layout stays as wide as the phone').toBe(iPhone.viewport.width);
            const bell = await p.locator('header').getByRole('button', { name: /^Open notifications/ }).boundingBox();
            expect((bell?.x ?? Infinity) + (bell?.width ?? 0), 'the bell stays on the screen').toBeLessThanOrEqual(iPhone.viewport.width);
            await runAxe();
          },
        });
      });
    }

    // On a phone, more content scrolls sideways. A scroll container with nothing to
    // focus inside it is a Tab stop in Chromium, and needs a ring like any other.
    for (const name of ['compare, with a difference', 'audit log', 'users']) {
      test(`focus rings: ${name} (${mode}, phone)`, async ({ page }) => {
        test.setTimeout(120_000);
        const spec = pageList(seed).find((p) => p.name === name)?.spec as PageSetup;
        await checkPage(page, mode, {
          ...spec,
          phone: true,
          ready: (p) => focusRingSweep(p, `${name} (${mode}, phone)`, { maxStops: 300 }),
        });
      });
    }

    test(`focus rings on the app bar and the page (${mode}, phone)`, async ({ page }) => {
      await checkPage(page, mode, {
        ...dashboard(),
        phone: true,
        // Each instance card has several stops, and other specs add instances.
        ready: (p) => focusRingSweep(p, `dashboard (${mode}, phone)`, { maxStops: 800 }),
      });
    });
  }
});
