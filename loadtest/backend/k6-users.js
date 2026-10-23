// k6 load test with many users for k8s-stack-manager. Each user has its own
// role and API key (X-API-Key), so the test does not measure bcrypt and the
// backend sees many distinct users.
//
// Usage:
//   k6 run loadtest/backend/k6-users.js                                 # all scenarios that the env allows
//   k6 run --env SCENARIO=user-journey --env USERS=100 loadtest/backend/k6-users.js
//   k6 run --env K8S_KUBECONFIG_PATH=/abs/kubeconfig --env DEPLOY_CHART_PATH=/abs/chart \
//          --env SCENARIO=deploy-path loadtest/backend/k6-users.js
//   k6 run --env API_URL_2=http://localhost:8082 --env SCENARIO=fanout loadtest/backend/k6-users.js
//
// Scenarios:
//   user-journey - USERS virtual users list, open, create, change and delete
//                  their own draft stacks.
//   deploy-path  - DEPLOY_VUS virtual users (roles user and devops) deploy,
//                  stop, clean and delete a small chart on a local cluster.
//                  Needs K8S_KUBECONFIG_PATH and DEPLOY_CHART_PATH.
//   fanout       - 10 virtual users: WebSocket on API_URL_2, create on
//                  API_URL, time until the events arrive on the other
//                  replica. Needs API_URL_2.
//
// Users: each enabled scenario gets its own users (USERS + DEPLOY_VUS + 10),
// so scenarios never share a user. A VU uses users[(__VU-1) % n] of its
// scenario. __VU counts over all scenarios, so two VUs of one scenario can
// share a user; each VU only touches instances with its own name prefix.
//
// Environment variables:
//   API_URL             - Backend URL (default: http://localhost:8081)
//   API_URL_2           - Second replica (default: empty = no fanout scenario)
//   ADMIN_USERNAME      - Admin user (default: admin)
//   ADMIN_PASSWORD      - Admin password (default: admin)
//   SCENARIO            - Run only this scenario (default: all enabled)
//   USERS               - Users and VUs of user-journey (default: 40)
//   USER_MIX            - Role weights user:devops:admin (default: 30:8:2);
//                         deploy-path uses only the user and devops weights
//   K8S_KUBECONFIG_PATH - Absolute path of a single-context kubeconfig (default: empty = no deploy-path)
//   DEPLOY_CHART_PATH   - Absolute path of a small local Helm chart (required with K8S_KUBECONFIG_PATH)
//   DEPLOY_VUS          - VUs of deploy-path (default: 10)
//   DEPLOY_ITERATIONS   - Deploy cycles per VU (default: 3)

import http from 'k6/http';
import ws from 'k6/ws';
import { check, group, sleep } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';
import { ensureDefaultCluster } from './lib/cluster.js';
import {
  adminLogin, createUsers, deleteUsers, newRunId, pickUser, rolePlan, usersOfGroup,
} from './lib/users.js';

// ── Config ──────────────────────────────────────────────────────────
const BASE_URL = __ENV.API_URL || 'http://localhost:8081';
const BASE_URL_2 = __ENV.API_URL_2 || '';
const ADMIN_USER = __ENV.ADMIN_USERNAME || 'admin';
const ADMIN_PASS = __ENV.ADMIN_PASSWORD || 'admin';
const KUBECONFIG_PATH = __ENV.K8S_KUBECONFIG_PATH || '';
const CHART_PATH = __ENV.DEPLOY_CHART_PATH || '';

function intEnv(name, def) {
  const raw = __ENV[name];
  if (raw === undefined || raw === '') return def;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1) throw new Error(`${name} must be a positive integer (got "${raw}")`);
  return n;
}

const USERS = intEnv('USERS', 40);
const USER_MIX = __ENV.USER_MIX || '30:8:2';
const DEPLOY_VUS = intEnv('DEPLOY_VUS', 10);
const DEPLOY_ITERATIONS = intEnv('DEPLOY_ITERATIONS', 3);

const FANOUT_VUS = 10;

// Fail at once on a bad mix, not after the users exist.
rolePlan(USERS, USER_MIX);
// deploy-path: the user and devops weights only.
const DEPLOY_MIX = `${USER_MIX.split(':').slice(0, 2).join(':')}:0`;

if (KUBECONFIG_PATH && !CHART_PATH) {
  throw new Error('DEPLOY_CHART_PATH is required with K8S_KUBECONFIG_PATH');
}
for (const [name, value] of [['K8S_KUBECONFIG_PATH', KUBECONFIG_PATH], ['DEPLOY_CHART_PATH', CHART_PATH]]) {
  if (value && !value.startsWith('/')) throw new Error(`${name} must be an absolute path (got "${value}")`);
}

const DEPLOY_CLUSTER = 'k6-local';
// Poll interval and time limit of each deploy, stop and clean.
const POLL_SECONDS = 2;
const OPERATION_TIMEOUT_MS = 5 * 60 * 1000;
// Fanout: an event that is not on the socket after this time is missed.
const FANOUT_WAIT_MS = 10000;

