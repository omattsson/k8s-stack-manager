import { test, expect } from '@playwright/test';
import type { APIRequestContext, Browser, BrowserContext, Page } from '@playwright/test';
import { API_BASE, apiLogin, uniqueName } from './helpers';

/**
 * Session lifecycle (#584): refresh, cross-tab single flight, refresh token
 * reuse, idle timeout, maximum lifetime, logout and logout-all.
 *
 * The spec needs a backend with short session limits. `make test-e2e-session`
 * (and the last phase of `make test-e2e`) starts one and runs the `session`
 * project with E2E_SESSION=1 and the limits below (same values as the backend).
 *
 * Notes on the setup:
 * - The tests log in through the API (`apiLogin` with `context.request`). The
 *   request context of a browser context shares its cookie jar, so the httpOnly
 *   refresh cookie (path /api/v1/auth) is in the browser. The access token is
 *   written to localStorage once (see openApp). The loginAs* helpers are not
 *   used: their init script writes the login token again on every page load,
 *   also on the full-page redirect to /login, which would bring back a stale
 *   session in the middle of these tests.
 * - With ACCESS_TOKEN_EXPIRATION below 30s, the app WebSocket (useWebSocket)
 *   refreshes the token before each connect, and the server closes the socket
 *   with 1008 when its token expires. So the app refreshes about once per
 *   access-token lifetime on its own. Tests that need a fixed timeline say how
 *   they handle that.
 */

// ---------------------------------------------------------------------------
// Limits

/** Parse a Go-style duration ("10s", "1m", "1m30s", "500ms") to milliseconds. */
function parseDurationMs(value: string | undefined, fallbackMs: number): number {
  const text = value?.trim();
  if (!text || !/^(\d+(\.\d+)?(ms|h|m|s))+$/.test(text)) return fallbackMs;
  const unitMs: Record<string, number> = { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 };
  let total = 0;
  for (const match of text.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)) {
    total += parseFloat(match[1]) * unitMs[match[2]];
  }
  return total > 0 ? total : fallbackMs;
}

const ACCESS_TTL_MS = parseDurationMs(process.env.E2E_SESSION_ACCESS_TTL, 10_000);
const IDLE_TIMEOUT_MS = parseDurationMs(process.env.E2E_SESSION_IDLE_TIMEOUT, 20_000);
const MAX_LIFETIME_MS = parseDurationMs(process.env.E2E_SESSION_MAX_LIFETIME, 60_000);
const REUSE_GRACE_MS = parseDurationMs(process.env.E2E_SESSION_REUSE_GRACE, 3_000);

/**
 * Session activity write interval of the backend: min(1m, idle/4), at least 1s
 * (middleware.SessionActivityInterval). A recorded activity lags the last
 * request by at most this time.
 */
const ACTIVITY_INTERVAL_MS = Math.min(60_000, Math.max(1_000, IDLE_TIMEOUT_MS / 4));

/** The refresh cookie Max-Age is whole seconds (rounded down), so it can expire up to 1s early. */
const COOKIE_EARLY_MS = 1_000;

const REFRESH_URL = `${API_BASE}/api/v1/auth/refresh`;
const WS_BASE = API_BASE.replace(/^http/, 'ws');
const REFRESH_COOKIE = 'refresh_token';
const TEST_PASSWORD = 'e2e-session-password';

// ---------------------------------------------------------------------------
// Helpers

/** Decoded claims that the tests use. */
interface Claims {
  exp: number;
  jti?: string;
}

function decodeClaims(token: string): Claims {
  return JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString('utf8')) as Claims;
}

/** Milliseconds until the token expires (negative when expired). */
function remainingMs(token: string): number {
  return decodeClaims(token).exp * 1000 - Date.now();
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, Math.max(0, ms)));
}

function isApi(url: string): boolean {
  return url.startsWith(`${API_BASE}/api/v1/`);
}

function isRefresh(url: string): boolean {
  return url.split('?')[0] === REFRESH_URL;
}

function isLoginPage(page: Page): boolean {
  return new URL(page.url()).pathname === '/login';
}

