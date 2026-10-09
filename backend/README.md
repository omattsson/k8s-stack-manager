# K8s Stack Manager — Backend

Go backend for the K8s Stack Manager, built with the Gin framework. Provides REST API for managing Helm-based application stack definitions, instances, templates, and value overrides. Supports JWT authentication, audit logging, Git provider integration (Azure DevOps + GitLab), Helm values generation, and multi-cluster management with encrypted kubeconfig storage.

## Project Structure

```
backend/
├── api/main.go                         # Bootstrap: config → repos → handlers → server
├── internal/
│   ├── api/
│   │   ├── handlers/                   # HTTP handlers (auth, templates, definitions, instances, clusters, analytics, etc.)
│   │   ├── middleware/                  # Auth (JWT + API key), CORS, audit logging, rate limiting, recovery
│   │   └── routes/                     # Route registration
│   ├── config/                         # Environment-based configuration
│   ├── cluster/                        # Multi-cluster registry, health poller, secret refresher
│   ├── database/
│   │   ├── factory.go                  # MySQL connection with retry
│   │   ├── repository.go              # Repository factory
│   │   ├── migrations.go              # Versioned schema migrations
│   │   └── errors.go                  # Re-exports from pkg/dberrors
│   ├── gitprovider/                    # Azure DevOps + GitLab branch listing
│   ├── health/                         # Liveness + readiness checks
│   ├── helm/                           # Values deep-merge + template substitution
│   ├── deployer/                       # Helm CLI wrapper for deploy/undeploy (multi-cluster)
│   ├── k8s/                            # Cluster client + status monitoring
│   ├── models/                         # Domain models + repository interfaces + validation
│   ├── scheduler/                      # Cron-based cleanup policy execution
│   ├── sessionstore/                   # Token blocklist + OIDC state persistence (MySQL/memory)
│   ├── ttl/                            # TTL reaper for auto-expiring stack instances
│   └── websocket/                      # Real-time event broadcasting (hub + clients)
├── pkg/
│   ├── crypto/                         # AES-GCM encryption/decryption for kubeconfig at rest (key derived via SHA-256)
│   ├── dberrors/                       # Canonical error types
│   └── utils/                         # Shared utility functions
└── docs/                               # Swagger/OpenAPI (auto-generated)
```

## API Routes

| Group | Prefix | Auth | Description |
|-------|--------|------|-------------|
| Health | `/health/*` | No | Liveness + readiness |
| Auth | `/api/v1/auth` | Login: No, Register/Me: Yes | JWT login, register, current user |
| Templates | `/api/v1/templates` | Yes (DevOps for writes) | Stack template CRUD, publish, chart management, instantiate |
| Definitions | `/api/v1/stack-definitions` | Yes | Stack definition CRUD, chart configs |
| Instances | `/api/v1/stack-instances` | Yes | Stack instance CRUD, clone, deploy, stop, clean, status, logs |
| Value Overrides | `/api/v1/stack-instances/:id/overrides` | Yes | Per-chart value overrides |
| Branch Overrides | `/api/v1/stack-instances/:id/branches` | Yes | Per-chart branch overrides |
| Git | `/api/v1/git` | Yes | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Yes | Filterable audit trail + CSV/JSON export |
| Users | `/api/v1/users` | Admin | List, delete, disable/enable users |
| API Keys | `/api/v1/users/:id/api-keys` | Yes | Per-user API key management |
| Clusters | `/api/v1/clusters` | Admin | Multi-cluster registration, health, test-connection |
| Shared Values | `/api/v1/clusters/:id/shared-values` | Admin | Per-cluster shared Helm values |
| Admin | `/api/v1/admin` | Admin | Orphaned namespace detection and cleanup |
| Cleanup Policies | `/api/v1/admin/cleanup-policies` | Admin | Cron-based cleanup policy management |
| Analytics | `/api/v1/analytics` | DevOps | Usage overview, template stats, user stats |
| Favorites | `/api/v1/favorites` | Yes | User bookmark management |
| Quick Deploy | `/api/v1/templates/:id/quick-deploy` | Yes | One-click template deployment |

## Prerequisites

- Go 1.26+

## Quick Start

```bash
# From project root — start with Docker Compose (recommended)
make dev

# Or run locally
make dev-local
```