// ── Scenario Selection ─────────────────────────────────────────────
const SCENARIOS = ['user-journey', 'deploy-path', 'fanout'];
const AVAILABLE = {
  'user-journey': true,
  'deploy-path': KUBECONFIG_PATH !== '' && CHART_PATH !== '',
  fanout: BASE_URL_2 !== '',
};
const NEEDS = {
  'deploy-path': 'K8S_KUBECONFIG_PATH and DEPLOY_CHART_PATH',
  fanout: 'API_URL_2',
};
const SELECTED = __ENV.SCENARIO || '';
if (SELECTED !== '' && !SCENARIOS.includes(SELECTED)) {
  throw new Error(`Unknown SCENARIO "${SELECTED}". Use one of: ${SCENARIOS.join(', ')}`);
}
if (SELECTED !== '' && !AVAILABLE[SELECTED]) {
  throw new Error(`SCENARIO ${SELECTED} needs ${NEEDS[SELECTED]}`);
}
function isEnabled(name) {
  return AVAILABLE[name] && (SELECTED === '' || SELECTED === name);
}

if (isEnabled('deploy-path') && DEPLOY_MIX.split(':').every((w) => Number(w) === 0)) {
  throw new Error('deploy-path needs a user or devops weight above 0 in USER_MIX');
}

// One group of users for each enabled scenario: j (journey), d (deploy), f (fanout).
const GROUPS = { 'user-journey': 'j', 'deploy-path': 'd', fanout: 'f' };
const USER_PLAN = [
  ...(isEnabled('user-journey') ? rolePlan(USERS, USER_MIX).map((role) => ({ role, group: 'j' })) : []),
  ...(isEnabled('deploy-path') ? rolePlan(DEPLOY_VUS, DEPLOY_MIX).map((role) => ({ role, group: 'd' })) : []),
  ...(isEnabled('fanout') ? rolePlan(FANOUT_VUS, USER_MIX).map((role) => ({ role, group: 'f' })) : []),
];

// ── Custom Metrics ──────────────────────────────────────────────────
const errorRate = new Rate('errors');
const apiCalls = new Counter('api_calls');
// 401 and 403 that the test did not expect: a user lost access.
const authFailureRate = new Rate('auth_failure_rate');
const authFailures = new Counter('auth_failures');
// 503 answers (tag name): /pods and /status accept them, but they count here.
const unavailable = new Counter('http_503');

const journeyList = new Trend('journey_list_duration', true);
const journeyStackPage = new Trend('journey_stack_page_duration', true);
const journeyStackPageLoad = new Trend('journey_stack_page_load', true);
const journeyWrite = new Trend('journey_write_duration', true);
const journeyNotifications = new Trend('journey_notification_duration', true);
const journeyAnalytics = new Trend('journey_analytics_duration', true);
const journeyIteration = new Trend('journey_iteration_duration', true);
const instanceLimitHits = new Counter('instance_limit_hits');

const deployApi = new Trend('deploy_api_duration', true);
const deployDuration = new Trend('deploy_duration', true);
const deployServerDuration = new Trend('deploy_server_duration', true);
const stopDuration = new Trend('stop_duration', true);
const cleanDuration = new Trend('clean_duration', true);
const deployCycle = new Trend('deploy_cycle_duration', true);
const deploySuccessRate = new Rate('deploy_success_rate');
const deploySuccess = new Counter('deploy_success');
const deployError = new Counter('deploy_error');
const deployTimeout = new Counter('deploy_timeout');
const lifecycleFailures = new Counter('lifecycle_failures');

const fanoutApi = new Trend('fanout_api_duration', true);
const wsConnect = new Trend('ws_connect_duration', true);
const wsErrors = new Rate('ws_errors');
const wsFanoutLag = new Trend('ws_fanout_lag', true);
const wsFanoutMissed = new Counter('ws_fanout_missed');
const wsNotificationLag = new Trend('ws_notification_lag', true);
const wsNotificationMissed = new Counter('ws_notification_missed');
const wsMessages = new Counter('ws_messages_received');

// ── Options ─────────────────────────────────────────────────────────
const scenarios = {};
if (isEnabled('user-journey')) {
  scenarios['user-journey'] = {
    executor: 'ramping-vus',
    startVUs: 0,
    stages: [
      { duration: '1m', target: USERS },
      { duration: '4m', target: USERS },
      { duration: '30s', target: 0 },
    ],
    gracefulRampDown: '30s',
    exec: 'userJourney',
    tags: { scenario: 'user-journey' },
  };
}
if (isEnabled('deploy-path')) {
  scenarios['deploy-path'] = {
    executor: 'per-vu-iterations',
    vus: DEPLOY_VUS,
    iterations: DEPLOY_ITERATIONS,
    // Three operations of at most 5 minutes each, plus the delete.
    maxDuration: `${DEPLOY_ITERATIONS * 20}m`,
    exec: 'deployPath',
    tags: { scenario: 'deploy-path' },
  };
}
if (isEnabled('fanout')) {
  scenarios.fanout = {
    executor: 'constant-vus',
    vus: FANOUT_VUS,
    duration: '5m',
    exec: 'fanoutTest',
    tags: { scenario: 'fanout' },
  };
}

