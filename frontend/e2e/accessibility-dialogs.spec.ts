import { test, expect } from '@playwright/test';
import { MODES, checkPage, createSeed, deleteSeed, hoverAndCheck } from './a11y';
import { DIALOGS, admin, checkDialog, createAdminSeed, deleteAdminSeed, instancePage, openActions, seed } from './a11yDialogs';
import type { Notification, NotificationListResponse, UnreadNotificationCount } from '../src/types';

// Every dialog (the list is in a11yDialogs.ts, also run on a phone), every menu and
// the notification popover (#758), in both colour modes.

test.describe('Accessibility: all dialogs, menus and popovers', () => {
  // Tall enough for the longest dialog: axe skips what is outside the screen.
  test.use({ viewport: { width: 1400, height: 2000 } });

  test.beforeAll(async ({ request }) => {
    Object.assign(seed, await createSeed(request));
    Object.assign(admin, await createAdminSeed(request));
  });

  test.afterAll(async ({ request }) => {
    await deleteAdminSeed(request);
    await deleteSeed(request, seed);
  });

  for (const mode of MODES) {
    for (const state of DIALOGS) {
      test(`dialog ${state.name} (${mode})`, ({ page }) => checkDialog(page, mode, state));
    }

    // -----------------------------------------------------------------------
    // Menus. A Select also opens a Menu, and an Autocomplete opens its own list.

    const instanceSetup = () => instancePage('running', { ttl_minutes: 600, expires_at: new Date(Date.now() + 3_600_000).toISOString() });

    test(`menu export values (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...instanceSetup(),
        ready: async (p, runAxe) => {
          await openActions(p, /^Export values/);
          const item = p.getByRole('menu', { name: 'Export values' }).getByRole('menuitem').first();
          await expect(item).toBeVisible();
          await hoverAndCheck(p, item, '[role=menu]', runAxe);
        },
      });
    });

    test(`menu extend the TTL (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        ...instanceSetup(),
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: /^Extend/ }).first().click();
          const item = p.getByRole('menuitem', { name: 'Custom...' });
          await expect(item).toBeVisible();
          await hoverAndCheck(p, item, '[role=menu]', runAxe);
        },
      });
    });

    test(`menu audit log export (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/audit-log',
        heading: 'Audit Log',
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: 'Export' }).click();
          const item = p.getByRole('menuitem').first();
          await expect(item).toBeVisible();
          await hoverAndCheck(p, item, '[role=menu]', runAxe);
        },
      });
    });

    test(`menu of a select field (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/stack-instances/new',
        heading: 'Create Stack Instance',
        ready: async (p, runAxe) => {
          await p.getByRole('combobox').first().click();
          const listbox = p.getByRole('listbox');
          await expect(listbox).toBeVisible();
          const option = listbox.getByRole('option').first();
          await runAxe({ selector: '[role=listbox]', checked: option });
        },
      });
    });

    test(`list of an autocomplete field (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/stack-instances/compare',
        heading: 'Compare Stack Instances',
        ready: async (p, runAxe) => {
          await p.getByRole('combobox').first().click();
          const option = p.getByRole('listbox').getByRole('option').first();
          await expect(option).toBeVisible();
          await runAxe({ selector: '[role=listbox]', checked: option });
        },
      });
    });

    // -----------------------------------------------------------------------
    // The notification popover keeps its elevation overlay in dark mode.

    test(`notification popover, rows on hover (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/',
        heading: 'Stack Instances',
        allowed5xx: [/^GET \/api\/v1\/stack-instances\/[^/]+\/status$/],
        before: async (p) => {
          const notifications: Notification[] = [
            { id: 'n1', user_id: 'u', type: 'deployment.error', title: 'A failed deploy', message: 'The deploy failed', is_read: false, created_at: '2026-10-19T10:00:00Z' },
            { id: 'n2', user_id: 'u', type: 'deployment.success', title: 'A good deploy', message: 'The deploy worked', is_read: true, created_at: '2026-10-19T09:00:00Z' },
          ];
          await p.route(/\/api\/v1\/notifications(\?.*)?$/, (route) => (
            route.request().method() !== 'GET'
              ? route.continue()
              : route.fulfill({ json: { notifications, total: 2, unread_count: 1 } satisfies NotificationListResponse })
          ));
          await p.route(/\/api\/v1\/notifications\/count$/, (route) => route.fulfill({ json: { unread_count: 1 } satisfies UnreadNotificationCount }));
        },
        ready: async (p, runAxe) => {
          await p.getByRole('button', { name: /^Open notifications/ }).first().click();
          const popover = p.locator('.MuiPopover-paper');
          await expect(popover).toBeVisible();
          const scope = '.MuiPopover-paper';
          const unread = popover.getByRole('button', { name: /A failed deploy/ });
          const read = popover.getByRole('button', { name: /A good deploy/ });
          await expect(unread).toBeVisible();
          await runAxe({ selector: scope, checked: unread });
          await hoverAndCheck(p, unread, scope, runAxe);
          await hoverAndCheck(p, read, scope, runAxe);
          await hoverAndCheck(p, popover.getByRole('button', { name: 'View all notifications' }), scope, runAxe);
        },
      });
    });
  }
});
