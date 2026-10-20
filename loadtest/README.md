# Load Testing

This folder has scripts that load-test the K8s Stack Manager backend API and the frontend UI. It also has a script that records the memory and CPU use of the backend during a test (see [Measure memory and CPU](#measure-memory-and-cpu)).

## Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| k6 | Backend API and WebSocket load tests | `brew install k6` or [k6.io/docs](https://k6.io/docs/get-started/installation/) |
| Playwright | Frontend browser load tests | Installed with `cd frontend && npm install` |
| Docker | MySQL container of `docker-compose.yml` | Docker Desktop, Rancher Desktop or Docker Engine |
| Go | Builds the backend for the test, reads the profiles | Go 1.27 or later |

The Makefile targets build the backend and run it as a local process, not in Docker. The backend log is in `backend/tmp/loadtest.log`. The start target does these steps:

1. With `LOADTEST_MYSQL=docker` (the default), it starts the MySQL container (`make mysql-start`) and stops the Docker `backend` container.
2. It fails when the backend port or the pprof port is in use.
3. It starts the backend with these settings:
   - Release mode (`GIN_MODE=release`, `APP_DEBUG=false`): no debug log of each SQL statement.
   - `SERVER_HOST=127.0.0.1`: the backend is not reachable from other computers.
   - `RATE_LIMIT=1000000` and `LOGIN_RATE_LIMIT=1000000` (requests each minute for each IP address): all k6 users share one IP address, so the default limits give HTTP 429.
   - The MySQL container: `DB_HOST=127.0.0.1`, `DB_PORT=3306`, `DB_USER=root`, `DB_PASSWORD=rootpassword`, `DB_NAME=app`.
   - No kubeconfig (`KUBECONFIG`, `KUBECONFIG_PATH` and `IN_CLUSTER_FALLBACK_KUBECONFIG` are empty) and no git provider token. The backend reaches no cluster and no git host.
   - pprof on `127.0.0.1:6060` (`PPROF_ENABLED=true`).

A shell variable overrides each default, for example `DB_PASSWORD=secret make loadtest-backend`.

The tests write to the database `app`, the same database as `make dev`. `setup()` of the k6 tests needs a default cluster to create instances. When no default cluster exists, `setup()` creates the cluster `k6-loadtest` with the in-cluster config. Outside a pod, the backend cannot reach this cluster. The cluster stays for the next run. Note: when `make dev` registered another default cluster, the tests use that cluster. The tests do not deploy, but the `stack-page` scenario reads the pod status from it.

Seed test data (optional, recommended for richer results):

```bash
make seed                                          # backend on port 8081
./scripts/seed-dev-data.sh http://localhost:18081   # backend on another port
```

Note: `make seed` calls the API, so a backend must run. For example, run it after `make loadtest-start-backend`. The data stays in MySQL.

### Makefile variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LOADTEST_PORT` | `8081` | Port of the load test backend. The health checks and the k6 `API_URL` use it. The frontend load test needs `8081`. |
| `LOADTEST_MYSQL` | `docker` | `docker`: start the MySQL container and stop the Docker backend. `external`: use the MySQL server of the `DB_*` variables and run no Docker command. |
| `LOADTEST_CAPTURE` | (empty) | `1`: run each k6 test through `loadtest/capture-resources.sh` (see [Measure memory and CPU](#measure-memory-and-cpu)). |
| `CAPTURE_ARGS` | (empty) | Options of `loadtest/capture-resources.sh`, for example `-i 2 -c 20`. |
| `OTEL_TRACE_SAMPLE_RATE` | `0.05` | Trace sample rate when OpenTelemetry is on. |
| `PPROF_ADDR` | `127.0.0.1:6060` | pprof address of the backend. |
| `RATE_LIMIT`, `LOGIN_RATE_LIMIT` | `1000000` | Rate limits (requests each minute for each IP address). |

Example: a backend on port 18081 with a MySQL server on port 3307:

```bash
DB_PORT=3307 make loadtest-backend LOADTEST_MYSQL=external LOADTEST_PORT=18081
```

## Backend Load Tests (k6)

### API Load Test

The test calls the main API routes: authentication, CRUD workflows, dashboard simulation, batch requests, analytics, nested resources, and the requests of the stack page. `setup()` signs in once as admin and creates an API key. All scenarios use that key. So the test does not load bcrypt on each iteration.

```bash
# Default: all five scenarios (see below)
k6 run loadtest/backend/k6-api.js

# One scenario (it starts at once)
k6 run --env SCENARIO=stack-page loadtest/backend/k6-api.js

# Custom VU count and duration. k6 then replaces the scenarios with one
# scenario that runs the function of SCENARIO (default: load).
k6 run --vus 50 --duration 5m loadtest/backend/k6-api.js
k6 run --env SCENARIO=stack-page --vus 20 --duration 10m loadtest/backend/k6-api.js

# Custom backend URL
k6 run --env API_URL=http://localhost:18081 loadtest/backend/k6-api.js
```

**Scenarios**:

| Scenario | Load | What it does |
|---|---|---|
| `smoke` | 5 virtual users (VUs) for 30s | Health checks, login, ping |
| `load` | Starts at 35s. Ramps 0, 20 (30s), hold (2m), 50 (30s), hold (1m), 0 (30s). Total 4.5 min. | Full CRUD workflows, browsing, dashboard batch |
| `analytics` | Starts at 35s. 10 VUs for 2m. | Aggregation-heavy reads and notifications |
| `nested-crud` | Starts at 35s. Ramps 0, 10 (30s), 20 (2m), 0 (30s). | Sub-resources, clone, compare, export |
| `stack-page` | Starts at 35s. 10 VUs for 2m. | The requests of the web UI when a user opens an instance page |

The `stack-page` scenario sends the requests of `pages/StackInstances/Detail.tsx` and its hooks and components, in three steps. Each step sends its requests at the same time (`http.batch`), as the browser does:

1. The instance, the Deployment History (`deploy-log?view=summary&limit=10`), the rollback targets (`deploy-log?view=summary&action=deploy&status=success&limit=50`), the effective quota, the custom actions, the action runs, the favorite check and the notification count.
2. The definition of the instance, the value overrides and the branch overrides.
3. The pods and the namespace status.

Notes:

- `setup()` creates a draft instance of a definition with two charts, a value override and a branch override. `teardown()` deletes them. To use another instance (for example one with a deploy history), set `STACK_PAGE_INSTANCE_ID`.
- The draft instance has no deploy history, so the two `deploy-log` requests return empty pages.
- Without cluster access, `/pods` and `/status` answer HTTP 502 at once. The scenario accepts 502 and counts it in `stack_page_cluster_unavailable`. The duration of these requests then does not include a Kubernetes API call. The web UI requests `/pods` only for an active instance.
- `GET /stack-instances/:id/action-runs` (#640) answers HTTP 404 on a backend without this route. The scenario accepts 404. k6 does not count it in `http_req_failed`. When #640 is on main, the scenario must accept only 200 (TODO in `k6-api.js`).
- Without cluster access, `/health/ready` of the `smoke` scenario answers HTTP 503, because no registered cluster is reachable. The scenario accepts 503.

**Thresholds**:

- `http_req_duration` p(95) < 500ms, p(99) < 1000ms
- `errors` rate < 5%
- `login_duration` p(95) < 300ms
- `list_duration` p(95) < 450ms
- `crud_duration` p(95) < 500ms
- `analytics_duration` p(95) < 600ms
- `stack_page_duration` (each request of the stack page) p(95) < 400ms
- `stack_page_load` (all three steps of one page load) p(95) < 1000ms

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
k6 run --env SCENARIO=cleanup-notify loadtest/backend/k6-stress.js

# One scenario with your own VU count and duration (default function: soak)
k6 run --env SCENARIO=soak --vus 30 --duration 30m loadtest/backend/k6-stress.js
```

With the Makefile:

```bash
make loadtest-stress          # start MySQL and the backend, run the stress tests, stop the backend
make loadtest-stress-run      # run only (the backend must be running)
```

When you run all scenarios, they run one after the other in this order: `rate-limit`, `bulk-ops`, `auth-contention`, `pagination`, `write-contention`, `spike`, `soak`. The `otel-overhead` scenario starts at 0s and runs at the same time as the others. Select it alone with `SCENARIO=otel-overhead`. The `cleanup-notify` scenario runs only when you select it, because its cleanup policy runs every minute.

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
| `cleanup-notify` | 20 | 5 min | Background work: a scheduled cleanup policy, in-app notifications, notification reads and, optionally, notification channel delivery. Only when selected. |

**The `cleanup-notify` scenario**:

- `setup()` creates `SEED_INSTANCES` draft instances (default 100) and a cleanup policy: action `delete`, condition `status:draft`, all clusters, every minute, dry run. Each scheduled run reads all instances, matches every draft and sends `cleanup.policy.executed` to the admin and devops users. The match also includes the drafts of the VUs and any other drafts in the database. The dry run deletes nothing.
- Each iteration creates an instance (notification `instance.created`), reads the notifications and the unread count, marks all read (every fifth iteration) and deletes the instance (notification `instance.deleted`). VU 1 runs the policy by hand (`?dry_run=true`) every tenth iteration.
- With `NOTIFY_WEBHOOK_URL`, `setup()` also creates a notification channel for these three events. The URL must use `https://`, and the backend must trust its certificate.
- On macOS, the backend refuses the self-signed certificate of the e2e stub (`https://127.0.0.1:18443/webhook/k6`). Each delivery then fails and waits 2 seconds for its one retry. The channel queue (100 events) fills, and the backend drops events. So the run measures a channel that is down, not a working channel.
- The backend has no cluster in the load tests. So deploy, stop and clean fail before they write a log or send a notification, and the policy cannot use the `stop` or `clean` action.
- `teardown()` deletes the policy, the channel, the seed instances, the definition and the API key.

The Makefile starts the backend with `RATE_LIMIT=1000000`. So the `rate-limit` scenario sees few or no 429 responses. To test the limiter, start the backend with a low limit, for example `RATE_LIMIT=100 make loadtest-start-backend`.

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
| `API_URL` | `http://localhost:8081` | Backend base URL. The Makefile sets it from `LOADTEST_PORT`. |
| `ADMIN_USERNAME` | `admin` | Login username |
| `ADMIN_PASSWORD` | `admin` | Login password |
| `SCENARIO` | (all) | API and stress tests: run one scenario (for example `stack-page`, `spike`, `soak`) |
| `SEED_INSTANCES` | `100` | Stress test, `cleanup-notify`: draft instances that `setup()` creates |
| `NOTIFY_WEBHOOK_URL` | (empty) | Stress test, `cleanup-notify`: https URL of a notification channel. Empty: no channel. |
| `STACK_PAGE_INSTANCE_ID` | (empty) | API test: the instance of the `stack-page` scenario. Empty: `setup()` creates one. |

The `make` targets start the backend with `ADMIN_USERNAME=admin` and `ADMIN_PASSWORD=admin` (unless your shell sets them).

## Frontend Load Tests (Playwright)

The tests simulate users in a real browser. Parallel Playwright workers create concurrent sessions. Each test signs in through the login page.

The frontend dev server calls the backend on port 8081. So the frontend tests need `LOADTEST_PORT=8081` (the default).

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

These targets start MySQL and the backend in release mode, and stop the backend when the tests end:

```bash
make loadtest                  # Run all load tests (backend and frontend)
make loadtest-backend          # Run the k6 API and WebSocket load tests
make loadtest-stress           # Run the stress and optimization tests
make loadtest-frontend         # Run the Playwright frontend load tests
```

For manual control (for example to run the tests again without a restart):

```bash
make loadtest-start-backend    # Start MySQL, build and start the backend (release mode)
make loadtest-start            # The same, and the frontend dev server
make loadtest-backend-run      # Run the k6 tests (the backend must be running)
make loadtest-stress-run       # Run the k6 stress tests (the backend must be running)
make loadtest-frontend-run     # Run the Playwright tests (the backend and the frontend must be running)
make loadtest-capture          # Record memory, CPU and profiles of the running backend (Ctrl-C stops)
make loadtest-stop             # Stop the backend and the frontend dev server
```

The targets do not stop MySQL. Stop it with `make mysql-stop`.

For a run with MySQL, OpenTelemetry, and a MySQL exporter, use `make loadtest-mysql-backend` or `make loadtest-mysql-stress`. These targets start MySQL, the collector, Tempo, Prometheus, Grafana, and `mysqld-exporter` in Docker. They start the backend with `OTEL_ENABLED=true`, `OTEL_TRACE_SAMPLE_RATE=0.05`, 200 database connections, and the high rate limits. They stop everything when the tests end (`make loadtest-mysql-stop`). The default sample rate keeps the trace export cost low, so the memory and CPU values stay close to a run without traces. To trace each request, set `OTEL_TRACE_SAMPLE_RATE=1.0`.

## Measure Memory and CPU

Use a backend without OpenTelemetry for clean values: `make loadtest-backend`, `make loadtest-stress` or `make loadtest-start-backend` (OpenTelemetry is off unless your shell sets `OTEL_ENABLED=true`). With OpenTelemetry on, keep a low sample rate (`OTEL_TRACE_SAMPLE_RATE=0.05`, the default of the load test targets). With the sample rate `1.0`, the backend exports a span for each request, and this adds memory and CPU use.

### Run the capture script

`loadtest/capture-resources.sh` records the resident memory (RSS) and the CPU use of the backend process every 5 seconds. It reads the goroutine count every 6th sample (`-g`), because this read stops the backend for a short time. It takes heap, goroutine and 30-second CPU profiles through pprof:

- At the start.
- When the RSS grows: each time the RSS passes the RSS of the last peak profiles by the margin (`-m`, default 10%), the script takes the peak profiles again. The newest peak profiles replace the older ones. So the peak profiles are from the last growth step, not from the exact RSS maximum. The CSV shows the maximum.
- At the end, when the backend still runs.

The script does not load the backend. A CPU profile samples the running code only. The script works on macOS and Linux.

Option 1: one command. The Makefile runs each k6 test through the script:

```bash
make loadtest-stress LOADTEST_CAPTURE=1
make loadtest-backend LOADTEST_CAPTURE=1    # one capture for the API test, one for the WebSocket test
```

Option 2: the script runs a k6 command, for example a long soak or the stack page:

```bash
make loadtest-start-backend
loadtest/capture-resources.sh -- k6 run --env SCENARIO=soak --vus 30 --duration 30m loadtest/backend/k6-stress.js
loadtest/capture-resources.sh -- k6 run --env SCENARIO=stack-page --vus 20 --duration 10m loadtest/backend/k6-api.js
make loadtest-stop-backend
```

Option 3: two terminals.

```bash
# Terminal 1
make loadtest-start-backend
# Terminal 2: start before the test, press Ctrl-C after the test
make loadtest-capture
# Terminal 1
make loadtest-stress-run
make loadtest-stop-backend
```

With a command after `--`, the script stops when the command ends. Ctrl-C then goes to the command, and `-d` stops only the capture, not the command. Without a command, the script stops when the backend exits, after `-d` seconds, or on Ctrl-C. Then it takes the end profiles (about 30 seconds for the CPU profile) and prints a summary: the RSS at the start, at the end and the maximum, the average and maximum CPU use, the maximum goroutine count and the file paths.

Options (`CAPTURE_ARGS` for the Makefile targets):

| Option | Default | Description |
|--------|---------|-------------|
| `-p PID` | PID in `backend/tmp/loadtest-backend.pid` or `backend/tmp/loadtest-mysql.pid` | Backend process |
| `-a ADDR` | `PPROF_ADDR`, else `127.0.0.1:6060` | pprof address |
| `-i SECONDS` | `5` | Sample interval |
| `-c SECONDS` | `30` | Length of each CPU profile. `0`: no CPU profiles. |
| `-m PERCENT` | `10` | RSS growth that takes the peak profiles again |
| `-g N` | `6` | Read the goroutine count every N samples. `0`: never. |
| `-t SECONDS` | (empty) | Take the middle profiles once at this elapsed time, not at the RSS peak. A running capture finishes first. |
| `-d SECONDS` | (no limit) | Stop the capture after this time. A command after `--` continues. |
| `-o DIR` | `loadtest/results` | Output folder |

Without pprof (`PPROF_ENABLED=false`), the script records RSS and CPU only.

### Output

Git ignores `loadtest/results/`.

`loadtest/results/resources-<timestamp>.csv` has one row for each sample:

| Column | Description |
|--------|-------------|
| `time` | Local time of the sample |
| `elapsed_s` | Seconds since the start of the capture |
| `rss_kib` | Resident memory of the process in KiB (`ps -o rss`) |
| `cpu_pct` | CPU use since the previous sample, from the CPU time of the process. 100 is one full core. Empty in the first row. |
| `goroutines` | Goroutine count (first line of `/debug/pprof/goroutine?debug=1`), in the first row and every `-g` samples. Empty in the other rows and without pprof. |

`loadtest/results/profiles/<timestamp>/` has the profiles:

- `heap-<label>.pb.gz`, `goroutine-<label>.pb.gz`, `cpu-<label>.pb.gz`. The label is `start`, `peak`, `at-<N>s` or `end`.
- `captures.txt`: the time, the elapsed seconds and the RSS of each capture.

### Read the results

```bash
P=loadtest/results/profiles/<timestamp>

go tool pprof -top $P/cpu-peak.pb.gz                              # functions with the most CPU time
go tool pprof -top -sample_index=inuse_space $P/heap-peak.pb.gz   # live heap at the last peak capture
go tool pprof -top -sample_index=alloc_space $P/heap-end.pb.gz    # all allocations since the start of the process
go tool pprof -top -base $P/heap-start.pb.gz $P/heap-end.pb.gz    # heap growth during the test
go tool pprof -top -base $P/goroutine-start.pb.gz $P/goroutine-end.pb.gz   # goroutines that stay
go tool pprof -http=127.0.0.1:8090 $P/cpu-peak.pb.gz              # flame graph in the browser
```

Notes:

- The RSS includes memory that the Go runtime keeps after a garbage collection. The RSS can stay high after the load ends. The heap profile shows the live heap.
- When the end heap is much larger than the start heap, or the goroutine count does not go back down after the test, look for a leak with the `-base` commands.
- The API rate limiter keeps one timestamp for each request of the last minute for each client IP address. All k6 users share one IP address, so `RateLimiter.Allow` can show in the CPU profile at high request rates.

### Second option: OpenTelemetry runtime metrics in Grafana

`make loadtest-mysql-backend` and `make loadtest-mysql-stress` start the observability stack. The backend sends its runtime metrics (OpenTelemetry Go runtime instrumentation) to the collector. Open Grafana at http://localhost:3001, select Explore and the Prometheus data source, and search for the metrics that start with `go_`, for example:

- `go_memory_used_bytes`: memory that the Go runtime uses (by `go_memory_type`)
- `go_memory_gc_goal_bytes`: heap size goal of the next garbage collection
- `rate(go_memory_allocated_bytes_total[1m])`: allocation rate
- `go_goroutine_count`: goroutines

The exact names depend on the collector version. The panels of the dashboard "Go Runtime & Application" that use `go_memstats_*` or `go_goroutines` read the Prometheus endpoint of the Docker backend (`backend:9090`). They show no data for the local load test backend. The values of the capture script and the profiles are more exact than the metrics. The metrics show the trend over the full test.

## OpenTelemetry Trace Correlation

When the backend runs with `OTEL_ENABLED=true`, all k6 requests include W3C `traceparent` headers. Each sampled k6 iteration is then linked to a backend trace. Use this to look at single requests in the observability stack. The backend samples by trace ID (`OTEL_TRACE_SAMPLE_RATE`); the sampled flag of the header does not change this.

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
# 1. Start the backend with OpenTelemetry on (all requests traced)
OTEL_ENABLED=true OTEL_TRACE_SAMPLE_RATE=1.0 make loadtest-start-backend

# Run the overhead scenario
k6 run --env SCENARIO=otel-overhead loadtest/backend/k6-stress.js --out json=otel-on.json

make loadtest-stop-backend

# 2. Start the backend with OpenTelemetry off
OTEL_ENABLED=false make loadtest-start-backend

# Run the same scenario
k6 run --env SCENARIO=otel-overhead loadtest/backend/k6-stress.js --out json=otel-off.json

make loadtest-stop-backend
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
