// k6 Stress & Optimization Load Tests for k8s-stack-manager backend
// Targets specific bottlenecks: rate limiting, bulk ops, write contention,
// pagination, auth overhead, and sustained load (soak).
//
// Usage:
//   k6 run loadtest/backend/k6-stress.js                                    # all scenarios
//   k6 run --env SCENARIO=spike loadtest/backend/k6-stress.js               # single scenario
//   k6 run --env API_URL=http://backend:8081 loadtest/backend/k6-stress.js  # custom URL
//
// Environment variables:
//   API_URL           - Backend URL (default: http://localhost:8081)
//   ADMIN_USERNAME    - Admin user (default: admin)
//   ADMIN_PASSWORD    - Admin password (default: admin)
//   SCENARIO          - Run only this scenario (default: all)

import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Rate, Trend, Counter, Gauge } from 'k6/metrics';

// ── Custom Metrics ──────────────────────────────────────────────────
const errorRate = new Rate('errors');
const apiCalls = new Counter('api_calls');
const rateLimitHits = new Counter('rate_limit_429s');
const optimisticLockConflicts = new Counter('optimistic_lock_409s');
const bulkDuration = new Trend('bulk_operation_duration', true);
const paginationDuration = new Trend('pagination_duration', true);
const authDuration = new Trend('auth_duration', true);
const writeDuration = new Trend('write_duration', true);
const concurrentUsers = new Gauge('concurrent_users');

// ── Scenario Selection ─────────────────────────────────────────────
const SELECTED = __ENV.SCENARIO || '';

function isEnabled(name) {
  return SELECTED === '' || SELECTED === name;
}

// ── Options ─────────────────────────────────────────────────────────
export const options = {
  scenarios: {
    // 1. Rate limiter stress: burst requests to trip the per-IP sliding window
    ...(isEnabled('rate-limit') && {
      'rate-limit': {
        executor: 'constant-arrival-rate',
        rate: 200,            // 200 requests/sec (limit is 100/min = ~1.67/sec)
        timeUnit: '1s',
        duration: '30s',
        preAllocatedVUs: 50,
        maxVUs: 100,
        exec: 'rateLimitStress',
        tags: { scenario: 'rate-limit' },
      },
    }),

    // 2. Bulk operations: stress bulk deploy/stop/delete endpoints
    ...(isEnabled('bulk-ops') && {
      'bulk-ops': {
        executor: 'ramping-vus',
        startVUs: 0,
        stages: [
          { duration: '15s', target: 10 },
          { duration: '1m', target: 20 },
          { duration: '15s', target: 0 },
        ],
        startTime: isEnabled('rate-limit') ? '35s' : '0s',
        exec: 'bulkOpsStress',
        tags: { scenario: 'bulk-ops' },
      },
    }),

    // 3. Auth contention: token reuse vs re-auth under load
    ...(isEnabled('auth-contention') && {
      'auth-contention': {
        executor: 'constant-vus',
        vus: 30,
        duration: '1m',
        startTime: isEnabled('bulk-ops') ? '2m' : '0s',
        exec: 'authContentionTest',
        tags: { scenario: 'auth-contention' },
      },
    }),

    // 4. Pagination stress: large offset queries
    ...(isEnabled('pagination') && {
      pagination: {
        executor: 'ramping-vus',
        startVUs: 0,
        stages: [
          { duration: '15s', target: 15 },
          { duration: '1m30s', target: 30 },
          { duration: '15s', target: 0 },
        ],
        startTime: isEnabled('auth-contention') ? '3m10s' : '0s',
        exec: 'paginationStress',
        tags: { scenario: 'pagination' },
      },
    }),

    // 5. Write contention: concurrent updates to same resources
    ...(isEnabled('write-contention') && {
      'write-contention': {
        executor: 'constant-vus',
        vus: 20,
        duration: '1m',
        startTime: isEnabled('pagination') ? '5m10s' : '0s',
        exec: 'writeContentionTest',
        tags: { scenario: 'write-contention' },
      },
    }),

    // 6. Spike test: sharp ramp to reveal queuing / pool exhaustion
    ...(isEnabled('spike') && {
      spike: {
        executor: 'ramping-vus',
        startVUs: 0,
        stages: [
          { duration: '5s', target: 100 },   // instant spike
          { duration: '30s', target: 100 },   // hold at peak
          { duration: '5s', target: 0 },      // instant drop
          { duration: '20s', target: 0 },     // cooldown — watch for lingering errors
        ],
        startTime: isEnabled('write-contention') ? '6m15s' : '0s',
        exec: 'spikeTest',
        tags: { scenario: 'spike' },
      },
    }),

    // 7. Soak test: sustained moderate load to surface leaks
    ...(isEnabled('soak') && {
      soak: {
        executor: 'constant-vus',
        vus: 30,
        duration: '10m',
        startTime: isEnabled('spike') ? '7m15s' : '0s',
        exec: 'soakTest',
        tags: { scenario: 'soak' },
      },
    }),
  },

  thresholds: {
    http_req_duration: ['p(95)<800', 'p(99)<2000'],
    errors: ['rate<0.10'],
    bulk_operation_duration: ['p(95)<3000'],   // bulk ops are slower
    pagination_duration: ['p(95)<600'],
    auth_duration: ['p(95)<200'],
    write_duration: ['p(95)<500'],
  },
};

