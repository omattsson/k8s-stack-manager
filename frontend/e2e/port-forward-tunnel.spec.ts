import { test, expect, APIRequestContext } from '@playwright/test';
import fs from 'fs';
import path from 'path';
import { API_BASE, E2E_DB_SKIP_REASON, apiCreateDefinition, apiLogin, e2eDbConfigured, runE2eSql, uniqueName } from './helpers';

/**
 * Port-forward through a real cluster (#722), only with E2E_KUBECONFIG.
 * The spec registers its own cluster from E2E_KUBECONFIG with
 * allow_port_forward on (the default e2e cluster keeps its setting), deploys
 * e2e/fixtures/charts/e2e-tunnel (nginx behind the Service web, port 80 ->
 * the named container port http) and talks HTTP through the tunnel.
 *
 * The tunnel is a WebSocket with the Authorization header (the route never
 * reads ?token=). Browsers cannot set that header, so the spec uses the
 * WebSocket of Node (22 or later), which takes `headers`.
 *
 * Without E2E_DB_*, the shared namespace check is skipped (it writes a row
 * directly, as a row from before the namespace became immutable).
 */

test.describe.configure({ mode: 'serial' });

const auth = (token: string) => ({ Authorization: `Bearer ${token}` });
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const NAMESPACE = /^[a-z0-9-]{1,63}$/;

type NodeWebSocket = new (url: string, init: { headers: Record<string, string> }) => WebSocket;

interface Tunnel {
  ws: WebSocket;
  /** Everything the pod sent, as text. */
  received: () => string;
  /** Resolves when the server or the client closed the tunnel. */
  closed: Promise<{ code: number; reason: string }>;
}

/** Open a tunnel and wait for the upgrade. Rejects when the server refuses it. */
function openTunnel(token: string, instanceId: string, service = 'web', port = 80): Promise<Tunnel> {
  const url = `${API_BASE.replace(/^http/, 'ws')}/api/v1/stack-instances/${instanceId}/port-forward?service=${service}&port=${port}`;
  const ws = new (WebSocket as unknown as NodeWebSocket)(url, { headers: auth(token) });
  ws.binaryType = 'arraybuffer';
  const chunks: string[] = [];
  const decoder = new TextDecoder();
  ws.onmessage = (e: MessageEvent) => {
    chunks.push(decoder.decode(e.data as ArrayBuffer, { stream: true }));
  };
  const closed = new Promise<{ code: number; reason: string }>((resolve) => {
    ws.addEventListener('close', (e: CloseEvent) => resolve({ code: e.code, reason: e.reason }));
  });
  return new Promise((resolve, reject) => {
    ws.addEventListener('open', () => resolve({ ws, received: () => chunks.join(''), closed }), { once: true });
    ws.addEventListener('error', () => reject(new Error('the tunnel was refused (no 101)')), { once: true });
  });
}

/** Send one HTTP GET through a new tunnel and return the raw response. */
async function httpGetThroughTunnel(token: string, instanceId: string): Promise<string> {
  const t = await openTunnel(token, instanceId);
  t.ws.send(new TextEncoder().encode('GET / HTTP/1.1\r\nHost: e2e\r\nConnection: close\r\n\r\n'));
  // nginx closes the connection after the response, and the backend then
  // closes the tunnel.
  const close = await Promise.race([
    t.closed,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error('the tunnel did not close')), 15_000)),
  ]);
  expect(close.code, close.reason).toBe(1000);
  return t.received();
}

let api: APIRequestContext;
let adminToken: string;
let clusterId = '';
let definitionId = '';
let instanceId = '';
let namespace = '';
let marker = '';
let userId = '';
let userToken = '';
let userInstanceId = '';

