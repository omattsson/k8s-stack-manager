# K8s Stack Manager

## Architecture Overview

Full-stack app: **Go (Gin) backend** + **React (TypeScript, Vite, MUI) frontend**, with **MySQL** (GORM) or **Azure Table Storage** as swappable data stores. Docker Compose orchestrates all services.

**Bootstrap flow**: `backend/api/main.go` → `config.LoadConfig()` → `database.NewRepository(cfg)` (factory selects MySQL or Azure based on `USE_AZURE_TABLE`) → `routes.SetupRoutes(router, repo, healthChecker, cfg, hub)` → `http.Server` with graceful shutdown (`SIGINT`/`SIGTERM`).

**Ports**: Backend `:8081` on host, frontend `:3000` in dev. Inside Docker, nginx (`proxy_pass` with trailing `/`) and Vite (`rewrite: ^/api → ""`) both strip the `/api` prefix when proxying to `backend:8081`. Local non-Docker dev hits `localhost:8081` directly (`frontend/src/api/config.ts`).

## Development Commands

| Task | Command |
|---|---|
| Full stack (Docker) | `make dev` |
| Backend tests (unit, no DB) | `cd backend && go test ./... -v -short` |
| All backend tests (unit + integration) | `make test-backend-all` (starts MySQL + Azurite) |
| Frontend tests | `cd frontend && npm test` |
| E2E tests | `make test-e2e` (starts infra + backend + Playwright) |
| Swagger docs | `cd backend && make docs` (runs `swag init -g api/main.go`) |
| Coverage (80% threshold) | `cd backend && make test-coverage` |
| Lint | `make lint` (`go vet` + `npm run lint`) |
| Install deps | `make install` |

## Backend Structure

```
backend/
  api/main.go                    # Bootstrap: config → repo → router → server → shutdown
  internal/
    api/routes/routes.go         # All route registration + middleware ordering
    api/handlers/
      items.go                   # CRUD handler pattern (reference implementation)
      handlers.go                # Health handlers (closure-injection, not Handler struct)
      rate_limiter.go            # Per-IP sliding window, returned from SetupRoutes for shutdown
      errors.go                  # handleDBError() maps repo errors to HTTP status
      auth.go                    # AuthHandler: login, register, current user
      stack_definitions.go       # DefinitionHandler: CRUD + chart management
      stack_instances.go         # InstanceHandler: CRUD + clone + deploy/stop/clean
      stack_templates.go         # TemplateHandler: CRUD + publish + instantiate
      chart_configs.go           # Chart config management (nested under definitions)
      template_charts.go         # Template chart config management
      value_overrides.go         # Per-chart value overrides
      audit_logs.go              # AuditLogHandler: filterable audit log viewer
      users.go                   # UserHandler: user management
      api_keys.go                # APIKeyHandler: API key management
      admin.go                   # AdminHandler: orphaned namespace detection/cleanup
      clusters.go                # ClusterHandler: CRUD + test-connection + health
      git.go                     # GitHandler: branch listing, validation
      websocket.go               # WebSocket upgrade handler
      mock_repository.go         # In-memory mock for Item tests (same package)
    api/middleware/
      middleware.go              # CORS, Logger, Recovery, RequestID, MaxBodySize
      auth.go                    # JWT authentication + token generation
      combined_auth.go           # Combined JWT + API key auth middleware
      audit.go                   # Audit logging middleware (applied to route groups)
      role.go                    # RequireAdmin, RequireDevOps role-based access
    config/config.go             # Env vars with godotenv .env fallback, typed config structs
    database/factory.go          # MySQL connection with retry (5x, 2s delay)
    database/repository.go       # NewRepository() factory: MySQL vs Azure Table
    database/migrations.go       # Versioned migrations via schema.Migrator, auto-run on startup
    database/errors.go           # Re-exports from pkg/dberrors (single source of truth)
    models/                      # Domain models, repository interfaces, validation
    health/health.go             # Dependency health checks (liveness/readiness)
    cluster/                     # ClusterRegistry (multi-cluster coordination) + health poller
    deployer/                    # Helm CLI wrapper for deploy/undeploy/status (multi-cluster via registry)
    k8s/                         # Kubernetes cluster client and status monitoring
    gitprovider/                 # Azure DevOps + GitLab branch listing, URL detection
    helm/                        # Values deep-merge, template variable substitution
    websocket/                   # WebSocket hub, client, message types
  pkg/dberrors/errors.go         # Canonical error types: ErrNotFound, ErrDuplicateKey, ErrValidation
  pkg/crypto/                    # AES-GCM encryption/decryption for kubeconfig data at rest (key derived via SHA-256)
```