// ── Config ──────────────────────────────────────────────────────────
const BASE_URL = __ENV.API_URL || 'http://localhost:8081';
const ADMIN_USER = __ENV.ADMIN_USERNAME || 'admin';
const ADMIN_PASS = __ENV.ADMIN_PASSWORD || 'admin';

const jsonHeaders = { 'Content-Type': 'application/json' };

function authHeaders(token) {
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
}

// ── Helpers ─────────────────────────────────────────────────────────
function login(username, password) {
  const start = Date.now();
  const res = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ username, password }),
    { headers: jsonHeaders, tags: { name: 'POST /auth/login' } },
  );
  authDuration.add(Date.now() - start);
  apiCalls.add(1);

  if (res.status !== 200) {
    errorRate.add(true);
    return null;
  }
  errorRate.add(false);
  return JSON.parse(res.body).token;
}

function registerUser(suffix) {
  const username = `stress-user-${suffix}-${__VU}-${Date.now()}`;
  const res = http.post(
    `${BASE_URL}/api/v1/auth/register`,
    JSON.stringify({ username, password: 'stress123', display_name: `Stress ${suffix}` }),
    { headers: jsonHeaders, tags: { name: 'POST /auth/register' } },
  );
  apiCalls.add(1);
  if (res.status !== 201 && res.status !== 200) return null;
  return { username, password: 'stress123' };
}

function createDefinition(headers, suffix) {
  const res = http.post(
    `${BASE_URL}/api/v1/stack-definitions`,
    JSON.stringify({
      name: `stress-def-${suffix}`,
      description: 'Stress test definition',
      default_branch: 'main',
    }),
    { headers, tags: { name: 'POST /stack-definitions' } },
  );
  apiCalls.add(1);
  if (res.status !== 201) return null;
  return JSON.parse(res.body);
}

function createInstance(headers, defId, suffix) {
  const res = http.post(
    `${BASE_URL}/api/v1/stack-instances`,
    JSON.stringify({
      stack_definition_id: defId,
      name: `stress-inst-${suffix}`,
      branch: 'main',
      ttl_minutes: 60,
    }),
    { headers, tags: { name: 'POST /stack-instances' } },
  );
  apiCalls.add(1);
  if (res.status !== 201) return null;
  return JSON.parse(res.body);
}

function deleteResource(headers, path) {
  const res = http.del(`${BASE_URL}${path}`, null, {
    headers,
    tags: { name: `DELETE ${path.replace(/[a-f0-9-]{36}/g, ':id')}` },
  });
  apiCalls.add(1);
  return res;
}

// ═══════════════════════════════════════════════════════════════════
// 1. RATE LIMITER STRESS
// ═══════════════════════════════════════════════════════════════════
// Fires requests at 200/sec against a 100/min limit.
// Reveals: rate limiter accuracy, memory use under burst, 429 response time.

export function rateLimitStress() {
  const res = http.get(`${BASE_URL}/api/v1/ping`, {
    tags: { name: 'GET /ping (rate-limit)' },
  });
  apiCalls.add(1);

  if (res.status === 429) {
    rateLimitHits.add(1);
    // 429 is expected here — not an error
    check(res, {
      'rate limit returns 429': (r) => r.status === 429,
      'rate limit response fast': (r) => r.timings.duration < 50,
    });
  } else {
    const ok = check(res, {
      'ping 200': (r) => r.status === 200,
    });
    errorRate.add(!ok);
  }
}

