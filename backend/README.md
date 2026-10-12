# K8s Stack Manager: Backend

This is the Go backend of K8s Stack Manager. It uses the Gin framework. It provides a REST API for stack definitions, stack instances, templates, and value overrides. It supports JWT authentication, audit logging, Git provider integration (Azure DevOps and GitLab), Helm values generation, and multi-cluster management with encrypted kubeconfig storage.

## Project Structure

```
backend/
|-- api/                                # main.go (entry point) and bootstrap.go (config -> repos -> handlers -> server)
|-- internal/
|   |-- api/
|   |   |-- handlers/                   # HTTP handlers (auth, templates, definitions, instances, clusters, analytics, and others)
|   |   |-- middleware/                 # Auth (JWT + API key), CORS, audit logging, role checks, security headers, recovery
|   |   `-- routes/                     # Route registration
|   |-- auth/                           # OIDC provider (PKCE)
|   |-- cache/                          # In-memory TTL cache
|   |-- cluster/                        # Cluster registry, health poller, quota and secret monitors, access reconciler
|   |-- config/                         # Configuration from environment variables
|   |-- database/
|   |   |-- factory.go                  # MySQL connection with retry
|   |   |-- repository.go               # Repository factory
|   |   |-- migrations.go               # Versioned schema migrations
|   |   `-- errors.go                   # Re-exports from pkg/dberrors
|   |-- deployer/                       # Helm CLI wrapper for deploy, undeploy, rollback (multi-cluster)
|   |-- gitprovider/                    # Azure DevOps and GitLab branch listing
|   |-- health/                         # Liveness and readiness checks
|   |-- helm/                           # Values deep merge and template substitution
|   |-- hooks/                          # Outbound lifecycle hooks and custom actions
|   |-- k8s/                            # Cluster client, status monitoring, quotas
|   |-- leader/                         # Leader election (Kubernetes Lease)
|   |-- models/                         # Domain models, repository interfaces, validation
|   |-- notifier/                       # In-app notifications and notification channels
|   |-- replica/                        # Replica identity and heartbeat
|   |-- scheduler/                      # Cron-based cleanup policy execution
|   |-- sessionstore/                   # Token blocklist and OIDC state (MySQL or memory)
|   |-- telemetry/                      # OpenTelemetry setup and metrics
|   |-- ttl/                            # TTL reaper for expired instances
|   `-- websocket/                      # Real-time event hub, clients, fan-out
|-- pkg/
|   |-- crypto/                         # AES-GCM encryption for kubeconfig data at rest (key derived with SHA-256)
|   |-- dberrors/                       # Canonical error types
|   `-- utils/                          # Shared utility functions
|-- docs/                               # Swagger / OpenAPI (generated) and hooks.md
`-- examples/                           # Webhook handler examples (Go and Python)
```

## API Routes

All routes are under `/api/v1` except `/health/*` and `/ws`. Swagger documents the details. The "Access" column shows the rule that the router enforces. Some handlers add checks, for example the owner of an instance.

| Group | Prefix | Access | Description |
|-------|--------|--------|-------------|
| Health | `/health/*` | Public | Liveness and readiness |
| Auth | `/api/v1/auth` | Login, refresh, logout, OIDC: public. Register, me, logout-all: signed in | Login, register, current user, token refresh, logout |
| UI Config | `/api/v1/ui-config` | Public | Branding values |
| Items | `/api/v1/items` | Public | CRUD reference implementation (the web UI does not use it) |
| Templates | `/api/v1/templates` | Signed in. `devops` for writes (and owner or `admin` for changes) | Template CRUD, publish, unpublish, chart management, instantiate, clone, versions, bulk operations |
| Quick Deploy | `/api/v1/templates/:id/quick-deploy` | Signed in | One-click template deployment |
| Definitions | `/api/v1/stack-definitions` | Signed in. `devops` to create, import, export, and add or remove charts | Definition CRUD, chart configs, import, export, upgrade |
| Instances | `/api/v1/stack-instances` | Signed in. Owner, `devops`, or `admin` to change. `devops` for bulk | Instance CRUD, clone, deploy, deploy preview, stop, clean, rollback, status, logs, compare, extend, follow, custom actions |
| Value Overrides | `/api/v1/stack-instances/:id/overrides` | Owner, `devops`, or `admin` | Per-chart value overrides |
| Branch Overrides | `/api/v1/stack-instances/:id/branches` | Owner, `devops`, or `admin` | Per-chart branch overrides |
| Quota Overrides | `/api/v1/stack-instances/:id/quota-overrides` | Owner, `devops`, or `admin` | Per-instance resource quota override |
| Git | `/api/v1/git` | Signed in | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | `devops` to read, `admin` to export | Filterable audit trail and CSV or JSON export |
| Users | `/api/v1/users` | `admin` | List, delete, disable, enable, password reset, role change |
| API Keys | `/api/v1/users/:id/api-keys` | Own user or `admin` | Per-user API key management |
| Clusters | `/api/v1/clusters` | List and get: signed in. Health, nodes, namespaces, quota read, utilization: `devops`. Other writes: `admin` | Cluster registration, health, test connection, quotas |
| Shared Values | `/api/v1/clusters/:id/shared-values` | `admin` | Per-cluster shared Helm values |
| Admin | `/api/v1/admin` | `admin` | Orphaned namespace detection and cleanup |
| Cleanup Policies | `/api/v1/admin/cleanup-policies` | `admin` | Cron-based cleanup policy management |
| Notification Channels | `/api/v1/admin/notification-channels` | `devops` | Outbound webhook channels, subscriptions, test, delivery logs |
| Analytics | `/api/v1/analytics` | `devops` (`/users`: `admin`) | Usage overview, template statistics, user statistics |
| Dashboard | `/api/v1/dashboard` | Signed in | Overview for the landing page |
| Notifications | `/api/v1/notifications` | Signed in | List, mark read, count, preferences |
| Favorites | `/api/v1/favorites` | Signed in | User bookmarks |
| WebSocket | `/ws` | Token or API key | Real-time status and log events |

## Prerequisites

- Go 1.26 or later
- MySQL 8.0 or later (the Docker Compose stack uses 8.4)

## Quick Start

```bash
# From the project root: start with Docker Compose (recommended)
make dev

# Or run locally (starts MySQL in Docker, the backend, and the frontend)
make dev-local
```

## Configuration

The backend reads all configuration from environment variables. It also reads a `.env` file in the working directory (or the file in `ENV_FILE`). The code is in `internal/config/config.go`. `docker-compose.yml` and `helm/k8s-stack-manager/values.yaml` show the values for each deployment.

### Server

| Variable | Default | Description |
|---|---|---|
| `SERVER_HOST` | empty (all interfaces) | Listen address |
| `SERVER_PORT` | `8081` | Listen port |
| `SERVER_READ_TIMEOUT` | `10s` | HTTP read timeout |
| `SERVER_WRITE_TIMEOUT` | `0` (off) | HTTP write timeout. `0` is needed for WebSocket and long responses. |
| `SERVER_IDLE_TIMEOUT` | `30s` | HTTP idle timeout |
| `SERVER_SHUTDOWN_TIMEOUT` | `30s` | Time to finish requests at shutdown |
| `RATE_LIMIT` | `100` | Requests per minute for each IP address on `/api/v1` |
| `LOGIN_RATE_LIMIT` | `10` | Requests per minute for each IP address on login and refresh. `0` uses the general limiter. |
| `CORS_ALLOWED_ORIGINS` | `*` | Allowed CORS origins. Restrict this in production. |
| `ENABLE_SWAGGER` | `false` | Serve the Swagger UI at `/swagger/index.html` |
| `APP_DEBUG` | `true` | Debug mode. `false` in the Helm chart. Gin runs in release mode unless `APP_DEBUG=true` or `GIN_MODE` is set. |
| `GIN_MODE` | not set | Gin mode (`debug`, `release`, `test`). It wins over `APP_DEBUG`. |
| `HEALTH_VERBOSE` | `false` | Show details in the readiness response |
| `PPROF_ENABLED`, `PPROF_ADDR` | `false`, `127.0.0.1:6060` | Serve Go pprof on a separate address |
| `DEFAULT_INSTANCE_TTL_MINUTES` | `0` | TTL for a new instance that does not set one. `0` means no TTL. |

### Database

| Variable | Default | Description |
|---|---|---|
| `DB_HOST`, `DB_PORT` | `localhost`, `3306` | MySQL address |
| `DB_USER`, `DB_PASSWORD`, `DB_NAME` | `root`, empty, `app` | MySQL credentials and database name |
| `DB_MAX_OPEN_CONNS`, `DB_MAX_IDLE_CONNS` | `25`, `5` | Connection pool size |
| `DB_CONN_MAX_LIFETIME` | `5m` | Maximum age of a connection |

### Authentication and sessions

| Variable | Default | Description |
|---|---|---|
| `JWT_SECRET` | (required) | JWT signing secret (minimum 16 characters) |
| `JWT_EXPIRATION` | `24h` | Lifetime of the long-lived SSO CLI token |
| `ADMIN_USERNAME` | `admin` | Username of the initial admin user |
| `ADMIN_PASSWORD` | (required) | Password of the initial admin user. The backend creates the user at startup when it does not exist. |
| `SELF_REGISTRATION` | `false` | Let any signed-in user register new `user` accounts. With `false`, only `admin` users register users. |
| `SESSION_STORE` | `mysql` | Session store for the token blocklist and OIDC state (`mysql` or `memory`) |
| `ACCESS_TOKEN_EXPIRATION` | `15m` | Access-token lifetime |
| `REFRESH_TOKEN_EXPIRATION` | `168h` | Upper limit for one refresh token |
| `SESSION_IDLE_TIMEOUT` | `30m` | End a session after this time without authenticated requests |
| `SESSION_MAX_LIFETIME` | `12h` | Maximum session time from login. A refresh never extends it. At least `ACCESS_TOKEN_EXPIRATION`. |
| `REFRESH_REUSE_GRACE` | `30s` | A just-rotated refresh token still gets a new access token (no new cookie) for this time. `0` disables it. Maximum `5m`. |
| `MAX_REFRESH_TOKENS_PER_USER` | `10` | When a user has this number of active refresh tokens, the next login revokes the older ones. `0` means no limit. |
| `API_KEY_MAX_LIFETIME_DAYS` | `0` | Maximum lifetime of an API key in days. `0` means no limit. |
| `LOGIN_CACHE_TTL` | `30s` | Time that a successful password check stays cached (it skips only the password check) |
| `SECURE_COOKIES` | `false` | Set the `Secure` flag on the refresh-token cookie. Use `true` behind HTTPS. |
| `COOKIE_SAMESITE` | `strict` | `SameSite` of the refresh-token cookie (`strict`, `lax`, or `none`). `none` needs `SECURE_COOKIES=true`. |
| `OIDC_ENABLED`, `OIDC_*` | `false` | OpenID Connect settings. See [OIDC single sign-on](../WIKI.md#authentication). |

### Git providers

| Variable | Default | Description |
|---|---|---|
| `AZURE_DEVOPS_PAT` | empty | Azure DevOps personal access token (PAT) |
| `AZURE_DEVOPS_AUTH` | `pat` | `workload-identity` uses an Entra ID token (`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_FEDERATED_TOKEN_FILE`) instead of the PAT |
| `GITLAB_TOKEN` | empty | GitLab access token |
| `GITLAB_BASE_URL` | empty (`https://gitlab.com`) | Base URL of a self-hosted GitLab |
| `DEFAULT_BRANCH` | `master` | Default Git branch |

### Deployment and clusters

| Variable | Default | Description |
|---|---|---|
| `HELM_BINARY` | `helm` | Path of the Helm binary |
| `KUBECONFIG_PATH` | `KUBECONFIG`, else empty | Kubeconfig of the default cluster |
| `KUBECONFIG_ENCRYPTION_KEY` | empty | Passphrase. The backend derives an AES-256 key from it (SHA-256) to encrypt kubeconfig data, registry passwords, and webhook channel secrets at rest. Without it, the backend rejects a cluster that has kubeconfig data. |
| `DEPLOYMENT_TIMEOUT` | `10m` | `helm --timeout` for one chart. A deploy of N charts can take N times this value. |
| `DEPLOY_STABILIZE_TIMEOUT` | `5m` | Time to wait for pod readiness after the installs. `0` turns the wait off. |
| `DEPLOY_STABILIZE_POLL_INTERVAL` | `5s` | Poll interval of the readiness wait |
| `MAX_CONCURRENT_DEPLOYS` | `5` | Number of operations that run at the same time |
| `CLUSTER_HEALTH_POLL_INTERVAL` | `60s` | Time between cluster health checks |
| `ALLOWED_CHART_REPOS` | empty | Comma-separated chart repository URL prefixes (`oci://`, `https://`, `http://`). Empty allows every chart source. See [Chart sources](../WIKI.md#chart-sources). |
| `NAMESPACE_LABELS` | empty | Extra labels on stack namespaces (`key=value,key=value`) |
| `STACK_ACCESS_CLUSTER_ROLE`, `STACK_ACCESS_SERVICE_ACCOUNT` | empty | Namespace-scoped access of the backend. The Helm chart sets both. See [Backend rights on the cluster](../WIKI.md#backend-rights-on-the-cluster). |
| `NAMESPACE_ROLE_BINDINGS_JSON` | empty | RoleBindings that the deployer adds to each stack namespace (Helm: `backend.namespaceRoleBindings`) |
| `WILDCARD_TLS_SOURCE_NAMESPACE`, `WILDCARD_TLS_SOURCE_SECRET`, `WILDCARD_TLS_TARGET_SECRET` | empty | Copy a TLS secret into each stack namespace |
| `HOOKS_CONFIG_FILE` | empty | Hook and action configuration file (see [EXTENDING.md](../EXTENDING.md)) |

### Replicas and WebSocket

| Variable | Default | Description |
|---|---|---|
| `LEADER_ELECTION_ENABLED` | `false` | Elect one replica (Kubernetes Lease) to run the background workers. `true` needs the in-cluster service account. `false`: this process always runs them. |
| `LEADER_ELECTION_LEASE_NAME` | `k8s-stack-manager-workers` | Name of the Lease |
| `LEADER_ELECTION_NAMESPACE` | `POD_NAMESPACE` | Namespace of the Lease (else the namespace file of the service account) |
| `LEADER_ELECTION_LEASE_DURATION` | `15s` | Lease duration |
| `LEADER_ELECTION_RENEW_DEADLINE` | `10s` | The leader stops its workers when it cannot renew for this time |
| `LEADER_ELECTION_RETRY_PERIOD` | `2s` | Time between acquire and renew attempts |
| `POD_NAME` | host name | Replica identity: leader election and the origin of `ws_events` rows |
| `WS_FANOUT_ENABLED` | `false` | Share WebSocket messages between replicas through the `ws_events` table. `false`: no `ws_events` reads or writes (one replica). |
| `WS_FANOUT_POLL_INTERVAL` | `500ms` | Time between two `ws_events` polls (50ms to 1m) |
| `WS_FANOUT_RETENTION` | `5m` | The leader deletes `ws_events` rows older than this (at least 1m) |

### Observability and branding

| Variable | Default | Description |
|---|---|---|
| `OTEL_ENABLED` | `false` | Export OpenTelemetry traces and metrics |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OTLP gRPC endpoint |
| `OTEL_SERVICE_NAME` | `k8s-stack-manager` | Service name |
| `OTEL_TRACE_SAMPLE_RATE` | `1.0` | Trace sampling rate (0.0 to 1.0) |
| `METRICS_ENABLED` | `false` | Serve Prometheus metrics at `/metrics` on a separate address |
| `METRICS_ADDR` | `:9090` | Address of the metrics server |
| `APP_TITLE`, `APP_LOGO_URL`, `APP_FAVICON_URL` | see [Branding](../WIKI.md#branding) | Product name, logo, and favicon of the web UI |

See [Sessions](../WIKI.md#sessions) for the way the session limits work together.

## Data Storage

MySQL (GORM) stores all domain entities. These are users, templates, definitions, instances, overrides, chart configs, API keys, audit logs, clusters, shared values, cleanup policies, favorites, branch overrides, quota overrides, notifications, notification channels, deployment logs, followers, refresh tokens, session entries (token blocklist and OIDC state), `ws_events`, and `replica_heartbeats`. The migrations in `internal/database/migrations.go` run at startup.

## Testing

```bash
cd backend && go test ./... -v -short    # Unit tests
cd backend && make test-coverage         # With coverage report (80% threshold)
make test-backend-all                    # Unit and integration tests (starts MySQL)
```

The tests use testify and httptest with mock repositories, table-driven patterns, and `t.Parallel()`.

## Swagger

Set `ENABLE_SWAGGER=true`. The Swagger UI is then at http://localhost:8081/swagger/index.html. `make dev` sets it for you. To regenerate the documentation, run `cd backend && make docs`.