const thresholds = {
  errors: ['rate<0.05'],
  auth_failure_rate: ['rate<0.001'],
};
if (isEnabled('user-journey')) {
  Object.assign(thresholds, {
    'http_req_duration{scenario:user-journey}': ['p(95)<500', 'p(99)<1000'],
    journey_list_duration: ['p(95)<450'],
    journey_stack_page_duration: ['p(95)<400'],
    journey_stack_page_load: ['p(95)<1000'],
    journey_write_duration: ['p(95)<500'],
    journey_notification_duration: ['p(95)<300'],
    journey_analytics_duration: ['p(95)<600'],
  });
}
if (isEnabled('deploy-path')) {
  Object.assign(thresholds, {
    deploy_api_duration: ['p(95)<1000'],
    deploy_duration: ['p(95)<180000'],
    deploy_success_rate: ['rate>0.9'],
    deploy_timeout: ['count<1'],
  });
}
if (isEnabled('fanout')) {
  Object.assign(thresholds, {
    ws_errors: ['rate<0.01'],
    ws_connect_duration: ['p(95)<1000'],
    ws_fanout_lag: ['p(95)<1500'],
    ws_fanout_missed: ['count<1'],
  });
}

export const options = {
  scenarios,
  thresholds,
  setupTimeout: '10m',
  teardownTimeout: '15m',
};

// ── W3C Trace Context ──────────────────────────────────────────────
// Links k6 requests to backend traces when OTEL_ENABLED=true.
function traceparent() {
  const hex = (n) => [...Array(n)].map(() => Math.floor(Math.random() * 16).toString(16)).join('');
  return `00-${hex(32)}-${hex(16)}-01`;
}

function keyHeaders(apiKey) {
  return { 'Content-Type': 'application/json', 'X-API-Key': apiKey, traceparent: traceparent() };
}

function jwtHeaders(token) {
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, traceparent: traceparent() };
}

// ── Request Helpers ────────────────────────────────────────────────
// record checks the status and adds the metrics of one response.
function record(res, name, expect, trend) {
  apiCalls.add(1);
  if (trend) trend.add(res.timings.duration);
  const ok = check(res, { [`${name} ${expect.join(' or ')}`]: (r) => expect.includes(r.status) });
  errorRate.add(!ok);
  const authFail = (res.status === 401 || res.status === 403) && !expect.includes(res.status);
  authFailureRate.add(authFail);
  if (authFail) authFailures.add(1, { name });
  if (res.status === 503) unavailable.add(1, { name });
  return res;
}

// api sends one request with the API key of the user.
function api(method, url, body, key, name, expect, trend) {
  const res = http.request(method, url, body === null || body === undefined ? null : JSON.stringify(body), {
    headers: keyHeaders(key),
    tags: { name },
    responseCallback: http.expectedStatuses(...expect),
  });
  return record(res, name, expect, trend);
}

// apiBatch sends the requests at the same time. Each request is
// [method, url, body, name, expected statuses].
function apiBatch(key, requests, trend) {
  const responses = http.batch(requests.map(([method, url, body, name, expect]) => [
    method,
    url,
    body === null || body === undefined ? null : JSON.stringify(body),
    { headers: keyHeaders(key), tags: { name }, responseCallback: http.expectedStatuses(...expect) },
  ]));
  return responses.map((res, i) => record(res, requests[i][3], requests[i][4], trend));
}

function json(res, path) {
  try {
    return res.json(path);
  } catch (e) {
    return undefined;
  }
}

function think(min, max) {
  sleep(min + Math.random() * (max - min));
}

// ── Setup & Teardown ────────────────────────────────────────────────
export function setup() {
  const state = { runId: newRunId(), users: [], groups: {}, journey: null, deploy: null };
  try {
    const token = adminLogin(BASE_URL, ADMIN_USER, ADMIN_PASS);
    if (!token) throw new Error('setup: admin login failed');
    const headers = jwtHeaders(token);

    if (isEnabled('deploy-path')) setupDeploy(headers, state);
    if (isEnabled('user-journey') || isEnabled('fanout')) setupJourney(headers, state);

    createUsers(BASE_URL, headers, USER_PLAN, state.runId, state.users);
    for (const group of Object.values(GROUPS)) state.groups[group] = usersOfGroup(state.users, group);
    const counts = state.users.reduce((acc, u) => {
      const key = `${u.group}:${u.role}`;
      return { ...acc, [key]: (acc[key] || 0) + 1 };
    }, {});
    console.log(`setup: run ${state.runId}, ${state.users.length} users (group:role) ${JSON.stringify(counts)}`);
    return state;
  } catch (e) {
    // k6 runs no teardown after a failed setup: delete what exists.
    console.error(`setup failed, cleaning up: ${e.message}`);
    cleanupRun(state);
    throw e;
  }
}

// clusterById returns the cluster with the ID (admin list), or undefined.
function clusterById(headers, id) {
  const res = http.get(`${BASE_URL}/api/v1/clusters`, { headers, tags: { name: 'GET /clusters (setup)' } });
  if (res.status !== 200) throw new Error(`setup: GET /clusters failed (status ${res.status})`);
  return (res.json() || []).find((c) => c.id === id || (id === null && c.name === DEPLOY_CLUSTER));
}

