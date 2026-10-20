// k6 API Load Test for k8s-stack-manager backend
// Install: brew install k6  (or https://k6.io/docs/get-started/installation/)
//
// Usage:
//   k6 run loadtest/backend/k6-api.js                          # all scenarios
//   k6 run --env SCENARIO=stack-page loadtest/backend/k6-api.js  # one scenario
//   k6 run --vus 50 --duration 5m loadtest/backend/k6-api.js   # 50 VUs for 5 min (load scenario function)
//   k6 run --env SCENARIO=stack-page --vus 2 --duration 30s loadtest/backend/k6-api.js
//   k6 run --env API_URL=http://backend:8081 loadtest/backend/k6-api.js  # custom URL
//
// Auth strategy: setup() logs in once as admin, creates an API key, and
// passes it to all scenario functions.  This avoids hammering bcrypt on
// every VU iteration.
//
// Environment variables:
//   API_URL           - Backend URL (default: http://localhost:8081)
//   ADMIN_USERNAME    - Admin user (default: admin)
//   ADMIN_PASSWORD    - Admin password (default: admin)
//   SCENARIO          - Run only this scenario (default: all)
//   STACK_PAGE_INSTANCE_ID - Instance of the stack-page scenario (default: setup() creates one)

import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';
import { ensureDefaultCluster } from './lib/cluster.js';

// ── Custom Metrics ──────────────────────────────────────────────────
const errorRate = new Rate('errors');
const loginDuration = new Trend('login_duration', true);
const listDuration = new Trend('list_duration', true);
const crudDuration = new Trend('crud_duration', true);
const deployDuration = new Trend('deploy_duration', true);
const analyticsDuration = new Trend('analytics_duration', true);
const stackPageDuration = new Trend('stack_page_duration', true);
const stackPageLoad = new Trend('stack_page_load', true);
const clusterUnavailable = new Counter('stack_page_cluster_unavailable');
const apiCalls = new Counter('api_calls');

// ── Scenario Selection ─────────────────────────────────────────────
const SCENARIOS = ['smoke', 'load', 'analytics', 'nested-crud', 'stack-page'];
const SELECTED = __ENV.SCENARIO || '';
if (SELECTED !== '' && !SCENARIOS.includes(SELECTED)) {
  throw new Error(`Unknown SCENARIO "${SELECTED}". Use one of: ${SCENARIOS.join(', ')}`);
}

function isEnabled(name) {
  return SELECTED === '' || SELECTED === name;
}

// The scenarios after smoke start at 35s; a selected scenario starts at once.
const AFTER_SMOKE = SELECTED === '' ? '35s' : '0s';

// ── Options ─────────────────────────────────────────────────────────
export const options = {
  scenarios: {
    // Smoke test: quick validation
    ...(isEnabled('smoke') && {
      smoke: {
        executor: 'constant-vus',
        vus: 5,
        duration: '30s',
        tags: { scenario: 'smoke' },
        exec: 'smokeTest',
      },
    }),
    // Load test: sustained concurrent users
    ...(isEnabled('load') && {
      load: {
        executor: 'ramping-vus',
        startVUs: 0,
        stages: [
          { duration: '30s', target: 20 },  // ramp up
          { duration: '2m', target: 20 },   // hold
          { duration: '30s', target: 50 },  // spike
          { duration: '1m', target: 50 },   // hold spike
          { duration: '30s', target: 0 },   // ramp down
        ],
        startTime: AFTER_SMOKE,
        tags: { scenario: 'load' },
        exec: 'loadTest',
      },
    }),
    // Analytics: aggregation-heavy reads + notifications
    ...(isEnabled('analytics') && {
      analytics: {
        executor: 'constant-vus',
        vus: 10,
        duration: '2m',
        startTime: AFTER_SMOKE, // runs in parallel with load
        exec: 'analyticsTest',
        tags: { scenario: 'analytics' },
      },
    }),
    // Nested CRUD: sub-resources, clone, compare, export
    ...(isEnabled('nested-crud') && {
      'nested-crud': {
        executor: 'ramping-vus',
        startVUs: 0,
        stages: [
          { duration: '30s', target: 10 },
          { duration: '2m', target: 20 },
          { duration: '30s', target: 0 },
        ],
        startTime: AFTER_SMOKE,
        exec: 'nestedCrudTest',
        tags: { scenario: 'nested-crud' },
      },
    }),
    // Stack page: the requests of the browser when a user opens an instance
    ...(isEnabled('stack-page') && {
      'stack-page': {
        executor: 'constant-vus',
        vus: 10,
        duration: '2m',
        startTime: AFTER_SMOKE,
        exec: 'stackPageTest',
        tags: { scenario: 'stack-page' },
      },
    }),
  },
  thresholds: {
    http_req_duration: ['p(95)<500', 'p(99)<1000'],  // 95th < 500ms, 99th < 1s
    errors: ['rate<0.05'],                             // <5% error rate
    login_duration: ['p(95)<300'],
    list_duration: ['p(95)<450'],
    crud_duration: ['p(95)<500'],
    analytics_duration: ['p(95)<600'],
    stack_page_duration: ['p(95)<400'],
    stack_page_load: ['p(95)<1000'],
  },
};

