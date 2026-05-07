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

- Go 1.25+

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
| `GITLAB_TOKEN` | | GitLab access token |
| `DEFAULT_BRANCH` | `master` | Default Git branch |
| `KUBECONFIG_ENCRYPTION_KEY` | | Passphrase for deriving AES-256 key (SHA-256) to encrypt kubeconfig data and registry passwords at rest |
| `RATE_LIMIT` | `100` | Requests per minute per IP |
| `CORS_ALLOWED_ORIGINS` | `*` | Allowed CORS origins |
| `SESSION_STORE` | `mysql` | Session store backend (`mysql` or `memory`) for token blocklist and OIDC state |

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