// createDefinition creates a definition with one chart and records it in
// target.definitionId at once (for the cleanup after a failure).
function createDefinition(headers, target, name, chart) {
  const defRes = http.post(
    `${BASE_URL}/api/v1/stack-definitions`,
    JSON.stringify({ name, description: 'Created by k6-users.js', default_branch: 'main' }),
    { headers, tags: { name: 'POST /stack-definitions (setup)' } },
  );
  if (defRes.status !== 201) {
    throw new Error(`setup: create definition ${name} failed (status ${defRes.status}): ${defRes.body}`);
  }
  target.definitionId = defRes.json('id');
  const chartRes = http.post(
    `${BASE_URL}/api/v1/stack-definitions/${target.definitionId}/charts`,
    JSON.stringify(chart),
    { headers, tags: { name: 'POST /stack-definitions/:id/charts (setup)' } },
  );
  if (chartRes.status !== 201) {
    throw new Error(`setup: add chart to ${name} failed (status ${chartRes.status}): ${chartRes.body}`);
  }
  target.chartId = chartRes.json('id');
}

// setupDeploy registers the local cluster (or reuses it) and a definition
// with the local chart.
function setupDeploy(headers, state) {
  state.deploy = { clusterId: '', clusterCreated: false, maxPerUser: 0, definitionId: '', chartId: '' };
  // A default cluster first: the backend makes the first cluster the
  // default, and k6-local must never be it (teardown deletes it).
  ensureDefaultCluster(BASE_URL, headers);
  const existing = clusterById(headers, null);
  if (existing) {
    state.deploy.clusterId = existing.id;
    state.deploy.maxPerUser = existing.max_instances_per_user || 0;
    // Point it at the kubeconfig of this run.
    const update = { kubeconfig_path: KUBECONFIG_PATH, use_in_cluster: false };
    if (!existing.api_server_url) update.api_server_url = 'https://127.0.0.1:6443';
    const res = http.put(`${BASE_URL}/api/v1/clusters/${existing.id}`, JSON.stringify(update), {
      headers,
      tags: { name: 'PUT /clusters/:id (setup)' },
    });
    if (res.status !== 200) {
      throw new Error(`setup: update cluster ${DEPLOY_CLUSTER} failed (status ${res.status}): ${res.body}`);
    }
    console.log(`setup: reuse cluster ${DEPLOY_CLUSTER} (${existing.id})`);
  } else {
    // The backend reads the server URL from the kubeconfig; api_server_url
    // is only shown in the UI. k6 does not read the kubeconfig.
    const res = http.post(
      `${BASE_URL}/api/v1/clusters`,
      JSON.stringify({
        name: DEPLOY_CLUSTER,
        description: 'Local cluster of the k6 deploy-path load test.',
        api_server_url: 'https://127.0.0.1:6443',
        kubeconfig_path: KUBECONFIG_PATH,
      }),
      { headers, tags: { name: 'POST /clusters (setup)' } },
    );
    if (res.status !== 201) {
      throw new Error(`setup: create cluster ${DEPLOY_CLUSTER} failed (status ${res.status}): ${res.body}`);
    }
    state.deploy.clusterId = res.json('id');
    state.deploy.clusterCreated = true;
  }

  const test = http.post(`${BASE_URL}/api/v1/clusters/${state.deploy.clusterId}/test`, null, {
    headers,
    tags: { name: 'POST /clusters/:id/test (setup)' },
  });
  if (test.status !== 200) {
    throw new Error(`setup: the backend cannot reach ${DEPLOY_CLUSTER} with K8S_KUBECONFIG_PATH (status ${test.status}): ${test.body}`);
  }

  createDefinition(headers, state.deploy, `k6-deploy-${state.runId}`, {
    chart_name: 'k6-tiny',
    chart_path: CHART_PATH,
    default_values: '',
  });
}

// setupJourney creates the definition of user-journey and fanout. The
// instances go to the deploy cluster when it exists, else to the default.
function setupJourney(headers, state) {
  state.journey = { clusterId: '', maxPerUser: 0, definitionId: '', chartId: '' };
  if (state.deploy) {
    state.journey.clusterId = state.deploy.clusterId;
    state.journey.maxPerUser = state.deploy.maxPerUser;
  } else {
    const cluster = ensureDefaultCluster(BASE_URL, headers);
    state.journey.clusterId = cluster.id;
    const full = clusterById(headers, cluster.id);
    state.journey.maxPerUser = (full && full.max_instances_per_user) || 0;
  }
  if (state.journey.maxPerUser > 0) {
    console.log(`setup: cluster limit ${state.journey.maxPerUser} instances per user`);
  }
  // A role user cannot create definitions, so all users share this one.
  createDefinition(headers, state.journey, `k6-users-${state.runId}`, {
    chart_name: 'k6-app',
    repository_url: 'https://charts.example.com/k6-app',
    chart_version: '1.0.0',
    default_values: 'replicaCount: 1\nimage:\n  tag: latest',
  });
}

export function teardown(data) {
  if (!data) return;
  cleanupRun(data);
}