// ── Config ──────────────────────────────────────────────────────────
const BASE_URL = __ENV.API_URL || 'http://localhost:8081';
const ADMIN_USER = __ENV.ADMIN_USERNAME || 'admin';
const ADMIN_PASS = __ENV.ADMIN_PASSWORD || 'admin';

// ── W3C Trace Context ──────────────────────────────────────────────
// Generate a W3C traceparent header for trace correlation.
// When the backend runs with OTEL_ENABLED=true, these headers link
// k6 iterations to backend traces visible in Jaeger / Grafana.
function traceparent() {
  // version-traceid-spanid-flags (sampled)
  const hex = (n) => [...Array(n)].map(() => Math.floor(Math.random() * 16).toString(16)).join('');
  return `00-${hex(32)}-${hex(16)}-01`;
}

const jsonHeaders = { 'Content-Type': 'application/json', traceparent: traceparent() };

function authHeaders(token) {
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, traceparent: traceparent() };
}

function apiKeyHeaders(apiKey) {
  return { 'Content-Type': 'application/json', 'X-API-Key': apiKey, traceparent: traceparent() };
}

// ── Helpers ─────────────────────────────────────────────────────────
function login(username, password) {
  const res = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ username, password }),
    { headers: jsonHeaders, tags: { name: 'POST /auth/login' } },
  );
  loginDuration.add(res.timings.duration);
  apiCalls.add(1);

  const ok = check(res, {
    'login status 200': (r) => r.status === 200,
    'login has token': (r) => {
      try { return JSON.parse(r.body).token !== undefined; } catch { return false; }
    },
  });

  if (res.status !== 200) return null;
  return JSON.parse(res.body).token;
}

function registerUser(suffix) {
  const username = `loadtest-user-${suffix}-${__VU}-${Date.now()}`;
  const res = http.post(
    `${BASE_URL}/api/v1/auth/register`,
    JSON.stringify({ username, password: 'loadtest123', display_name: `Load Test ${suffix}` }),
    { headers: jsonHeaders, tags: { name: 'POST /auth/register' } },
  );
  apiCalls.add(1);
  if (res.status !== 201 && res.status !== 200) return null;
  return { username, password: 'loadtest123' };
}

// ── Setup & Teardown ────────────────────────────────────────────────
export function setup() {
  // Login once as admin
  const loginRes = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS }),
    { headers: jsonHeaders },
  );
  if (loginRes.status !== 200) {
    throw new Error(`setup: admin login failed (status ${loginRes.status})`);
  }
  const token = JSON.parse(loginRes.body).token;

  // Get admin user ID
  const meRes = http.get(`${BASE_URL}/api/v1/auth/me`, {
    headers: authHeaders(token),
  });
  if (meRes.status !== 200) {
    throw new Error(`setup: GET /auth/me failed (status ${meRes.status})`);
  }
  const userId = JSON.parse(meRes.body).id;

  // Create an API key for load testing
  const keyRes = http.post(
    `${BASE_URL}/api/v1/users/${userId}/api-keys`,
    JSON.stringify({ name: 'k6-loadtest', expires_in_days: 1 }),
    { headers: authHeaders(token) },
  );
  if (keyRes.status !== 201 && keyRes.status !== 200) {
    throw new Error(`setup: create API key failed (status ${keyRes.status})`);
  }
  const keyBody = JSON.parse(keyRes.body);
  console.log(`setup: created API key ${keyBody.id} for user ${userId}`);

  const cluster = ensureDefaultCluster(BASE_URL, authHeaders(token));
  console.log(`setup: default cluster ${cluster.name} (${cluster.id})`);

  const stackPage = isEnabled('stack-page') ? stackPageInstance(authHeaders(token)) : null;

  return { apiKey: keyBody.raw_key, userId: userId, apiKeyId: keyBody.id, stackPage };
}

