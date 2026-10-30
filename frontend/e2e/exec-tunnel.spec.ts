import { test, expect, APIRequestContext } from '@playwright/test';
import fs from 'fs';
import path from 'path';
import { API_BASE, apiCreateDefinition, apiLogin, uniqueName } from './helpers';

/**
 * Exec through a real cluster (#723), only with E2E_KUBECONFIG.
 * The spec registers its own cluster from E2E_KUBECONFIG with allow_exec on,
 * deploys e2e/fixtures/charts/e2e-tunnel (container `web`, nginx-unprivileged
 * with sh, automountServiceAccountToken false) and runs commands over the
 * WebSocket. The e2e backend allows the container `web`
 * (STACK_EXEC_ALLOWED_CONTAINERS, Makefile) and the default command limit.
 *
 * The route never reads ?token=. Browsers cannot set the Authorization header,
 * so the spec uses the WebSocket of Node (22 or later), which takes `headers`.
 */

test.describe.configure({ mode: 'serial' });

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });

type NodeWebSocket = new (url: string, init: { headers: Record<string, string> }) => WebSocket;

interface Frame {
  /** 1 stdout, 2 stderr (binary frames). */
  stream?: number;
  data?: string;
  /** Parsed JSON of a text frame. */
  json?: Record<string, unknown>;
}

interface Session {
  ws: WebSocket;
  frames: Frame[];
  stdout: () => string;
  stderr: () => string;
  texts: () => Record<string, unknown>[];
  closed: Promise<{ code: number; reason: string }>;
}

/** Open the exec WebSocket and wait for the upgrade. Rejects when the server refuses it. */
function openExec(token: string, instanceId: string, query = 'chart=e2e-tunnel'): Promise<Session> {
  const url = `${API_BASE.replace(/^http/, 'ws')}/api/v1/stack-instances/${instanceId}/exec?${query}`;
  const ws = new (WebSocket as unknown as NodeWebSocket)(url, { headers: auth(token) });
  ws.binaryType = 'arraybuffer';
  const frames: Frame[] = [];
  ws.onmessage = (e: MessageEvent) => {
    if (typeof e.data === 'string') {
      frames.push({ json: JSON.parse(e.data) });
      return;
    }
    const bytes = new Uint8Array(e.data as ArrayBuffer);
    frames.push({ stream: bytes[0], data: new TextDecoder().decode(bytes.subarray(1)) });
  };
  const closed = new Promise<{ code: number; reason: string }>((resolve) => {
    ws.addEventListener('close', (e: CloseEvent) => resolve({ code: e.code, reason: e.reason }));
  });
  const join = (n: number) => frames.filter((f) => f.stream === n).map((f) => f.data).join('');
  return new Promise((resolve, reject) => {
    ws.addEventListener(
      'open',
      () =>
        resolve({
          ws,
          frames,
          stdout: () => join(1),
          stderr: () => join(2),
          texts: () => frames.filter((f) => f.json).map((f) => f.json!),
          closed,
        }),
      { once: true },
    );
    ws.addEventListener('error', () => reject(new Error('exec was refused (no 101)')), { once: true });
  });
}

const sendStart = (s: Session, command: unknown) => s.ws.send(JSON.stringify({ type: 'start', command }));

async function closeWithin(s: Session, ms = 30_000) {
  return Promise.race([
    s.closed,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error('the exec session did not close')), ms)),
  ]);
}

/** Run a command to the end. The pod can need a moment after the deploy (503 no_ready_pod). */
async function run(token: string, instanceId: string, command: string[]) {
  let s!: Session;
  await expect
    .poll(async () => {
      try {
        s = await openExec(token, instanceId);
        return 'open';
      } catch (e) {
        return (e as Error).message;
      }
    }, { timeout: 60_000, intervals: [2000] })
    .toBe('open');
  sendStart(s, command);
  const close = await closeWithin(s);
  return { s, close };
}

let api: APIRequestContext;
let adminToken: string;
let clusterId = '';
let definitionId = '';
let instanceId = '';
let userId = '';
let userToken = '';

async function auditDetails(action: string): Promise<Record<string, unknown>[]> {
  const res = await api.get(`${API_BASE}/api/v1/audit-logs?entity_id=${instanceId}&action=${action}&limit=200`, { headers: auth(adminToken) });
  return ((await res.json()).data ?? []).map((e: { details: string }) => JSON.parse(e.details));
}