// cleanupRun deletes everything the run created. It works with a partial state.
function cleanupRun(state) {
  const token = adminLogin(BASE_URL, ADMIN_USER, ADMIN_PASS);
  if (!token) {
    console.warn(`cleanup: admin login failed; delete the users k6u-${state.runId}-* by hand`);
    return;
  }
  const headers = jwtHeaders(token);

  const definitions = [state.journey, state.deploy].filter((t) => t && t.definitionId).map((t) => t.definitionId);
  for (const defId of definitions) {
    const left = deleteInstancesOf(headers, defId);
    if (left < 0) console.warn(`cleanup: could not list the instances of definition ${defId}; the delete may fail`);
    else if (left > 0) console.warn(`cleanup: ${left} instances of definition ${defId} remain`);
    const res = http.del(`${BASE_URL}/api/v1/stack-definitions/${defId}`, null, {
      headers,
      tags: { name: 'DELETE /stack-definitions/:id (teardown)' },
    });
    if (res.status !== 204 && res.status !== 404) {
      console.warn(`cleanup: delete definition ${defId} failed (status ${res.status})`);
    }
  }

  const failed = deleteUsers(BASE_URL, headers, state.users);
  console.log(`cleanup: deleted ${state.users.length - failed} of ${state.users.length} users`);

  if (state.deploy && state.deploy.clusterCreated && state.deploy.clusterId) {
    let status = 0;
    for (let attempt = 0; attempt < 6; attempt++) {
      status = http.del(`${BASE_URL}/api/v1/clusters/${state.deploy.clusterId}`, null, {
        headers,
        tags: { name: 'DELETE /clusters/:id (teardown)' },
      }).status;
      if (status !== 409) break;
      sleep(10); // an instance delete still runs
    }
    console.log(`cleanup: delete cluster ${DEPLOY_CLUSTER} (status ${status})`);
  }
}

// deleteInstancesOf deletes all instances of the definition (all owners).
// An instance with cluster resources is cleaned first (202); one in a
// transient state answers 409. Both are tried again until the time limit.
// Returns the number of instances left.
function deleteInstancesOf(headers, defId) {
  const deadline = Date.now() + 10 * 60 * 1000;
  let left = 0;
  for (;;) {
    const res = http.get(`${BASE_URL}/api/v1/stack-instances?definition_id=${defId}&pageSize=100`, {
      headers,
      tags: { name: 'GET /stack-instances?definition_id (teardown)' },
    });
    if (res.status !== 200) return -1;
    const instances = json(res, 'data') || [];
    left = json(res, 'total') || instances.length;
    if (left === 0 || Date.now() > deadline) return left;
    const busy = ['deploying', 'queued', 'stabilizing', 'stopping', 'cleaning'];
    const ready = instances.filter((i) => !busy.includes(i.status));
    if (ready.length > 0) {
      http.batch(ready.map((i) => [
        'DELETE',
        `${BASE_URL}/api/v1/stack-instances/${i.id}`,
        null,
        { headers, tags: { name: 'DELETE /stack-instances/:id (teardown)' } },
      ]));
    }
    // Drafts go at once; a clean before a delete takes a while.
    sleep(ready.length < instances.length || instances.some((i) => i.status !== 'draft') ? 5 : 1);
  }
}

// ── Shared Instance Helpers ────────────────────────────────────────
// vuPrefix is the name prefix of the instances of this VU in a scenario.
function vuPrefix(runId, tag) {
  return `k6-${runId}-${tag}${__VU}-`;
}

// freeCapacity keeps the user below the instance limit of the cluster: it
// deletes the instances with the prefix of this VU that an earlier,
// stopped iteration left behind. It never touches other instances.
// Returns false when the user is still at the limit.
function freeCapacity(key, prefix, clusterId, limit, trend) {
  if (!limit) return true;
  const res = api('GET', `${BASE_URL}/api/v1/stack-instances?owner=me&cluster_id=${clusterId}&pageSize=100`, null, key,
    'GET /stack-instances?owner=me&cluster_id (capacity)', [200], trend);
  if (res.status !== 200) return true;
  const own = json(res, 'data') || [];
  let count = json(res, 'total') || own.length;
  if (count < limit) return true;
  const busy = ['deploying', 'queued', 'stabilizing', 'stopping', 'cleaning'];
  const leftovers = own.filter((i) => i.name.startsWith(prefix) && !busy.includes(i.status));
  for (const inst of leftovers) {
    if (count < limit) break;
    // 202: a clean runs first, the slot is free later.
    const del = api('DELETE', `${BASE_URL}/api/v1/stack-instances/${inst.id}`, null, key,
      'DELETE /stack-instances/:id (capacity)', [202, 204, 404], trend);
    if (del.status === 204 || del.status === 404) count--;
  }
  if (count >= limit) {
    instanceLimitHits.add(1);
    return false;
  }
  return true;
}

// createInstance returns the new instance, or null. A 409 for the
// instance limit counts in instance_limit_hits, not as an error.
function createInstance(baseUrl, key, body, name, trend) {
  const res = http.post(`${baseUrl}/api/v1/stack-instances`, JSON.stringify(body), {
    headers: keyHeaders(key),
    tags: { name },
    responseCallback: http.expectedStatuses(201),
  });
  if (res.status === 409 && String(json(res, 'error') || '').startsWith('Maximum instances per user')) {
    apiCalls.add(1);
    instanceLimitHits.add(1);
    return null;
  }
  record(res, name, [201], trend);
  return res.status === 201 ? res.json() : null;
}

// ── User Journey ───────────────────────────────────────────────────
// Statuses of /pods and /status: 502 without cluster access (the in-cluster
// default outside a pod), 404 or 200 for a draft on a reachable cluster.
const CLUSTER_DEPENDENT = [200, 404, 502, 503];