// ═══════════════════════════════════════════════════════════════════
// 2. BULK OPERATIONS STRESS
// ═══════════════════════════════════════════════════════════════════
// Creates N instances then bulk-stops/deletes them.
// Reveals: DB transaction overhead, goroutine pressure, per-item error handling.

export function bulkOpsStress() {
  const token = login(ADMIN_USER, ADMIN_PASS);
  if (!token) return;
  const headers = authHeaders(token);
  const suffix = `${__VU}-${__ITER}-${Date.now()}`;

  // Create a definition + multiple instances for bulk testing
  const def = createDefinition(headers, `bulk-${suffix}`);
  if (!def) return;

  const instanceIds = [];
  const batchSize = 5 + Math.floor(Math.random() * 6); // 5-10 instances

  group('Bulk: Create instances', () => {
    for (let i = 0; i < batchSize; i++) {
      const inst = createInstance(headers, def.id, `bulk-${suffix}-${i}`);
      if (inst) instanceIds.push(inst.id);
    }
  });

  if (instanceIds.length === 0) {
    deleteResource(headers, `/api/v1/stack-definitions/${def.id}`);
    return;
  }

  // Bulk stop
  group('Bulk: Stop instances', () => {
    const res = http.post(
      `${BASE_URL}/api/v1/stack-instances/bulk/stop`,
      JSON.stringify({ ids: instanceIds }),
      { headers, tags: { name: 'POST /stack-instances/bulk/stop' } },
    );
    bulkDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, {
      'bulk stop 200': (r) => r.status === 200,
    });
    errorRate.add(!ok);

    if (res.status === 200) {
      try {
        const body = JSON.parse(res.body);
        check(body, {
          'bulk stop has results': (b) => Array.isArray(b.results),
          'bulk stop all succeeded': (b) =>
            b.results && b.results.every((r) => r.success === true),
        });
      } catch { /* non-JSON response */ }
    }
  });

  // Bulk delete
  group('Bulk: Delete instances', () => {
    const res = http.post(
      `${BASE_URL}/api/v1/stack-instances/bulk/delete`,
      JSON.stringify({ ids: instanceIds }),
      { headers, tags: { name: 'POST /stack-instances/bulk/delete' } },
    );
    bulkDuration.add(res.timings.duration);
    apiCalls.add(1);
    const ok = check(res, {
      'bulk delete 200': (r) => r.status === 200,
    });
    errorRate.add(!ok);
  });

  // Cleanup definition
  deleteResource(headers, `/api/v1/stack-definitions/${def.id}`);
  sleep(0.5);
}

// ═══════════════════════════════════════════════════════════════════
// 3. AUTH CONTENTION TEST
// ═══════════════════════════════════════════════════════════════════
// Half the VUs cache a token and reuse it (10 requests per token),
// the other half re-authenticate every request.
// Reveals: JWT parsing overhead, middleware cost, token validation scaling.

export function authContentionTest() {
  concurrentUsers.add(1);
  const reuseToken = __VU % 2 === 0;

  if (reuseToken) {
    // Strategy A: login once, reuse token for multiple requests
    const token = login(ADMIN_USER, ADMIN_PASS);
    if (!token) return;
    const headers = authHeaders(token);

    for (let i = 0; i < 10; i++) {
      const endpoint = [
        '/api/v1/templates',
        '/api/v1/stack-instances',
        '/api/v1/stack-definitions',
        '/api/v1/favorites',
        '/api/v1/notifications/count',
      ][i % 5];

      const res = http.get(`${BASE_URL}${endpoint}`, {
        headers,
        tags: { name: `GET ${endpoint} (cached-token)` },
      });
      apiCalls.add(1);
      const ok = check(res, { 'cached-token 200': (r) => r.status === 200 });
      errorRate.add(!ok);
      sleep(0.1);
    }
  } else {
    // Strategy B: re-authenticate every request (worst case)
    for (let i = 0; i < 5; i++) {
      const token = login(ADMIN_USER, ADMIN_PASS);
      if (!token) continue;
      const headers = authHeaders(token);

      const res = http.get(`${BASE_URL}/api/v1/stack-instances`, {
        headers,
        tags: { name: 'GET /stack-instances (fresh-token)' },
      });
      apiCalls.add(1);
      const ok = check(res, { 'fresh-token 200': (r) => r.status === 200 });
      errorRate.add(!ok);
      sleep(0.2);
    }
  }

  concurrentUsers.add(-1);
}