/** One POST /auth/refresh answer seen in the browser or sent by the test. */
interface RefreshRecord {
  /** Time of the answer. */
  at: number;
  /** Time the browser sent the request (the answer time for test refreshes). */
  startedAt: number;
  status: number;
  source: string;
  /** `error` field of a non-200 answer (filled asynchronously; may stay unset after a page unload). */
  error?: string;
  /** New refresh_token value of a rotation (Set-Cookie of a 200 answer; a grace answer has none). */
  cookie?: string;
}

/** One answer of another /api/v1 endpoint seen in the browser. */
interface ApiRecord {
  at: number;
  url: string;
  status: number;
  page: Page | null;
}

/** API traffic of a browser context, observed without changing it. */
interface Traffic {
  /** Start times of the POST /auth/refresh requests that the browser sent. */
  refreshRequests: number[];
  refreshes: RefreshRecord[];
  api: ApiRecord[];
  /** Pending reads of answer bodies and headers; await settled() before reading `error`. */
  pending: Promise<unknown>[];
}

/** Wait until the body and header reads of the recorded answers are done. */
async function settled(traffic: Traffic): Promise<void> {
  await Promise.allSettled(traffic.pending);
}

/** refresh_token value set by the newest rotation that the browser saw (call settled() first). */
function latestRotatedCookie(traffic: Traffic): string | undefined {
  return [...traffic.refreshes].reverse().find((r) => r.cookie)?.cookie;
}

/**
 * Record the POST /auth/refresh requests and answers and the other /api/v1
 * answers of all pages of a context. CORS preflights (OPTIONS) are skipped.
 */
function recordTraffic(context: BrowserContext, source: string): Traffic {
  const traffic: Traffic = { refreshRequests: [], refreshes: [], api: [], pending: [] };
  const started = new WeakMap<object, number>();
  context.on('request', (request) => {
    if (request.method() === 'POST' && isRefresh(request.url())) {
      const at = Date.now();
      started.set(request, at);
      traffic.refreshRequests.push(at);
    }
  });
  context.on('response', (response) => {
    const at = Date.now();
    const request = response.request();
    const url = response.url();
    if (request.method() === 'OPTIONS' || !isApi(url)) return;
    if (isRefresh(url)) {
      const record: RefreshRecord = { at, startedAt: started.get(request) ?? at, status: response.status(), source };
      traffic.refreshes.push(record);
      if (record.status !== 200) {
        traffic.pending.push(
          response
            .json()
            .then((body: { error?: string }) => {
              record.error = body?.error;
            })
            .catch(() => {}),
        );
      } else {
        // A rotation sets the new cookie; a grace answer sets none.
        traffic.pending.push(
          response
            .headerValue('set-cookie')
            .then((header) => {
              const match = header?.match(new RegExp(`(?:^|\\n|, )${REFRESH_COOKIE}=([^;]+)`));
              if (match && match[1]) record.cookie = match[1];
            })
            .catch(() => {}),
        );
      }
      return;
    }
    traffic.api.push({ at, url, status: response.status(), page: request.frame()?.page() ?? null });
  });
  return traffic;
}

/** Refresh through an API request context (the cookie jar of a browser context, or a Cookie header). */
async function refreshVia(
  request: APIRequestContext,
  headers?: Record<string, string>,
): Promise<RefreshRecord & { token?: string }> {
  const res = await request.post(REFRESH_URL, { headers });
  const at = Date.now();
  const body = (await res.json().catch(() => ({}))) as { token?: string; error?: string };
  return { at, startedAt: at, status: res.status(), source: 'test', token: body.token, error: body.error };
}

/**
 * Value of the refresh cookie in the context. Read all cookies: a URL filter
 * such as `context.cookies(API_BASE)` also matches the cookie path, and the
 * refresh cookie has the path /api/v1/auth, so it would never be returned.
 */
async function refreshCookie(context: BrowserContext): Promise<string | undefined> {
  const apiHost = new URL(API_BASE).hostname;
  const cookies = await context.cookies();
  return cookies.find((c) => c.name === REFRESH_COOKIE && c.domain.replace(/^\./, '') === apiHost)?.value;
}

async function storedToken(page: Page): Promise<string | null> {
  return page.evaluate(() => localStorage.getItem('token'));
}