// stackPage sends the requests of the web UI for an instance page
// (the stack-page scenario of k6-api.js).
function stackPage(key, id) {
  const inst = `${BASE_URL}/api/v1/stack-instances/${id}`;
  const start = Date.now();
  const first = apiBatch(key, [
    ['GET', inst, null, 'GET /stack-instances/:id', [200]],
    ['GET', `${inst}/deploy-log?view=summary&limit=10`, null, 'GET /stack-instances/:id/deploy-log (summary)', [200]],
    ['GET', `${inst}/deploy-log?view=summary&action=deploy&status=success&limit=50`, null,
      'GET /stack-instances/:id/deploy-log (rollback targets)', [200]],
    ['GET', `${inst}/quota`, null, 'GET /stack-instances/:id/quota', [200]],
    ['GET', `${inst}/actions`, null, 'GET /stack-instances/:id/actions', [200]],
    ['GET', `${inst}/action-runs`, null, 'GET /stack-instances/:id/action-runs', [200]],
    ['GET', `${BASE_URL}/api/v1/favorites/check?entity_type=instance&entity_id=${id}`, null, 'GET /favorites/check', [200]],
    ['GET', `${BASE_URL}/api/v1/notifications/count`, null, 'GET /notifications/count', [200]],
  ], journeyStackPage);
  if (first[0].status === 200) {
    const defId = json(first[0], 'stack_definition_id');
    apiBatch(key, [
      ['GET', `${BASE_URL}/api/v1/stack-definitions/${defId}`, null, 'GET /stack-definitions/:id', [200]],
      ['GET', `${inst}/overrides`, null, 'GET /stack-instances/:id/overrides', [200]],
      ['GET', `${inst}/branches`, null, 'GET /stack-instances/:id/branches', [200]],
    ], journeyStackPage);
    apiBatch(key, [
      ['GET', `${inst}/pods`, null, 'GET /stack-instances/:id/pods', CLUSTER_DEPENDENT],
      ['GET', `${inst}/status`, null, 'GET /stack-instances/:id/status', CLUSTER_DEPENDENT],
    ], journeyStackPage);
  }
  journeyStackPageLoad.add(Date.now() - start);
}

export function userJourney(data) {
  const user = pickUser(data.groups.j, __VU);
  if (!user || !data.journey) {
    sleep(1);
    return;
  }
  const key = user.key;
  const j = data.journey;
  const start = Date.now();

  group('Home', () => {
    apiBatch(key, [
      ['GET', `${BASE_URL}/api/v1/stack-instances?owner=me&page=1&pageSize=25`, null, 'GET /stack-instances?owner=me', [200]],
      ['GET', `${BASE_URL}/api/v1/stack-instances/recent`, null, 'GET /stack-instances/recent', [200]],
      ['GET', `${BASE_URL}/api/v1/dashboard`, null, 'GET /dashboard', [200]],
      ['GET', `${BASE_URL}/api/v1/notifications/count`, null, 'GET /notifications/count', [200]],
    ], journeyList);
  });
  think(0.5, 1.5);

  const prefix = vuPrefix(data.runId, 'j');
  if (!freeCapacity(key, prefix, j.clusterId, j.maxPerUser, journeyList)) {
    sleep(2);
    return;
  }

  let inst = null;
  group('Create Instance', () => {
    inst = createInstance(BASE_URL, key, {
      stack_definition_id: j.definitionId,
      name: `${prefix}${__ITER}`,
      branch: 'main',
      cluster_id: j.clusterId,
      ttl_minutes: 120,
    }, 'POST /stack-instances', journeyWrite);
  });
  if (!inst) {
    sleep(1);
    return;
  }
  const url = `${BASE_URL}/api/v1/stack-instances/${inst.id}`;

  try {
    group('Stack Page', () => stackPage(key, inst.id));
    think(0.5, 1.5);

    group('Edit Instance', () => {
      api('PUT', `${url}/overrides/${j.chartId}`, { values: `replicaCount: 2\nimage:\n  tag: k6-${__ITER}` }, key,
        'PUT /stack-instances/:id/overrides/:chartId', [200], journeyWrite);
      api('PUT', `${url}/branches/${j.chartId}`, { branch: `feature/k6-${__VU}` }, key,
        'PUT /stack-instances/:id/branches/:chartId', [200], journeyWrite);
      api('POST', `${url}/extend`, { minutes: 30 }, key, 'POST /stack-instances/:id/extend', [200], journeyWrite);
      api('POST', `${url}/follow`, null, key, 'POST /stack-instances/:id/follow', [200], journeyWrite);
      api('DELETE', `${url}/follow`, null, key, 'DELETE /stack-instances/:id/follow', [200], journeyWrite);
    });
    think(0.5, 1.5);

    group('Notifications', () => {
      const list = api('GET', `${BASE_URL}/api/v1/notifications?unread_only=true&limit=20&offset=0`, null, key,
        'GET /notifications', [200], journeyNotifications);
      const unread = (list.status === 200 && json(list, 'notifications')) || [];
      if (unread.length > 0) {
        api('POST', `${BASE_URL}/api/v1/notifications/${unread[0].id}/read`, null, key,
          'POST /notifications/:id/read', [200], journeyNotifications);
      }
      if (__ITER % 5 === 4) {
        api('POST', `${BASE_URL}/api/v1/notifications/read-all`, null, key,
          'POST /notifications/read-all', [200], journeyNotifications);
      }
    });

    if (user.role === 'devops' || user.role === 'admin') {
      group('Analytics', () => {
        const requests = [
          ['GET', `${BASE_URL}/api/v1/analytics/overview`, null, 'GET /analytics/overview', [200]],
          ['GET', `${BASE_URL}/api/v1/analytics/templates`, null, 'GET /analytics/templates', [200]],
          ['GET', `${BASE_URL}/api/v1/audit-logs?limit=50&offset=0`, null, 'GET /audit-logs', [200]],
        ];
        if (user.role === 'admin') {
          requests.push(['GET', `${BASE_URL}/api/v1/analytics/users`, null, 'GET /analytics/users', [200]]);
        }
        apiBatch(key, requests, journeyAnalytics);
      });
    }
    think(0.5, 1.5);
  } finally {
    // A draft is deleted at once (204).
    api('DELETE', url, null, key, 'DELETE /stack-instances/:id', [204], journeyWrite);
  }

  journeyIteration.add(Date.now() - start);
  think(1, 3);
}

