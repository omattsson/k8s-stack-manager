import { test, expect, type Locator, type Page, type WebSocketRoute } from '@playwright/test';
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
// A third block (#758) opens the states that a resting page does not show: a
// selected row, menus on hover and on keyboard focus, dialogs, toasts, and a
// button on the page background on hover. Each of its axe runs is limited to the
// surface and must have measured a named element.
//
// Not covered: focus rings, tooltips, charts, and colour as the only signal. The
// app WebSocket is blocked (answered by the test, which sends only for the info
// toast), so that no pushed message reloads a list or shows a toast while axe
// runs. A page that renders after the gradients are flattened would reach axe
// with its gradient. DB-backed, no cluster required.

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
  before?: (page: Page) => Promise<unknown>;
  /** Gets the socket of the app, for a test that pushes a message. */
  onSocket?: (socket: WebSocketRoute) => void;
  /**
   * Replaces the plain axe run: checks that an element is visible first, then
   * calls `runAxe` (once or more, for example after a hover).
   */
  ready?: (page: Page, runAxe: (scope?: AxeScope) => Promise<void>) => Promise<void>;
}

/** Limits an axe run to one surface (a menu, a dialog, the toasts, ...). */
interface AxeScope {
  /** CSS selector of the surface. */
  selector: string;
  /** Axe must have measured this element or an element inside it, and passed it. */
  checked: Locator;
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
  // A transition that an open or a hover just asked for starts on a later frame.
  await page.evaluate(() => new Promise((done) => {
    requestAnimationFrame(() => requestAnimationFrame(done));
  }));
  await page.waitForFunction(() =>
    document.getAnimations().every((a) =>
      (a.playState !== 'running' && !a.pending)
      || a.effect?.getComputedTiming().iterations === Infinity));
}

/**
 * Axe compares the elements under each line of a text. The page under a modal
 * differs from line to line, so multi-line text in a dialog comes back undecided
 * (elmPartiallyObscuring). Dialogs and menus are opaque, so hiding the app behind
 * them changes no colour. Returns the function that shows it again.
 */
async function hideAppBehindModal(page: Page, selector: string): Promise<() => Promise<void>> {
  const hid = await page.evaluate((sel) => {
    const root = document.getElementById('root');
    if (!root || !document.querySelector(sel)?.closest('.MuiModal-root')) return false;
    root.style.visibility = 'hidden';
    return true;
  }, selector);
  return async () => {
    if (hid) await page.evaluate(() => { document.getElementById('root')?.style.removeProperty('visibility'); });
  };
}

/**
 * MUI mounts the ripple element of a button or menu item at its first hover or
 * focus. It is transparent and takes no pointer events, but axe sees it on top of
 * the text: `bgOverlap`, so a control that the test just touched goes unmeasured.
 * Hide it while it is empty (nothing to paint). A running ripple has a child, so
 * it still counts.
 */
