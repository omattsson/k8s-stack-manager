# Development

How to run, build and test K8s Stack Manager on your machine. Contributors also read [.github/CONTRIBUTING.md](../.github/CONTRIBUTING.md). See the [README](../README.md) for an overview and the [documentation map](../README.md#documentation).

## Quick Start (development)

### Prerequisites

- Docker and Docker Compose
- Go 1.27.3 or later (for local backend development)
- Node.js 22.12 or later (for local frontend development). CI and the Docker images use Node.js 26.
- MySQL 8.0 or later for an external database. Docker Compose uses MySQL 8.4. The MySQL in the Helm chart uses image tag `8.0` by default.

### Start with Docker Compose

```bash
cp .env.example .env   # first time only; sets COMPOSE_PROFILES=full
make dev
```

This command starts these services:

- **Frontend**: http://localhost:3000
- **Backend API**: http://localhost:8081
- **Swagger docs**: http://localhost:8081/swagger/index.html

The default admin credentials are `admin` / `admin`. These are the defaults of `docker-compose.yml`. Use them for local development only.

Note: The Swagger UI is on only when `ENABLE_SWAGGER=true`. Docker Compose sets it to `true`. The default of the backend and of the Helm chart is `false`.

### Start locally (without Docker for the application)

```bash
# Start MySQL, the backend, and the frontend with hot reload in one terminal.
make dev-local

# Or run them in separate terminals:
make mysql-start           # MySQL in Docker; dev-local-backend does not start it
make dev-local-backend     # backend (uses "air" for hot reload when installed)
cd frontend && npm install && npm run dev
```

## Commands

| Command | Description |
|---|---|
| `make dev` | Start the full stack with Docker Compose |
| `make dev-k8s` | Start the full stack with access to a local Kubernetes cluster (Docker Desktop, Rancher Desktop) |
| `make dev-otel` | Start the full stack and the OpenTelemetry stack (Grafana on port 3001, Prometheus on port 9090) |
| `make compose-api-only` | Start the backend and MySQL only (headless) |
| `make dev-api-only` | Same as `compose-api-only`, with development settings |
| `make dev-local` | Run MySQL (Docker), and the backend and frontend locally |
| `make mysql-start` | Start the MySQL container |
| `make seed` | Add sample data to a running dev stack |
| `make test` | Run all unit tests (backend and frontend) |
| `make test-backend` | Run the backend unit tests |
| `make test-frontend` | Run the frontend unit tests |
| `make test-backend-all` | Run the backend unit and integration tests (starts MySQL) |
| `make test-e2e` | Run the end-to-end Playwright tests (starts MySQL and the backend; set `E2E_KUBECONFIG` to a local cluster for the specs that need one), then the isolated tests (session lifecycle, last admin) with short session limits |
| `make test-e2e-isolated` | Run only the isolated e2e tests (session lifecycle with short session limits, last admin), serially |
| `make docs` | Regenerate the Swagger documentation |
| `make fmt` | Format the backend and frontend code |
| `make lint` | Lint the backend (`go vet`) and the frontend |
| `make clean` | Stop the containers and remove the volumes (this deletes the database) |
| `make install` | Install all dependencies |
| `make helm-lint` | Lint the Helm chart |
| `make helm-template` | Render the templates locally (dry run) |
| `make helm-test` | Check the default render and the External Secrets render |
| `make helm-install` | Install the chart into the current cluster (needs `JWT_SECRET`) |
| `make helm-upgrade` | Upgrade an existing release (needs `JWT_SECRET`) |
| `make helm-uninstall` | Uninstall the release |
| `make loadtest` | Run the load tests (see [loadtest/README.md](../loadtest/README.md)) |

## Testing

```bash
make test                         # All unit tests
make test-backend-all             # Backend unit and integration tests
make test-e2e                     # Playwright end-to-end tests
cd backend && make test-coverage  # Coverage report (80% threshold)
```

## Project Structure

```
backend/                        Go API server
  api/                          Entry point (main.go) and bootstrap
  internal/
    api/                        Handlers, middleware, routes
    auth/                       OIDC provider (PKCE)
    cache/                      In-memory TTL cache
    cluster/                    Multi-cluster registry, health poller, quota and secret monitors
    config/                     Environment-based configuration
    database/                   Repositories and migrations
    deployer/                   Helm CLI wrapper for deploy, stop, clean, rollback
    gitprovider/                Azure DevOps, GitHub and GitLab integration
    health/                     Liveness and readiness checks
    helm/                       Values merge and template substitution
    hooks/                      Outbound lifecycle hooks and custom actions
    k8s/                        Cluster client, status watcher, quotas
    leader/                     Leader election (Kubernetes Lease)
    models/                     Domain models and interfaces
    notifier/                   In-app notifications and notification channels
    replica/                    Replica identity and heartbeat
    scheduler/                  Cron-based cleanup policy execution
    sessionstore/               Token blocklist and OIDC state
    telemetry/                  OpenTelemetry setup and metrics
    ttl/                        TTL reaper for expired instances
    websocket/                  Real-time event hub and fan-out
  pkg/                          crypto (AES-GCM), dberrors, utils
  docs/                         Swagger / OpenAPI (generated)
  examples/                     Webhook handler examples (Go, Python)
frontend/                       React single-page application
  src/
    api/                        API client and types
    components/                 Shared UI components
    context/                    Auth, branding, notification, and theme contexts
    hooks/                      WebSocket and utility hooks
    pages/                      Page components
    routes.tsx                  Route definitions
helm/k8s-stack-manager/         Helm chart
  templates/backend/            Backend workload, services, configuration, RBAC
  templates/frontend/           Frontend workload, services, nginx configuration
  templates/traefik/            Traefik IngressRoute and middleware
  templates/mysql/              Bundled MySQL
  templates/otel/               OpenTelemetry collector
  templates/external-secrets/   External Secrets Operator resources
examples/starter-templates/     Starter definition bundles
infra/                          Infrastructure examples
loadtest/                       Load tests (k6 and Playwright)
docs/                           Guides and screenshots
docker-compose.yml              Local development stack
```

## Architecture

```
Frontend (React + MUI + TypeScript)
        |
        v
Backend (Go + Gin)
  |-- REST API with JWT and API-key auth
  |-- MySQL (GORM)
  |-- Git providers (Azure DevOps + GitHub + GitLab)
  |-- Helm values (deep merge + template substitution)
  |-- Multi-cluster support (kubeconfig encrypted at rest)
  |-- Hooks and actions (HTTP, HMAC signed)
  `-- Audit logging
```

See [ARCHITECTURE.md](../ARCHITECTURE.md) for details.