// ── Deploy Path ────────────────────────────────────────────────────
// waitForStatus polls the instance until its status is one of targets.
// Returns {status, timedOut, elapsed}; status "gone" after a 404.
function waitForStatus(key, id, targets, name) {
  const start = Date.now();
  let status = '';
  while (Date.now() - start < OPERATION_TIMEOUT_MS) {
    sleep(POLL_SECONDS);
    const res = api('GET', `${BASE_URL}/api/v1/stack-instances/${id}`, null, key, name, [200, 404], deployApi);
    if (res.status === 404) return { status: 'gone', timedOut: false, elapsed: Date.now() - start };
    if (res.status === 200) status = json(res, 'status');
    if (targets.includes(status)) return { status, timedOut: false, elapsed: Date.now() - start };
  }
  return { status, timedOut: true, elapsed: Date.now() - start };
}

function lifecycleFailed(phase, detail) {
  lifecycleFailures.add(1, { phase });
  console.warn(`deploy-path VU ${__VU}: ${phase} ${detail}`);
}

// removeInstance stops, cleans and deletes the instance, from any status.
function removeInstance(key, id, status) {
  const url = `${BASE_URL}/api/v1/stack-instances/${id}`;
  let current = status;

  if (['running', 'partial', 'deploying', 'stabilizing'].includes(current)) {
    const t0 = Date.now();
    const res = api('POST', `${url}/stop`, null, key, 'POST /stack-instances/:id/stop', [202], deployApi);
    if (res.status === 202) {
      const r = waitForStatus(key, id, ['stopped', 'error'], 'GET /stack-instances/:id (poll stop)');
      current = r.status;
      if (r.timedOut) lifecycleFailed('stop', 'timed out');
      else if (current === 'stopped') stopDuration.add(Date.now() - t0);
      else lifecycleFailed('stop', `ended with status ${current}`);
    } else {
      lifecycleFailed('stop', `answered ${res.status}`);
    }
  }

  if (['running', 'partial', 'stopped', 'error'].includes(current)) {
    const t0 = Date.now();
    const res = api('POST', `${url}/clean`, null, key, 'POST /stack-instances/:id/clean', [202], deployApi);
    if (res.status === 202) {
      const r = waitForStatus(key, id, ['draft', 'error'], 'GET /stack-instances/:id (poll clean)');
      current = r.status;
      if (r.timedOut) lifecycleFailed('clean', 'timed out');
      else if (current === 'draft') cleanDuration.add(Date.now() - t0);
      else lifecycleFailed('clean', `ended with status ${current}`);
    } else {
      lifecycleFailed('clean', `answered ${res.status}`);
    }
  }

  if (current === 'gone') return;
  // 204 for a draft; 202 cleans first and deletes later (teardown checks).
  const del = api('DELETE', url, null, key, 'DELETE /stack-instances/:id', [202, 204], deployApi);
  if (del.status !== 202 && del.status !== 204) lifecycleFailed('delete', `answered ${del.status}`);
}

