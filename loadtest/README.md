# Load Testing

Scripts for load testing the k8s-stack-manager backend API and frontend UI.

## Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| k6 | Backend API + WebSocket load tests | `brew install k6` or [k6.io/docs](https://k6.io/docs/get-started/installation/) |
| Playwright | Frontend browser load tests | Already installed via `cd frontend && npm install` |

The Makefile targets automatically build and start the backend in **release mode** (`GIN_MODE=release`) with a high rate limit (10,000 req/min).

Seed test data (optional, recommended for richer results):
```bash
make seed
```

## Backend Load Tests (k6)

### API Load Test

Tests all API endpoints: authentication, CRUD workflows, dashboard simulation, and batch requests.

```bash
# Default: smoke (5 VUs, 30s) + load (ramp to 50 VUs, 4.5min)
k6 run loadtest/backend/k6-api.js

# Custom VU count and duration
k6 run --vus 50 --duration 5m loadtest/backend/k6-api.js

# Custom backend URL
k6 run --env API_URL=http://backend:8081 loadtest/backend/k6-api.js
```

**Scenarios**:
- `smoke` — 5 virtual users for 30s. Health checks, auth login, ping.
- `load` — Ramps 0 → 20 → 50 → 0 over 4.5 min. Full CRUD workflows, browsing, dashboard batch.

**Thresholds**:
- `http_req_duration` p(95) < 500ms, p(99) < 1000ms
- `errors` rate < 5%

### WebSocket Load Test

Tests concurrent WebSocket connections with authenticated sessions.

```bash
k6 run loadtest/backend/k6-websocket.js
```

Ramps from 0 → 100 concurrent WebSocket connections over 2.5 minutes. Each connection authenticates, connects, listens for 5–15 seconds, then disconnects.

**Thresholds**:
- `ws_errors` rate < 10%
- `ws_connect_duration` p(95) < 1000ms

### Stress & Optimization Tests

Dedicated scenarios that target specific backend bottlenecks and reveal optimization opportunities.

```bash
# All stress scenarios (sequential: rate-limit → bulk-ops → auth → pagination → contention → spike → soak)
k6 run loadtest/backend/k6-stress.js

# Single scenario
k6 run --env SCENARIO=spike loadtest/backend/k6-stress.js
k6 run --env SCENARIO=soak loadtest/backend/k6-stress.js
k6 run --env SCENARIO=rate-limit loadtest/backend/k6-stress.js
```

Or via Makefile:
```bash
make loadtest-stress          # start backend + run stress tests + stop
make loadtest-stress-run      # run only (backend must be running)
```

**Scenarios**:

| Scenario | VUs | Duration | What it reveals |
|----------|-----|----------|-----------------|
| `rate-limit` | 50–100 | 30s | Rate limiter accuracy under 200 req/s burst. Memory use per-IP. 429 response latency. |
| `bulk-ops` | 10–20 | 1.5min | Bulk stop/delete transaction overhead. Goroutine pressure. Per-item error isolation. |
| `auth-contention` | 30 | 1min | JWT parsing overhead: cached token (10 reqs) vs fresh login per request. Middleware cost. |
| `pagination` | 15–30 | 2min | Missing DB indexes. Large-offset scan performance. COUNT query cost. Filter efficiency. |
| `write-contention` | 20 | 1min | Optimistic locking under concurrent updates. Version mismatch (409) rate. Row-lock contention. |
| `spike` | 0→100 | 1min | Connection pool starvation. Goroutine explosion. Response degradation under instant load. |
| `soak` | 30 | 10min | Memory leaks. Connection pool exhaustion. GC pauses. Goroutine leaks. P95 drift over time. |
| `otel-overhead` | 20 | 1min | Latency impact of OpenTelemetry instrumentation. Compare p50/p95/p99 with `OTEL_ENABLED=true` vs `false`. |

**Thresholds**:
- `http_req_duration` p(95) < 800ms, p(99) < 2000ms
- `errors` rate < 10%
- `bulk_operation_duration` p(95) < 3000ms
- `pagination_duration` p(95) < 600ms
- `auth_duration` p(95) < 200ms
- `write_duration` p(95) < 500ms
- `otel_overhead_duration` p(95) < 600ms

**Custom Metrics**:
- `rate_limit_429s` — Count of expected 429 responses during rate-limit test
- `optimistic_lock_409s` — Count of version mismatch conflicts during write-contention
- `concurrent_users` — Active VU gauge (useful for correlating with response time)
- `otel_overhead_duration` — Per-request latency trend used for OTel overhead comparison

### Environment Variables (k6)