## Configuration

Key environment variables (see `docker-compose.yml` for full list):

| Variable | Default | Description |
|---|---|---|
| `JWT_SECRET` | (required) | JWT signing secret (min 16 chars) |
| `JWT_EXPIRATION` | `24h` | Token expiration |
| `ADMIN_USERNAME` | `admin` | Initial admin username |
| `ADMIN_PASSWORD` | (required) | Initial admin password |
| `SELF_REGISTRATION` | `false` | Allow self-registration |
| `AZURE_DEVOPS_PAT` | | Azure DevOps personal access token |
| `AZURE_DEVOPS_AUTH` | `pat` | `workload-identity` uses an Entra ID token (`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_FEDERATED_TOKEN_FILE`) instead of the PAT |
| `GITLAB_TOKEN` | | GitLab access token |
| `DEFAULT_BRANCH` | `master` | Default Git branch |
| `KUBECONFIG_ENCRYPTION_KEY` | | Passphrase for deriving AES-256 key (SHA-256) to encrypt kubeconfig data and registry passwords at rest |
| `RATE_LIMIT` | `100` | Requests per minute per IP |
| `CORS_ALLOWED_ORIGINS` | `*` | Allowed CORS origins |
| `SESSION_STORE` | `mysql` | Session store backend (`mysql` or `memory`) for token blocklist and OIDC state |
| `ACCESS_TOKEN_EXPIRATION` | `15m` | Access-token lifetime when refresh tokens are on |
| `REFRESH_TOKEN_EXPIRATION` | `168h` | Upper limit for one refresh token |
| `SESSION_IDLE_TIMEOUT` | `30m` | End a session after this time without authenticated requests |
| `SESSION_MAX_LIFETIME` | `12h` | Absolute session lifetime from login; refresh never extends it (at least `ACCESS_TOKEN_EXPIRATION`) |
| `REFRESH_REUSE_GRACE` | `30s` | A just-rotated refresh token still gets a new access token (no new cookie) for this time; `0` disables, max `5m` |
| `SECURE_COOKIES` | `false` | `Secure` flag on the refresh-token cookie; set `true` behind HTTPS |
| `LEADER_ELECTION_ENABLED` | `false` | Elect one replica (Kubernetes Lease) to run the background workers; `true` needs the in-cluster service account. `false`: this process always runs them |
| `LEADER_ELECTION_LEASE_NAME` | `k8s-stack-manager-workers` | Name of the Lease |
| `LEADER_ELECTION_NAMESPACE` | `POD_NAMESPACE` | Namespace of the Lease (else the service account namespace file) |
| `LEADER_ELECTION_LEASE_DURATION` | `15s` | Lease duration |
| `LEADER_ELECTION_RENEW_DEADLINE` | `10s` | The leader stops its workers when it cannot renew for this time |
| `LEADER_ELECTION_RETRY_PERIOD` | `2s` | Time between acquire and renew attempts |
| `POD_NAME` | host name | Replica identity: leader election and the origin of `ws_events` rows |
| `WS_FANOUT_ENABLED` | `false` | Share WebSocket messages between replicas through the `ws_events` table. `false`: no `ws_events` reads or writes (one replica) |
| `WS_FANOUT_POLL_INTERVAL` | `500ms` | Time between two `ws_events` polls (50ms to 1m) |
| `WS_FANOUT_RETENTION` | `5m` | The leader deletes `ws_events` rows older than this (at least 1m) |

See [Sessions](../WIKI.md#sessions) for how the session limits work together.

## Data Storage

- **MySQL** (GORM): All domain entities — Users, Templates, Definitions, Instances, Overrides, ChartConfigs, APIKeys, AuditLogs, Clusters, SharedValues, CleanupPolicies, Favorites, BranchOverrides, SessionEntries (token blocklist + OIDC state)

## Testing

```bash
cd backend && go test ./... -v -short    # Unit tests
cd backend && make test-coverage         # With coverage report (80% threshold)
make test-backend-all                    # Unit + integration tests
```

Tests use testify + httptest with mock repositories, table-driven patterns, and `t.Parallel()`.

## Swagger

Available at http://localhost:8081/swagger/index.html when running.
Regenerate: `cd backend && make docs`
