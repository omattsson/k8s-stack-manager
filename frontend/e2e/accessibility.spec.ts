import { test, expect, type Locator, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { API_BASE, apiAddChart, apiCreateDefinition, apiLogin, loginAsAdmin, uniqueName } from './helpers';
import { mockDeployLog, type MockDeployLog } from './deployLogMock';

// Colour contrast and list structure (#740). Runs the axe rules `color-contrast`
// and `listitem` (and no other rule) on the 21 pages of the issue, in the light
// and the dark colour mode, at 1400x1000. A second block forces the status
// colours of the instance page that a run without a cluster never shows
// (status running, error, partial, ...; page.route edits the instance answer).
//
// A page that did not load must not pass: before axe runs, each test checks the
// heading, the URL, the sidebar, the colour mode, that no spinner is left and
// that no API call answered 5xx. After axe, the rule must have checked a
// minimum of nodes, and every node that axe could not decide ("incomplete") must
// have a known harmless reason.
//
// Not covered: focus rings, dialogs, menus, tooltips, snackbars (toasts), charts,
// and colour as the only signal. The pages are checked in their resting state:
// the app WebSocket is blocked (answered by the test, it never sends), so that
// no pushed message reloads a list or shows a toast while axe runs. A page that
// renders after the gradients are flattened would reach axe with its gradient.
// DB-backed, no cluster required.

type Mode = 'light' | 'dark';
const MODES: Mode[] = ['light', 'dark'];

/** Computed background of `body` per mode (theme/palette.ts, CssBaseline). */
const BODY_BACKGROUND: Record<Mode, string> = {
  dark: 'rgb(18, 18, 18)',
  light: 'rgb(245, 245, 245)',
};

/**
 * Fewest `color-contrast` nodes that must pass inside <main> (on the login page,
 * which has no <main>: on the whole page). The sidebar alone has more nodes than
 * a small minimum, so only the page content counts. A page with a heading and
 * an empty <main> fails.
 */
const MIN_CONTRAST_PASSES = 3;

/**
 * Reasons for which axe may leave a `color-contrast` node undecided:
 * shortTextContent (one character, for example an icon letter), emptyValue (an
 * empty input) and bgOverlap (an element covers the text). Any other reason,
 * above all `bgGradient`, fails the test, so a new surface cannot escape the
 * check unseen.
 */
const HARMLESS_INCOMPLETE = new Set(['shortTextContent', 'emptyValue', 'bgOverlap']);

type AxeResults = Awaited<ReturnType<AxeBuilder['analyze']>>;
type AxeNode = AxeResults['violations'][number]['nodes'][number];

interface Seed {
  definitionId: string;
  instanceId: string;
  instanceName: string;
  templateId: string;
  templateName: string;
}
const seed = {} as Seed;
let adminToken = '';

/** What a test checks before axe runs. */
interface PageCheck {
  /** Path the test opens. The final URL must have this path. */
  path: string;
  /** Text of the h1 (exact). */
  heading: string;
  /** False for the login page: no sidebar. */
  sidebar?: boolean;
  /** Spinners (indeterminate progress bars) that the page legitimately keeps. Default 0. */
  progressbars?: number;
  /**
   * API calls that may answer 5xx on this page, matched against
   * `METHOD /path` (no query). Each entry needs a comment that says why the
   * call fails without a cluster.
   */
  allowed5xx?: RegExp[];
  /** Runs before the page opens (route mocks). */
  before?: (page: Page) => Promise<void>;
  /**
   * Replaces the plain axe run: checks that an element is visible first, then
   * calls `runAxe` (once or more, for example after a hover).
   */
  ready?: (page: Page, runAxe: () => Promise<void>) => Promise<void>;
}

/**
 * MUI draws every dark Paper with `background-image: linear-gradient(c, c)`
 * (two equal stops: the elevation overlay). Axe cannot read the colour behind
 * text on such an element and returns the text as `incomplete` (messageKey
 * `bgGradient`) instead of checking it. In the dark mode that left hundreds of
 * nodes unchecked, among them real failures. Draw the same pixels without the
 * gradient: composite the overlay colour over the element's own background
 * colour and set it inline with `background-image: none`.
 */
async function flattenGradients(page: Page): Promise<void> {
  await page.evaluate(() => {
    const parse = (c: string): [number, number, number, number] => {
      const m = c.match(/rgba?\(([^)]+)\)/);
      if (!m) return [0, 0, 0, 0];
      const p = m[1].split(/[, /]+/).filter(Boolean).map(Number);
      return [p[0], p[1], p[2], p[3] ?? 1];
    };
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('*'))) {
      const cs = getComputedStyle(el);
      const m = cs.backgroundImage.match(/^linear-gradient\((rgba?\([^)]+\)), (rgba?\([^)]+\))\)$/);
      if (!m || m[1] !== m[2]) continue;
      const [r, g, b, a] = parse(m[1]);
      const [br, bg, bb, ba] = parse(cs.backgroundColor);
      const outA = a + ba * (1 - a);
      if (outA === 0) continue;
      const mix = (over: number, under: number) => Math.round((over * a + under * ba * (1 - a)) / outA);
      el.style.backgroundImage = 'none';
      el.style.backgroundColor = `rgba(${mix(r, br)}, ${mix(g, bg)}, ${mix(b, bb)}, ${outA})`;
    }
  });
}

