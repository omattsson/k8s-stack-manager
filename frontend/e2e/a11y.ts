import { test, expect, type APIRequestContext, type JSHandle, type Locator, type Page, type WebSocketRoute } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { API_BASE, apiAddChart, apiCreateDefinition, apiLogin, loginAsAdmin, uniqueName } from './helpers';
import type { StackInstance } from '../src/types';

// The shared part of the accessibility specs (#740, #758): opening a page, the
// axe run, the explicit measurements, and the seed data.
//
// Axe rules: `color-contrast` (also on one-character text), `listitem`,
// `label-content-name-mismatch` (WCAG 2.5.3) and `link-in-text-block` (a link
// that only colour sets apart). Axe cannot read everything, so an explicit check
// measures placeholders and step icon numbers, and `focusRingSweep` measures
// focus rings. A node that axe leaves undecided fails, unless KNOWN_UNDECIDED
// names its selector and why.

export type Mode = 'light' | 'dark';
export const MODES: Mode[] = ['light', 'dark'];

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

/** AA for text, and for a focus ring or an icon that carries meaning (WCAG 1.4.3, 1.4.11). */
const AA_TEXT = 4.5;
export const AA_NON_TEXT = 3;

/** Widest viewport that gets the phone layout (MUI `sm` breakpoint is 600). */
const PHONE_MAX_WIDTH = 599;

/**
 * The only nodes that axe may leave undecided. Any other undecided node fails, so
 * a new surface or a new overlay cannot escape the check unseen. `phoneOnly`: only
 * at the phone width.
 */
const KNOWN_UNDECIDED: { rule: string; reason: string; selector: string; why: string; phoneOnly?: boolean }[] = [
  {
    rule: 'color-contrast',
    reason: 'emptyValue',
    selector: '.MuiInputBase-input',
    why: 'An empty field has no text. Filled fields are measured, and expectExplicitContrast measures placeholders.',
  },
  {
    rule: 'color-contrast',
    reason: 'emptyValue',
    selector: 'textarea.ime-text-area, textarea.iPadShowKeyboard',
    why: 'Helpers of the Monaco editor that never show text.',
  },
  {
    rule: 'color-contrast',
    reason: 'elmPartiallyObscured',
    phoneOnly: true,
    selector: '.MuiTableContainer-root td, .MuiTableContainer-root td *, .MuiTableContainer-root th, .MuiTableContainer-root th *',
    why: 'A table on a phone scrolls sideways; cells off screen are reachable by scroll and keyboard; checked by the "table scroll" tests in accessibility-phone.spec.ts. The same cells are measured at the desktop width.',
  },
  {
    rule: 'color-contrast',
    reason: 'bgOverlap',
    phoneOnly: true,
    selector: '[class*="react-diff-"][class*="-line-number"], [class*="react-diff-"][class*="-word-diff"]',
    why: 'The diff viewer scrolls sideways on a phone, and its code slides under the line numbers and changed words, so axe finds no single background. The same nodes pass at the desktop width.',
  },
  {
    rule: 'color-contrast',
    reason: 'elmPartiallyObscuring',
    selector: '.MuiAutocomplete-listbox *',
    why: 'A wrapped option over the page: what is under each line differs. The option text is measured, and its colours do not depend on the page.',
  },
  {
    rule: 'color-contrast',
    reason: 'bgOverlap',
    selector: '.MuiStepIcon-text',
    why: 'SVG text on an SVG circle, which axe cannot read. expectExplicitContrast measures it.',
  },
];

type AxeResults = Awaited<ReturnType<AxeBuilder['analyze']>>;
type AxeNode = AxeResults['violations'][number]['nodes'][number];

/** Limits an axe run to one surface (a menu, a dialog, the toasts, ...). */
export interface AxeScope {
  /** CSS selector of the surface. */
  selector: string;
  /** Axe must have measured this element or an element inside it, and passed it. */
  checked: Locator;
}

export type RunAxe = (scope?: AxeScope) => Promise<void>;

/** What a test checks before axe runs. */
export interface PageCheck {
  /** Path the test opens. The final URL must have this path. */
  path: string;
  /** Text of the h1 (exact). */
  heading: string;
  /** False for the login page: no sidebar. */
  sidebar?: boolean;
  /** Phone layout: an app bar with a menu button instead of the sidebar. */
  phone?: boolean;
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
  ready?: (page: Page, runAxe: RunAxe) => Promise<void>;
}

/** Setup of a page without its test steps. */
export type PageSetup = Pick<PageCheck, 'path' | 'heading' | 'allowed5xx' | 'before' | 'progressbars' | 'sidebar'>;

/** The colour arithmetic in the browser, shared by the explicit checks. */
interface A11yBrowserHelpers {
  ratio: (fg: string, bg: string) => number;
  /** The opaque colour behind an element, with translucent layers composited. */
  background: (el: Element | null) => string;
  /** A colour with an extra opacity over an opaque colour, as an opaque colour. */
  over: (color: string, opacity: number, under: string) => string;
}
declare global {
  interface Window { a11y: A11yBrowserHelpers }
}

