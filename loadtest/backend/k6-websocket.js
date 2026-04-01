// k6 WebSocket Load Test for k8s-stack-manager
// Tests WebSocket connection handling under concurrent load.
//
// Auth strategy: setup() logs in once, creates an API key, and obtains a JWT
// token.  All VUs reuse the same token for WS connections (WebSocket requires
// JWT — API key auth is not supported on the WS upgrade path).
//
// Usage:
//   k6 run loadtest/backend/k6-websocket.js
//   k6 run --vus 100 --duration 2m loadtest/backend/k6-websocket.js

import ws from 'k6/ws';
import { check, sleep } from 'k6';
import { Rate, Counter, Trend } from 'k6/metrics';
import http from 'k6/http';

const wsErrors = new Rate('ws_errors');
const wsConnections = new Counter('ws_connections');
const wsMessages = new Counter('ws_messages_received');
const wsConnectTime = new Trend('ws_connect_duration', true);

export const options = {
  scenarios: {
    websocket_load: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '15s', target: 25 },
        { duration: '1m', target: 50 },
        { duration: '30s', target: 100 },
        { duration: '30s', target: 100 },
        { duration: '15s', target: 0 },
      ],
    },
  },
  thresholds: {
    ws_errors: ['rate<0.1'],
    ws_connect_duration: ['p(95)<1000'],
  },
};

const BASE_URL = __ENV.API_URL || 'http://localhost:8081';
const WS_URL = BASE_URL.replace(/^http/, 'ws') + '/ws';
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

  // Create an API key for cleanup (WS uses JWT, but we manage lifecycle via API key)
  const keyRes = http.post(
    `${BASE_URL}/api/v1/users/${userId}/api-keys`,
    JSON.stringify({ name: 'k6-websocket' }),
    { headers: authHeaders(token) },
  );
  if (keyRes.status !== 201 && keyRes.status !== 200) {
    throw new Error(`setup: create API key failed (status ${keyRes.status})`);
  }
  const keyBody = JSON.parse(keyRes.body);

  console.log(`setup: created API key ${keyBody.id} for user ${userId}`);
  return {
    token: token,
    apiKey: keyBody.raw_key,
    userId: userId,
    apiKeyId: keyBody.id,
  };
}

export function teardown(data) {
  if (!data || !data.apiKeyId) return;

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

export default function (data) {
  // Reuse the JWT token obtained in setup — avoids bcrypt on every VU iteration
  const wsUrl = data.token ? `${WS_URL}?token=${data.token}` : WS_URL;

  const startTime = Date.now();
  const res = ws.connect(wsUrl, {}, function (socket) {
    wsConnections.add(1);
    wsConnectTime.add(Date.now() - startTime);

    socket.on('open', () => {
      // Send a ping/subscribe message
      socket.send(JSON.stringify({ type: 'ping' }));
    });

    socket.on('message', (msg) => {
      wsMessages.add(1);
      try {
        const data = JSON.parse(msg);
        check(data, {
          'ws message has type': (d) => d.type !== undefined,
        });
      } catch {
        // Binary or non-JSON message — still counts
      }
    });

    socket.on('error', (e) => {
      wsErrors.add(1);
      console.error(`WS error VU=${__VU}: ${e.error()}`);
    });

    // Keep connection alive for 5-15 seconds (simulates real user)
    socket.setTimeout(function () {
      socket.close();
    }, 5000 + Math.random() * 10000);
  });

  const ok = check(res, {
    'ws connected': (r) => r && r.status === 101,
  });
  wsErrors.add(!ok);

  sleep(1);
}