// stackPageInstance returns the instance of the stack-page scenario:
// STACK_PAGE_INSTANCE_ID, or a new draft instance of a definition with two
// charts, a value override and a branch override.
function stackPageInstance(headers) {
  if (__ENV.STACK_PAGE_INSTANCE_ID) {
    const res = http.get(`${BASE_URL}/api/v1/stack-instances/${__ENV.STACK_PAGE_INSTANCE_ID}`, { headers });
    if (res.status !== 200) {
      throw new Error(`setup: STACK_PAGE_INSTANCE_ID not found (status ${res.status})`);
    }
    return { instanceId: __ENV.STACK_PAGE_INSTANCE_ID, created: false };
  }

  const suffix = Date.now();
  const defRes = http.post(
    `${BASE_URL}/api/v1/stack-definitions`,
    JSON.stringify({ name: `k6-stack-page-${suffix}`, description: 'Stack page load test', default_branch: 'main' }),
    { headers },
  );
  if (defRes.status !== 201) {
    throw new Error(`setup: create stack page definition failed (status ${defRes.status})`);
  }
  const defId = defRes.json().id;

  const chartIds = [];
  for (const [name, values] of [
    ['frontend-app', 'replicaCount: 2\nimage:\n  tag: latest'],
    ['backend-api', 'replicaCount: 3\nresources:\n  limits:\n    memory: 512Mi'],
  ]) {
    const chartRes = http.post(
      `${BASE_URL}/api/v1/stack-definitions/${defId}/charts`,
      JSON.stringify({
        chart_name: name,
        repository_url: `https://charts.example.com/${name}`,
        chart_version: '1.0.0',
        default_values: values,
      }),
      { headers },
    );
    if (chartRes.status !== 201) {
      throw new Error(`setup: add stack page chart ${name} failed (status ${chartRes.status})`);
    }
    chartIds.push(chartRes.json().id);
  }

  const instRes = http.post(
    `${BASE_URL}/api/v1/stack-instances`,
    JSON.stringify({ stack_definition_id: defId, name: `k6-stack-page-${suffix}`, branch: 'main', ttl_minutes: 480 }),
    { headers },
  );
  if (instRes.status !== 201) {
    throw new Error(`setup: create stack page instance failed (status ${instRes.status}): ${instRes.body}`);
  }
  const instanceId = instRes.json().id;

  const overrideRes = http.put(
    `${BASE_URL}/api/v1/stack-instances/${instanceId}/overrides/${chartIds[0]}`,
    JSON.stringify({ values: 'replicaCount: 1\nimage:\n  tag: v2.0.0' }),
    { headers },
  );
  const branchRes = http.put(
    `${BASE_URL}/api/v1/stack-instances/${instanceId}/branches/${chartIds[1]}`,
    JSON.stringify({ branch: 'feature/k6' }),
    { headers },
  );
  if (overrideRes.status !== 200 || branchRes.status !== 200) {
    console.warn(`setup: stack page overrides: value ${overrideRes.status}, branch ${branchRes.status}`);
  }

  console.log(`setup: stack page instance ${instanceId}`);
  return { instanceId, definitionId: defId, created: true };
}

export function teardown(data) {
  if (!data || !data.apiKeyId) return;

  // Login to get a token for cleanup
  const loginRes = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS }),
    { headers: jsonHeaders },
  );
  if (loginRes.status !== 200) {
    console.warn('teardown: admin login failed, skipping API key cleanup');
    return;
  }
  const token = JSON.parse(loginRes.body).token;

  const delRes = http.del(
    `${BASE_URL}/api/v1/users/${data.userId}/api-keys/${data.apiKeyId}`,
    null,
    { headers: authHeaders(token) },
  );
  console.log(`teardown: deleted API key ${data.apiKeyId} (status ${delRes.status})`);

  if (data.stackPage && data.stackPage.created) {
    const instDel = http.del(`${BASE_URL}/api/v1/stack-instances/${data.stackPage.instanceId}`, null, {
      headers: authHeaders(token),
    });
    const defDel = http.del(`${BASE_URL}/api/v1/stack-definitions/${data.stackPage.definitionId}`, null, {
      headers: authHeaders(token),
    });
    console.log(`teardown: deleted stack page instance (status ${instDel.status}) and definition (status ${defDel.status})`);
  }
}

