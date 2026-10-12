# Load Testing

This folder has scripts that load-test the K8s Stack Manager backend API and the frontend UI.

## Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| k6 | Backend API and WebSocket load tests | `brew install k6` or [k6.io/docs](https://k6.io/docs/get-started/installation/) |
| Playwright | Frontend browser load tests | Installed with `cd frontend && npm install` |
| MySQL | Database of the backend | `make mysql-start` (Docker) |
| Go | Builds the backend for the test | Go 1.26 or later |

The Makefile targets build the backend and run it as a local process, not in Docker. They stop the Docker `backend` container first. The backend runs in **release mode** (`GIN_MODE=release`) with the rate limit `RATE_LIMIT=1000000` (requests each minute for each IP address). The log is in `backend/tmp/loadtest.log`.

The backend needs a MySQL database. The `loadtest-start-backend` target does not start MySQL and does not set the `DB_*` variables. The backend uses the defaults (`localhost:3306`, user `root`, empty password, database `app`), or the `DB_*` variables of your shell. For the MySQL container of `docker-compose.yml`, run:

```bash
make mysql-start
export DB_PASSWORD=rootpassword
```

Seed test data (optional, recommended for richer results):

```bash
make seed
```

Note: `make seed` calls the API at `http://localhost:8081`, so a backend must run. For example, run it after `make dev`, or after `make loadtest-start`. The data stays in MySQL.

The login limiter (`LOGIN_RATE_LIMIT`, default 10 each minute) is not raised by the Makefile. A test that logs in many times can get HTTP 429 from the login route. To avoid this, set `LOGIN_RATE_LIMIT` in your shell before you start the backend.

## Backend Load Tests (k6)

### API Load Test

The test calls the main API routes: authentication, CRUD workflows, dashboard simulation, batch requests, analytics, and nested resources. `setup()` signs in once as admin and creates an API key. All scenarios use that key. So the test does not load bcrypt on each iteration.

```bash
# Default: four scenarios (see below)
k6 run loadtest/backend/k6-api.js

# Custom VU count and duration (k6 then ignores the scenarios of the script)
k6 run --vus 50 --duration 5m loadtest/backend/k6-api.js

# Custom backend URL
k6 run --env API_URL=http://backend:8081 loadtest/backend/k6-api.js
```

**Scenarios**:

| Scenario | Load | What it does |
|---|---|---|
| `smoke` | 5 virtual users (VUs) for 30s | Health checks, login, ping |
| `load` | Starts at 35s. Ramps 0, 20 (30s), hold (2m), 50 (30s), hold (1m), 0 (30s). Total 4.5 min. | Full CRUD workflows, browsing, dashboard batch |
| `analytics` | Starts at 35s. 10 VUs for 2m. | Aggregation-heavy reads and notifications |
| `nested-crud` | Starts at 35s. Ramps 0, 10 (30s), 20 (2m), 0 (30s). | Sub-resources, clone, compare, export |

**Thresholds**:

- `http_req_duration` p(95) < 500ms, p(99) < 1000ms
- `errors` rate < 5%
- `login_duration` p(95) < 300ms
- `list_duration` p(95) < 450ms
- `crud_duration` p(95) < 500ms
- `analytics_duration` p(95) < 600ms

### WebSocket Load Test

The test opens many concurrent WebSocket connections. `setup()` signs in once. All VUs use the same JWT for the WebSocket upgrade.

```bash
k6 run loadtest/backend/k6-websocket.js
```

The load ramps from 0 to 25 VUs (15s), 50 VUs (1m), and 100 VUs (30s), holds 100 VUs (30s), and ramps down (15s). The total time is 2.5 minutes. Each VU connects, listens for 5 to 15 seconds, and disconnects.

**Thresholds**:

- `ws_errors` rate < 10%
- `ws_connect_duration` p(95) < 1000ms

### Stress and Optimization Tests

These scenarios target specific backend bottlenecks.

```bash
# All stress scenarios (see the start times below)
k6 run loadtest/backend/k6-stress.js

# One scenario
k6 run --env SCENARIO=spike loadtest/backend/k6-stress.js
k6 run --env SCENARIO=soak loadtest/backend/k6-stress.js
k6 run --env SCENARIO=rate-limit loadtest/backend/k6-stress.js
```

