# Load Testing

Scripts for load testing the k8s-stack-manager backend API and frontend UI.

## Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| k6 | Backend API + WebSocket load tests | `brew install k6` or [k6.io/docs](https://k6.io/docs/get-started/installation/) |
| Playwright | Frontend browser load tests | Already installed via `cd frontend && npm install` |

The Makefile targets automatically build and start the backend in **release mode** (`GIN_MODE=release`) with a high rate limit (10,000 req/min). Azurite is started automatically.

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

### Environment Variables (k6)

| Variable | Default | Description |
|----------|---------|-------------|
| `API_URL` | `http://localhost:8081` | Backend base URL |
| `ADMIN_USERNAME` | `admin` | Login username |
| `ADMIN_PASSWORD` | `admin` | Login password |

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
make loadtest-frontend         # Run Playwright frontend load tests
```

For manual control (e.g. running tests repeatedly without restart):

```bash
make loadtest-start            # Build + start backend in release mode
make loadtest-backend-run      # Run k6 tests (backend must be running)
make loadtest-frontend-run     # Run Playwright tests (backend must be running)
make loadtest-stop             # Stop the backend
```

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