/**
 * Axe cannot read text that a scroll container clips (reason
 * elmPartiallyObscured), for example the rows below the first 19 of the audit
 * log table (maxHeight 600): the number of rows depends on the data of the
 * run. Let such containers grow to their content, so that all text is on the
 * page and gets checked. The colours do not change. The sidebar list is left
 * alone: it scrolls by design and fits at this height.
 */
async function revealClippedContent(page: Page): Promise<void> {
  await page.evaluate(() => {
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('*'))) {
      if (el.closest('nav')) continue;
      const cs = getComputedStyle(el);
      if ((cs.overflowY === 'auto' || cs.overflowY === 'scroll') && el.scrollHeight > el.clientHeight + 1) {
        el.style.maxHeight = 'none';
        el.style.height = 'auto';
        el.style.overflowY = 'visible';
      }
    }
  });
}

/**
 * Wait until no CSS transition or animation with an end is running. A colour
 * in the middle of a transition (hover, theme, Collapse) gives a wrong ratio.
 * Endless animations (skeleton pulse) never end and do not change a colour
 * that axe reads, so they do not count.
 */
async function waitForTransitions(page: Page): Promise<void> {
  await page.waitForFunction(() =>
    document.getAnimations().every((a) =>
      (a.playState !== 'running' && !a.pending)
      || a.effect?.getComputedTiming().iterations === Infinity));
}

function describeNode(rule: string, node: AxeNode): string {
  const d = (node.any[0]?.data ?? {}) as {
    fgColor?: string; bgColor?: string; contrastRatio?: number; expectedContrastRatio?: string;
  };
  const colours = d.fgColor
    ? `fg ${d.fgColor} bg ${d.bgColor} ratio ${d.contrastRatio} (needs ${d.expectedContrastRatio}) `
    : '';
  return `  ${rule}: ${colours}${node.target.join(' ')}\n    ${node.html}`;
}

const messageKeyOf = (node: AxeNode): string => {
  for (const check of [...node.any, ...node.all, ...node.none]) {
    const key = (check.data as { messageKey?: string } | null)?.messageKey;
    if (key) return key;
  }
  return 'none';
};

/**
 * Run the two rules and fail on a violation, on an undecided node with an
 * unknown reason, and on a page whose content `color-contrast` checked too little.
 */