## Key Backend Patterns

**Repository interface**: The generic `models.Repository` interface uses `Create`, `FindByID`, `Update`, `Delete`, `List` — all take `context.Context` first. Two implementations: `GenericRepository` (GORM/MySQL) and `azure.TableRepository`. Domain-specific repositories (e.g., `StackInstanceRepository`, `UserRepository`, `AuditLogRepository`) have dedicated interfaces in their model files with custom method signatures; these currently use `context.Background()` internally in the Azure implementation. The repository auto-calls `Validate()` on create/update if the model implements `Validator`.

**Handler struct**: `handlers.Handler` holds `models.Repository` and optional `websocket.BroadcastSender` via constructor injection (`NewHandler(repo)` or `NewHandlerWithHub(repo, hub)`). Domain handlers (e.g., `InstanceHandler`, `DefinitionHandler`, `AdminHandler`) use separate structs with specialized repository dependencies injected via their own constructors. Health handlers use factory functions returning `gin.HandlerFunc` via closure.

**Error flow**: Repository returns `*dberrors.DatabaseError` wrapping sentinel errors → `handleDBError()` in `handlers/items.go` maps via `errors.As`/`errors.Is` to HTTP status (400 validation, 404 not found, 409 duplicate/version conflict, 500 internal). **Never expose raw error messages for 500s** — always return `"Internal server error"`.

**Optimistic locking**: Models embed `Version uint` field. Repository `Update()` uses `WHERE version = ?` — returns `"version mismatch"` error (mapped to 409).

**Filter whitelist**: `GenericRepository` has `allowedFilterFields` map. New entities need `NewRepositoryWithFilterFields()` or the existing repo must be extended.

**Routes registration**: `SetupRoutes()` accepts a `Deps` struct with all handler and repository dependencies. Returns `*RateLimiter` (caller must call `Stop()` on shutdown). Middleware order: RequestID → Logger → Recovery → CORS → MaxBodySize (1MB). Auth uses `CombinedAuth` middleware (JWT + API key). Role-based access via `RequireAdmin()`/`RequireDevOps()`. Audit logging via `NewAuditMiddleware()` on route groups. WebSocket at `/ws`, health at `/health/*`, API at `/api/v1/*` (100 req/min per IP).

## Frontend Structure

```
frontend/src/
  api/config.ts          # API_BASE_URL: localhost:8081 (dev) | /api (prod)
  api/client.ts          # Axios instance + service objects
  routes.tsx             # Route definitions
  components/Layout/     # AppBar + nav + footer shell
  pages/{Name}/index.tsx # Page components (one dir per page)
```

**Patterns**: MUI components (no raw HTML), `sx` prop for styling, functional components only, `useState`/`useEffect` for state, service objects with async methods for API calls.

## Testing Conventions

**Backend**: `testify/assert`, table-driven (`tests := []struct{...}` + `t.Run`), `t.Parallel()` on parent and subtests, capture loop var `tt := tt`. Use `MockRepository` (in-memory, same package as handlers). Test setup: `setupTestRouter()` returns `(*gin.Engine, *MockRepository)`. JSON responses validated against schemas in `test_schemas.go` via `gojsonschema`.

**Frontend**: Vitest + Testing Library (unit), Playwright (e2e).

**Integration test naming**: `TestDatabase*` (MySQL), `TestAzureTable*`/`TestAzure*Integration` (Azure).

## Adding a New API Resource

