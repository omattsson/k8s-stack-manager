import { test, expect, type Locator, type Page, type WebSocketRoute } from '@playwright/test';
import {
  MODES,
  checkPage,
  createSeed,
  deleteSeed,
  forceInstanceFields,
  focusAndCheck,
  hoverAndCheck,
  pageList,
  type Mode,
  type PageSetup,
  type RunAxe,
  type Seed,
} from './a11y';
import { mockDeployLog, type MockDeployLog } from './deployLogMock';

// Colour contrast and list structure (#740, #758), in the light and the dark
// colour mode, at 1400x1000. See a11y.ts for the rules and the explicit checks.
//
// - All pages in their resting state.
// - FORCED: the status colours of the instance page that a run without a cluster
//   never shows (page.route edits the instance answer).
// - STATE: a selected row, menus on hover and on keyboard focus, toasts, expiry
//   and API-key chips, and buttons on the page background on hover. Each run is
//   limited to the surface and must have measured a named element.
//
// Other specs: accessibility-dialogs (all dialogs, menus, popovers),
// accessibility-signals (focus rings, tooltips, bars, colour as the only signal),
// accessibility-phone (the phone layout).
//
// A page that did not load must not pass: before axe runs, each test checks the
// heading, the URL, the sidebar, the colour mode, that no spinner is left and that
// no API call answered 5xx. The app WebSocket is blocked (answered by the test,
// which sends only for the info toast), so that no pushed message reloads a list
// or shows a toast while axe runs. A page that renders after the gradients are
// flattened would reach axe with its gradient. DB-backed, no cluster required.

/**
 * A text or outlined button in a status colour with no Paper behind it tints itself
 * on the page background, where it has the least contrast. Hover each one that the
 * page shows, so that a new one is measured without a new test.
 */
async function hoverStatusButtonsOnPage(page: Page, runAxe: RunAxe): Promise<void> {
  const selector = ['warning', 'info', 'error', 'success', 'secondary']
    .flatMap((c) => [`.MuiButton-text.MuiButton-color${c[0].toUpperCase()}${c.slice(1)}`, `.MuiButton-outlined.MuiButton-color${c[0].toUpperCase()}${c.slice(1)}`])
    .map((s) => `main ${s}:not(:disabled)`)
    .join(', ');
  const buttons = page.locator(selector);
  const names: string[] = [];
  for (let i = 0; i < await buttons.count(); i++) {
    const button = buttons.nth(i);
    if (await button.evaluate((el) => el.closest('.MuiPaper-root') !== null)) continue;
    names.push((await button.textContent()) ?? '');
    await hoverAndCheck(page, button, 'main', runAxe);
  }
  test.info().annotations.push({ type: 'page-background', description: `${new URL(page.url()).pathname}: ${names.length ? names.join(', ') : 'none'}` });
}