/**
 * Open the stack definitions page (no background polling of its own; only the
 * NotificationCenter in the app bar polls, every 30s). With `token`, the token
 * is written to localStorage first (first tab of a context). Without it, the
 * page uses the token that the other tabs of the context share.
 */
async function openApp(context: BrowserContext, token?: string): Promise<Page> {
  const page = await context.newPage();
  if (token) {
    await page.goto('/login');
    await page.evaluate((t) => localStorage.setItem('token', t), token);
  }
  await page.goto('/stack-definitions');
  await expect(page.getByRole('heading', { level: 1, name: 'Stack Definitions' })).toBeVisible({
    timeout: 10_000,
  });
  return page;
}

/**
 * Make the app send authenticated requests: a client-side navigation between
 * Definitions and Templates (each page loads its list).
 */
async function navigateInApp(page: Page): Promise<void> {
  const target = new URL(page.url()).pathname.startsWith('/templates') ? 'Definitions' : 'Templates';
  await page
    .getByRole('navigation', { name: 'Main navigation' })
    .getByRole('link', { name: target, exact: true })
    .click({ timeout: 5_000 });
}

/**
 * Send requests from the tab until it is on the login page. A revoked session
 * still has a valid access token (up to ACCESS_TTL_MS); the tab goes to the
 * login page at the first request that needs a refresh.
 */
async function expectTabLandsOnLogin(page: Page, timeout: number): Promise<void> {
  await expect(async () => {
    if (!isLoginPage(page)) {
      await navigateInApp(page).catch(() => {});
    }
    await expect(page).toHaveURL(/\/login(\?|$)/, { timeout: 2_000 });
  }).toPass({ timeout, intervals: [500] });
  await expect(page.getByRole('heading', { level: 1, name: 'Sign In' })).toBeVisible({ timeout: 10_000 });
}

/** Wait until the token in localStorage is valid for at least `minMs` (the app refreshes it on its own). */
async function waitForFreshToken(page: Page, minMs: number): Promise<string> {
  let token = '';
  await expect
    .poll(
      async () => {
        token = (await storedToken(page)) ?? '';
        return token ? remainingMs(token) : -1;
      },
      { timeout: ACCESS_TTL_MS + 15_000, intervals: [250] },
    )
    .toBeGreaterThanOrEqual(minMs);
  return token;
}

/** Open, close or error event of a WebSocket that a page created. */
interface WsEvent {
  socket: string;
  /** Socket URL without the query (the token). */
  url: string;
  type: 'open' | 'close';
  at: number;
  code?: number;
  reason?: string;
}

/**
 * Record the open and close events (with the close code) of every WebSocket
 * that the pages of a context create, including the app socket. Playwright's
 * WebSocket `close` event has no close code, so an init script wraps the
 * WebSocket constructor; the sockets still talk to the real server.
 */
async function recordWebSockets(context: BrowserContext): Promise<WsEvent[]> {
  const events: WsEvent[] = [];
  await context.exposeFunction('__e2eWsEvent', (event: Omit<WsEvent, 'at'>) => {
    events.push({ ...event, at: Date.now() });
  });
  await context.addInitScript(() => {
    type Report = (event: { socket: string; url: string; type: string; code?: number; reason?: string }) => void;
    const report: Report = (event) => {
      const fn = (globalThis as unknown as { __e2eWsEvent?: Report }).__e2eWsEvent;
      if (fn) void fn(event);
    };
    const Native = globalThis.WebSocket;
    const prefix = Math.random().toString(36).slice(2, 8);
    let seq = 0;
    class TrackedWebSocket extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        seq += 1;
        const socket = `${prefix}-${seq}`;
        const plain = String(url).split('?')[0];
        this.addEventListener('open', () => report({ socket, url: plain, type: 'open' }));
        this.addEventListener('close', (e) =>
          report({ socket, url: plain, type: 'close', code: e.code, reason: e.reason }),
        );
      }
    }
    (globalThis as unknown as { WebSocket: typeof WebSocket }).WebSocket = TrackedWebSocket;
  });
  return events;
}

/** App sockets (`${WS_BASE}/ws`) of the events. Skips other sockets, such as the Vite HMR socket. */
function appSocketEvents(events: WsEvent[]): WsEvent[] {
  return events.filter((e) => e.url === `${WS_BASE}/ws`);
}