1. Model in `internal/models/models.go` (embed `Base` for ID/timestamps/soft-delete)
2. Validation in `internal/models/validation.go` (implement `Validator` interface)
3. Migration in `internal/database/migrations.go` (incrementing version string)
4. Handler file in `internal/api/handlers/` (follow `items.go` CRUD pattern with `handleDBError()`)
5. Routes in `internal/api/routes/routes.go` under `/api/v1` group
6. Swagger annotations on handlers, then `cd backend && make docs`
7. Tests with `MockRepository` + table-driven pattern
8. Frontend service methods in `api/client.ts`, new page in `pages/`, register in `routes.tsx`

## Domain — K8s Stack Manager

This application enables developers to configure, store, and deploy multi-service Helm-based application stacks to a shared AKS Arc cluster.

### Domain Packages

```
backend/internal/
  cluster/               # ClusterRegistry: multi-cluster client management, health poller
  gitprovider/           # Azure DevOps + GitLab branch listing, URL detection, caching
  helm/                  # Values deep-merge, template variable substitution, YAML export
  deployer/              # Helm CLI wrapper for deploy/undeploy/status (multi-cluster via registry)
  k8s/                   # Kubernetes cluster client and status monitoring
```

### Domain Conventions

- **Audit trail**: Every mutating API endpoint (POST, PUT, DELETE) must create an AuditLog entry
- **Branch default**: "master" unless overridden per stack definition's `DefaultBranch` field
- **Namespace naming**: Auto-generated as `stack-{instance-name}-{owner}`
- **Multi-cluster**: Clusters are registered via `/api/v1/clusters` with kubeconfig data (encrypted at rest via `pkg/crypto`) or kubeconfig path. `ClusterRegistry` manages per-cluster K8s/Helm clients. Health poller monitors cluster status. Stack instances target a specific cluster (or the default).
- **Git provider detection**: URL-based — `dev.azure.com`/`visualstudio.com` → Azure DevOps; `gitlab.com` or custom → GitLab
- **Helm values merge**: Deep-merge chart defaults + instance overrides; substitute template vars `{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`
- **Auth**: JWT with `Authorization: Bearer <token>` header; middleware injects `userID`, `username`, `role` into Gin context
- **Generic design**: No company-specific hardcoding; all branding and configurable values via environment variables

### API Route Groups

| Group | Prefix | Description |
|-------|--------|-------------|
| Auth | `/api/v1/auth` | Login, register, current user |
| Templates | `/api/v1/templates` | CRUD + publish, unpublish, instantiate, clone |
| Stack Definitions | `/api/v1/stack-definitions` | CRUD + nested chart management |
| Stack Instances | `/api/v1/stack-instances` | CRUD + clone, deploy, stop, clean, status, logs |
| Value Overrides | `/api/v1/stack-instances/:id/overrides` | Per-chart value overrides |
| Git | `/api/v1/git` | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Filterable audit log viewer |
| Users | `/api/v1/users` | User management (admin) |
| API Keys | `/api/v1/users/:id/api-keys` | API key management |
| Admin | `/api/v1/admin` | Orphaned namespace detection and cleanup |
| Clusters | `/api/v1/clusters` | Multi-cluster registration, health, test-connection |
| Health | `/health/*` | Liveness + readiness |

## Security Rules

- Never expose raw error messages for 500s — return `"Internal server error"`
- All secrets via environment variables, never hardcode credentials
- GORM parameterizes queries — never interpolate user input in raw SQL
- CORS allows `*` in dev only; restrict `CORS_ALLOWED_ORIGINS` for production
- Rate limiting: per-IP sliding window on API routes
- Recovery middleware catches panics globally — never remove it

## Scalability Notes

- MySQL connection pool: `DB_MAX_OPEN_CONNS` (25), `DB_MAX_IDLE_CONNS` (5), `DB_CONN_MAX_LIFETIME` (5m)
- DB retry: 5 attempts, 2s delay on startup
- Docker networks: `backend-net` (db, backend, azurite) and `frontend-net` (backend, frontend) — maintain separation
- Health checks: register for all external dependencies via `healthChecker.AddCheck()`
- Always implement pagination for list endpoints
- Struct field ordering: optimize for memory alignment (8-byte fields first)

## Project Plan

See `PLAN.md` for the complete phased plan, data models, API specifications, and agent mapping.
