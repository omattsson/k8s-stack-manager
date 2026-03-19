# K8s Stack Manager — Backend

Go backend for the K8s Stack Manager, built with the Gin framework. Provides REST API for managing Helm-based application stack definitions, instances, templates, and value overrides. Supports JWT authentication, audit logging, Git provider integration (Azure DevOps + GitLab), and Helm values generation.

## Project Structure

```
backend/
├── api/main.go                         # Bootstrap: config → repos → handlers → server
├── internal/
│   ├── api/
│   │   ├── handlers/                   # HTTP handlers (auth, templates, definitions, instances, etc.)
│   │   ├── middleware/                  # Auth (JWT + API key), CORS, audit logging, rate limiting, recovery
│   │   └── routes/                     # Route registration
│   ├── config/                         # Environment-based configuration
│   ├── database/
│   │   ├── azure/                      # Azure Table Storage repositories
│   │   ├── factory.go                  # MySQL connection with retry
│   │   ├── repository.go              # Repository factory (MySQL vs Azure)
│   │   ├── migrations.go              # Versioned schema migrations
│   │   └── errors.go                  # Re-exports from pkg/dberrors
│   ├── gitprovider/                    # Azure DevOps + GitLab branch listing
│   ├── health/                         # Liveness + readiness checks
│   ├── helm/                           # Values deep-merge + template substitution
│   ├── models/                         # Domain models + repository interfaces + validation
│   └── websocket/                      # Real-time event broadcasting (hub + clients)
├── pkg/dberrors/                       # Canonical error types
└── docs/                               # Swagger/OpenAPI (auto-generated)
```

## API Routes

| Group | Prefix | Auth | Description |
|-------|--------|------|-------------|
| Health | `/health/*` | No | Liveness + readiness |
| Auth | `/api/v1/auth` | Login: No, Register/Me: Yes | JWT login, register, current user |
| Templates | `/api/v1/templates` | Yes (DevOps for writes) | Stack template CRUD, publish, chart management, instantiate |
| Definitions | `/api/v1/stack-definitions` | Yes | Stack definition CRUD, chart configs |
| Instances | `/api/v1/stack-instances` | Yes | Stack instance CRUD, clone, value export |
| Git | `/api/v1/git` | Yes | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Yes | Filterable audit trail |
| Users | `/api/v1/users` | Admin | List and delete users |
| API Keys | `/api/v1/users/:id/api-keys` | Yes | Per-user API key management |

## Prerequisites

- Go 1.24+
- Docker (for Azurite local dev)

## Quick Start

```bash
# From project root — start with Docker Compose (recommended)
make dev

# Or run locally with Azurite
make azurite-start
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
| `USE_AZURE_TABLE` | `false` | Enable Azure Table Storage |
| `USE_AZURITE` | `false` | Use Azurite emulator |
| `AZURE_TABLE_ACCOUNT_NAME` | | Azure Storage account name |
| `AZURE_TABLE_ACCOUNT_KEY` | | Azure Storage account key |
| `AZURE_TABLE_ENDPOINT` | | Azure Table endpoint |
| `AZURE_DEVOPS_PAT` | | Azure DevOps personal access token |
| `GITLAB_TOKEN` | | GitLab access token |
| `DEFAULT_BRANCH` | `master` | Default Git branch |
| `RATE_LIMIT` | `100` | Requests per minute per IP |
| `CORS_ALLOWED_ORIGINS` | `*` | Allowed CORS origins |

## Data Storage

- **Azure Table Storage** (via Azurite locally): Users, Templates, Definitions, Instances, Overrides, ChartConfigs, APIKeys, AuditLogs
- **MySQL** (GORM): Legacy Items table only

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