With the Makefile:

```bash
make loadtest-stress          # start the backend, run the stress tests, stop the backend
make loadtest-stress-run      # run only (the backend must be running)
```

When you run all scenarios, they run one after the other in this order: `rate-limit`, `bulk-ops`, `auth-contention`, `pagination`, `write-contention`, `spike`, `soak`. The `otel-overhead` scenario starts at 0s and runs at the same time as the others. Select it alone with `SCENARIO=otel-overhead`.

**Scenarios**:

| Scenario | VUs | Duration | What it shows |
|----------|-----|----------|---------------|
| `rate-limit` | 50 to 100 | 30s | Rate limiter accuracy at 200 requests each second. Memory use for each IP. Speed of the 429 response. |
| `bulk-ops` | 10 to 20 | 1.5 min | Transaction cost of bulk stop and delete. Goroutine load. Error isolation for each item. |
| `auth-contention` | 30 | 1 min | JWT parsing cost: a cached token (10 requests) against a new login for each request. Middleware cost. |
| `pagination` | 15 to 30 | 2 min | Missing database indexes. Speed of large-offset scans. COUNT query cost. Filter efficiency. |
| `write-contention` | 20 | 1 min | Optimistic locking with concurrent updates. Rate of version conflicts (409). Row lock contention. |
| `spike` | 0 to 100 | 1 min | Connection pool starvation. Goroutine growth. Slower responses under sudden load. |
| `soak` | 30 | 10 min | Memory leaks. Connection pool exhaustion. GC pauses. Goroutine leaks. Drift of p95 over time. |
| `otel-overhead` | 20 | 1 min | Latency cost of OpenTelemetry. Compare p50, p95, and p99 with `OTEL_ENABLED=true` and `false`. |

The Makefile starts the backend with `RATE_LIMIT=1000000`. So the `rate-limit` scenario sees few or no 429 responses. To test the limiter, start the backend yourself with a low `RATE_LIMIT`.

**Thresholds**:

- `http_req_duration` p(95) < 800ms, p(99) < 2000ms
- `errors` rate < 10%
- `bulk_operation_duration` p(95) < 3000ms
- `pagination_duration` p(95) < 600ms
- `auth_duration` p(95) < 200ms
- `write_duration` p(95) < 500ms
- `otel_overhead_duration` p(95) < 600ms

**Custom metrics**:

- `rate_limit_429s`: count of the expected 429 responses in the rate-limit scenario
- `optimistic_lock_409s`: count of version conflicts in the write-contention scenario
- `concurrent_users`: gauge of the active VUs (use it to compare with the response time)
- `otel_overhead_duration`: latency trend for the OpenTelemetry comparison

### Environment Variables (k6)

| Variable | Default | Description |
|----------|---------|-------------|
| `API_URL` | `http://localhost:8081` | Backend base URL |
| `ADMIN_USERNAME` | `admin` | Login username |
| `ADMIN_PASSWORD` | `admin` | Login password |
| `SCENARIO` | (all) | Stress tests only: run one scenario (for example `spike`, `soak`, `rate-limit`) |

The `make` targets start the backend with `ADMIN_USERNAME=admin` and `ADMIN_PASSWORD=admin` (unless your shell sets them).

## Frontend Load Tests (Playwright)

The tests simulate users in a real browser. Parallel Playwright workers create concurrent sessions. Each test signs in through the login page.

```bash
# Default: 5 parallel browser sessions
cd frontend && NODE_PATH=node_modules npx playwright test --config=../loadtest/frontend/playwright.config.ts

# 10 parallel sessions, each test repeated 3 times
cd frontend && NODE_PATH=node_modules npx playwright test --config=../loadtest/frontend/playwright.config.ts --workers=10 --repeat-each=3
```

Or use the Makefile targets:

```bash
make loadtest-frontend                  # 5 workers
LOAD_WORKERS=10 make loadtest-frontend  # 10 workers
```

**Scenarios**:

1. **Dashboard Browse.** Sign in, render the dashboard, switch tabs.
2. **Dashboard API Calls.** Count all `/api/v1/` calls during the dashboard load.
3. **Template Gallery.** Open the templates page.
4. **Definition CRUD.** Create and delete a stack definition.
5. **Instance Detail.** Open the instance detail page.
6. **Rapid Navigation.** Visit 8 routes one after the other and check that no `/api/v1/` response is 5xx.
7. **Audit Log Pagination.** Open the audit log and page through it.
8. **Profile.** Open the profile page.

