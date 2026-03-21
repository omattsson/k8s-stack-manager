// k6 WebSocket Load Test for k8s-stack-manager
// Tests WebSocket connection handling under concurrent load.
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

export default function () {
  // Get a token for authenticated WebSocket (optional — WS may not require auth)
  const loginRes = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS }),
    { headers: { 'Content-Type': 'application/json' } },
  );

  let wsUrl = WS_URL;
  if (loginRes.status === 200) {
    const token = JSON.parse(loginRes.body).token;
    wsUrl = `${WS_URL}?token=${token}`;
  }

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