test.describe('Port-forward through a cluster (E2E_KUBECONFIG)', () => {
  test.skip(!process.env.E2E_KUBECONFIG, 'Needs a local cluster: set E2E_KUBECONFIG (see make test-e2e)');

  test.beforeAll(async ({ playwright }) => {
    test.setTimeout(300_000);
    api = await playwright.request.newContext();
    adminToken = await apiLogin(api);

    // A relative E2E_KUBECONFIG is relative to the repository root (as in the Makefile).
    const kc = process.env.E2E_KUBECONFIG!;
    const kubeconfig = fs.readFileSync(path.isAbsolute(kc) ? kc : path.resolve('..', kc), 'utf8');
    const server = /server:\s*(\S+)/.exec(kubeconfig)?.[1] ?? 'https://127.0.0.1:6443';
    const clusterRes = await api.post(`${API_BASE}/api/v1/clusters`, {
      headers: auth(adminToken),
      data: { name: uniqueName('e2e-pf-cluster'), api_server_url: server, kubeconfig_data: kubeconfig, is_default: false, allow_port_forward: true },
    });
    expect(clusterRes.status(), await clusterRes.text()).toBe(201);
    clusterId = (await clusterRes.json()).id;

    marker = uniqueName('m');
    definitionId = await apiCreateDefinition(api, adminToken, uniqueName('e2e-pf-tunnel-def'));
    const chartRes = await api.post(`${API_BASE}/api/v1/stack-definitions/${definitionId}/charts`, {
      headers: auth(adminToken),
      data: {
        chart_name: 'e2e-tunnel',
        repository_url: '',
        chart_path: path.resolve('e2e/fixtures/charts/e2e-tunnel'),
        chart_version: '',
        default_values: `marker: ${marker}\n`,
      },
    });
    expect(chartRes.status(), await chartRes.text()).toBe(201);

    const instRes = await api.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(adminToken),
      data: { stack_definition_id: definitionId, name: uniqueName('e2e-pf-tunnel'), branch: 'main', cluster_id: clusterId },
    });
    expect(instRes.status(), await instRes.text()).toBe(201);
    const inst = await instRes.json();
    instanceId = inst.id;
    namespace = inst.namespace;

    const dep = await api.post(`${API_BASE}/api/v1/stack-instances/${instanceId}/deploy`, { headers: auth(adminToken) });
    expect(dep.status(), await dep.text()).toBe(202);
    await expect
      .poll(async () => (await (await api.get(`${API_BASE}/api/v1/stack-instances/${instanceId}`, { headers: auth(adminToken) })).json()).status, {
        timeout: 240_000,
        intervals: [2000],
      })
      .toBe('running');

    // A user (role user) with an instance of their own on the same cluster.
    const username = uniqueName('e2e-pf-user');
    const password = `Pw-${username}`;
    const reg = await api.post(`${API_BASE}/api/v1/auth/register`, {
      headers: auth(adminToken),
      data: { username, password, role: 'user' },
    });
    expect(reg.ok(), await reg.text()).toBe(true);
    userId = (await reg.json()).id;
    userToken = await apiLogin(api, username, password);
    const own = await api.post(`${API_BASE}/api/v1/stack-instances`, {
      headers: auth(userToken),
      data: { stack_definition_id: definitionId, name: uniqueName('e2e-pf-own'), branch: 'main', cluster_id: clusterId },
    });
    expect(own.status(), await own.text()).toBe(201);
    userInstanceId = (await own.json()).id;
  });

  test.afterAll(async () => {
    test.setTimeout(300_000);
    if (!api) return;
    if (userInstanceId) await api.delete(`${API_BASE}/api/v1/stack-instances/${userInstanceId}`, { headers: auth(adminToken) });
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

  test('carries HTTP to the named port behind the service port', async () => {
    // The pod can need a moment after the deploy to become ready (503 no_ready_pod).
    let response = '';
    await expect
      .poll(async () => {
        response = await httpGetThroughTunnel(adminToken, instanceId).catch((e: Error) => e.message);
        return response.split('\r\n')[0];
      }, { timeout: 60_000, intervals: [2000] })
      .toBe('HTTP/1.1 200 OK');
    expect(response).toContain(`marker=${marker}`);
  });

  test('keeps parallel tunnels apart', async () => {
    const responses = await Promise.all([1, 2, 3].map(() => httpGetThroughTunnel(adminToken, instanceId)));
    for (const r of responses) {
      expect(r.split('\r\n')[0]).toBe('HTTP/1.1 200 OK');
      expect(r).toContain(`marker=${marker}`);
    }
  });

  test('writes a start and an end audit entry for each tunnel', async () => {
    await expect
      .poll(async () => {
        const res = await api.get(`${API_BASE}/api/v1/audit-logs?entity_id=${instanceId}&limit=200`, { headers: auth(adminToken) });
        const actions: string[] = ((await res.json()).data ?? []).map((e: { action: string }) => e.action);
        const count = (a: string) => actions.filter((x) => x === a).length;
        return { starts: count('port_forward') >= 4, endsMatch: count('port_forward_end') === count('port_forward') };
      }, { timeout: 15_000 })
      .toEqual({ starts: true, endsMatch: true });
  });

  test("refuses another user's instance", async () => {
    const res = await api.get(`${API_BASE}/api/v1/stack-instances/${instanceId}/port-forward?service=web&port=80`, { headers: auth(userToken) });
    expect(res.status()).toBe(403);
    expect((await res.json()).code).toBe('forbidden');
  });

  test('a user cannot move their instance into the namespace of another stack', async () => {
    const res = await api.put(`${API_BASE}/api/v1/stack-instances/${userInstanceId}`, {
      headers: auth(userToken),
      data: { namespace },
    });
    expect(res.status(), await res.text()).toBe(400);
    expect((await res.json()).error).toBe('namespace cannot be changed');
    const stored = await (await api.get(`${API_BASE}/api/v1/stack-instances/${userInstanceId}`, { headers: auth(userToken) })).json();
    expect(stored.namespace).not.toBe(namespace);
  });

  test('refuses a namespace that another instance row also names', async () => {
    test.skip(!e2eDbConfigured(), E2E_DB_SKIP_REASON);
    expect(userInstanceId).toMatch(UUID);
    expect(namespace).toMatch(NAMESPACE);
    const own = await (await api.get(`${API_BASE}/api/v1/stack-instances/${userInstanceId}`, { headers: auth(userToken) })).json();
    const ownNamespace: string = own.namespace;
    expect(ownNamespace).toMatch(NAMESPACE);

    // A row from before the namespace became immutable: the user's instance
    // names the namespace of the admin's running stack.
    await runE2eSql(`UPDATE stack_instances SET namespace = '${namespace}', status = 'running' WHERE id = '${userInstanceId}'`);
    try {
      for (const [token, id] of [[userToken, userInstanceId], [adminToken, instanceId]]) {
        const res = await api.get(`${API_BASE}/api/v1/stack-instances/${id}/port-forward?service=web&port=80`, { headers: auth(token) });
        expect(res.status(), `instance ${id}`).toBe(403);
        expect((await res.json()).code).toBe('namespace_not_allowed');
      }
      await expect(openTunnel(userToken, userInstanceId)).rejects.toThrow('refused');
    } finally {
      // Back to a draft in its own namespace, so its delete cleans nothing.
      await runE2eSql(`UPDATE stack_instances SET namespace = '${ownNamespace}', status = 'draft' WHERE id = '${userInstanceId}'`);
    }
    const res = await httpGetThroughTunnel(adminToken, instanceId);
    expect(res).toContain(`marker=${marker}`);
  });

  test('stop closes an open tunnel with 1008', async () => {
    const t = await openTunnel(adminToken, instanceId);
    const stop = await api.post(`${API_BASE}/api/v1/stack-instances/${instanceId}/stop`, { headers: auth(adminToken) });
    expect(stop.status(), await stop.text()).toBe(202);
    const close = await Promise.race([
      t.closed,
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('the tunnel stayed open after stop')), 15_000)),
    ]);
    expect(close.code).toBe(1008);
    expect(close.reason).toBe('access revoked');
  });
});