### Environment Variables (Playwright)

| Variable | Default | Description |
|----------|---------|-------------|
| `FRONTEND_URL` | `http://localhost:3000` | Frontend base URL |
| `ADMIN_USERNAME` | `admin` | Login username |
| `ADMIN_PASSWORD` | `admin` | Login password |
| `LOAD_WORKERS` | `5` | Parallel browser workers |

## Makefile Targets

These targets start the backend in release mode and stop it when the tests end:

```bash
make loadtest                  # Run all load tests (backend and frontend)
make loadtest-backend          # Run the k6 API and WebSocket load tests
make loadtest-stress           # Run the stress and optimization tests
make loadtest-frontend         # Run the Playwright frontend load tests
```

For manual control (for example to run the tests again without a restart):

```bash
make loadtest-start            # Build and start the backend (release mode) and the frontend dev server
make loadtest-backend-run      # Run the k6 tests (the backend must be running)
make loadtest-stress-run       # Run the k6 stress tests (the backend must be running)
make loadtest-frontend-run     # Run the Playwright tests (the backend and the frontend must be running)
make loadtest-stop             # Stop the backend and the frontend dev server
```

For a run with MySQL, OpenTelemetry, and a MySQL exporter, use `make loadtest-mysql-backend` or `make loadtest-mysql-stress`. These targets start MySQL, the collector, Tempo, Prometheus, Grafana, and `mysqld-exporter` in Docker. They start the backend with `OTEL_ENABLED=true`, 200 database connections, and `RATE_LIMIT=1000000`. They stop everything when the tests end (`make loadtest-mysql-stop`).

## OpenTelemetry Trace Correlation

When the backend runs with `OTEL_ENABLED=true`, all k6 requests include W3C `traceparent` headers. Each k6 iteration is then linked to a backend trace. Use this to look at single requests in the observability stack.

### Start the observability stack

```bash
make dev-otel   # starts the backend, the frontend, the OpenTelemetry collector, Tempo, Prometheus, and Grafana
```

### View the traces

- **Grafana** at http://localhost:3001 has the data sources Prometheus (metrics) and Tempo (traces). Use Explore with the Tempo data source and search for the service name `k8s-stack-manager`. Each request has a unique trace ID from the `traceparent` header.
- **Prometheus** is at http://localhost:9090.

### Measure the OpenTelemetry overhead

Run the `otel-overhead` scenario two times: once with instrumentation and once without. Compare the metric `otel_overhead_duration`:

```bash
# 1. Start the backend with OpenTelemetry on
OTEL_ENABLED=true make loadtest-start

# Run the overhead scenario
k6 run --env SCENARIO=otel-overhead loadtest/backend/k6-stress.js --out json=otel-on.json

make loadtest-stop

# 2. Start the backend with OpenTelemetry off
OTEL_ENABLED=false make loadtest-start

# Run the same scenario
k6 run --env SCENARIO=otel-overhead loadtest/backend/k6-stress.js --out json=otel-off.json

make loadtest-stop
```

Compare the p50, p95, and p99 values of `otel_overhead_duration` between the two runs. Note: With `OTEL_ENABLED=true`, the backend sends traces to `localhost:4317` (the default of `OTEL_EXPORTER_OTLP_ENDPOINT`). Start the collector with `make otel-start` to receive them.

## Interpret the Results

### k6 Output

k6 prints a summary table with these values:

- **http_req_duration**: response time percentiles (p50, p90, p95, p99)
- **errors**: percentage of failed checks
- **api_calls**: total number of API requests
- **iterations**: number of finished virtual user iterations

Look for these results:

- p95 response times below the thresholds of the script
- An error rate below the threshold
- Stable response times during the hold phases (no slowdown)

### Playwright Output

Each test writes `[METRIC]` lines with timing data:

```
[METRIC] dashboard_load_ms=1234
[METRIC] rapid_nav_total_ms=4567 pages=8 errors=0
```

The HTML report is in `loadtest/results/frontend/index.html`.