async function expectContrastAndLists(page: Page, what: string, hasMain: boolean): Promise<void> {
  await waitForTransitions(page);
  await revealClippedContent(page);
  await flattenGradients(page);
  const results = await new AxeBuilder({ page }).withRules(['color-contrast', 'listitem']).analyze();

  const violations = results.violations.flatMap((v) => v.nodes.map((n) => describeNode(v.id, n)));
  expect(violations, `${what}: ${violations.length} axe violation(s)\n${violations.join('\n')}`).toEqual([]);

  const undecided: string[] = [];
  const reasons: Record<string, number> = {};
  for (const n of (results.incomplete.find((i) => i.id === 'color-contrast')?.nodes ?? [])) {
    const reason = messageKeyOf(n);
    reasons[reason] = (reasons[reason] ?? 0) + 1;
    if (HARMLESS_INCOMPLETE.has(reason)) continue;
    const top = await page.evaluate((sel) => {
      const el = document.querySelector(sel);
      if (!el) return 'not found';
      const r = el.getBoundingClientRect();
      return document.elementsFromPoint(r.x + r.width / 2, r.y + r.height / 2).slice(0, 3)
        .map((e) => `${e.tagName}.${String(e.className).slice(0, 80)}`).join(' > ');
    }, n.target[0] as string).catch(() => 'unknown');
    undecided.push(`  reason ${reason}: ${n.target.join(' ')}\n    ${n.html}\n    topmost at its centre: ${top}`);
  }
  expect(
    undecided,
    `${what}: axe could not check ${undecided.length} node(s) for a reason that is not one of ${[...HARMLESS_INCOMPLETE].join(', ')}\n${undecided.join('\n')}`,
  ).toEqual([]);

  const passedNodes = results.passes.find((p) => p.id === 'color-contrast')?.nodes ?? [];
  const targets = passedNodes.map((n) => n.target[0] as string);
  const passed = hasMain
    ? await page.evaluate((sels) => sels.filter((sel) => {
      try { return Boolean(document.querySelector(sel)?.closest('main')); } catch { return false; }
    }).length, targets)
    : passedNodes.length;
  expect(
    passed,
    `${what}: only ${passed} color-contrast node(s) passed ${hasMain ? 'inside <main> (the sidebar does not count)' : 'on the page'}; is the page content empty?`,
  ).toBeGreaterThanOrEqual(MIN_CONTRAST_PASSES);

  // Shown in the HTML report; the reasons stay visible when nothing fails.
  test.info().annotations.push({
    type: 'axe',
    description: `${what}: color-contrast passes ${passedNodes.length} (${passed} in the content), incomplete ${JSON.stringify(reasons)}`,
  });
}

/**
 * Open a page in one colour mode, check that it really loaded, then run axe.
 * All page tests and all forced-state tests go through here.
 */
async function checkPage(page: Page, mode: Mode, spec: PageCheck): Promise<void> {
  const { path, heading } = spec;
  const what = `${path} (${mode})`;
  const sidebar = spec.sidebar ?? true;

  // The colour mode comes from localStorage, as the toggle stores it.
  await page.addInitScript((m) => localStorage.setItem('theme-mode', m), mode);
  if (sidebar) await loginAsAdmin(page);

  // The test answers the app socket; it never sends. See the top of the file.
  await page.routeWebSocket(/\/ws(\?|$)/, () => {});

  const failed: string[] = [];
  page.on('response', (res) => {
    if (res.status() < 500 || !res.url().includes('/api/')) return;
    const key = `${res.request().method()} ${new URL(res.url()).pathname}`;
    if (!spec.allowed5xx?.some((re) => re.test(key))) failed.push(`${res.status()} ${key}`);
  });

  await spec.before?.(page);
  await page.goto(path);

  const url = () => new URL(page.url()).pathname;
  await expect.poll(url, { message: `${what}: final URL (a redirect to /login means no session)` }).toBe(path);
  await expect(
    page.getByRole('heading', { name: heading, level: 1, exact: true }),
    `${what}: heading "${heading}" is not visible`,
  ).toBeVisible({ timeout: 15_000 });

  if (sidebar) {
    await expect(page.locator('main'), `${what}: the page has no <main> element`).toBeVisible();
    const nav = page.getByRole('navigation', { name: 'Main navigation' });
    await expect(nav, `${what}: sidebar "Main navigation" is not visible`).toBeVisible();
    await expect(nav, `${what}: sidebar has no "Administration" section`).toContainText('Administration');
  }
  await expect(page.locator('body'), `${what}: the ${mode} mode did not take effect`)
    .toHaveCSS('background-color', BODY_BACKGROUND[mode]);
  // Only an indeterminate bar means loading: a determinate one shows a value
  // (the success rate on Analytics, the usage on Cluster Health) and stays.
  await expect(page.locator('[role=progressbar]:not([aria-valuenow])'), `${what}: a spinner is still shown`)
    .toHaveCount(spec.progressbars ?? 0, { timeout: 20_000 });
  // Data that arrives after the spinner is gone (lists, charts, editors).
  await page.waitForLoadState('networkidle', { timeout: 15_000 });
  await expect.poll(url, { message: `${what}: final URL after loading` }).toBe(path);

  const runAxe = () => expectContrastAndLists(page, what, sidebar);
  await (spec.ready ? spec.ready(page, runAxe) : runAxe());

  expect(failed, `${what}: API calls answered 5xx:\n${failed.join('\n')}`).toEqual([]);
}

