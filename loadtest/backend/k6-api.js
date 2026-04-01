// k6 API Load Test for k8s-stack-manager backend
// Install: brew install k6  (or https://k6.io/docs/get-started/installation/)
//
// Usage:
//   k6 run loadtest/backend/k6-api.js                          # default scenarios (smoke + load)
//   k6 run --vus 50 --duration 5m loadtest/backend/k6-api.js   # 50 VUs for 5 min
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

import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';

// ── Custom Metrics ──────────────────────────────────────────────────
const errorRate = new Rate('errors');
const loginDuration = new Trend('login_duration', true);
const listDuration = new Trend('list_duration', true);
const crudDuration = new Trend('crud_duration', true);
const deployDuration = new Trend('deploy_duration', true);
const analyticsDuration = new Trend('analytics_duration', true);
const apiCalls = new Counter('api_calls');

// ── Options ─────────────────────────────────────────────────────────
export const options = {
  scenarios: {
    // Smoke test: quick validation
    smoke: {
      executor: 'constant-vus',
      vus: 5,
      duration: '30s',
      tags: { scenario: 'smoke' },
      exec: 'smokeTest',
    },
    // Load test: sustained concurrent users
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
      startTime: '35s', // start after smoke
      tags: { scenario: 'load' },
      exec: 'loadTest',
    },
    // Analytics: aggregation-heavy reads + notifications
    analytics: {
      executor: 'constant-vus',
      vus: 10,
      duration: '2m',
      startTime: '35s', // starts after smoke, runs in parallel with load
      exec: 'analyticsTest',
      tags: { scenario: 'analytics' },
    },
    // Nested CRUD: sub-resources, clone, compare, export
    'nested-crud': {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 10 },
        { duration: '2m', target: 20 },
        { duration: '30s', target: 0 },
      ],
      startTime: '35s', // starts after smoke
      exec: 'nestedCrudTest',
      tags: { scenario: 'nested-crud' },
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<500', 'p(99)<1000'],  // 95th < 500ms, 99th < 1s
    errors: ['rate<0.05'],                             // <5% error rate
    login_duration: ['p(95)<300'],
    list_duration: ['p(95)<450'],
    crud_duration: ['p(95)<500'],
    analytics_duration: ['p(95)<600'],
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
    JSON.stringify({ name: 'k6-loadtest' }),
    { headers: authHeaders(token) },
  );
  if (keyRes.status !== 201 && keyRes.status !== 200) {
    throw new Error(`setup: create API key failed (status ${keyRes.status})`);
  }
  const keyBody = JSON.parse(keyRes.body);

  console.log(`setup: created API key ${keyBody.id} for user ${userId}`);
  return { apiKey: keyBody.raw_key, userId: userId, apiKeyId: keyBody.id };
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
}

// ── Smoke Test ──────────────────────────────────────────────────────
export function smokeTest(data) {
  group('Health Checks', () => {
    const endpoints = ['/health', '/health/live', '/health/ready'];
    for (const ep of endpoints) {
      const res = http.get(`${BASE_URL}${ep}`, { tags: { name: `GET ${ep}` } });
      apiCalls.add(1);
      const ok = check(res, {
        [`${ep} status 200`]: (r) => r.status === 200,
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
      JSON.stringify({ ttl_minutes: 480 }),
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
        values_yaml: 'replicaCount: 2\nimage:\n  tag: latest',
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
        values_yaml: 'replicaCount: 3\nresources:\n  limits:\n    memory: 512Mi',
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
        JSON.stringify({ values_yaml: 'replicaCount: 5\nimage:\n  tag: v2.0.0' }),
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