// ═══════════════════════════════════════════════════════════════════
// 4. PAGINATION STRESS
// ═══════════════════════════════════════════════════════════════════
// Hammers list endpoints with varying offsets and limits.
// Reveals: missing DB indexes, O(n) offset scans, slow COUNT queries.

export function paginationStress() {
  const token = login(ADMIN_USER, ADMIN_PASS);
  if (!token) return;
  const headers = authHeaders(token);

  const endpoints = [
    { path: '/api/v1/audit-logs', supportsOffset: true },
    { path: '/api/v1/stack-instances', supportsOffset: false },
    { path: '/api/v1/templates', supportsOffset: false },
    { path: '/api/v1/stack-definitions', supportsOffset: false },
    { path: '/api/v1/notifications', supportsOffset: true },
  ];

  for (const ep of endpoints) {
    group(`Pagination: ${ep.path}`, () => {
      // Page 1: small limit
      const page1 = http.get(
        `${BASE_URL}${ep.path}?limit=10&offset=0`,
        { headers, tags: { name: `GET ${ep.path}?limit=10&offset=0` } },
      );
      paginationDuration.add(page1.timings.duration);
      apiCalls.add(1);
      check(page1, { 'page1 200': (r) => r.status === 200 });

      // Page with large limit
      const large = http.get(
        `${BASE_URL}${ep.path}?limit=100&offset=0`,
        { headers, tags: { name: `GET ${ep.path}?limit=100` } },
      );
      paginationDuration.add(large.timings.duration);
      apiCalls.add(1);
      check(large, { 'large-limit 200': (r) => r.status === 200 });

      // Deep pagination (large offset)
      if (ep.supportsOffset) {
        const deep = http.get(
          `${BASE_URL}${ep.path}?limit=10&offset=500`,
          { headers, tags: { name: `GET ${ep.path}?offset=500` } },
        );
        paginationDuration.add(deep.timings.duration);
        apiCalls.add(1);
        check(deep, { 'deep-offset 200': (r) => r.status === 200 });
      }

      // Filtered query (common dashboard pattern)
      if (ep.path === '/api/v1/audit-logs') {
        const filtered = http.get(
          `${BASE_URL}${ep.path}?limit=50&action=create&entity_type=stack_instance`,
          { headers, tags: { name: 'GET /audit-logs?filtered' } },
        );
        paginationDuration.add(filtered.timings.duration);
        apiCalls.add(1);
        check(filtered, { 'filtered 200': (r) => r.status === 200 });
      }
    });
  }

  sleep(0.5);
}

// ═══════════════════════════════════════════════════════════════════
// 5. WRITE CONTENTION TEST
// ═══════════════════════════════════════════════════════════════════
// Multiple VUs update the same resources concurrently.
// Reveals: optimistic locking effectiveness, version mismatch handling,
// DB row-level lock contention.