test.describe('Exec through a cluster (E2E_KUBECONFIG)', () => {
  test.skip(!process.env.E2E_KUBECONFIG, 'Needs a local cluster: set E2E_KUBECONFIG (see make test-e2e)');

  test.beforeAll(async ({ playwright }) => {
    test.setTimeout(300_000);
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);

    const kc = process.env.E2E_KUBECONFIG!;
    const kubeconfig = fs.readFileSync(path.isAbsolute(kc) ? kc : path.resolve('..', kc), 'utf8');
    const server = /server:\s*(\S+)/.exec(kubeconfig)?.[1] ?? 'https://127.0.0.1:6443';
    const clusterRes = await api.post(`${API_BASE}/api/v1/clusters`, {
      headers: auth(adminToken),
      data: { name: uniqueName('e2e-exec-cluster'), api_server_url: server, kubeconfig_data: kubeconfig, is_default: false, allow_exec: true },
    });
    expect(clusterRes.status(), await clusterRes.text()).toBe(201);
    clusterId = (await clusterRes.json()).id;

    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-exec-tunnel-def'));
    const chartRes = await api.post(`${API_BASE}/api/v1/stack-definitions/${definitionId}/charts`, {
      headers: auth(adminToken),
      data: {
        chart_name: 'e2e-tunnel',
        repository_url: '',
        chart_path: path.resolve('e2e/fixtures/charts/e2e-tunnel'),
        chart_version: '',
        default_values: `marker: ${uniqueName('m')}\n`,
      },
    });
    expect(chartRes.status(), await chartRes.text()).toBe(201);

    const instRes = await api.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(adminToken),
      data: { stack_definition_id: definitionId, name: uniqueName('e2e-exec-tunnel'), branch: 'main', cluster_id: clusterId },
    });
    expect(instRes.status(), await instRes.text()).toBe(201);
    instanceId = (await instRes.json()).id;

    const dep = await api.post(`${API_BASE}/api/v1/stack-instances/${instanceId}/deploy`, { headers: auth(adminToken) });
    expect(dep.status(), await dep.text()).toBe(202);
    await expect
      .poll(async () => (await (await api.get(`${API_BASE}/api/v1/stack-instances/${instanceId}`, { headers: auth(adminToken) })).json()).status, {
        timeout: 240_000,
        intervals: [2000],
      })
      .toBe('running');

    const username = uniqueName('e2e-exec-user');
    const password = `Pw-${username}`;
    const reg = await api.post(`${API_BASE}/api/v1/auth/register`, { headers: auth(adminToken), data: { username, password, role: 'user' } });
    expect(reg.ok(), await reg.text()).toBe(true);
    userId = (await reg.json()).id;
    userToken = await apiLogin(api, username, password);
  });

  test.afterAll(async () => {
    test.setTimeout(300_000);
    if (!api) return;
    if (instanceId) {
      // The last test stops the instance: a delete during the stop gives 409,
      // so send it again until the instance is gone.
      const url = `${API_BASE}/api/v1/stack-instances/${instanceId}`;
      await expect
        .poll(async () => {
          const status = (await api.get(url, { headers: auth(adminToken) })).status();
          if (status !== 404) await api.delete(url, { headers: auth(adminToken) });
          return status;
        }, { timeout: 240_000, intervals: [3000] })
        .toBe(404);
    }
    if (definitionId) await api.delete(`${API_BASE}/api/v1/stack-definitions/${definitionId}`, { headers: auth(adminToken) });
    if (clusterId) await api.delete(`${API_BASE}/api/v1/clusters/${clusterId}`, { headers: auth(adminToken) });
    if (userId) await api.delete(`${API_BASE}/api/v1/users/${userId}`, { headers: auth(adminToken) });
  });

  test('streams stdout and stderr apart and reports the exit code', async () => {
    const { s, close } = await run(adminToken, instanceId, ['sh', '-c', 'echo out; echo err >&2; exit 3']);
    const texts = s.texts();
    expect(texts[0]).toMatchObject({ type: 'started', container: 'web' });
    expect(String(texts[0].pod)).toMatch(/^[a-z0-9.-]+$/);
    expect(s.stdout()).toBe('out\n');
    expect(s.stderr()).toBe('err\n');
    expect(texts[texts.length - 1]).toEqual({ type: 'exit', exit_code: 3 });
    expect(close.code, close.reason).toBe(1000);
  });

  test('exit code 0 for a successful command', async () => {
    const { s, close } = await run(adminToken, instanceId, ['echo', 'hello']);
    expect(s.stdout()).toBe('hello\n');
    expect(s.texts().pop()).toEqual({ type: 'exit', exit_code: 0 });
    expect(close.code).toBe(1000);
  });

  test('writes an exec and an exec_end audit entry without output', async () => {
    const { close } = await run(adminToken, instanceId, ['sh', '-c', 'echo OUT-$((6*7))']);
    expect(close.code).toBe(1000);
    await expect
      .poll(async () => {
        const starts = await auditDetails('exec');
        const ends = await auditDetails('exec_end');
        return starts.length >= 3 && ends.length === starts.length;
      }, { timeout: 15_000 })
      .toBe(true);

    const starts = await auditDetails('exec');
    const start = starts.find((d) => String(d.command).includes('OUT-$((6*7))'));
    expect(start).toMatchObject({ chart: 'e2e-tunnel', container: 'web', auth: 'jwt' });
    expect(String(start!.pod)).not.toBe('');

    const ends = await auditDetails('exec_end');
    const withExit3 = ends.find((d) => d.exit_code === 3);
    expect(withExit3).toMatchObject({ result: 'nonzero', container: 'web', close_code: 1000 });
    expect(withExit3!.bytes_stdout).toBe(4);
    expect(withExit3!.bytes_stderr).toBe(4);
    const all = JSON.stringify([...starts, ...ends]);
    expect(all).not.toContain('OUT-42');
    expect(all).not.toMatch(/HOSTNAME|NGINX_VERSION/);
  });

  test('another user gets 403 forbidden', async () => {
    const res = await api.get(`${API_BASE}/api/v1/stack-instances/${instanceId}/exec?chart=e2e-tunnel`, { headers: auth(userToken) });
    expect(res.status()).toBe(403);
    expect((await res.json()).code).toBe('forbidden');
    await expect(openExec(userToken, instanceId)).rejects.toThrow('refused');
  });

  for (const [name, query, status, code] of [
    ['a chart of another definition', 'chart=nope', 404, 'chart_not_found'],
    ['a container that is not on the allowed list', 'chart=e2e-tunnel&container=db', 403, 'container_not_allowed'],
    ['a request without the upgrade headers', 'chart=e2e-tunnel', 400, 'upgrade_required'],
  ] as const) {
    test(`${name} is ${status} ${code}`, async () => {
      const res = await api.get(`${API_BASE}/api/v1/stack-instances/${instanceId}/exec?${query}`, { headers: auth(adminToken) });
      expect(res.status()).toBe(status);
      expect((await res.json()).code).toBe(code);
    });
  }

  test('no start message in 10 seconds closes with 4408', async () => {
    test.setTimeout(60_000);
    const s = await openExec(adminToken, instanceId);
    const close = await closeWithin(s, 20_000);
    expect(close.code).toBe(4408);
  });

  test('a command above the limit closes with 4400 and never runs', async () => {
    const s = await openExec(adminToken, instanceId);
    sendStart(s, ['echo', 'x'.repeat(5000)]);
    const close = await closeWithin(s);
    expect(close.code, close.reason).toBe(4400);
    expect(s.stdout()).toBe('');
    expect(s.texts().some((t) => t.type === 'started')).toBe(false);
  });

  test('an empty command and a bad first message close with 4400', async () => {
    for (const send of [
      (s: Session) => sendStart(s, []),
      (s: Session) => sendStart(s, ['', 'x']),
      (s: Session) => s.ws.send('not json'),
    ]) {
      const s = await openExec(adminToken, instanceId);
      send(s);
      const close = await closeWithin(s);
      expect(close.code, close.reason).toBe(4400);
    }
  });

  test('stop closes an open long-running exec with 1008', async () => {
    const s = await openExec(adminToken, instanceId);
    sendStart(s, ['sleep', '60']);
    await expect.poll(() => s.texts().some((t) => t.type === 'started'), { timeout: 15_000 }).toBe(true);
    const stop = await api.post(`${API_BASE}/api/v1/stack-instances/${instanceId}/stop`, { headers: auth(adminToken) });
    expect(stop.status(), await stop.text()).toBe(202);
    const close = await closeWithin(s, 15_000);
    expect(close.code).toBe(1008);
    expect(close.reason).toBe('access revoked');
    expect(s.texts().some((t) => t.type === 'exit')).toBe(false);
  });
});