test.describe('Accessibility: colour contrast and lists', () => {
  // The size of the #740 measurements; the default 1280x720 hides the bottom of the sidebar and of tables.
  test.use({ viewport: { width: 1400, height: 1000 } });

  const seed = {} as Seed;

  test.beforeAll(async ({ request }) => {
    Object.assign(seed, await createSeed(request));
  });

  test.afterAll(async ({ request }) => {
    await deleteSeed(request, seed);
  });

  // The names must exist before the seed: Playwright lists the tests first.
  const PAGE_NAMES = pageList({
    token: '', definitionId: '', definition2Id: '', instanceId: '', instanceName: '', instance2Id: '', instance2Name: '',
    templateId: '', templateName: '', publishedTemplateId: '', publishedTemplateName: '',
  }).map((p) => p.name);

  for (const mode of MODES) {
    for (const name of PAGE_NAMES) {
      test(`${name} (${mode})`, async ({ page }) => {
        const spec = pageList(seed).find((p) => p.name === name)?.spec as PageSetup;
        await checkPage(page, mode, {
          ...spec,
          ready: async (p, runAxe) => {
            await runAxe();
            if (name === 'templates') {
              // The seeded draft template is on the tab "My Templates" (the admin owns it)
              // with a warning "Draft" chip: check the chip on the card of that template.
              await p.getByRole('tab', { name: 'My Templates' }).click();
              const card = p.locator('.MuiCard-root').filter({ hasText: seed.templateName });
              await expect(card.getByText('Draft', { exact: true }), 'the Draft chip of the seeded template is not visible').toBeVisible();
              await runAxe();
            }
            if (spec.sidebar !== false) await hoverStatusButtonsOnPage(p, runAxe);
          },
        });
      });
    }
  }

  // -------------------------------------------------------------------------
  // FORCED states. Without a cluster, an instance never reaches these states.
  // The tests answer `GET /api/v1/stack-instances/:id` (that exact path) with
  // the real answer and a changed status. The route stays for the whole test:
  // the page fetches the instance again after some actions, and a real answer
  // would put the draft status back.

  interface ForcedState {
    name: string;
    status: string;
    errorMessage?: string;
    /** Two successful deploys: the page offers Rollback. */
    deployHistory?: boolean;
    /** Spinners that the state shows on purpose. */
    progressbars?: number;
    /** The element that proves the state is on screen. */
    visible: (page: Page) => Locator;
    /** Hover this element and check the colours again. */
    hover?: boolean;
  }

  const statusChip = (label: string) => (page: Page) =>
    page.locator('[aria-live=polite]').getByText(label, { exact: true });

  const FORCED: ForcedState[] = [
    {
      name: 'draft: Deploy button (contained success)',
      status: 'draft',
      visible: (page) => page.getByRole('button', { name: 'Deploy', exact: true }),
      hover: true,
    },
    {
      name: 'running: Stop button (contained warning)',
      status: 'running',
      visible: (page) => page.getByRole('button', { name: 'Stop', exact: true }),
      hover: true,
    },
    {
      name: 'running with deploy history: Rollback button (outlined warning)',
      status: 'running',
      deployHistory: true,
      visible: (page) => page.getByRole('button', { name: 'Rollback', exact: true }),
      hover: true,
    },
    { name: 'error: status chip', status: 'error', errorMessage: 'Forced error message', visible: statusChip('error') },
    { name: 'partial: status chip', status: 'partial', errorMessage: 'Forced partial message', visible: statusChip('partial') },
    // The lifecycle alert of stopping and cleaning shows a spinner on purpose.
    { name: 'stopping: status chip', status: 'stopping', progressbars: 1, visible: statusChip('stopping') },
    { name: 'deploying: status chip', status: 'deploying', visible: statusChip('deploying') },
    { name: 'cleaning: status chip', status: 'cleaning', progressbars: 1, visible: statusChip('cleaning') },
    {
      name: 'stopped by the TTL: Expired chip',
      status: 'stopped',
      errorMessage: 'Expired (TTL)',
      visible: (page) => page.getByText('Expired', { exact: true }),
    },
  ];

  /** Two successful deploys, the newer one current: Rollback needs another one. */
  const deployHistory = (): MockDeployLog[] => [
    { id: 'a11y-d2', stack_instance_id: seed.instanceId, action: 'deploy', status: 'success', branch: 'main',
      started_at: '2026-10-19T10:00:00Z', completed_at: '2026-10-19T10:00:45Z' },
    { id: 'a11y-d1', stack_instance_id: seed.instanceId, action: 'deploy', status: 'success', branch: 'main',
      started_at: '2026-10-19T09:00:00Z', completed_at: '2026-10-19T09:00:45Z' },
  ];

  /** The instance page in a forced status: a status other than draft reads the pods, 502 without a cluster. */
  const instancePage = (status: string, errorMessage?: string, withHistory = false): PageSetup => ({
    path: `/stack-instances/${seed.instanceId}`,
    heading: seed.instanceName,
    allowed5xx: [new RegExp(`^GET /api/v1/stack-instances/${seed.instanceId}/pods$`)],
    before: async (p) => {
      await forceInstanceFields(p, seed.instanceId, { status, error_message: errorMessage });
      if (withHistory) await mockDeployLog(p, seed.instanceId, deployHistory);
    },
  });

  for (const mode of MODES) {
    for (const forced of FORCED) {
      test(`FORCED instance ${forced.name} (${mode})`, async ({ page }) => {
        await checkPage(page, mode, {
          ...instancePage(forced.status, forced.errorMessage, forced.deployHistory),
          progressbars: forced.progressbars,
          ready: async (p, runAxe) => {
            await expect(forced.visible(p), `forced ${forced.name}: the element is not visible`).toBeVisible();
            await runAxe();
            if (forced.hover) {
              const target = forced.visible(p);
              const background = () => target.evaluate((el) => getComputedStyle(el).backgroundColor);
              const before = await background();
              await target.hover();
              // The hover must change the colour, or the second run checks nothing new.
              // runAxe then waits until the transition has ended.
              await expect.poll(background, { message: `forced ${forced.name}: the hover did not change the background (${before})` })
                .not.toBe(before);
              await runAxe();
            }
            await hoverStatusButtonsOnPage(p, runAxe);
          },
        });
      });
    }
  }

  // -------------------------------------------------------------------------
  // INTERACTIVE states (#758). These surfaces are a small part of the page, so each
  // axe run is limited to the surface and must have measured the named element. A
  // node that axe left undecided does not count.

  interface ToastState {
    severity: 'Success' | 'Info' | 'Warning' | 'Error';
    setup: () => PageSetup;
    trigger: (page: Page, socket: () => WebSocketRoute | undefined) => Promise<void>;
  }

  /** The Profile page answers a saved preference with a toast: success, or error on a refusal. */
  const preferencesSaveAnswers = (status: number): PageSetup => ({
    path: '/profile',
    heading: 'My Profile',
    before: (p) => p.route(/\/api\/v1\/notifications\/preferences$/, (route) => (
      route.request().method() === 'PUT' ? route.fulfill({ status, json: {} }) : route.continue()
    )),
  });
  const savePreferences = (page: Page) => page.getByRole('button', { name: 'Save', exact: true }).click();

  const TOASTS: ToastState[] = [
    { severity: 'Success', setup: () => preferencesSaveAnswers(200), trigger: savePreferences },
    { severity: 'Error', setup: () => preferencesSaveAnswers(400), trigger: savePreferences },
    {
      severity: 'Warning',
      setup: () => ({
        path: '/admin/notification-channels',
        heading: 'Notification Channels',
        before: (p) => p.route(/\/api\/v1\/admin\/notification-channels$/, (route) => (
          route.request().method() === 'POST'
            ? route.fulfill({ status: 201, json: { id: 'a11y-channel', name: 'a11y', warnings: ['forced warning'] } })
            : route.continue()
        )),
      }),
      trigger: async (page) => {
        await page.getByRole('button', { name: 'Create Channel' }).click();
        const dialog = page.getByRole('dialog');
        await dialog.getByRole('textbox', { name: 'Name' }).fill('a11y');
        await dialog.getByRole('textbox', { name: 'Webhook URL' }).fill('https://example.com/hook');
        await dialog.getByRole('button', { name: 'Create', exact: true }).click();
      },
    },
    {
      severity: 'Info',
      setup: () => ({ path: '/profile', heading: 'My Profile' }),
      // NotificationCenter shows a pushed notification as an info toast.
      trigger: async (_page, socket) => {
        await expect.poll(socket, { message: 'the app did not open its socket' }).toBeTruthy();
        socket()?.send(JSON.stringify({
          type: 'notification.new',
          payload: {
            id: 'a11y-note',
            user_id: 'a11y-user',
            type: 'deployment.success',
            title: 'Forced info toast',
            message: 'Forced by the accessibility check',
            is_read: false,
            created_at: '2026-10-19T10:00:00Z',
          },
        }));
      },
    },
  ];

  /** A dashboard row. The expiry chip reads only these fields. */
  const row = (id: string, name: string, fields: Record<string, unknown>) => ({
    id,
    name,
    namespace: `stack-${name}`,
    branch: 'main',
    status: 'running',
    stack_definition_id: seed.definitionId,
    owner_id: 'a11y-owner',
    created_at: '2026-10-19T09:00:00Z',
    updated_at: '2026-10-19T09:00:00Z',
    ...fields,
  });

  const minutesFromNow = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString();

  /** The expiry chips: the three colours of the countdown, for a TTL and for a fixed stop time. */
  const expiryRows = () => [
    row('a11y-ttl-ok', 'ttl-ok', { ttl_minutes: 600, expires_at: minutesFromNow(300) }),
    row('a11y-ttl-warn', 'ttl-warn', { ttl_minutes: 600, expires_at: minutesFromNow(45) }),
    row('a11y-ttl-crit', 'ttl-crit', { ttl_minutes: 600, expires_at: minutesFromNow(10) }),
    row('a11y-stop-ok', 'stop-ok', { expires_at: minutesFromNow(300) }),
    row('a11y-stop-warn', 'stop-warn', { expires_at: minutesFromNow(45) }),
    row('a11y-stop-crit', 'stop-crit', { expires_at: minutesFromNow(10) }),
    row('a11y-stop-draft', 'stop-draft', { status: 'draft', expires_at: minutesFromNow(300) }),
    row('a11y-expired', 'expired', { status: 'stopped', error_message: 'Expired (TTL)' }),
  ];

  const mockDashboardRows = async (page: Page, rows: () => unknown[]) => {
    await page.route(/\/api\/v1\/stack-instances(\?.*)?$/, (route) => (
      route.request().method() !== 'GET'
        ? route.continue()
        : route.fulfill({ json: { data: rows(), page: 1, pageSize: 25, total: rows().length } })
    ));
    await page.route(/\/api\/v1\/stack-instances\/a11y-[^/]+\/status$/, (route) => route.fulfill({
      json: { namespace: 'a11y', status: 'healthy', charts: [], last_checked: '2026-10-19T09:00:00Z' },
    }));
  };

  const openActionsMenu = async (page: Page): Promise<Locator> => {
    await page.getByRole('button', { name: 'Actions', exact: true }).click();
    const menu = page.getByRole('menu');
    await expect(menu, 'the Actions menu did not open').toBeVisible();
    return menu;
  };

  for (const mode of MODES as Mode[]) {
    test(`STATE selection toolbar on the dashboard (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/',
        heading: 'Stack Instances',
        allowed5xx: [/^GET \/api\/v1\/stack-instances\/[^/]+\/status$/],
        ready: async (p, runAxe) => {
          await p.getByRole('checkbox', { name: 'Select all instances' }).check();
          const toolbar = p.getByRole('toolbar', { name: 'Bulk actions' });
          await expect(toolbar, 'the selection toolbar did not open').toBeVisible();
          await runAxe({ selector: '[role=toolbar]', checked: toolbar.getByText(/\d+ selected/) });
          for (const name of ['Deploy', 'Stop', 'Clean', 'Delete', 'Clear Selection']) {
            await hoverAndCheck(p, toolbar.getByRole('button', { name, exact: true }), '[role=toolbar]', runAxe);
          }
        },
      });
    });

    // The tab decides the action: Unpublish on My Templates, Publish on All Drafts.
    for (const [tab, action] of [['My Templates', 'Unpublish'], ['All Drafts', 'Publish']]) {
      test(`STATE selection toolbar in the template gallery, ${tab} (${mode})`, async ({ page }) => {
        await checkPage(page, mode, {
          path: '/templates',
          heading: 'Template Gallery',
          ready: async (p, runAxe) => {
            await p.getByRole('tab', { name: tab }).click();
            await p.getByRole('checkbox', { name: `Select ${seed.templateName}` }).check();
            const toolbar = p.getByRole('toolbar', { name: 'Bulk actions' });
            await expect(toolbar, 'the selection toolbar did not open').toBeVisible();
            await runAxe({ selector: '[role=toolbar]', checked: toolbar.getByText(/\d+ selected/) });
            for (const name of [new RegExp(`^${action} \\(`), /^Delete \(/, /^Clear Selection$/]) {
              await hoverAndCheck(p, toolbar.getByRole('button', { name }), '[role=toolbar]', runAxe);
            }
          },
        });
      });
    }

    test(`STATE instance Actions menu on hover and focus (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        // The page offers Clean namespace only from some states, among them running.
        ...instancePage('running'),
        ready: async (p, runAxe) => {
          const menu = await openActionsMenu(p);
          const scope = '[role=menu]';
          const clean = menu.getByRole('menuitem', { name: 'Clean namespace' });
          const del = menu.getByRole('menuitem', { name: 'Delete' });
          await runAxe({ selector: scope, checked: del });
          await hoverAndCheck(p, clean, scope, runAxe);
          await hoverAndCheck(p, del, scope, runAxe);
          // Delete is the last item, Clean namespace the one above it.
          await focusAndCheck(p, del, 'End', scope, runAxe);
          await focusAndCheck(p, clean, 'ArrowUp', scope, runAxe);
        },
      });
    });

    for (const { severity, setup, trigger } of TOASTS) {
      test(`STATE toast ${severity} (${mode})`, async ({ page }) => {
        let socket: WebSocketRoute | undefined;
        await checkPage(page, mode, {
          ...setup(),
          onSocket: (ws) => { socket = ws; },
          ready: async (p, runAxe) => {
            await trigger(p, () => socket);
            const toast = p.locator(`[data-testid=toast-stack] .MuiAlert-color${severity}`);
            await expect(toast, `the ${severity} toast did not appear`).toBeVisible();
            // The pointer on a toast pauses its timer while axe runs.
            await toast.hover();
            await runAxe({ selector: '[data-testid=toast-stack]', checked: toast.locator('.MuiAlert-message') });
          },
        });
      });
    }

    // Button tints are translucent: on the page background (no Paper) a light
    // mode outlined button gets its darkest hover colour.
    test(`STATE outlined warning button on the page background, on hover (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: `/templates/${seed.publishedTemplateId}`,
        heading: seed.publishedTemplateName,
        ready: async (p, runAxe) => {
          const button = p.getByRole('button', { name: 'Unpublish', exact: true });
          await expect(button, 'the Unpublish button is not visible').toBeVisible();
          const onPage = await button.evaluate((el) => el.closest('.MuiPaper-root') === null);
          expect(onPage, 'the button must sit directly on the page background').toBe(true);
          await hoverAndCheck(p, button, 'main', runAxe);
        },
      });
    });

    // The boxes of the preview page have their own background.
    test(`STATE default and locked values of a template chart (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: `/templates/${seed.publishedTemplateId}`,
        heading: seed.publishedTemplateName,
        ready: async (p, runAxe) => {
          const defaults = p.locator('pre').filter({ hasText: 'replicaCount: 1' });
          const locked = p.locator('pre').filter({ hasText: 'pullPolicy: Always' });
          await expect(defaults, 'the Default Values box is not visible').toBeVisible();
          await expect(locked, 'the Locked Values box is not visible').toBeVisible();
          await runAxe({ selector: 'main', checked: defaults });
          await runAxe({ selector: 'main', checked: locked });
        },
      });
    });

    test(`STATE Expires in and Stops at chips, all colours (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/',
        heading: 'Stack Instances',
        before: (p) => mockDashboardRows(p, expiryRows),
        ready: async (p, runAxe) => {
          const chips = p.locator('main .MuiChip-root').filter({ hasText: /^(Expires in|Stops at|Expired)/ });
          await expect(chips, 'the 8 rows show 8 expiry chips').toHaveCount(8);
          // Each colour of the countdown is on screen, so that each is measured.
          for (const colour of ['Success', 'Warning', 'Error', 'Default']) {
            await expect(chips.and(p.locator(`.MuiChip-color${colour}`)).first(), `no ${colour} expiry chip`).toBeVisible();
          }
          for (let i = 0; i < 8; i++) {
            await runAxe({ selector: 'main', checked: chips.nth(i) });
          }
        },
      });
    });

    test(`STATE API-key expiry chips on the profile (${mode})`, async ({ page }) => {
      await checkPage(page, mode, {
        path: '/profile',
        heading: 'My Profile',
        before: (p) => p.route(/\/api\/v1\/users\/[^/]+\/api-keys$/, (route) => (
          route.request().method() !== 'GET' ? route.continue() : route.fulfill({
            json: [
              { id: 'k-exp', user_id: 'u', name: 'expired-key', prefix: 'aa', created_at: '2026-01-01T00:00:00Z', expires_at: minutesFromNow(-1440) },
              { id: 'k-soon', user_id: 'u', name: 'soon-key', prefix: 'bb', created_at: '2026-01-01T00:00:00Z', expires_at: minutesFromNow(1440) },
            ],
          })
        )),
        ready: async (p, runAxe) => {
          const expired = p.locator('main .MuiChip-root').filter({ hasText: /^Expired / });
          const soon = p.locator('main .MuiChip-root').filter({ hasText: /^Expires soon / });
          await expect(expired, 'the expired chip').toBeVisible();
          await expect(soon, 'the expiring-soon chip says so in words').toBeVisible();
          await runAxe({ selector: 'main', checked: expired });
          await runAxe({ selector: 'main', checked: soon });
        },
      });
    });
  }
});