// ── Smoke Test ──────────────────────────────────────────────────────
export function smokeTest(data) {
  group('Health Checks', () => {
    // Readiness is DOWN (503) when no registered cluster is reachable, for
    // example the in-cluster default cluster outside a pod.
    const endpoints = [['/health', [200]], ['/health/live', [200]], ['/health/ready', [200, 503]]];
    for (const [ep, expected] of endpoints) {
      const res = http.get(`${BASE_URL}${ep}`, {
        tags: { name: `GET ${ep}` },
        responseCallback: http.expectedStatuses(...expected),
      });
      apiCalls.add(1);
      const ok = check(res, {
        [`${ep} status ${expected.join(' or ')}`]: (r) => expected.includes(r.status),
      });
      errorRate.add(!ok);
    }
  });

  group('Ping', () => {
    const res = http.get(`${BASE_URL}/api/v1/ping`, { tags: { name: 'GET /ping' } });
    apiCalls.add(1);
    check(res, { 'ping 200': (r) => r.status === 200 });
  });

  group('Auth Flow', () => {
    // Keep one login call to validate the auth endpoint still works
    const token = login(ADMIN_USER, ADMIN_PASS);
    if (!token) {
      errorRate.add(true);
      return;
    }
    errorRate.add(false);

    // Use API key for the "me" check
    const me = http.get(`${BASE_URL}/api/v1/auth/me`, {
      headers: apiKeyHeaders(data.apiKey),
      tags: { name: 'GET /auth/me' },
    });
    apiCalls.add(1);
    const ok = check(me, { 'me status 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  sleep(1);
}

// ── Load Test ───────────────────────────────────────────────────────
export function loadTest(data) {
  const headers = apiKeyHeaders(data.apiKey);

  // ── Browse (read-heavy, most common) ──────────────────
  group('Browse Templates', () => {
    const res = http.get(`${BASE_URL}/api/v1/templates`, {
      headers,
      tags: { name: 'GET /templates' },
    });
    listDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'templates 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  group('Browse Definitions', () => {
    const res = http.get(`${BASE_URL}/api/v1/stack-definitions`, {
      headers,
      tags: { name: 'GET /stack-definitions' },
    });
    listDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'definitions 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  group('Browse Instances', () => {
    const res = http.get(`${BASE_URL}/api/v1/stack-instances`, {
      headers,
      tags: { name: 'GET /stack-instances' },
    });
    listDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'instances 200': (r) => r.status === 200 });
    errorRate.add(!ok);

    // Also hit recent
    const recent = http.get(`${BASE_URL}/api/v1/stack-instances/recent`, {
      headers,
      tags: { name: 'GET /stack-instances/recent' },
    });
    listDuration.add(recent.timings.duration);
    apiCalls.add(1);
    check(recent, { 'recent 200': (r) => r.status === 200 });
  });

  group('Browse Favorites', () => {
    const res = http.get(`${BASE_URL}/api/v1/favorites`, {
      headers,
      tags: { name: 'GET /favorites' },
    });
    listDuration.add(res.timings.duration);
    apiCalls.add(1);
    check(res, { 'favorites 200': (r) => r.status === 200 });
  });

  group('Browse Audit Logs', () => {
    const res = http.get(`${BASE_URL}/api/v1/audit-logs?limit=50&offset=0`, {
      headers,
      tags: { name: 'GET /audit-logs' },
    });
    listDuration.add(res.timings.duration);
    apiCalls.add(1);
    check(res, { 'audit-logs 200': (r) => r.status === 200 });
  });

  sleep(0.5);

  // ── CRUD Workflow: Definition → Instance ──────────────
  group('CRUD: Create Definition', () => {
    const defPayload = JSON.stringify({
      name: `loadtest-def-${__VU}-${__ITER}-${Date.now()}`,
      description: 'Load test definition',
      default_branch: 'main',
    });

    const createRes = http.post(`${BASE_URL}/api/v1/stack-definitions`, defPayload, {
      headers,
      tags: { name: 'POST /stack-definitions' },
    });
    crudDuration.add(createRes.timings.duration);
    apiCalls.add(1);
    const ok = check(createRes, {
      'create def 201': (r) => r.status === 201,
    });
    errorRate.add(!ok);

    if (createRes.status !== 201) return;
    const def = JSON.parse(createRes.body);

    // Read it back
    const getRes = http.get(`${BASE_URL}/api/v1/stack-definitions/${def.id}`, {
      headers,
      tags: { name: 'GET /stack-definitions/:id' },
    });
    crudDuration.add(getRes.timings.duration);
    apiCalls.add(1);
    check(getRes, { 'get def 200': (r) => r.status === 200 });

    // Create instance from it
    const instName = `lt-inst-${__VU}-${__ITER}-${Date.now()}`;
    const instPayload = JSON.stringify({
      stack_definition_id: def.id,
      name: instName,
      branch: 'main',
      ttl_minutes: 240,
    });

    const instRes = http.post(`${BASE_URL}/api/v1/stack-instances`, instPayload, {
      headers,
      tags: { name: 'POST /stack-instances' },
    });
    crudDuration.add(instRes.timings.duration);
    apiCalls.add(1);
    const instOk = check(instRes, {
      'create instance 201': (r) => r.status === 201,
    });
    errorRate.add(!instOk);

    if (instRes.status !== 201) return;
    const inst = JSON.parse(instRes.body);

    // Get instance detail
    const instGet = http.get(`${BASE_URL}/api/v1/stack-instances/${inst.id}`, {
      headers,
      tags: { name: 'GET /stack-instances/:id' },
    });
    crudDuration.add(instGet.timings.duration);
    apiCalls.add(1);
    check(instGet, { 'get instance 200': (r) => r.status === 200 });

    // Favorite the instance
    const favRes = http.post(
      `${BASE_URL}/api/v1/favorites`,
      JSON.stringify({ entity_type: 'instance', entity_id: inst.id }),
      { headers, tags: { name: 'POST /favorites' } },
    );
    apiCalls.add(1);
    check(favRes, { 'favorite 200/201': (r) => r.status === 200 || r.status === 201 });

    // Check favorite
    const checkFav = http.get(
      `${BASE_URL}/api/v1/favorites/check?entity_type=instance&entity_id=${inst.id}`,
      { headers, tags: { name: 'GET /favorites/check' } },
    );
    apiCalls.add(1);
    check(checkFav, { 'check fav 200': (r) => r.status === 200 });

    // Update instance TTL
    const extendRes = http.post(
      `${BASE_URL}/api/v1/stack-instances/${inst.id}/extend`,
      JSON.stringify({ minutes: 60 }),
      { headers, tags: { name: 'POST /stack-instances/:id/extend' } },
    );
    crudDuration.add(extendRes.timings.duration);
    apiCalls.add(1);
    check(extendRes, { 'extend TTL 200': (r) => r.status === 200 });

    // Unfavorite
    const unfavRes = http.del(
      `${BASE_URL}/api/v1/favorites/instance/${inst.id}`,
      null,
      { headers, tags: { name: 'DELETE /favorites/:type/:id' } },
    );
    apiCalls.add(1);
    check(unfavRes, { 'unfavorite 200/204': (r) => r.status === 200 || r.status === 204 });

    // Delete instance
    const delInst = http.del(`${BASE_URL}/api/v1/stack-instances/${inst.id}`, null, {
      headers,
      tags: { name: 'DELETE /stack-instances/:id' },
    });
    crudDuration.add(delInst.timings.duration);
    apiCalls.add(1);
    check(delInst, { 'delete instance 200/204': (r) => r.status === 200 || r.status === 204 });

    // Delete definition
    const delDef = http.del(`${BASE_URL}/api/v1/stack-definitions/${def.id}`, null, {
      headers,
      tags: { name: 'DELETE /stack-definitions/:id' },
    });
    crudDuration.add(delDef.timings.duration);
    apiCalls.add(1);
    check(delDef, { 'delete def 200/204': (r) => r.status === 200 || r.status === 204 });
  });

  sleep(0.5);

  // ── Concurrent reads (simulates dashboard) ───────────
  group('Dashboard Simulation', () => {
    const responses = http.batch([
      ['GET', `${BASE_URL}/api/v1/stack-instances`, null, { headers, tags: { name: 'batch GET /instances' } }],
      ['GET', `${BASE_URL}/api/v1/stack-instances/recent`, null, { headers, tags: { name: 'batch GET /recent' } }],
      ['GET', `${BASE_URL}/api/v1/favorites`, null, { headers, tags: { name: 'batch GET /favorites' } }],
      ['GET', `${BASE_URL}/api/v1/templates`, null, { headers, tags: { name: 'batch GET /templates' } }],
    ]);

    for (const res of responses) {
      apiCalls.add(1);
      const ok = check(res, { 'batch 200': (r) => r.status === 200 });
      errorRate.add(!ok);
    }
  });

  sleep(1);
}

// ── Analytics Test ─────────────────────────────────────────────────
export function analyticsTest(data) {
  const headers = apiKeyHeaders(data.apiKey);

  group('Analytics Overview', () => {
    const res = http.get(`${BASE_URL}/api/v1/analytics/overview`, {
      headers,
      tags: { name: 'GET /analytics/overview' },
    });
    analyticsDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'analytics overview 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  group('Analytics Templates', () => {
    const res = http.get(`${BASE_URL}/api/v1/analytics/templates`, {
      headers,
      tags: { name: 'GET /analytics/templates' },
    });
    analyticsDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'analytics templates 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  group('Notifications List', () => {
    const res = http.get(`${BASE_URL}/api/v1/notifications`, {
      headers,
      tags: { name: 'GET /notifications' },
    });
    analyticsDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'notifications 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  group('Notifications Count', () => {
    const res = http.get(`${BASE_URL}/api/v1/notifications/count`, {
      headers,
      tags: { name: 'GET /notifications/count' },
    });
    analyticsDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'notification count 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  group('Notifications Read All', () => {
    const res = http.post(`${BASE_URL}/api/v1/notifications/read-all`, null, {
      headers,
      tags: { name: 'POST /notifications/read-all' },
    });
    analyticsDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { 'read-all 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  });

  sleep(0.5);
}

// ── Nested CRUD Test ───────────────────────────────────────────────
export function nestedCrudTest(data) {
  const headers = apiKeyHeaders(data.apiKey);
  const suffix = `${__VU}-${__ITER}-${Date.now()}`;

  let defId = null;
  let chartId1 = null;
  let chartId2 = null;
  let instId = null;
  let clonedId = null;

  group('Nested CRUD: Setup Definition + Charts', () => {
    // Create definition
    const defRes = http.post(
      `${BASE_URL}/api/v1/stack-definitions`,
      JSON.stringify({
        name: `loadtest-nested-${suffix}`,
        description: 'Nested CRUD load test definition',
        default_branch: 'main',
      }),
      { headers, tags: { name: 'POST /stack-definitions' } },
    );
    crudDuration.add(defRes.timings.duration);
    apiCalls.add(1);
    const defOk = check(defRes, { 'nested create def 201': (r) => r.status === 201 });
    errorRate.add(!defOk);
    if (defRes.status !== 201) return;

    const def = JSON.parse(defRes.body);
    defId = def.id;

    // Add chart 1: frontend-app
    const chart1Res = http.post(
      `${BASE_URL}/api/v1/stack-definitions/${defId}/charts`,
      JSON.stringify({
        chart_name: 'frontend-app',
        repository_url: 'https://charts.example.com/frontend',
        chart_version: '1.0.0',
        default_values: 'replicaCount: 2\nimage:\n  tag: latest',
      }),
      { headers, tags: { name: 'POST /stack-definitions/:id/charts' } },
    );
    crudDuration.add(chart1Res.timings.duration);
    apiCalls.add(1);
    const chart1Ok = check(chart1Res, {
      'add chart 1 201': (r) => r.status === 201,
    });
    errorRate.add(!chart1Ok);
    if (chart1Res.status === 201) {
      chartId1 = JSON.parse(chart1Res.body).id;
    }

    // Add chart 2: backend-api
    const chart2Res = http.post(
      `${BASE_URL}/api/v1/stack-definitions/${defId}/charts`,
      JSON.stringify({
        chart_name: 'backend-api',
        repository_url: 'https://charts.example.com/backend',
        chart_version: '2.1.0',
        default_values: 'replicaCount: 3\nresources:\n  limits:\n    memory: 512Mi',
      }),
      { headers, tags: { name: 'POST /stack-definitions/:id/charts' } },
    );
    crudDuration.add(chart2Res.timings.duration);
    apiCalls.add(1);
    const chart2Ok = check(chart2Res, {
      'add chart 2 201': (r) => r.status === 201,
    });
    errorRate.add(!chart2Ok);
    if (chart2Res.status === 201) {
      chartId2 = JSON.parse(chart2Res.body).id;
    }
  });

  if (!defId) { sleep(0.5); return; }

  group('Nested CRUD: Instance + Override + Clone + Compare', () => {
    // Create instance
    const instRes = http.post(
      `${BASE_URL}/api/v1/stack-instances`,
      JSON.stringify({
        stack_definition_id: defId,
        name: `lt-nested-inst-${suffix}`,
        branch: 'main',
        ttl_minutes: 120,
      }),
      { headers, tags: { name: 'POST /stack-instances' } },
    );
    crudDuration.add(instRes.timings.duration);
    apiCalls.add(1);
    const instOk = check(instRes, { 'nested create instance 201': (r) => r.status === 201 });
    errorRate.add(!instOk);
    if (instRes.status !== 201) return;

    const inst = JSON.parse(instRes.body);
    instId = inst.id;

    // Set value override on chart 1 (if chart was created)
    if (chartId1) {
      const overrideRes = http.put(
        `${BASE_URL}/api/v1/stack-instances/${instId}/overrides/${chartId1}`,
        JSON.stringify({ values: 'replicaCount: 5\nimage:\n  tag: v2.0.0' }),
        { headers, tags: { name: 'PUT /stack-instances/:id/overrides/:chartId' } },
      );
      crudDuration.add(overrideRes.timings.duration);
      apiCalls.add(1);
      const overrideOk = check(overrideRes, {
        'set override 200': (r) => r.status === 200,
      });
      errorRate.add(!overrideOk);
    }

    // Clone the instance
    const cloneRes = http.post(
      `${BASE_URL}/api/v1/stack-instances/${instId}/clone`,
      null,
      { headers, tags: { name: 'POST /stack-instances/:id/clone' } },
    );
    crudDuration.add(cloneRes.timings.duration);
    apiCalls.add(1);
    const cloneOk = check(cloneRes, {
      'clone instance 201': (r) => r.status === 201,
    });
    errorRate.add(!cloneOk);
    if (cloneRes.status === 201) {
      clonedId = JSON.parse(cloneRes.body).id;
    }

    // Compare the two instances (only if clone succeeded)
    if (clonedId) {
      const compareRes = http.get(
        `${BASE_URL}/api/v1/stack-instances/compare?left=${instId}&right=${clonedId}`,
        { headers, tags: { name: 'GET /stack-instances/compare' } },
      );
      crudDuration.add(compareRes.timings.duration);
      apiCalls.add(1);
      const compareOk = check(compareRes, {
        'compare instances 200': (r) => r.status === 200,
      });
      errorRate.add(!compareOk);
    }

    // Export the definition
    const exportRes = http.get(
      `${BASE_URL}/api/v1/stack-definitions/${defId}/export`,
      { headers, tags: { name: 'GET /stack-definitions/:id/export' } },
    );
    crudDuration.add(exportRes.timings.duration);
    apiCalls.add(1);
    const exportOk = check(exportRes, {
      'export definition 200': (r) => r.status === 200,
    });
    errorRate.add(!exportOk);
  });

  // Cleanup: delete cloned instance, original instance, definition
  group('Nested CRUD: Cleanup', () => {
    if (clonedId) {
      const delClone = http.del(`${BASE_URL}/api/v1/stack-instances/${clonedId}`, null, {
        headers,
        tags: { name: 'DELETE /stack-instances/:id' },
      });
      crudDuration.add(delClone.timings.duration);
      apiCalls.add(1);
      check(delClone, { 'delete clone 200/204': (r) => r.status === 200 || r.status === 204 });
    }

    if (instId) {
      const delInst = http.del(`${BASE_URL}/api/v1/stack-instances/${instId}`, null, {
        headers,
        tags: { name: 'DELETE /stack-instances/:id' },
      });
      crudDuration.add(delInst.timings.duration);
      apiCalls.add(1);
      check(delInst, { 'delete nested inst 200/204': (r) => r.status === 200 || r.status === 204 });
    }

    if (defId) {
      const delDef = http.del(`${BASE_URL}/api/v1/stack-definitions/${defId}`, null, {
        headers,
        tags: { name: 'DELETE /stack-definitions/:id' },
      });
      crudDuration.add(delDef.timings.duration);
      apiCalls.add(1);
      check(delDef, { 'delete nested def 200/204': (r) => r.status === 200 || r.status === 204 });
    }
  });

  sleep(0.5);
}

// ── Stack Page Test ────────────────────────────────────────────────
// The requests of the web UI when a user opens an instance page
// (pages/StackInstances/Detail.tsx and its hooks and components). Without
// cluster access, /pods and /status answer 502 at once. /action-runs (#640)
// answers 404 on a backend without the route.

const NO_CLUSTER = [200, 502];

// stackPageBatch sends the requests at the same time and checks each status.
// Each request is [label, URL, request name, accepted statuses].
function stackPageBatch(headers, requests) {
  const responses = http.batch(requests.map(([, url, name, accepted]) => [
    'GET',
    url,
    null,
    { headers, tags: { name }, responseCallback: http.expectedStatuses(...accepted) },
  ]));
  requests.forEach(([label, , , accepted], i) => {
    const res = responses[i];
    stackPageDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, { [`stack page ${label} ${accepted.join(' or ')}`]: (r) => accepted.includes(r.status) });
    errorRate.add(!ok);
    if (res.status === 502) clusterUnavailable.add(1);
  });
  return responses;
}

export function stackPageTest(data) {
  if (!data.stackPage) {
    sleep(1);
    return;
  }
  const headers = apiKeyHeaders(data.apiKey);
  const id = data.stackPage.instanceId;
  const inst = `${BASE_URL}/api/v1/stack-instances/${id}`;
  const start = Date.now();

  group('Stack Page', () => {
    // The page and its hooks start these requests at the same time.
    const first = stackPageBatch(headers, [
      ['instance', inst, 'GET /stack-instances/:id', [200]],
      ['deploy history', `${inst}/deploy-log?view=summary&limit=10`,
        'GET /stack-instances/:id/deploy-log (summary)', [200]],
      ['rollback targets', `${inst}/deploy-log?view=summary&action=deploy&status=success&limit=50`,
        'GET /stack-instances/:id/deploy-log (rollback targets)', [200]],
      ['quota', `${inst}/quota`, 'GET /stack-instances/:id/quota', [200]],
      ['actions', `${inst}/actions`, 'GET /stack-instances/:id/actions', [200]],
      ['action runs', `${inst}/action-runs`, 'GET /stack-instances/:id/action-runs', [200, 404]],
      ['favorite', `${BASE_URL}/api/v1/favorites/check?entity_type=instance&entity_id=${id}`,
        'GET /favorites/check', [200]],
      ['notification count', `${BASE_URL}/api/v1/notifications/count`, 'GET /notifications/count', [200]],
    ]);
    if (first[0].status !== 200) return;

    // After the instance: its definition and overrides, then the pod state.
    const defId = first[0].json('stack_definition_id');
    stackPageBatch(headers, [
      ['definition', `${BASE_URL}/api/v1/stack-definitions/${defId}`, 'GET /stack-definitions/:id', [200]],
      ['overrides', `${inst}/overrides`, 'GET /stack-instances/:id/overrides', [200]],
      ['branches', `${inst}/branches`, 'GET /stack-instances/:id/branches', [200]],
    ]);
    stackPageBatch(headers, [
      ['pods', `${inst}/pods`, 'GET /stack-instances/:id/pods', NO_CLUSTER],
      ['status', `${inst}/status`, 'GET /stack-instances/:id/status', NO_CLUSTER],
    ]);
  });

  stackPageLoad.add(Date.now() - start);
  sleep(1);
}

// ── Default Function ───────────────────────────────────────────────
// k6 runs it with --vus/--duration (the CLI then replaces the scenarios).
// SCENARIO selects the function; the default is the load scenario.
const SCENARIO_FUNCTIONS = {
  smoke: smokeTest,
  load: loadTest,
  analytics: analyticsTest,
  'nested-crud': nestedCrudTest,
  'stack-page': stackPageTest,
};

export default function (data) {
  (SCENARIO_FUNCTIONS[SELECTED] || loadTest)(data);
}