function installBrowserHelpers(): void {
  type Rgba = [number, number, number, number];
  const parse = (c: string): Rgba => {
    const m = c.match(/rgba?\(([^)]+)\)/);
    if (!m) return [0, 0, 0, 0];
    const p = m[1].split(/[, /]+/).filter(Boolean).map(Number);
    return [p[0], p[1], p[2], p[3] ?? 1];
  };
  const blend = (top: Rgba, under: Rgba): Rgba => {
    const a = top[3] + under[3] * (1 - top[3]);
    if (a === 0) return [0, 0, 0, 0];
    const mix = (i: number) => (top[i] * top[3] + under[i] * under[3] * (1 - top[3])) / a;
    return [mix(0), mix(1), mix(2), a];
  };
  const text = (c: Rgba) => `rgba(${c[0]}, ${c[1]}, ${c[2]}, ${c[3]})`;
  const luminance = (c: Rgba) => {
    const [r, g, b] = [c[0], c[1], c[2]].map((v) => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const background = (el: Element | null): string => {
    const layers: Rgba[] = [];
    for (let node = el; node; node = node.parentElement) {
      const cs = getComputedStyle(node);
      // The elevation overlay of dark Paper: linear-gradient(c, c).
      const overlay = cs.backgroundImage.match(/^linear-gradient\((rgba?\([^)]+\)), /);
      if (overlay) layers.push(parse(overlay[1]));
      const own = parse(cs.backgroundColor);
      layers.push(own);
      if (own[3] === 1) break;
    }
    return text(layers.reduceRight<Rgba>((under, top) => blend(top, under), [255, 255, 255, 1]));
  };
  const ratio = (fg: string, bg: string): number => {
    const under = parse(bg);
    const l1 = luminance(blend(parse(fg), under));
    const l2 = luminance(under);
    return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
  };
  const over = (color: string, opacity: number, under: string): string => {
    const c = parse(color);
    return text(blend([c[0], c[1], c[2], c[3] * opacity], parse(under)));
  };
  window.a11y = { ratio, background, over };
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
export async function flattenGradients(page: Page): Promise<void> {
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
    // From the inside out: a list that grows can make the popup around it clip.
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('*')).reverse()) {
      if (el.closest('nav') || el.closest('.monaco-editor')) continue;
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
export async function waitForTransitions(page: Page): Promise<void> {
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
 * Four elements paint nothing over text, but axe puts them on top of it
 * (`bgOverlap`), so the text goes unmeasured. The ripple element, mounted at the
 * first hover or focus of a control: hidden while empty, as a running ripple has
 * a child and does paint. The `fieldset` of an outlined field: only a border, so
 * the text of a filled field gets measured. The bar of a LinearProgress: moved
 * sideways by a transform and clipped by its root, so it does not paint over the
 * cell next to it. The line layer of a Monaco editor: taller than the editor and
 * clipped by it, so it is cut to the editor. Returns the function that puts them back.
 */
async function hideTransparentOverlays(page: Page): Promise<() => Promise<void>> {
  const style = await page.addStyleTag({
    content: `.MuiTouchRipple-root:empty, .MuiOutlinedInput-notchedOutline, .MuiLinearProgress-bar { display: none !important; }
      .monaco-editor .lines-content { max-height: 100% !important; }`,
  });
  return async () => { await style.evaluate((node) => (node as Element).remove()); };
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
 * What axe does not measure. A placeholder (an empty field goes undecided), and
 * the number of a step icon (SVG text on an SVG circle).
 */
async function expectExplicitContrast(page: Page, what: string, selector: string): Promise<void> {
  await page.evaluate(installBrowserHelpers);
  const { measured, failures } = await page.evaluate(({ selector: sel, aa }) => {
    const { ratio, background, over } = window.a11y;
    const measuredNodes: string[] = [];
    const failed: string[] = [];
    const record = (label: string, value: number) => {
      const line = `${label}: ${value.toFixed(2)}`;
      measuredNodes.push(line);
      if (value < aa) failed.push(line);
    };
    const root = document.querySelector(sel) ?? document.body;
    for (const input of Array.from(root.querySelectorAll<HTMLInputElement>('input[placeholder], textarea[placeholder]'))) {
      if (input.value || input.disabled || !input.placeholder || !input.getClientRects().length) continue;
      const cs = getComputedStyle(input, '::placeholder');
      const opacity = Number(cs.opacity);
      // MUI hides it while the label sits on the field.
      if (opacity === 0) continue;
      const bg = background(input);
      record(`placeholder "${input.placeholder}"`, ratio(over(cs.color, opacity, bg), bg));
    }
    for (const icon of Array.from(root.querySelectorAll<SVGElement>('svg.MuiStepIcon-root'))) {
      const number = icon.querySelector('text');
      if (!number) continue;
      const circle = over(getComputedStyle(icon).color, 1, background(icon));
      record(`step icon ${number.textContent}`, ratio(getComputedStyle(number).fill, circle));
    }
    return { measured: measuredNodes, failures: failed };
  }, { selector, aa: AA_TEXT });
  expect(failures, `${what}: below ${AA_TEXT}:1 (explicit measurement)\n  ${failures.join('\n  ')}`).toEqual([]);
  if (measured.length) test.info().annotations.push({ type: 'explicit', description: `${what}: ${measured.join('; ')}` });
}

/**
 * Run the rules and fail on a violation, on an undecided node outside
 * KNOWN_UNDECIDED, and on a page whose content `color-contrast` checked too little.
 * With a scope: only that surface, which must include a measured `checked` element.
 */
export async function expectContrastAndLists(page: Page, what: string, hasMain: boolean, scope?: AxeScope): Promise<void> {
  await waitForTransitions(page);
  // Own pass before revealClippedContent: that removes vertical scroll, which this rule needs.
  const early = await (scope ? new AxeBuilder({ page }).include(scope.selector) : new AxeBuilder({ page }))
    .withRules(['scrollable-region-focusable']).analyze();
  await revealClippedContent(page);
  await flattenGradients(page);
  await expectExplicitContrast(page, what, scope?.selector ?? 'body');
  const showOverlays = await hideTransparentOverlays(page);
  // Options first: `options()` replaces what `withRules()` set.
  const builder = new AxeBuilder({ page })
    // One-character text is measured too, for example a badge count.
    .options({ checks: { 'color-contrast': { options: { ignoreLength: true } } } } as unknown as Parameters<AxeBuilder['options']>[0])
    .withRules(['color-contrast', 'listitem', 'label-content-name-mismatch', 'link-in-text-block', 'region', 'aria-progressbar-name', 'empty-table-header']);
  const showApp = scope ? await hideAppBehindModal(page, scope.selector) : async () => undefined;
  const results = await (scope ? builder.include(scope.selector) : builder).analyze();
  await showApp();
  await showOverlays();

  const violations: string[] = [];
  for (const v of [...early.violations, ...results.violations]) {
    for (const n of v.nodes) {
      // Axe cuts the HTML short: for a name mismatch, show the two strings it compared.
      const compared = v.id !== 'label-content-name-mismatch' ? '' : await page.evaluate((sel) => {
        const el = document.querySelector<HTMLElement>(sel);
        return `\n    name "${el?.getAttribute('aria-label')}"\n    visible "${el?.innerText.replace(/\s+/g, ' ')}"`;
      }, n.target[0] as string);
      violations.push(`${describeNode(v.id, n)}${compared}`);
    }
  }
  expect(violations, `${what}: ${violations.length} axe violation(s)\n${violations.join('\n')}`).toEqual([]);

  const phoneWidth = (page.viewportSize()?.width ?? Infinity) <= PHONE_MAX_WIDTH;
  const undecided: string[] = [];
  const reasons: Record<string, number> = {};
  for (const result of results.incomplete) {
    for (const n of result.nodes) {
      const reason = messageKeyOf(n);
      const key = `${result.id}/${reason}`;
      reasons[key] = (reasons[key] ?? 0) + 1;
      const selectors = KNOWN_UNDECIDED.filter((k) => k.rule === result.id && k.reason === reason && (!k.phoneOnly || phoneWidth)).map((k) => k.selector);
      const { known, top } = await page.evaluate(({ sel, known: list }) => {
        const el = document.querySelector(sel);
        if (!el) return { known: false, top: 'not found' };
        const r = el.getBoundingClientRect();
        const name = (e: Element) => `${e.tagName}.${String(e.getAttribute('class')).slice(0, 60)}`;
        // What axe itself sees on top of the text: elementsFromPoint skips an
        // element without pointer events.
        const axe = (window as unknown as {
          axe: { setup: (e: Element) => void; teardown: () => void; commons: { dom: { getTextElementStack: (e: Element) => Element[][] } } };
        }).axe;
        axe.setup(document.documentElement);
        const stack = (axe.commons.dom.getTextElementStack(el)[0] ?? []).slice(0, 3).map(name).join(' > ');
        axe.teardown();
        return {
          known: list.some((s) => el.matches(s)),
          top: `${document.elementsFromPoint(r.x + r.width / 2, r.y + r.height / 2).slice(0, 3).map(name).join(' > ')}
    axe stack: ${stack}
    rect x ${Math.round(r.x)} to ${Math.round(r.right)}, y ${Math.round(r.y)} to ${Math.round(r.bottom)}, screen ${innerWidth}x${innerHeight}`,
        };
      }, { sel: n.target[0] as string, known: selectors }).catch((e) => ({ known: false, top: `unknown: ${e}` }));
      if (!known) undecided.push(`  ${result.id}, reason ${reason}: ${n.target.join(' ')}\n    ${n.html}\n    topmost at its centre: ${top}`);
    }
  }
  expect(
    undecided,
    `${what}: axe could not check ${undecided.length} node(s), and KNOWN_UNDECIDED does not name them\n${undecided.join('\n')}`,
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
  runAxe: RunAxe,
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
export async function hoverAndCheck(page: Page, target: Locator, selector: string, runAxe: RunAxe): Promise<void> {
  await page.mouse.move(0, 0);
  await waitForTransitions(page);
  const rest = await backgroundOf(target);
  await target.hover();
  await checkInState(page, target, selector, runAxe, `hover on ${target}`, rest);
}

/** Move the keyboard focus to `target` with `key` and check the surface. */
export async function focusAndCheck(page: Page, target: Locator, key: string, selector: string, runAxe: RunAxe): Promise<void> {
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
export async function checkPage(page: Page, mode: Mode, spec: PageCheck): Promise<void> {
  const { path, heading } = spec;
  const what = `${path} (${mode}${spec.phone ? ', phone' : ''})`;
  const sidebar = spec.sidebar ?? true;

  // The colour mode comes from localStorage, as the toggle stores it.
  await page.addInitScript((m) => localStorage.setItem('theme-mode', m), mode);
  if (sidebar) await loginAsAdmin(page);

  // The test answers the app socket, and sends only through onSocket: no pushed
  // message may reload a list or show a toast while axe runs.
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
  const expectedPath = new URL(path, 'http://x').pathname;
  await expect.poll(url, { message: `${what}: final URL (a redirect to /login means no session)` }).toBe(expectedPath);
  await expect(
    page.getByRole('heading', { name: heading, level: 1, exact: true }),
    `${what}: heading "${heading}" is not visible`,
  ).toBeVisible({ timeout: 15_000 });

  if (sidebar) {
    await expect(page.locator('main'), `${what}: the page has no <main> element`).toBeVisible();
    if (spec.phone) {
      await expect(page.locator('header').getByRole('button', { name: 'Open navigation' }), `${what}: no menu button in the app bar`).toBeVisible();
    } else {
      const nav = page.getByRole('navigation', { name: 'Main navigation' });
      await expect(nav, `${what}: sidebar "Main navigation" is not visible`).toBeVisible();
      await expect(nav, `${what}: sidebar has no "Administration" section`).toContainText('Administration');
    }
  }
  await expect(page.locator('body'), `${what}: the ${mode} mode did not take effect`)
    .toHaveCSS('background-color', BODY_BACKGROUND[mode]);
  // Only an indeterminate bar means loading: a determinate one shows a value
  // (the success rate on Analytics, the usage on Cluster Health) and stays.
  await expect(page.locator('[role=progressbar]:not([aria-valuenow])'), `${what}: a spinner is still shown`)
    .toHaveCount(spec.progressbars ?? 0, { timeout: 20_000 });
  // Data that arrives after the spinner is gone (lists, charts, editors).
  await page.waitForLoadState('networkidle', { timeout: 15_000 });
  await expect.poll(url, { message: `${what}: final URL after loading` }).toBe(expectedPath);

  const runAxe: RunAxe = (scope) => expectContrastAndLists(page, what, sidebar, scope);
  await (spec.ready ? spec.ready(page, runAxe) : runAxe());

  expect(failed, `${what}: API calls answered 5xx:\n${failed.join('\n')}`).toEqual([]);
}

/** What one Tab stop shows as its focus indicator. */
export interface FocusStop {
  element: string;
  /** outline, border (a field), or none. */
  kind: string;
  ratio: number;
  detail: string;
  /** The side of the ring that an `overflow` parent cuts off, if any. */
  clipped?: string;
  /** `outline: auto`: the colour is up to the browser and the platform. */
  browserDefault?: boolean;
}

/** How far a focus ring sweep goes. */
export interface SweepOptions {
  /** The key that moves the focus. Default Tab. */
  key?: string;
  /** Fail when this many stops did not get round. Default 60. A list that grows with the data needs more. */
  maxStops?: number;
  /** Measure only this many stops, and say why. Without it, the sweep must get all the way round. */
  partial?: { stops: number; reason: string };
  /** Why a code editor on this surface may be jumped over: Monaco keeps the Tab key. */
  skipEditors?: string;
}

/** Measure the focus indicator of the focused element against what is behind it. */
async function measureFocus(page: Page): Promise<FocusStop | null> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    if (!el || el === document.body) return null;
    const { ratio, background } = window.a11y;
    const label = (el.getAttribute('aria-label') || el.textContent || el.getAttribute('name') || el.id || '').trim().slice(0, 40);
    const element = `${el.tagName.toLowerCase()}${el.getAttribute('role') ? `[${el.getAttribute('role')}]` : ''} "${label}"`;
    // A checkbox, radio or switch has the focus on an inner input.
    const host = el.closest<HTMLElement>('.Mui-focusVisible') ?? el;
    const cs = getComputedStyle(host);
    if (cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) >= 1) {
      // An outline with an offset paints on what is behind the element.
      const behind = background(parseFloat(cs.outlineOffset) >= 0 ? host.parentElement : host);
      // The outer edge of the ring, against each parent that cuts off its overflow.
      const reach = parseFloat(cs.outlineOffset) + parseFloat(cs.outlineWidth);
      const r = host.getBoundingClientRect();
      const sides: string[] = [];
      for (let p = host.parentElement; p && p !== document.body; p = p.parentElement) {
        const o = getComputedStyle(p);
        if (o.overflowX === 'visible' && o.overflowY === 'visible') continue;
        const c = p.getBoundingClientRect();
        const by = (side: string, px: number) => sides.push(`${side} by ${Math.round(px)}px at ${p.tagName.toLowerCase()}.${String(p.getAttribute('class')).replace(/css-\S+/g, '').trim().slice(0, 40)} (overflow ${o.overflowX}, scrolled ${Math.round(p.scrollLeft)} of ${p.scrollWidth - p.clientWidth}, focus link ${Math.round(r.left)} to ${Math.round(r.right)}, box ${Math.round(c.left)} to ${Math.round(c.right)})`);
        if (o.overflowX !== 'visible' && r.left - reach < c.left - 0.5) by('left', c.left - r.left + reach);
        if (o.overflowX !== 'visible' && r.right + reach > c.right + 0.5) by('right', r.right + reach - c.right);
        if (o.overflowY !== 'visible' && r.top - reach < c.top - 0.5) by('top', c.top - r.top + reach);
        if (o.overflowY !== 'visible' && r.bottom + reach > c.bottom + 0.5) by('bottom', r.bottom + reach - c.bottom);
      }
      return {
        element,
        kind: `outline ${cs.outlineStyle}`,
        ratio: ratio(cs.outlineColor, behind),
        detail: `${cs.outlineColor} on ${behind}, overflow-x ${cs.overflowX}`,
        browserDefault: cs.outlineStyle === 'auto',
        clipped: sides.length ? [...new Set(sides)].join(', ') : undefined,
      };
    }
    const field = el.closest('.MuiOutlinedInput-root');
    const border = field?.querySelector('.MuiOutlinedInput-notchedOutline');
    if (field && border) {
      const b = getComputedStyle(border);
      const behind = background(field);
      return {
        element,
        kind: `border ${b.borderTopWidth}`,
        ratio: ratio(b.borderTopColor, behind),
        detail: `${b.borderTopColor} on ${behind}`,
      };
    }
    return { element, kind: 'none', ratio: 0, detail: `outline ${cs.outlineStyle}, no field border` };
  });
}

/**
 * Press a key through a surface and measure the focus indicator of every stop
 * (WCAG 1.4.11, 3:1 against what is behind it, and not cut off by a parent). The
 * sweep must get all the way round: back to the first element, or out of the
 * document. It fails when it reaches the cap first, or lands in a code editor that
 * the test did not name, so that it can never measure part of a page unseen.
 */
export async function focusRingSweep(page: Page, what: string, options: SweepOptions = {}): Promise<void> {
  await waitForTransitions(page);
  await flattenGradients(page);
  await page.evaluate(installBrowserHelpers);
  const cap = options.partial?.stops ?? options.maxStops ?? 60;
  const stops: FocusStop[] = [];
  let first: JSHandle | null = null;
  let finished = false;
  let editors = 0;
  let pressKey = true;
  for (let i = 0; i < cap; i++) {
    if (pressKey) await page.keyboard.press(options.key ?? 'Tab');
    pressKey = true;
    const inEditor = await page.evaluate(() => Boolean(document.activeElement?.closest('.monaco-editor')));
    if (inEditor) {
      expect(options.skipEditors, `${what}: the sweep landed in a code editor after ${stops.length} stops; name it with skipEditors`).toBeTruthy();
      editors++;
      // Monaco keeps the Tab key. Go on with the first control after the editor.
      const moved = await page.evaluate(() => {
        const editor = document.activeElement?.closest('.monaco-editor') as Element;
        const next = Array.from(document.querySelectorAll<HTMLElement>('a[href], button, input, select, textarea, [tabindex]')).find((el) =>
          // eslint-disable-next-line no-bitwise
          (editor.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
          && !el.closest('.monaco-editor') && el.tabIndex >= 0 && !(el as HTMLButtonElement).disabled && el.getClientRects().length > 0);
        next?.focus();
        return Boolean(next);
      });
      if (!moved) {
        finished = true;
        break;
      }
      pressKey = false;
      continue;
    }
    // Focus scrolls the element into view, which can take effect on a later frame.
    await page.evaluate(() => new Promise((done) => {
      requestAnimationFrame(() => requestAnimationFrame(done));
    }));
    const stop = await measureFocus(page);
    if (!stop) {
      // Out of the document, past the last control: the whole page has been seen.
      finished = true;
      break;
    }
    if (first && await first.evaluate((el) => el === document.activeElement)) {
      finished = true;
      break;
    }
    first ??= await page.evaluateHandle(() => document.activeElement);
    stops.push(stop);
  }
  if (options.partial) {
    test.info().annotations.push({ type: 'focus', description: `${what}: measured ${stops.length} stops on purpose: ${options.partial.reason}` });
  } else {
    expect(finished, `${what}: reached the cap of ${cap} stops without getting back to the first one; part of the surface was not measured`).toBe(true);
  }
  if (options.skipEditors) {
    expect(editors, `${what}: skipEditors is set, but no code editor came up`).toBeGreaterThan(0);
    test.info().annotations.push({ type: 'focus', description: `${what}: went past ${editors} code editor(s): ${options.skipEditors}` });
  }
  expect(stops.length, `${what}: the Tab key reached no element`).toBeGreaterThan(0);
  const bad = stops.filter((s) => s.ratio < AA_NON_TEXT);
  const lines = bad.map((s) => `  ${s.element}: ${s.kind}, ${s.ratio.toFixed(2)}:1 (${s.detail})`);
  expect(lines, `${what}: ${bad.length} of ${stops.length} Tab stops have no focus ring with ${AA_NON_TEXT}:1\n${lines.join('\n')}`).toEqual([]);
  // Whatever its ratio here: the same ring measured 3.13:1 on macOS and 1.02:1 on Linux.
  const browserRings = stops.filter((s) => s.browserDefault).map((s) => `${s.element}: ${s.ratio.toFixed(2)}:1 on this platform (${s.detail})`);
  expect(
    browserRings,
    `${what}: ${browserRings.length} of ${stops.length} stops rely on the browser's default ring, which differs by platform\n  ${browserRings.join('\n  ')}`,
  ).toEqual([]);
  const clipped = stops.filter((s) => s.clipped).map((s) => `${s.element}: ${s.clipped}`);
  expect(clipped, `${what}: a parent cuts off the focus ring of ${clipped.length} of ${stops.length} stops\n  ${clipped.join('\n  ')}`).toEqual([]);
  const lowest = stops.reduce((a, b) => (b.ratio < a.ratio ? b : a));
  test.info().annotations.push({
    type: 'focus',
    description: `${what}: ${stops.length} stops, lowest ${lowest.ratio.toFixed(2)}:1 at ${lowest.element} (${lowest.detail})`,
  });
}

/** What `expectTableCellsReachable` found on one page. */
export interface TableScrollStats {
  tables: number;
  /** Tables wider than their box: they scroll sideways. */
  scrolling: number;
  /** Controls that start (partly) outside the box of their table. */
  cutOff: number;
  /** Per table: whether it scrolls, and how many controls it has cut off. */
  perTable: { scrolls: boolean; cutOff: number }[];
}

/**
 * On a phone a wide table scrolls sideways. Focus each control that starts outside
 * the box of its table: it must scroll into view, and be fully visible then (#760).
 */
export async function expectTableCellsReachable(page: Page, what: string): Promise<TableScrollStats> {
  const result = await page.evaluate(async () => {
    const frame = () => new Promise((done) => { requestAnimationFrame(() => requestAnimationFrame(done)); });
    const stats: { tables: number; scrolling: number; cutOff: number; perTable: { scrolls: boolean; cutOff: number }[] } = { tables: 0, scrolling: 0, cutOff: 0, perTable: [] };
    const problems: string[] = [];
    // A table can grow after the first paint (fonts, late rows): a scroll that ran before that stops short.
    await document.fonts.ready;
    const widths = () => Array.from(document.querySelectorAll<HTMLElement>('.MuiTableContainer-root')).map((b) => b.scrollWidth).join();
    let stable = false;
    for (let seen = '', same = 0, tries = 0; tries < 120; tries++) {
      if (same >= 10) { stable = true; break; }
      await frame();
      const now = widths();
      same = now === seen ? same + 1 : 0;
      seen = now;
    }
    if (!stable) return { stats, problems: [], unstable: true };
    const outside = (r: DOMRect, c: DOMRect) => r.left < c.left - 0.5 || r.right > c.right + 0.5;
    for (const box of Array.from(document.querySelectorAll<HTMLElement>('.MuiTableContainer-root'))) {
      stats.tables++;
      const scrolls = box.scrollWidth > box.clientWidth;
      if (scrolls) stats.scrolling++;
      let own = 0;
      const controls = Array.from(box.querySelectorAll<HTMLElement>('td a[href], td button:not([disabled]), td input:not([disabled]), td [role=checkbox], th button:not([disabled]), th input:not([disabled])'));
      for (const el of controls) {
        // A control in a cell that a breakpoint hides has no box.
        if (el.getClientRects().length === 0 || !outside(el.getBoundingClientRect(), box.getBoundingClientRect())) continue;
        stats.cutOff++;
        own++;
        el.focus();
        await frame();
        const r = el.getBoundingClientRect();
        const c = box.getBoundingClientRect();
        const label = `${el.tagName.toLowerCase()} "${(el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 40)}"`;
        if (document.activeElement !== el) problems.push(`${label}: did not take the focus`);
        else if (outside(r, c)) problems.push(`${label}: ${Math.round(r.left)} to ${Math.round(r.right)} after focus, box ${Math.round(c.left)} to ${Math.round(c.right)} (scrolled ${Math.round(box.scrollLeft)} of ${box.scrollWidth - box.clientWidth})`);
        else if (r.left < 0 || r.right > window.innerWidth) problems.push(`${label}: off the screen after focus (${Math.round(r.left)} to ${Math.round(r.right)})`);
      }
      stats.perTable.push({ scrolls, cutOff: own });
    }
    return { stats, problems, unstable: false };
  });
  expect(result.unstable, `${what}: the table widths did not settle in 120 frames, so no scroll was measured`).toBe(false);
  expect(result.problems, `${what}: ${result.problems.length} control(s) in a cut-off cell are not fully visible after focus\n  ${result.problems.join('\n  ')}`).toEqual([]);
  const stuck = result.stats.perTable.flatMap((t, i) => (t.cutOff > 0 && !t.scrolls ? [`table ${i + 1}: ${t.cutOff} cut-off control(s), but it does not scroll`] : []));
  expect(stuck, `${what}: ${stuck.join('; ')}`).toEqual([]);
  test.info().annotations.push({ type: 'table', description: `${what}: ${JSON.stringify(result.stats)}` });
  return result.stats;
}

/**
 * Walk the controls in the body of a table with a sticky header, forward and back,
 * and check that none sits under the header: its top edge is below the header, and
 * the topmost element at its centre is the control itself (#760). The table must
 * scroll, or the check proves nothing.
 */
export async function expectFocusClearOfStickyHeader(page: Page, table: Locator, what: string): Promise<void> {
  const controls = table.locator('tbody').locator('a[href], button:not([disabled]), input:not([disabled]), select, [tabindex="0"]');
  const count = await controls.count();
  expect(count, `${what}: too few controls in the rows to scroll through`).toBeGreaterThan(10);
  const container = table.locator('xpath=ancestor::*[contains(@class,"MuiTableContainer-root")][1]');
  const covered: string[] = [];
  const measure = () => page.evaluate(() => new Promise<{ inTable: boolean; problem?: string }>((done) => {
    requestAnimationFrame(() => requestAnimationFrame(() => {
      const el = document.activeElement as HTMLElement | null;
      if (!el || !el.closest('tbody')) return done({ inTable: false });
      const r = el.getBoundingClientRect();
      const head = el.closest('table')?.querySelector('thead')?.getBoundingClientRect();
      const top = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
      const label = `${el.tagName.toLowerCase()} "${(el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 40)}"`;
      if (head && r.top < head.bottom - 0.5) return done({ inTable: true, problem: `${label}: top edge ${Math.round(r.top)}, header bottom ${Math.round(head.bottom)}` });
      if (top && el !== top && !el.contains(top)) {
        return done({ inTable: true, problem: `${label}: covered by ${top.tagName.toLowerCase()} at its centre` });
      }
      return done({ inTable: true });
    }));
  }));
  await controls.first().focus();
  // Forward: the first control is at the top, so scroll down to the last.
  for (let i = 0; i < count + 5; i++) {
    const stop = await measure();
    if (stop.problem) covered.push(stop.problem);
    if (await controls.last().evaluate((el) => el === document.activeElement)) break;
    await page.keyboard.press('Tab');
  }
  expect(await container.evaluate((el) => el.scrollTop), `${what}: Tab through the rows scrolled the table`).toBeGreaterThan(0);
  // Back: Shift+Tab puts a control at the top edge, where a sticky header covers it.
  for (let i = 0; i < count + 5; i++) {
    await page.keyboard.press('Shift+Tab');
    const stop = await measure();
    if (!stop.inTable) break;
    if (stop.problem) covered.push(stop.problem);
  }
  expect(covered, `${what}: ${covered.length} focused control(s) under the sticky header\n  ${covered.join('\n  ')}`).toEqual([]);
}

/** Ratio of a non-text colour (an icon, a bar) against the background behind it. */
export async function nonTextRatio(target: Locator, property: 'color' | 'backgroundColor', behind?: Locator): Promise<number> {
  await target.page().evaluate(installBrowserHelpers);
  const backdrop = behind ?? target.locator('xpath=..');
  const bg = await backdrop.evaluate((el) => window.a11y.background(el));
  return target.evaluate((el, { prop, under }) =>
    window.a11y.ratio(getComputedStyle(el)[prop], under), { prop: property, under: bg });
}

/**
 * A tooltip is translucent, so its contrast depends on what is under it, and axe
 * leaves it undecided. Measure its text over the two extremes: white and black.
 */
export async function tooltipRatio(tooltip: Locator): Promise<number> {
  await tooltip.page().evaluate(installBrowserHelpers);
  return tooltip.evaluate((el) => {
    const { ratio, over } = window.a11y;
    const cs = getComputedStyle(el);
    return Math.min(
      ...['rgb(255, 255, 255)', 'rgb(0, 0, 0)'].map((under) => ratio(cs.color, over(cs.backgroundColor, 1, under))),
    );
  });
}

/** The seed data of the accessibility specs. */
export interface Seed {
  token: string;
  definitionId: string;
  definition2Id: string;
  instanceId: string;
  instanceName: string;
  instance2Id: string;
  instance2Name: string;
  templateId: string;
  templateName: string;
  publishedTemplateId: string;
  publishedTemplateName: string;
}

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

export async function createSeed(request: APIRequestContext): Promise<Seed> {
  const token = await apiLogin(request);
  const headers = auth(token);
  const createInstance = async (definitionId: string, name: string): Promise<string> => {
    const res = await request.post(`${API_BASE}/api/v1/stack-instances`, {
      headers,
      data: { stack_definition_id: definitionId, name, branch: 'main' },
    });
    expect(res.ok(), await res.text()).toBe(true);
    return (await res.json()).id;
  };
  const createTemplate = async (name: string, category: string): Promise<string> => {
    const res = await request.post(`${API_BASE}/api/v1/templates`, {
      headers,
      data: { name, description: 'Template for the accessibility check', category, version: '1.0.0', default_branch: 'main' },
    });
    expect(res.ok(), await res.text()).toBe(true);
    return (await res.json()).id;
  };

  const definitionId = await apiCreateDefinition(request, token, uniqueName('a11y-def'));
  await apiAddChart(request, token, definitionId, { chart_name: 'web', default_values: 'replicaCount: 1\n' });
  // A second definition with other values, so that Compare shows a difference.
  const definition2Id = await apiCreateDefinition(request, token, uniqueName('a11y-def2'));
  await apiAddChart(request, token, definition2Id, { chart_name: 'web', default_values: 'replicaCount: 3\n' });
  const instanceName = uniqueName('a11y');
  const instanceId = await createInstance(definitionId, instanceName);
  const instance2Name = uniqueName('a11y2');
  const instance2Id = await createInstance(definition2Id, instance2Name);

  // A draft template: the gallery shows a real warning "Draft" chip.
  const templateName = uniqueName('a11y-tpl');
  const templateId = await createTemplate(templateName, 'a11y');
  // A published template with a chart: the only kind that can be used.
  const publishedTemplateName = uniqueName('a11y-pub');
  const publishedTemplateId = await createTemplate(publishedTemplateName, 'Web');
  const chart = await request.post(`${API_BASE}/api/v1/templates/${publishedTemplateId}/charts`, {
    headers,
    // With values: the preview page shows them in boxes of their own.
    data: {
      chart_name: 'web',
      repository_url: 'https://charts.example.com',
      chart_version: '1.0.0',
      default_values: 'replicaCount: 1\n',
      locked_values: 'image:\n  pullPolicy: Always\n',
    },
  });
  expect(chart.ok(), await chart.text()).toBe(true);
  const published = await request.post(`${API_BASE}/api/v1/templates/${publishedTemplateId}/publish`, { headers });
  expect(published.ok(), await published.text()).toBe(true);

  return {
    token, definitionId, definition2Id, instanceId, instanceName, instance2Id, instance2Name,
    templateId, templateName, publishedTemplateId, publishedTemplateName,
  };
}

/** Best effort: a failed cleanup must not hide a test result. */
export async function deleteSeed(request: APIRequestContext, seed: Partial<Seed>): Promise<void> {
  if (!seed.token) return;
  const headers = auth(seed.token);
  for (const url of [
    seed.instanceId && `/api/v1/stack-instances/${seed.instanceId}`,
    seed.instance2Id && `/api/v1/stack-instances/${seed.instance2Id}`,
    seed.templateId && `/api/v1/templates/${seed.templateId}`,
    seed.publishedTemplateId && `/api/v1/templates/${seed.publishedTemplateId}`,
    seed.definitionId && `/api/v1/stack-definitions/${seed.definitionId}`,
    seed.definition2Id && `/api/v1/stack-definitions/${seed.definition2Id}`,
  ]) {
    if (url) await request.delete(`${API_BASE}${url}`, { headers }).catch(() => undefined);
  }
}

/** Answer the instance with this status, for as long as the page lives. */
export const forceInstanceFields = (page: Page, instanceId: string, fields: Partial<StackInstance>) =>
  page.route(new RegExp(`/api/v1/stack-instances/${instanceId}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    return route.fulfill({ response: res, json: { ...(await res.json()), ...fields } });
  });

/** All pages, with their setup. `path` needs the seed, so this is a function. */
export const pageList = (seed: Seed): { name: string; spec: PageSetup }[] => [
  { name: 'login', spec: { path: '/login', heading: 'Sign In', sidebar: false } },
  {
    name: 'dashboard',
    spec: {
      path: '/',
      heading: 'Stack Instances',
      // The page reads the status of each running instance, which needs a
      // cluster. Other specs set rows to running on the same backend.
      allowed5xx: [/^GET \/api\/v1\/stack-instances\/[^/]+\/status$/],
    },
  },
  { name: 'templates', spec: { path: '/templates', heading: 'Template Gallery' } },
  { name: 'template builder', spec: { path: '/templates/new', heading: 'Create Template' } },
  { name: 'template preview, draft', spec: { path: `/templates/${seed.templateId}`, heading: seed.templateName } },
  { name: 'template preview, published', spec: { path: `/templates/${seed.publishedTemplateId}`, heading: seed.publishedTemplateName } },
  {
    name: 'template instantiate',
    spec: { path: `/templates/${seed.publishedTemplateId}/use`, heading: `Use Template: ${seed.publishedTemplateName}` },
  },
  { name: 'stack definitions', spec: { path: '/stack-definitions', heading: 'Stack Definitions' } },
  { name: 'new stack definition', spec: { path: '/stack-definitions/new', heading: 'Create Stack Definition' } },
  {
    name: 'edit stack definition',
    spec: { path: `/stack-definitions/${seed.definitionId}/edit`, heading: 'Edit Stack Definition' },
  },
  { name: 'new stack instance', spec: { path: '/stack-instances/new', heading: 'Create Stack Instance' } },
  { name: 'stack instance', spec: { path: `/stack-instances/${seed.instanceId}`, heading: seed.instanceName } },
  {
    name: 'compare, with a difference',
    spec: {
      path: `/stack-instances/compare?left=${seed.instanceId}&right=${seed.instance2Id}`,
      heading: 'Compare Stack Instances',
    },
  },
  { name: 'compare, empty', spec: { path: '/stack-instances/compare', heading: 'Compare Stack Instances' } },
  { name: 'audit log', spec: { path: '/audit-log', heading: 'Audit Log' } },
  { name: 'users', spec: { path: '/admin/users', heading: 'User Management' } },
  {
    name: 'orphaned namespaces',
    spec: {
      path: '/admin/orphaned-namespaces',
      heading: 'Orphaned Namespaces',
      // The scan lists the namespaces of the cluster: 503 without cluster access.
      allowed5xx: [/^GET \/api\/v1\/admin\/orphaned-namespaces$/],
    },
  },
  { name: 'clusters', spec: { path: '/admin/clusters', heading: 'Cluster Management' } },
  {
    name: 'cluster health',
    spec: {
      path: '/admin/cluster-health',
      heading: 'Cluster Health',
      // The e2e backend has no cluster access: the live health (summary,
      // nodes), namespace and utilization reads of the default cluster answer 500.
      allowed5xx: [/^GET \/api\/v1\/clusters\/[^/]+\/(health\/(summary|nodes)|namespaces|utilization)$/],
    },
  },
  { name: 'analytics', spec: { path: '/admin/analytics', heading: 'Analytics' } },
  { name: 'shared values', spec: { path: '/admin/shared-values', heading: 'Shared Values' } },
  { name: 'cleanup policies', spec: { path: '/admin/cleanup-policies', heading: 'Cleanup Policies' } },
  { name: 'notification channels', spec: { path: '/admin/notification-channels', heading: 'Notification Channels' } },
  { name: 'profile', spec: { path: '/profile', heading: 'My Profile' } },
  { name: 'notifications', spec: { path: '/notifications', heading: 'Notifications' } },
  { name: 'not found', spec: { path: '/does-not-exist', heading: '404' } },
];

export { uniqueName };