| Variable | Default | Description |
|----------|---------|-------------|
| `API_URL` | `http://localhost:8081` | Backend base URL |
| `ADMIN_USERNAME` | `admin` | Login username |
| `ADMIN_PASSWORD` | `admin` | Login password |
| `SCENARIO` | _(all)_ | Stress tests only: run a single scenario (e.g. `spike`, `soak`, `rate-limit`) |

## Frontend Load Tests (Playwright)

Simulates real browser users navigating the application. Uses parallel Playwright workers to create concurrent sessions.

```bash
# Default: 5 parallel browser sessions
cd frontend && npx playwright test --config=../loadtest/frontend/playwright.config.ts

# 10 parallel sessions, each test repeated 3 times
cd frontend && npx playwright test --config=../loadtest/frontend/playwright.config.ts --workers=10 --repeat-each=3
```

Or use the Makefile targets:
```bash
make loadtest-frontend              # 5 workers
LOAD_WORKERS=10 make loadtest-frontend  # 10 workers
```

**Scenarios**:
1. **Dashboard Browse** — Login → render dashboard → interact with tabs
2. **Dashboard API Calls** — Monitor all `/api/v1/` calls during dashboard load
3. **Template Gallery** — Browse templates page
4. **Definition CRUD** — Create and manage stack definitions
5. **Instance Detail** — Navigate to instance detail page
6. **Rapid Navigation** — Rapidly switch between 8 pages, assert no 5xx errors
7. **Audit Log Pagination** — Browse and paginate audit logs
8. **Profile** — Load profile page

### Environment Variables (Playwright)

| Variable | Default | Description |
|----------|---------|-------------|
| `FRONTEND_URL` | `http://localhost:3000` | Frontend base URL |
| `API_URL` | `http://localhost:8081` | Backend base URL |
| `ADMIN_USERNAME` | `admin` | Login username |
| `ADMIN_PASSWORD` | `admin` | Login password |
| `LOAD_WORKERS` | `5` | Parallel browser workers |

## Makefile Targets

All targets automatically start the backend in release mode and stop it when done:

```bash
make loadtest                 # Run all load tests (backend + frontend)
make loadtest-backend          # Run k6 API + WebSocket load tests
make loadtest-stress           # Run stress/optimization tests (rate-limit, bulk, soak, etc.)
make loadtest-frontend         # Run Playwright frontend load tests
```

For manual control (e.g. running tests repeatedly without restart):

```bash
make loadtest-start            # Build + start backend in release mode
make loadtest-backend-run      # Run k6 tests (backend must be running)
make loadtest-frontend-run     # Run Playwright tests (backend must be running)
make loadtest-stop             # Stop the backend
```

## OpenTelemetry Trace Correlation

When the backend runs with `OTEL_ENABLED=true`, all k6 requests include W3C `traceparent` headers. This links every k6 iteration to a backend trace, making it easy to drill into individual requests in the observability stack.

### Starting the observability stack

```bash
make dev-otel   # starts backend + frontend + otel-collector + tempo + prometheus + grafana
```

### Viewing traces

- **Jaeger** at http://localhost:16686 -- search by service name `k8s-stack-manager` to see traces linked to k6 iterations. Each request carries a unique trace ID via the `traceparent` header.
- **Grafana** at http://localhost:3001 -- pre-configured with Prometheus (metrics) and Jaeger (traces) data sources.

### Measuring OTel overhead

Run the `otel-overhead` scenario twice -- once with instrumentation enabled, once without -- and compare the `otel_overhead_duration` metric:

```bash
# 1. Start backend with OTel enabled
OTEL_ENABLED=true make loadtest-start

# Run the overhead scenario
k6 run --env SCENARIO=otel-overhead loadtest/backend/k6-stress.js --out json=otel-on.json

make loadtest-stop

# 2. Start backend with OTel disabled
OTEL_ENABLED=false make loadtest-start

# Run the same scenario
k6 run --env SCENARIO=otel-overhead loadtest/backend/k6-stress.js --out json=otel-off.json

make loadtest-stop
```

Compare the p50/p95/p99 values of `otel_overhead_duration` between the two runs. Typical overhead should be under 5% for read-heavy workloads.

## Interpreting Results

### k6 Output

k6 prints a summary table with:
- **http_req_duration** — Response time percentiles (p50, p90, p95, p99)
- **errors** — Percentage of failed checks
- **api_calls** — Total API requests made
- **iterations** — Completed virtual user iterations

Look for:
- p95 response times under 500ms (threshold)
- Error rate under 5%
- Consistent response times during the "hold" phase (no degradation)

### Playwright Output

Each test logs `[METRIC]` lines with timing data:
```
[METRIC] dashboard_load_ms=1234
[METRIC] rapid_nav_total_ms=4567 pages=8 errors=0
```

The HTML report is generated at `loadtest/results/frontend/index.html`.