export function writeContentionTest() {
  const token = login(ADMIN_USER, ADMIN_PASS);
  if (!token) return;
  const headers = authHeaders(token);

  // All VUs share a small set of definition IDs (created by first VUs)
  const sharedSuffix = `contention-${__ITER % 3}`; // Only 3 shared resources

  group('Write Contention: Create or Fetch Shared Definition', () => {
    // Try to create — if it already exists (409), fetch it instead
    const createRes = http.post(
      `${BASE_URL}/api/v1/stack-definitions`,
      JSON.stringify({
        name: `stress-shared-${sharedSuffix}`,
        description: `Shared contention target ${sharedSuffix}`,
        default_branch: 'main',
      }),
      { headers, tags: { name: 'POST /stack-definitions (contention)' } },
    );
    writeDuration.add(createRes.timings.duration);
    apiCalls.add(1);

    let defId;
    if (createRes.status === 201) {
      defId = JSON.parse(createRes.body).id;
    } else if (createRes.status === 409) {
      // Already exists — list and find it
      const listRes = http.get(`${BASE_URL}/api/v1/stack-definitions`, {
        headers,
        tags: { name: 'GET /stack-definitions (contention-lookup)' },
      });
      apiCalls.add(1);
      if (listRes.status === 200) {
        try {
          const defs = JSON.parse(listRes.body);
          const match = (Array.isArray(defs) ? defs : defs.data || [])
            .find((d) => d.name === `stress-shared-${sharedSuffix}`);
          if (match) defId = match.id;
        } catch { /* parse error */ }
      }
    }

    if (!defId) return;

    // Concurrent update: change description (triggers optimistic lock check)
    const updateRes = http.put(
      `${BASE_URL}/api/v1/stack-definitions/${defId}`,
      JSON.stringify({
        name: `stress-shared-${sharedSuffix}`,
        description: `Updated by VU ${__VU} at ${Date.now()}`,
        default_branch: 'main',
        version: 1, // May cause version mismatch if another VU updated first
      }),
      { headers, tags: { name: 'PUT /stack-definitions/:id (contention)' } },
    );
    writeDuration.add(updateRes.timings.duration);
    apiCalls.add(1);

    if (updateRes.status === 409) {
      optimisticLockConflicts.add(1);
      check(updateRes, {
        'version conflict returns 409': (r) => r.status === 409,
        'conflict response fast': (r) => r.timings.duration < 200,
      });
    } else {
      const ok = check(updateRes, {
        'update 200': (r) => r.status === 200,
      });
      errorRate.add(!ok);
    }
  });

  // Concurrent instance creation under the same definition
  group('Write Contention: Concurrent Instance Creation', () => {
    const listRes = http.get(`${BASE_URL}/api/v1/stack-definitions`, {
      headers,
      tags: { name: 'GET /stack-definitions (for-instance-create)' },
    });
    apiCalls.add(1);
    if (listRes.status !== 200) return;

    let defs;
    try {
      const body = JSON.parse(listRes.body);
      defs = Array.isArray(body) ? body : body.data || [];
    } catch { return; }

    if (defs.length === 0) return;
    const def = defs[0];

    const instSuffix = `contention-${__VU}-${__ITER}-${Date.now()}`;
    const instRes = http.post(
      `${BASE_URL}/api/v1/stack-instances`,
      JSON.stringify({
        stack_definition_id: def.id,
        name: `stress-ci-${instSuffix}`,
        branch: 'main',
        ttl_minutes: 30,
      }),
      { headers, tags: { name: 'POST /stack-instances (contention)' } },
    );
    writeDuration.add(instRes.timings.duration);
    apiCalls.add(1);

    if (instRes.status === 201) {
      const inst = JSON.parse(instRes.body);
      // Immediately delete to avoid buildup
      deleteResource(headers, `/api/v1/stack-instances/${inst.id}`);
    }
  });

  sleep(0.3);
}

// ═══════════════════════════════════════════════════════════════════
// 6. SPIKE TEST
// ═══════════════════════════════════════════════════════════════════
// Instant 0→100 VUs. Reveals: connection pool starvation, goroutine
// explosion, response time degradation under sudden load.

export function spikeTest() {
  concurrentUsers.add(1);
  const token = login(ADMIN_USER, ADMIN_PASS);
  if (!token) { concurrentUsers.add(-1); return; }
  const headers = authHeaders(token);

  // Fire a batch of parallel reads (what happens when 100 users all
  // load the dashboard at the same instant)
  const responses = http.batch([
    ['GET', `${BASE_URL}/api/v1/stack-instances`, null, { headers, tags: { name: 'batch GET /instances (spike)' } }],
    ['GET', `${BASE_URL}/api/v1/templates`, null, { headers, tags: { name: 'batch GET /templates (spike)' } }],
    ['GET', `${BASE_URL}/api/v1/favorites`, null, { headers, tags: { name: 'batch GET /favorites (spike)' } }],
    ['GET', `${BASE_URL}/api/v1/notifications/count`, null, { headers, tags: { name: 'batch GET /notif-count (spike)' } }],
    ['GET', `${BASE_URL}/api/v1/analytics/overview`, null, { headers, tags: { name: 'batch GET /analytics (spike)' } }],
  ]);

  for (const res of responses) {
    apiCalls.add(1);
    const ok = check(res, { 'spike-batch 200': (r) => r.status === 200 });
    errorRate.add(!ok);
  }

  concurrentUsers.add(-1);
  sleep(0.2);
}