async function hideEmptyRipples(page: Page): Promise<void> {
  await page.addStyleTag({ content: '.MuiTouchRipple-root:empty { display: none; }' });
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
 * With a scope: only that surface, which must include a measured `checked` element.
 */
async function expectContrastAndLists(page: Page, what: string, hasMain: boolean, scope?: AxeScope): Promise<void> {
  await waitForTransitions(page);
  await revealClippedContent(page);
  await flattenGradients(page);
  await hideEmptyRipples(page);
  const builder = new AxeBuilder({ page }).withRules(['color-contrast', 'listitem']);
  const showApp = scope ? await hideAppBehindModal(page, scope.selector) : async () => undefined;
  const results = await (scope ? builder.include(scope.selector) : builder).analyze();
  await showApp();

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

  if (scope) {
    const { index, top } = await scope.checked.first().evaluate((el, sels) => {
      const r = el.getBoundingClientRect();
      return {
        index: sels.findIndex((sel) => {
          const node = document.querySelector(sel);
          return node !== null && el.contains(node);
        }),
        top: document.elementsFromPoint(r.x + r.width / 2, r.y + r.height / 2).slice(0, 3)
          .map((e) => `${e.tagName}.${String(e.className).slice(0, 80)}`).join(' > '),
      };
    }, targets);
    expect(
      index,
      `${what}: axe did not measure ${scope.checked} in ${scope.selector}: it passed ${targets.length} other node(s), and left ${JSON.stringify(reasons)} undecided. Topmost at its centre: ${top}`,
    ).toBeGreaterThanOrEqual(0);
    test.info().annotations.push({ type: 'axe', description: `${what}: ${describeNode('measured', passedNodes[index])}` });
    return;
  }

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

const backgroundOf = (target: Locator) => target.evaluate((el) => getComputedStyle(el).backgroundColor);

/**
 * Run axe with `target` in a state that changed its background. The state must
 * still hold after axe, or the run measured the wrong colours.
 */
async function checkInState(
  page: Page,
  target: Locator,
  selector: string,
  runAxe: (scope?: AxeScope) => Promise<void>,
  state: string,
  rest: string,
): Promise<void> {
  await expect.poll(() => backgroundOf(target), { message: `${state}: the background did not change (${rest})` }).not.toBe(rest);
  await waitForTransitions(page);
  const held = await backgroundOf(target);
  await runAxe({ selector, checked: target });
  expect(await backgroundOf(target), `${state}: the state did not hold while axe ran`).toBe(held);
}

/** Point at `target` and check the surface. The hover must change its background. */
async function hoverAndCheck(
  page: Page,
  target: Locator,
  selector: string,
  runAxe: (scope?: AxeScope) => Promise<void>,
): Promise<void> {
  await page.mouse.move(0, 0);
  await waitForTransitions(page);
  const rest = await backgroundOf(target);
  await target.hover();
  await checkInState(page, target, selector, runAxe, `hover on ${target}`, rest);
}

/** Move the keyboard focus to `target` with `key` and check the surface. */
async function focusAndCheck(
  page: Page,
  target: Locator,
  key: string,
  selector: string,
  runAxe: (scope?: AxeScope) => Promise<void>,
): Promise<void> {
  await page.mouse.move(0, 0);
  await waitForTransitions(page);
  const rest = await backgroundOf(target);
  await page.keyboard.press(key);
  await expect(target, `${key} did not move the focus to ${target}`).toBeFocused();
  await expect(target, `focus on ${target}: no focus-visible state`).toHaveClass(/Mui-focusVisible/);
  await checkInState(page, target, selector, runAxe, `focus on ${target}`, rest);
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

  // The test answers the app socket, and sends only through onSocket. See the top of the file.
  await page.routeWebSocket(/\/ws(\?|$)/, (socket) => spec.onSocket?.(socket));

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

  const runAxe = (scope?: AxeScope) => expectContrastAndLists(page, what, sidebar, scope);
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

  /** Answer the instance with this status, for as long as the page lives. */
  const forceInstanceStatus = (page: Page, instanceId: string, status: string, errorMessage?: string) =>
    page.route(new RegExp(`/api/v1/stack-instances/${instanceId}(\\?.*)?$`), async (route) => {
      if (route.request().method() !== 'GET') return route.continue();
      const res = await route.fetch();
      const body = { ...(await res.json()), status, error_message: errorMessage };
      return route.fulfill({ response: res, json: body });
    });

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
            await forceInstanceStatus(p, instanceId, forced.status, forced.errorMessage);
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

  // -------------------------------------------------------------------------
  // INTERACTIVE states (#758): a selected row, menus on hover and on keyboard
  // focus, dialogs, toasts, and a button directly on the page background. These
  // surfaces sit outside <main>, or are a small part of it, so each axe run is
  // limited to the surface and must have measured the named element. A node that
  // axe left undecided does not count.

  type PageSetup = Pick<PageCheck, 'path' | 'heading' | 'allowed5xx' | 'before'>;

  const instancePage = (status: string, withDeployHistory = false): PageSetup => {
    const { instanceId, instanceName } = seed;
    return {
      path: `/stack-instances/${instanceId}`,
      heading: instanceName,
      // A status other than draft makes the page read the pods: 502 without cluster access.
      allowed5xx: [new RegExp(`^GET /api/v1/stack-instances/${instanceId}/pods$`)],
      before: async (p) => {
        await forceInstanceStatus(p, instanceId, status);
        if (withDeployHistory) await mockDeployLog(p, instanceId, deployHistory);
      },
    };
  };

  const openActionsMenu = async (page: Page): Promise<Locator> => {
    await page.getByRole('button', { name: 'Actions', exact: true }).click();
    const menu = page.getByRole('menu');
    await expect(menu, 'the Actions menu did not open').toBeVisible();
    return menu;
  };

  interface DialogState {
    name: string;
    setup: () => PageSetup;
    open: (page: Page, dialog: Locator) => Promise<void>;
    /** The element that axe must measure in the dialog. */
    checked: (dialog: Locator) => Locator;
    /** Hover this element and check again. */
    hover?: (dialog: Locator) => Locator;
  }

  const DIALOGS: DialogState[] = [
    {
      // A text button in the error colour: it failed first at the top elevation.
      name: 'template link: Remove link',
      setup: () => ({
        path: `/stack-definitions/${seed.definitionId}/edit`,
        heading: 'Edit Stack Definition',
        // A real link needs a published template with charts; the dialog only needs the current link.
        before: (p) => p.route(new RegExp(`/api/v1/stack-definitions/${seed.definitionId}(\\?.*)?$`), async (route) => {
          if (route.request().method() !== 'GET') return route.continue();
          const res = await route.fetch();
          const body = { ...(await res.json()), source_template_id: seed.templateId, source_template_version: '1.0.0' };
          return route.fulfill({ response: res, json: body });
        }),
      }),
      open: (page) => page.getByRole('button', { name: 'Change Link' }).click(),
      checked: (dialog) => dialog.getByRole('button', { name: 'Remove link' }),
      hover: (dialog) => dialog.getByRole('button', { name: 'Remove link' }),
    },
    {
      name: 'clone: error text of a field',
      setup: () => instancePage('running'),
      open: async (page, dialog) => {
        await (await openActionsMenu(page)).getByRole('menuitem', { name: 'Clone' }).click();
        await dialog.getByRole('textbox', { name: 'Name' }).fill('NOT_VALID');
      },
      checked: (dialog) => dialog.locator('.MuiFormHelperText-root.Mui-error'),
    },
    {
      name: 'delete: contained error button',
      setup: () => instancePage('running'),
      open: async (page) => { await (await openActionsMenu(page)).getByRole('menuitem', { name: 'Delete' }).click(); },
      checked: (dialog) => dialog.getByRole('button', { name: 'Delete' }),
      hover: (dialog) => dialog.getByRole('button', { name: 'Delete' }),
    },
    {
      name: 'stop: confirm button',
      setup: () => instancePage('running'),
      open: (page) => page.getByRole('button', { name: 'Stop', exact: true }).click(),
      checked: (dialog) => dialog.getByRole('button', { name: 'Stop' }),
      hover: (dialog) => dialog.getByRole('button', { name: 'Stop' }),
    },
    {
      name: 'rollback: warning button and alerts',
      setup: () => instancePage('running', true),
      open: (page) => page.getByRole('button', { name: 'Rollback', exact: true }).click(),
      checked: (dialog) => dialog.locator('.MuiAlert-message').first(),
      hover: (dialog) => dialog.getByRole('button', { name: 'Roll Back' }),
    },
  ];

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

  for (const mode of MODES) {
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

    for (const { name, setup, open, checked, hover } of DIALOGS) {
      test(`STATE dialog ${name} (${mode})`, async ({ page }) => {
        await checkPage(page, mode, {
          ...setup(),
          ready: async (p, runAxe) => {
            const dialog = p.getByRole('dialog');
            await open(p, dialog);
            await expect(dialog, 'the dialog did not open').toBeVisible();
            await expect(dialog.locator('[role=progressbar]:not([aria-valuenow])'), 'the dialog is still loading').toHaveCount(0);
            await expect(checked(dialog), 'the element to measure is not visible').toBeVisible();
            await runAxe({ selector: '[role=dialog]', checked: checked(dialog) });
            if (hover) await hoverAndCheck(p, hover(dialog), '[role=dialog]', runAxe);
          },
        });
      });
    }

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
        path: `/templates/${seed.templateId}`,
        heading: seed.templateName,
        before: (p) => p.route(new RegExp(`/api/v1/templates/${seed.templateId}(\\?.*)?$`), async (route) => {
          if (route.request().method() !== 'GET') return route.continue();
          const res = await route.fetch();
          return route.fulfill({ response: res, json: { ...(await res.json()), is_published: true } });
        }),
        ready: async (p, runAxe) => {
          const button = p.getByRole('button', { name: 'Unpublish', exact: true });
          await expect(button, 'the Unpublish button is not visible').toBeVisible();
          const onPage = await button.evaluate((el) => el.closest('.MuiPaper-root') === null);
          expect(onPage, 'the button must sit directly on the page background').toBe(true);
          await hoverAndCheck(p, button, 'main', runAxe);
        },
      });
    });
  }
});