test.describe('Accessibility: colour contrast and lists (#740)', () => {
  // The size of the issue's measurements; the default 1280x720 hides the bottom of the sidebar and of tables.
  test.use({ viewport: { width: 1400, height: 1000 } });

  test.beforeAll(async ({ request }) => {
    adminToken = await apiLogin(request);
    seed.definitionId = await apiCreateDefinition(request, adminToken, uniqueName('a11y-def'));
    await apiAddChart(request, adminToken, seed.definitionId, { chart_name: 'web', default_values: 'replicaCount: 1\n' });
    seed.instanceName = uniqueName('a11y');
    const inst = await request.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: { Authorization: `Bearer ${adminToken}` },
      data: { stack_definition_id: seed.definitionId, name: seed.instanceName, branch: 'main' },
    });
    expect(inst.ok(), await inst.text()).toBe(true);
    seed.instanceId = (await inst.json()).id;
    // A draft template: the gallery shows a real warning "Draft" chip.
    seed.templateName = uniqueName('a11y-tpl');
    const tpl = await request.post(`${API_BASE}/api/v1/templates`, {
      headers: { Authorization: `Bearer ${adminToken}` },
      data: {
        name: seed.templateName,
        description: 'Draft template for the accessibility check',
        category: 'a11y',
        version: '1.0.0',
        default_branch: 'main',
      },
    });
    expect(tpl.ok(), await tpl.text()).toBe(true);
    seed.templateId = (await tpl.json()).id;
  });

  test.afterAll(async ({ request }) => {
    if (!adminToken) return;
    const headers = { Authorization: `Bearer ${adminToken}` };
    // Best effort: a failed cleanup must not hide a test result.
    for (const url of [
      seed.instanceId && `/api/v1/stack-instances/${seed.instanceId}`,
      seed.templateId && `/api/v1/templates/${seed.templateId}`,
      seed.definitionId && `/api/v1/stack-definitions/${seed.definitionId}`,
    ]) {
      if (url) await request.delete(`${API_BASE}${url}`, { headers }).catch(() => undefined);
    }
  });

  // The 21 pages of the issue. `path` is a function: the IDs exist after beforeAll.
  const PAGES: { name: string; spec: () => PageCheck }[] = [
    { name: 'login', spec: () => ({ path: '/login', heading: 'Sign In', sidebar: false }) },
    {
      name: 'dashboard',
      spec: () => ({
        path: '/',
        heading: 'Stack Instances',
        // The page reads the status of each running instance, which needs a
        // cluster. Other specs set rows to running on the same backend.
        allowed5xx: [/^GET \/api\/v1\/stack-instances\/[^/]+\/status$/],
      }),
    },
    {
      name: 'templates',
      spec: () => ({
        path: '/templates',
        heading: 'Template Gallery',
        // The seeded draft template is on the tab "My Templates" (the admin owns it)
        // with a warning "Draft" chip: check the chip on the card of that template.
        ready: async (page, runAxe) => {
          await runAxe();
          await page.getByRole('tab', { name: 'My Templates' }).click();
          const card = page.locator('.MuiCard-root').filter({ hasText: seed.templateName });
          await expect(card.getByText('Draft', { exact: true }), 'the Draft chip of the seeded template is not visible').toBeVisible();
          await runAxe();
        },
      }),
    },
    { name: 'template builder', spec: () => ({ path: '/templates/new', heading: 'Create Template' }) },
    { name: 'stack definitions', spec: () => ({ path: '/stack-definitions', heading: 'Stack Definitions' }) },
    { name: 'new stack definition', spec: () => ({ path: '/stack-definitions/new', heading: 'Create Stack Definition' }) },
    {
      name: 'edit stack definition',
      spec: () => ({ path: `/stack-definitions/${seed.definitionId}/edit`, heading: 'Edit Stack Definition' }),
    },
    { name: 'new stack instance', spec: () => ({ path: '/stack-instances/new', heading: 'Create Stack Instance' }) },
    {
      name: 'stack instance',
      spec: () => ({ path: `/stack-instances/${seed.instanceId}`, heading: seed.instanceName }),
    },
    { name: 'audit log', spec: () => ({ path: '/audit-log', heading: 'Audit Log' }) },
    { name: 'users', spec: () => ({ path: '/admin/users', heading: 'User Management' }) },
    {
      name: 'orphaned namespaces',
      spec: () => ({
        path: '/admin/orphaned-namespaces',
        heading: 'Orphaned Namespaces',
        // The scan lists the namespaces of the cluster: 503 without cluster access.
        allowed5xx: [/^GET \/api\/v1\/admin\/orphaned-namespaces$/],
      }),
    },
    { name: 'clusters', spec: () => ({ path: '/admin/clusters', heading: 'Cluster Management' }) },
    {
      name: 'cluster health',
      spec: () => ({
        path: '/admin/cluster-health',
        heading: 'Cluster Health',
        // The e2e backend has no cluster access: the live health (summary,
        // nodes), namespace and utilization reads of the default cluster answer 500.
        allowed5xx: [/^GET \/api\/v1\/clusters\/[^/]+\/(health\/(summary|nodes)|namespaces|utilization)$/],
      }),
    },
    { name: 'analytics', spec: () => ({ path: '/admin/analytics', heading: 'Analytics' }) },
    { name: 'shared values', spec: () => ({ path: '/admin/shared-values', heading: 'Shared Values' }) },
    { name: 'cleanup policies', spec: () => ({ path: '/admin/cleanup-policies', heading: 'Cleanup Policies' }) },
    { name: 'notification channels', spec: () => ({ path: '/admin/notification-channels', heading: 'Notification Channels' }) },
    { name: 'profile', spec: () => ({ path: '/profile', heading: 'My Profile' }) },
    { name: 'notifications', spec: () => ({ path: '/notifications', heading: 'Notifications' }) },
    { name: 'not found', spec: () => ({ path: '/does-not-exist', heading: '404' }) },
  ];

  for (const mode of MODES) {
    for (const { name, spec } of PAGES) {
      test(`${name} (${mode})`, async ({ page }) => {
        await checkPage(page, mode, spec());
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

  for (const mode of MODES) {
    for (const forced of FORCED) {
      test(`FORCED instance ${forced.name} (${mode})`, async ({ page }) => {
        const { instanceId, instanceName } = seed;
        await checkPage(page, mode, {
          path: `/stack-instances/${instanceId}`,
          heading: instanceName,
          progressbars: forced.progressbars,
          // A status other than draft makes the page read the pods: 502 without cluster access.
          allowed5xx: [new RegExp(`^GET /api/v1/stack-instances/${instanceId}/pods$`)],
          before: async (p) => {
            await p.route(new RegExp(`/api/v1/stack-instances/${instanceId}(\\?.*)?$`), async (route) => {
              if (route.request().method() !== 'GET') return route.continue();
              const res = await route.fetch();
              const body = { ...(await res.json()), status: forced.status, error_message: forced.errorMessage };
              return route.fulfill({ response: res, json: body });
            });
            if (forced.deployHistory) await mockDeployLog(p, instanceId, deployHistory);
          },
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
          },
        });
      });
    }
  }
});