/** App sockets with an open event and no close event. */
function openSockets(events: WsEvent[]): string[] {
  const app = appSocketEvents(events);
  const closed = new Set(app.filter((e) => e.type === 'close').map((e) => e.socket));
  return app.filter((e) => e.type === 'open' && !closed.has(e.socket)).map((e) => e.socket);
}

/** Create a user with role "user" through the admin API. */
async function createUser(request: APIRequestContext): Promise<string> {
  const adminToken = await apiLogin(request);
  const username = uniqueName('e2e-session');
  const res = await request.post(`${API_BASE}/api/v1/auth/register`, {
    headers: { Authorization: `Bearer ${adminToken}` },
    data: { username, password: TEST_PASSWORD, role: 'user' },
  });
  expect(res.ok(), `Register ${username} failed with status ${res.status()}`).toBe(true);
  return username;
}

async function newContext(browser: Browser): Promise<BrowserContext> {
  return browser.newContext({ baseURL: test.info().project.use.baseURL });
}

function seconds(ms: number): string {
  return `${(ms / 1000).toFixed(1)}s`;
}

// ---------------------------------------------------------------------------
// Tests

test.describe('Session lifecycle', () => {
  test.skip(
    !process.env.E2E_SESSION,
    'Needs a backend with short session limits: run `make test-e2e-session` (sets E2E_SESSION=1)',
  );

  test('1: an expired access token is refreshed once for two tabs, and the requests succeed', async ({
    context,
  }) => {
    // The app socket refreshes the token on each (re)connect, so with a real
    // socket the stored token is replaced at random times. Answer the socket in
    // the test (it stays open and never closes) so that the only refresh in the
    // measured window is the one that the 401s cause. HTTP is not mocked.
    await context.routeWebSocket(/\/ws(\?|$)/, () => {});

    const traffic = recordTraffic(context, 'browser');
    const token = await apiLogin(context.request);
    const tabA = await openApp(context, token);
    const tabB = await openApp(context);

    // Wait until the shared access token is expired (no refresh runs meanwhile).
    await expect
      .poll(async () => remainingMs((await storedToken(tabA)) ?? token), {
        timeout: ACCESS_TTL_MS + 10_000,
        intervals: [250],
      })
      .toBeLessThan(0);

    // Hold the refresh until both tabs got their 401 (at most 5s). The stored
    // token changes only when the refresh answers, so both tabs send their
    // request with the expired token, and the test sees the cross-tab case
    // (Web Lock + adopt the stored token) every time.
    let triggerAt = Number.POSITIVE_INFINITY;
    const got401 = (tab: Page) => traffic.api.some((r) => r.page === tab && r.status === 401 && r.at >= triggerAt);
    await context.route(
      (url) => isRefresh(url.toString()),
      async (route) => {
        const holdUntil = Date.now() + 5_000;
        while (!(got401(tabA) && got401(tabB)) && Date.now() < holdUntil) {
          await sleep(50);
        }
        await route.continue();
      },
    );

    const cookieBefore = await refreshCookie(context);
    triggerAt = Date.now();
    await Promise.all([navigateInApp(tabA), navigateInApp(tabB)]);

    for (const tab of [tabA, tabB]) {
      await expect(tab).toHaveURL(/\/templates$/);
      await expect(tab.getByRole('heading', { level: 1, name: 'Template Gallery' })).toBeVisible({ timeout: 10_000 });
    }
    // Let a late or second refresh show up before counting.
    await sleep(1_500);

    const refreshRequests = traffic.refreshRequests.filter((at) => at >= triggerAt);
    expect(refreshRequests, 'two tabs with an expired token must send exactly one refresh').toHaveLength(1);
    const refreshes = traffic.refreshes.filter((r) => r.at >= triggerAt);
    expect(refreshes.map((r) => r.status)).toEqual([200]);

    // Each tab: a request got 401 and the same request was sent again and got 200.
    for (const [name, tab] of [['A', tabA], ['B', tabB]] as const) {
      const seen = traffic.api.filter((r) => r.page === tab && r.at >= triggerAt);
      const rejected = seen.find((r) => r.status === 401);
      expect(rejected, `tab ${name}: no 401 for the request with the expired token`).toBeDefined();
      const retried = seen.find((r) => r.url === rejected!.url && r.status === 200 && r.at >= rejected!.at);
      expect(retried, `tab ${name}: ${rejected!.url} was not retried with success`).toBeDefined();
      expect(isLoginPage(tab)).toBe(false);
    }

    // One rotation: the cookie changed, and the shared token is valid again.
    const cookieAfter = await refreshCookie(context);
    expect(cookieAfter).toBeTruthy();
    expect(cookieAfter).not.toBe(cookieBefore);
    expect(remainingMs((await storedToken(tabB)) ?? '')).toBeGreaterThan(0);
  });

  test('2: reuse of a rotated refresh token after the grace window revokes the session in every tab', async ({
    context,
    request,
  }) => {
    const traffic = recordTraffic(context, 'browser');
    await apiLogin(context.request);
    const oldCookie = await refreshCookie(context);
    expect(oldCookie, 'login did not set the refresh cookie').toBeTruthy();

    // Rotate the login token now, so the time of its rotation is known.
    const rotation = await refreshVia(context.request);
    expect(rotation.status).toBe(200);
    expect(await refreshCookie(context)).not.toBe(oldCookie);

    const tabA = await openApp(context, rotation.token);
    const tabB = await openApp(context);

    // Past the reuse grace of the rotation (a fixed wait of grace + 1s).
    await sleep(rotation.at + REUSE_GRACE_MS + 1_000 - Date.now());

    // The newest refresh token of the session, before the replay and before
    // the tabs use it again.
    const current = await refreshCookie(context);
    expect(current, 'no refresh cookie in the browser').toBeTruthy();
    expect(current).not.toBe(oldCookie);

    // Replay the rotated token from another client (no cookie jar).
    const replay = await refreshVia(request, { Cookie: `${REFRESH_COOKIE}=${oldCookie}` });
    expect(replay.status, 'a replayed refresh token after the grace window must be rejected').toBe(401);
    expect(replay.error).toBe('Invalid refresh token');
    const replayAt = replay.at;

    // Server-side proof of the family revocation: the newest token, which
    // worked before the replay, is now rejected as revoked. (If a tab rotated
    // it in between, it is a rotated token of a family without an active
    // token: same answer. Without the revocation it would get 200.)
    const newest = await refreshVia(request, { Cookie: `${REFRESH_COOKIE}=${current}` });
    expect(newest.status, 'the newest token of the session after the replay').toBe(401);
    expect(newest.error).toBe('Invalid refresh token');

    // The family is revoked: the access tokens stay valid until they expire,
    // and the next request that needs a refresh sends each tab to the login page.
    const deadline = ACCESS_TTL_MS + 15_000;
    await expectTabLandsOnLogin(tabA, deadline);
    await expectTabLandsOnLogin(tabB, deadline);

    // No refresh succeeded after the replay, and the tabs did try one. The
    // first one sends the revoked cookie ("Invalid refresh token"); that answer
    // clears the cookie, so later ones send none ("Refresh token required").
    await settled(traffic);
    const after = traffic.refreshes.filter((r) => r.startedAt > replayAt);
    expect(after.length, 'the tabs never tried a refresh after the replay').toBeGreaterThan(0);
    expect(after.filter((r) => r.status === 200)).toEqual([]);
    for (const r of after) {
      expect(r.status).toBe(401);
      if (r.error !== undefined) {
        expect(['Invalid refresh token', 'Refresh token required']).toContain(r.error);
      }
    }
  });

  test('3: the session ends after the idle timeout, and a refresh is not activity', async ({ context }) => {
    // Idle window without any request from the app: the NotificationCenter
    // polls every 30s, and the app socket refreshes on each reconnect, so
    // background timers would decide the timeline. Block every /api/v1 request
    // of the browser during the window (blocked requests never reach the
    // server, so they are no activity). The test sends the refreshes of the
    // window itself, through the cookie jar of the browser context.
    const traffic = recordTraffic(context, 'browser');
    const token = await apiLogin(context.request);
    const page = await openApp(context, token);

    const apiMatcher = (url: URL) => isApi(url.toString());
    await context.route(apiMatcher, (route) => route.abort('blockedbyclient'));
    await sleep(250);
    // Last answer of any /api/v1 request: the last possible activity.
    const lastActivity = Math.max(...traffic.api.map((r) => r.at), ...traffic.refreshes.map((r) => r.at));
    const apiSeen = traffic.api.length + traffic.refreshes.length;

    // A refresh in the middle of the idle window succeeds (the session is alive).
    await sleep(lastActivity + IDLE_TIMEOUT_MS / 2 - Date.now());
    const midRefresh = await refreshVia(context.request);
    expect(midRefresh.status, `refresh ${seconds(midRefresh.at - lastActivity)} after the last request`).toBe(200);

    // Just past the idle deadline of the last request, the refresh fails. If
    // the refresh above counted as activity, the session would live until
    // midRefresh.at + IDLE_TIMEOUT_MS.
    await sleep(lastActivity + IDLE_TIMEOUT_MS + 1_500 - Date.now());
    const lateRefresh = await refreshVia(context.request);
    expect(lateRefresh.at).toBeLessThan(midRefresh.at + IDLE_TIMEOUT_MS);
    expect(lateRefresh.status, `refresh ${seconds(lateRefresh.at - lastActivity)} after the last request`).toBe(401);
    expect(lateRefresh.error).toBe('Session idle timeout exceeded');
    expect(traffic.api.length + traffic.refreshes.length, 'a browser request reached the server in the window').toBe(
      apiSeen,
    );

    // The user sees the login page at the next request.
    await context.unroute(apiMatcher);
    await expectTabLandsOnLogin(page, 15_000);
    expect(traffic.refreshes.filter((r) => r.at > lateRefresh.at && r.status === 200)).toEqual([]);
    test.info().annotations.push({
      type: 'timing',
      description: `idle: refresh ok at +${seconds(midRefresh.at - lastActivity)}, rejected at +${seconds(lateRefresh.at - lastActivity)}`,
    });
  });

  test('4: rotation never extends the session beyond the maximum lifetime', async ({ context, request }) => {
    test.setTimeout(MAX_LIFETIME_MS + ACCESS_TTL_MS + 60_000);
    const traffic = recordTraffic(context, 'browser');
    const beforeLogin = Date.now();
    const token = await apiLogin(context.request);
    const loginAt = Date.now();
    const page = await openApp(context, token);

    // Keep the session active: a request every few seconds (well inside the
    // idle timeout). Expired access tokens get refreshed (rotation) on the way.
    const pace = Math.min(3_000, ACTIVITY_INTERVAL_MS);
    const giveUpAt = loginAt + MAX_LIFETIME_MS + ACCESS_TTL_MS + 15_000;
    // Newest refresh cookie in the jar. The browser drops it at the session
    // deadline (its Max-Age is capped there), so keep the last value seen.
    let polledCookie: string | undefined;
    while (!isLoginPage(page) && Date.now() < giveUpAt) {
      polledCookie = (await refreshCookie(context)) ?? polledCookie;
      await navigateInApp(page).catch(() => {});
      await page.waitForURL(/\/login(\?|$)/, { timeout: pace }).catch(() => {});
    }
    const endedAt = Date.now();
    await expect(page).toHaveURL(/\/login(\?|$)/);
    await expect(page.getByRole('heading', { level: 1, name: 'Sign In' })).toBeVisible({ timeout: 10_000 });

    await settled(traffic);
    const ok = traffic.refreshes.filter((r) => r.status === 200);
    const failed = traffic.refreshes.filter((r) => r.status !== 200);
    const lastOk = ok[ok.length - 1];
    test.info().annotations.push({
      type: 'timing',
      description:
        `max lifetime: ${ok.length} rotations, last at +${seconds((lastOk?.at ?? loginAt) - loginAt)}, ` +
        `first rejection at +${seconds((failed[0]?.at ?? endedAt) - loginAt)}, login page at +${seconds(endedAt - loginAt)}`,
    });

    // Rotations happened, also late in the session.
    expect(ok.length, 'the session was never rotated').toBeGreaterThanOrEqual(3);
    expect(lastOk.at).toBeGreaterThanOrEqual(loginAt + MAX_LIFETIME_MS - ACCESS_TTL_MS - 8_000);

    // The first rejected refresh comes at the maximum lifetime, not earlier
    // (the activity kept the idle timeout away) and not later.
    expect(failed.length, 'no refresh was rejected').toBeGreaterThan(0);
    expect(failed[0].status).toBe(401);
    expect(failed[0].at).toBeGreaterThanOrEqual(beforeLogin + MAX_LIFETIME_MS - COOKIE_EARLY_MS - 500);
    expect(failed.map((r) => r.error)).not.toContain('Session idle timeout exceeded');
    expect(ok.filter((r) => r.at > failed[0].at), 'a refresh succeeded after the rejection').toEqual([]);
    // The browser has no cookie after the deadline ("Refresh token required").
    // A request sent just before the cookie expired can still reach the server
    // after the deadline ("Refresh token expired", see below).
    if (failed[0].error !== undefined) {
      expect(['Refresh token required', 'Refresh token expired']).toContain(failed[0].error);
    }

    // Server side: the newest refresh token (the last rotation before the
    // deadline) is rejected after the deadline. The rotation capped its expiry
    // at the session deadline (createRefreshToken), so the Refresh handler
    // stops at its token-expiry check, before the separate "Session expired"
    // check. If rotation extended the session, this token would still work.
    const newest = latestRotatedCookie(traffic) ?? polledCookie;
    expect(newest, 'no refresh cookie seen during the session').toBeTruthy();
    expect(Date.now()).toBeGreaterThan(loginAt + MAX_LIFETIME_MS);
    const replay = await refreshVia(request, { Cookie: `${REFRESH_COOKIE}=${newest}` });
    expect(replay.status, 'the newest refresh token after the maximum lifetime').toBe(401);
    expect(replay.error).toBe('Refresh token expired');

    // The user is sent to the login page at the end of the session: the last
    // access token is valid for up to ACCESS_TTL_MS after the deadline, then the
    // next request (at most `pace` later) gets 401 and the refresh fails.
    expect(endedAt).toBeGreaterThanOrEqual(beforeLogin + MAX_LIFETIME_MS - COOKIE_EARLY_MS - 500);
    expect(endedAt).toBeLessThanOrEqual(loginAt + MAX_LIFETIME_MS + ACCESS_TTL_MS + pace + 5_000);
  });

  test('5: logout blocks the access token and closes its WebSocket with 1008', async ({ context, request }) => {
    const token = await apiLogin(context.request);
    const page = await openApp(context, token);

    // The app closes its own socket before it calls POST /auth/logout (client
    // close, no server code), so the test opens a second socket with the same
    // token and watches the close code that the server sends.
    let closed: { code: number; reason: string } | null = null;
    await page.exposeFunction('__e2eRawWsClosed', (code: number, reason: string) => {
      closed = { code, reason };
    });

    // A token with time left, so the app does not refresh it before the logout
    // and the 401 below cannot come from the expiry.
    const current = await waitForFreshToken(page, Math.min(6_000, ACCESS_TTL_MS / 2));
    const me = await request.get(`${API_BASE}/api/v1/auth/me`, { headers: { Authorization: `Bearer ${current}` } });
    expect(me.status(), 'the token works before the logout').toBe(200);

    await page.evaluate(
      (url) =>
        new Promise<void>((resolve, reject) => {
          const ws = new WebSocket(url);
          ws.addEventListener('open', () => resolve());
          ws.addEventListener('error', () => reject(new Error('the WebSocket did not open')));
          ws.addEventListener('close', (e) => {
            void (globalThis as unknown as { __e2eRawWsClosed: (c: number, r: string) => Promise<void> })
              .__e2eRawWsClosed(e.code, e.reason);
          });
          (globalThis as unknown as { __e2eRawWs: WebSocket }).__e2eRawWs = ws;
        }),
      `${WS_BASE}/ws?token=${encodeURIComponent(current)}`,
    );

    // The refresh token of this session. The logout answer clears the cookie,
    // so the jar cannot show afterwards that the server revoked it.
    const refreshToken = await refreshCookie(context);
    expect(refreshToken, 'no refresh cookie before the logout').toBeTruthy();

    const logoutRequest = page.waitForRequest(
      (r) => r.method() === 'POST' && r.url() === `${API_BASE}/api/v1/auth/logout`,
    );
    const logoutResponse = page.waitForResponse(
      (r) => r.request().method() === 'POST' && r.url() === `${API_BASE}/api/v1/auth/logout`,
    );
    await page.getByRole('button', { name: 'Logout' }).click();
    expect((await logoutRequest).headers()['authorization']).toBe(`Bearer ${current}`);
    expect((await logoutResponse).status()).toBe(200);
    await expect(page).toHaveURL(/\/login(\?|$)/, { timeout: 10_000 });

    // The server closes the socket of the token: 1008 "session revoked" (not
    // "token expired", the close at the token expiry).
    await expect.poll(() => closed, { timeout: 5_000 }).not.toBeNull();
    expect(closed).toEqual({ code: 1008, reason: 'session revoked' });

    // The token is blocked while it is still valid.
    expect(remainingMs(current), 'the token expired before the check').toBeGreaterThan(0);
    const blocked = await request.get(`${API_BASE}/api/v1/auth/me`, {
      headers: { Authorization: `Bearer ${current}` },
    });
    expect(blocked.status()).toBe(401);

    // The refresh token is revoked on the server (not only removed from the
    // browser): sent explicitly, it gets the answer for a revoked token.
    const revoked = await refreshVia(request, { Cookie: `${REFRESH_COOKIE}=${refreshToken}` });
    expect(revoked.status).toBe(401);
    expect(revoked.error).toBe('Invalid refresh token');
  });

  test('6: logout-all sends another browser context of the user to the login page and closes its WebSocket', async ({
    browser,
    context,
    request,
  }) => {
    // logout-all ends every session of the user: use a user of this test only.
    const username = await createUser(request);
    const contextB = await newContext(browser);
    try {
      const socketsB = await recordWebSockets(contextB);
      const trafficB = recordTraffic(contextB, 'browser B');

      const pageA = await openApp(context, await apiLogin(context.request, username, TEST_PASSWORD));
      const pageB = await openApp(contextB, await apiLogin(contextB.request, username, TEST_PASSWORD));

      // B has an open app socket whose token is not about to expire, so the
      // close below is the revocation and not the close at the token expiry.
      await waitForFreshToken(pageB, Math.min(5_000, ACCESS_TTL_MS / 2));
      await expect.poll(() => openSockets(socketsB).length, { timeout: ACCESS_TTL_MS + 10_000 }).toBeGreaterThan(0);
      const socketB = openSockets(socketsB)[0];

      // Logout-all from session A (fresh access token through A's cookie).
      const fresh = await refreshVia(context.request);
      expect(fresh.status).toBe(200);
      const res = await context.request.post(`${API_BASE}/api/v1/auth/logout-all`, {
        headers: { Authorization: `Bearer ${fresh.token}` },
      });
      expect(res.status()).toBe(200);
      const logoutAllAt = Date.now();

      // B's socket gets 1008 "session revoked".
      await expect
        .poll(() => socketsB.find((e) => e.socket === socketB && e.type === 'close'), { timeout: 5_000 })
        .toBeDefined();
      const close = socketsB.find((e) => e.socket === socketB && e.type === 'close')!;
      expect({ code: close.code, reason: close.reason }).toEqual({ code: 1008, reason: 'session revoked' });

      // B (another browser, same user) goes to the login page, and so does A.
      await expectTabLandsOnLogin(pageB, ACCESS_TTL_MS + 15_000);
      await expectTabLandsOnLogin(pageA, ACCESS_TTL_MS + 15_000);

      // B never got a new session: no successful refresh that B sent after the
      // logout-all answer (request time: a refresh sent just before it may
      // still answer 200), and no new socket.
      expect(trafficB.refreshes.filter((r) => r.startedAt > logoutAllAt && r.status === 200)).toEqual([]);
      expect(appSocketEvents(socketsB).filter((e) => e.type === 'open' && e.at > close.at)).toEqual([]);
    } finally {
      await contextB.close();
    }
  });
});