// ═══════════════════════════════════════════════════════════════════
// 7. SOAK TEST
// ═══════════════════════════════════════════════════════════════════
// 30 VUs for 10 minutes. Reveals: memory leaks, connection pool
// exhaustion, GC pauses, DB connection leaks, goroutine leaks.
// Compare p50/p95/p99 at start vs end of the test.

export function soakTest() {
  const token = login(ADMIN_USER, ADMIN_PASS);
  if (!token) return;
  const headers = authHeaders(token);
  const suffix = `${__VU}-${__ITER}-${Date.now()}`;

  // Mix of reads and writes to simulate realistic sustained load
  const action = __ITER % 4;

  switch (action) {
    case 0:
      // Read-heavy: browse all list endpoints
      group('Soak: Browse All', () => {
        const endpoints = [
          '/api/v1/stack-instances',
          '/api/v1/templates',
          '/api/v1/stack-definitions',
          '/api/v1/audit-logs?limit=20',
          '/api/v1/favorites',
          '/api/v1/notifications/count',
          '/api/v1/analytics/overview',
        ];
        for (const ep of endpoints) {
          const res = http.get(`${BASE_URL}${ep}`, {
            headers,
            tags: { name: `GET ${ep.split('?')[0]} (soak)` },
          });
          apiCalls.add(1);
          const ok = check(res, { 'soak-read 200': (r) => r.status === 200 });
          errorRate.add(!ok);
        }
      });
      break;

    case 1:
      // Write: create + delete a definition
      group('Soak: Create-Delete Definition', () => {
        const def = createDefinition(headers, `soak-${suffix}`);
        if (def) {
          deleteResource(headers, `/api/v1/stack-definitions/${def.id}`);
        }
      });
      break;

    case 2:
      // Write: create instance + extend TTL + delete
      group('Soak: Instance Lifecycle', () => {
        // Use first available definition
        const listRes = http.get(`${BASE_URL}/api/v1/stack-definitions`, {
          headers,
          tags: { name: 'GET /stack-definitions (soak)' },
        });
        apiCalls.add(1);
        if (listRes.status !== 200) return;

        let defs;
        try {
          const body = JSON.parse(listRes.body);
          defs = Array.isArray(body) ? body : body.data || [];
        } catch { return; }

        if (defs.length === 0) {
          // Create one
          const def = createDefinition(headers, `soak-base-${suffix}`);
          if (!def) return;
          defs = [def];
        }

        const inst = createInstance(headers, defs[0].id, `soak-${suffix}`);
        if (!inst) return;

        // Extend TTL
        http.post(
          `${BASE_URL}/api/v1/stack-instances/${inst.id}/extend`,
          JSON.stringify({ ttl_minutes: 120 }),
          { headers, tags: { name: 'POST /extend (soak)' } },
        );
        apiCalls.add(1);

        // Get detail
        http.get(`${BASE_URL}/api/v1/stack-instances/${inst.id}`, {
          headers,
          tags: { name: 'GET /stack-instances/:id (soak)' },
        });
        apiCalls.add(1);

        // Delete
        deleteResource(headers, `/api/v1/stack-instances/${inst.id}`);
      });
      break;

    case 3:
      // Dashboard batch (parallel reads)
      group('Soak: Dashboard Batch', () => {
        const responses = http.batch([
          ['GET', `${BASE_URL}/api/v1/stack-instances`, null, { headers, tags: { name: 'batch GET /instances (soak)' } }],
          ['GET', `${BASE_URL}/api/v1/templates`, null, { headers, tags: { name: 'batch GET /templates (soak)' } }],
          ['GET', `${BASE_URL}/api/v1/notifications/count`, null, { headers, tags: { name: 'batch GET /notif-count (soak)' } }],
          ['GET', `${BASE_URL}/api/v1/analytics/overview`, null, { headers, tags: { name: 'batch GET /analytics (soak)' } }],
        ]);
        for (const res of responses) {
          apiCalls.add(1);
          const ok = check(res, { 'soak-batch 200': (r) => r.status === 200 });
          errorRate.add(!ok);
        }
      });
      break;
  }

  sleep(1 + Math.random()); // 1-2s think time
}