export function deployPath(data) {
  const user = pickUser(data.groups.d, __VU);
  if (!user || !data.deploy) {
    sleep(1);
    return;
  }
  const key = user.key;
  const d = data.deploy;
  const cycleStart = Date.now();

  const prefix = vuPrefix(data.runId, 'd');
  if (!freeCapacity(key, prefix, d.clusterId, d.maxPerUser, deployApi)) {
    lifecycleFailed('create', 'instance limit reached');
    return;
  }
  const inst = createInstance(BASE_URL, key, {
    stack_definition_id: d.definitionId,
    name: `${prefix}${__ITER}`,
    branch: 'main',
    cluster_id: d.clusterId,
    ttl_minutes: 60,
  }, 'POST /stack-instances (deploy-path)', deployApi);
  if (!inst) {
    lifecycleFailed('create', 'failed');
    deploySuccessRate.add(false);
    return;
  }
  const url = `${BASE_URL}/api/v1/stack-instances/${inst.id}`;
  let status = 'draft';

  try {
    group('Deploy', () => {
      const t0 = Date.now();
      const res = api('POST', `${url}/deploy`, null, key, 'POST /stack-instances/:id/deploy', [202], deployApi);
      if (res.status !== 202) {
        deployError.add(1);
        deploySuccessRate.add(false);
        return;
      }
      const logId = json(res, 'log_id');
      const r = waitForStatus(key, inst.id, ['running', 'partial', 'error'], 'GET /stack-instances/:id (poll deploy)');
      status = r.status;
      if (r.timedOut) {
        deployTimeout.add(1);
        deploySuccessRate.add(false);
        console.warn(`deploy-path VU ${__VU}: deploy timed out in status ${status}`);
        return;
      }
      const ok = status === 'running';
      deploySuccessRate.add(ok);
      if (ok) {
        deploySuccess.add(1);
        deployDuration.add(Date.now() - t0);
      } else {
        deployError.add(1);
      }

      // The log has the server times. started_at is the request time, so
      // this duration includes the wait for a deploy slot.
      if (logId) {
        const log = api('GET', `${url}/deploy-log/${logId}`, null, key,
          'GET /stack-instances/:id/deploy-log/:logId', [200], deployApi);
        const started = Date.parse(json(log, 'started_at') || '');
        const completed = Date.parse(json(log, 'completed_at') || '');
        if (Number.isFinite(started) && Number.isFinite(completed)) deployServerDuration.add(completed - started);
        if (!ok) console.warn(`deploy-path VU ${__VU}: deploy ${status}: ${json(log, 'error_message') || ''}`);
      }
    });
  } finally {
    group('Stop, Clean, Delete', () => removeInstance(key, inst.id, status));
  }
  deployCycle.add(Date.now() - cycleStart);
}

// ── Fanout ─────────────────────────────────────────────────────────
// The socket is on API_URL_2, the create on API_URL. instance.created goes
// to all sockets; notification.new only to the sockets of the owner.
export function fanoutTest(data) {
  const user = pickUser(data.groups.f, __VU);
  if (!user || !data.journey) {
    sleep(1);
    return;
  }
  const key = user.key;
  const j = data.journey;
  const wsUrl = `${BASE_URL_2.replace(/^http/, 'ws')}/ws`;
  const prefix = vuPrefix(data.runId, 'f');
  if (!freeCapacity(key, prefix, j.clusterId, j.maxPerUser, fanoutApi)) {
    sleep(2);
    return;
  }

  let instanceId = null;
  let createdAt = 0;
  let gotCreated = false;
  let gotNotification = false;
  const connectStart = Date.now();

  const res = ws.connect(wsUrl, {
    headers: { 'X-API-Key': key, traceparent: traceparent() },
    tags: { name: 'GET /ws (replica 2)' },
  }, (socket) => {
    socket.on('open', () => {
      wsConnect.add(Date.now() - connectStart);
      // Messages that arrive during this blocking call wait in the queue,
      // so the lag is measured from the end of the create response.
      const inst = createInstance(BASE_URL, key, {
        stack_definition_id: j.definitionId,
        name: `${prefix}${__ITER}`,
        branch: 'main',
        cluster_id: j.clusterId,
        ttl_minutes: 60,
      }, 'POST /stack-instances (fanout)', fanoutApi);
      if (!inst) {
        socket.close();
        return;
      }
      instanceId = inst.id;
      createdAt = Date.now();
      socket.setTimeout(() => socket.close(), FANOUT_WAIT_MS);
    });

    socket.on('message', (raw) => {
      wsMessages.add(1);
      if (!instanceId) return;
      let msg;
      try {
        msg = JSON.parse(raw);
      } catch (e) {
        return;
      }
      const payload = msg.payload || {};
      if (!gotCreated && msg.type === 'instance.created' && payload.instance_id === instanceId) {
        gotCreated = true;
        wsFanoutLag.add(Math.max(0, Date.now() - createdAt));
      }
      if (!gotNotification && msg.type === 'notification.new'
        && payload.type === 'instance.created' && payload.entity_id === instanceId) {
        gotNotification = true;
        wsNotificationLag.add(Math.max(0, Date.now() - createdAt));
      }
      if (gotCreated && gotNotification) socket.close();
    });

    socket.on('error', (e) => {
      wsErrors.add(1);
      console.error(`fanout VU ${__VU}: WebSocket error: ${e.error()}`);
    });
  });

  const connected = check(res, { 'ws (replica 2) 101': (r) => r && r.status === 101 });
  wsErrors.add(!connected);
  if (res && (res.status === 401 || res.status === 403)) {
    authFailureRate.add(true);
    authFailures.add(1, { name: 'GET /ws (replica 2)' });
  }
  if (res && res.status === 503) unavailable.add(1, { name: 'GET /ws (replica 2)' });

  if (instanceId) {
    if (!gotCreated) wsFanoutMissed.add(1);
    if (!gotNotification) wsNotificationMissed.add(1);
    api('DELETE', `${BASE_URL}/api/v1/stack-instances/${instanceId}`, null, key,
      'DELETE /stack-instances/:id (fanout)', [204], fanoutApi);
  }
  sleep(1);
}

// ── Default Function ───────────────────────────────────────────────
// k6 runs it with --vus/--duration (the CLI then replaces the scenarios).
const SCENARIO_FUNCTIONS = {
  'user-journey': userJourney,
  'deploy-path': deployPath,
  fanout: fanoutTest,
};

export default function (data) {
  (SCENARIO_FUNCTIONS[SELECTED] || userJourney)(data);
}
