# K8s Stack Manager

## Architecture Overview

Full-stack app: **Go (Gin) backend** + **React (TypeScript, Vite, MUI) frontend**, with **MySQL** (GORM) or **Azure Table Storage** as swappable data stores. Docker Compose orchestrates all services.

**Bootstrap flow**: `backend/api/main.go` → `config.LoadConfig()` → `database.NewRepositoryWithGormDB(cfg)` (factory selects MySQL or Azure based on `USE_AZURE_TABLE`) → `routes.SetupRoutes(router, routes.Deps{...})` → `http.Server` with graceful shutdown (`SIGINT`/`SIGTERM`).

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
| Helm lint | `make helm-lint` |
| Helm dry-run render | `make helm-template` |
| Helm install | `make helm-install` |
| Helm upgrade | `make helm-upgrade` |
| Helm uninstall | `make helm-uninstall` |

## Helm Chart (Kubernetes Deployment)

The Helm chart lives in `helm/k8s-stack-manager/` and deploys the full stack to Kubernetes using **Argo Rollouts** (canary strategy) and **Traefik** IngressRoute.

### Prerequisites
- Kubernetes cluster with `kubectl` context configured
- Helm 3+
- Argo Rollouts controller installed (`kubectl create namespace argo-rollouts && kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml`)
- Traefik ingress controller with CRDs installed

### Chart Structure

```
helm/k8s-stack-manager/
  Chart.yaml                              # Chart metadata (v0.1.0)
  values.yaml                             # All configurable values
  templates/_helpers.tpl                   # Reusable named templates
  templates/NOTES.txt                      # Post-install instructions
  templates/azurite/deployment.yaml        # Azurite for local Azure Table Storage
  templates/azurite/service.yaml           # Azurite ClusterIP (blob/queue/table ports)
  templates/azurite/pvc.yaml              # Persistent storage for Azurite data
  templates/backend/configmap.yaml         # Non-secret env vars
  templates/backend/secret.yaml            # Secret env vars (JWT, passwords, keys)
  templates/backend/serviceaccount.yaml    # Optional ServiceAccount
  templates/backend/service.yaml           # Stable service
  templates/backend/service-canary.yaml    # Canary service
  templates/backend/rollout.yaml           # Argo Rollout (canary: 20%→50%→80%)
  templates/frontend/configmap.yaml        # nginx.conf (SPA-only routing)
  templates/frontend/serviceaccount.yaml   # Optional ServiceAccount
  templates/frontend/service.yaml          # Stable service
  templates/frontend/service-canary.yaml   # Canary service
  templates/frontend/rollout.yaml          # Argo Rollout (canary: 20%→50%→80%)
  templates/traefik/middleware.yaml         # StripPrefix (/api) + secure headers
  templates/traefik/ingressroute.yaml      # Routes: /api→backend, /ws→backend, /→frontend
```

### Key Design Decisions
- **Argo Rollouts** instead of Deployments — canary strategy with stable + canary services for progressive delivery
- **Traefik IngressRoute** CRD — routes `/api/*` to backend (strips prefix), `/ws` to backend, `/` to frontend
- **Azurite enabled by default** (`azurite.enabled: true`) — provides Azure Table Storage emulator for local/dev clusters
- **nginx in frontend pod** serves SPA only (no API proxy); Traefik handles all routing
- **ConfigMap/Secret checksums** in pod annotations — trigger rollout on config changes
- **Security contexts** — backend runs as non-root (uid 65532), readOnlyRootFilesystem; frontend drops all capabilities

### Configuration
Key values in `values.yaml`:
- `backend.env.*` — Non-secret env vars (ConfigMap)
- `backend.secrets.*` — Secret env vars like `JWT_SECRET`, `ADMIN_PASSWORD` (Secret)
- `backend.replicas` / `frontend.replicas` — Independently scalable
- `azurite.enabled` — Toggle Azurite (disable for production with real Azure Table Storage)
- `ingress.enabled` / `ingress.host` — Traefik IngressRoute settings
- `ingress.tls.enabled` / `ingress.tls.secretName` — TLS termination

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
      errors.go                  # mapError() maps repo errors to HTTP status for domain handlers
      admin.go                   # AdminHandler: orphaned namespace detection/cleanup
      analytics.go               # AnalyticsHandler: overview, template, user stats
      api_keys.go                # APIKeyHandler: API key management
      audit_logs.go              # AuditLogHandler: filterable audit log viewer
      auth.go                    # AuthHandler: login, register, current user
      oidc.go                    # OIDCHandler: OpenID Connect authentication flow
      branch_overrides.go        # BranchOverrideHandler: per-chart branch overrides
      bulk_operations.go         # BulkHandler: bulk deploy/stop/clean/delete (up to 50 instances)
      bulk_template_operations.go # TemplateHandler: bulk delete/publish/unpublish (up to 50 templates)
      instance_quota_overrides.go  # InstanceQuotaOverrideHandler: per-instance resource quota overrides
      chart_configs.go           # Chart config management (nested under definitions)
      cleanup_policies.go        # CleanupPolicyHandler: CRUD + manual run
      clusters.go                # ClusterHandler: CRUD + test-connection + health + quotas + utilization
      favorites.go               # FavoriteHandler: user bookmark management
      git.go                     # GitHandler: branch listing, validation
      notifications.go           # NotificationHandler: list, read/unread, preferences
      quick_deploy.go            # QuickDeployHandler: template quick-deploy
      shared_values.go           # SharedValuesHandler: per-cluster shared values
      stack_definitions.go       # DefinitionHandler: CRUD + chart management + import/export
      stack_instances.go         # InstanceHandler: CRUD + clone + deploy/stop/clean + compare + extend TTL
      stack_templates.go         # TemplateHandler: CRUD + publish + instantiate + version snapshots
      template_charts.go         # Template chart config management
      template_versions.go       # TemplateVersionHandler: version history listing + diff
      users.go                   # UserHandler: user management
      value_overrides.go         # Per-chart value overrides
      websocket.go               # WebSocket upgrade handler
      mock_repository.go         # In-memory mock for Item tests (same package)
      mock_broadcast_sender.go   # Test double for websocket.BroadcastSender
    api/middleware/
      middleware.go              # CORS, Logger, Recovery, RequestID, MaxBodySize
      auth.go                    # JWT authentication + token generation
      combined_auth.go           # Combined JWT + API key auth middleware
      audit.go                   # Audit logging middleware (applied to route groups)
      role.go                    # RequireAdmin, RequireDevOps role-based access
    config/config.go             # Env vars with godotenv .env fallback, typed config structs
    database/database.go         # Database struct wrapping gorm.DB, NewDatabase(), Transaction, Ping
    database/config.go           # Database connection Config struct, loaded from env vars
    database/schema.go           # SchemaManager interface for table creation/dropping/inspection
    database/factory.go          # MySQL connection with retry (5x, 2s delay)
    database/repository.go       # NewRepository() factory: MySQL vs Azure Table
    database/migrations.go       # Versioned migrations via schema.Migrator, auto-run on startup
    database/errors.go           # Re-exports from pkg/dberrors (single source of truth)
    database/schema/             # Migrator and versioned migration structs
    models/                      # Domain models, repository interfaces, validation
    health/health.go             # Dependency health checks (liveness/readiness)
    auth/                        # OIDC provider + state store for OpenID Connect auth
    cluster/                     # ClusterRegistry (multi-cluster coordination) + health poller
    deployer/                    # Helm CLI wrapper for deploy/undeploy/status (multi-cluster via registry)
    k8s/                         # Kubernetes cluster client, status monitoring, resource quota management
    gitprovider/                 # Azure DevOps + GitLab branch listing, URL detection
    helm/                        # Values deep-merge, template variable substitution
    notifier/                    # Notification dispatch (creates notifications on deploy/stop/clean events)
    websocket/                   # WebSocket hub, client, message types
    scheduler/                   # Cron-based cleanup policy execution
    ttl/                         # TTL reaper for auto-expiring stack instances
  pkg/dberrors/errors.go         # Canonical error types: ErrNotFound, ErrDuplicateKey, ErrValidation, ErrNotImplemented
  pkg/crypto/                    # AES-GCM encryption/decryption for kubeconfig data at rest (key derived via SHA-256)
  pkg/utils/                     # Shared utilities (e.g., cryptographic random string generation)
  internal/test/test_helpers.go  # Shared test utilities (test server setup)
```

## Key Backend Patterns

**Repository interface**: The generic `models.Repository` interface uses `Create`, `FindByID`, `Update`, `Delete`, `List` — all take `context.Context` first. Two implementations: `GenericRepository` (GORM/MySQL) and `azure.TableRepository`. Domain-specific repositories (e.g., `StackInstanceRepository`, `UserRepository`, `AuditLogRepository`) have dedicated interfaces in their model files with custom method signatures; these currently use `context.Background()` internally in the Azure implementation. The repository auto-calls `Validate()` on create/update if the model implements `Validator`. List endpoints use `ListPaged(limit, offset)` returning `([]T, total, error)` — GORM uses `SELECT`+column projection+`LIMIT/OFFSET`, Azure fetches all then slices in-memory. Batch methods (`CountByTemplateIDs`, `FindByIDs`) eliminate N+1 queries for enrichment lookups.

**Handler struct**: `handlers.Handler` holds `models.Repository` and optional `websocket.BroadcastSender` via constructor injection (`NewHandler(repo)` or `NewHandlerWithHub(repo, hub)`). Domain handlers (e.g., `InstanceHandler`, `DefinitionHandler`, `AdminHandler`) use separate structs with specialized repository dependencies injected via their own constructors. Health handlers use factory functions returning `gin.HandlerFunc` via closure.

**Error flow**: Repository returns `*dberrors.DatabaseError` wrapping sentinel errors → two mapping functions translate to HTTP status: `handleDBError()` in `handlers/items.go` (Items reference implementation, uses `errors.As`/`errors.Is`) and `mapError()` in `handlers/errors.go` (domain handlers, takes entity name for contextual messages). Both map to 400 validation, 404 not found, 409 duplicate/conflict, 501 not implemented, 500 internal. **Never expose raw error messages for 500s** — always return `"Internal server error"`.

**Optimistic locking**: Models embed `Version uint` field. Repository `Update()` uses `WHERE version = ?` — returns `"version mismatch"` error (mapped to 409).

**Filter whitelist**: `GenericRepository` has `allowedFilterFields` map. New entities need `NewRepositoryWithFilterFields()` or the existing repo must be extended.

**Routes registration**: `SetupRoutes()` accepts a `Deps` struct with all handler and repository dependencies. Returns `*RateLimiter` (caller must call `Stop()` on shutdown). Middleware order: RequestID → Logger → Recovery → CORS → MaxBodySize (1MB). Auth uses `CombinedAuth` middleware (JWT + API key). Role-based access via `RequireAdmin()`/`RequireDevOps()`. Audit logging via `NewAuditMiddleware()` on route groups. WebSocket at `/ws`, health at `/health/*`, API at `/api/v1/*` (100 req/min per IP).

## Frontend Structure

```
frontend/src/
  api/config.ts                # API_BASE_URL: localhost:8081 (dev) | /api (prod)
  api/client.ts                # Axios instance + service objects
  routes.tsx                   # Route definitions
  App.tsx                      # Root component with providers
  main.tsx                     # Entry point
  components/
    Layout/                    # AppBar + nav + footer shell
    AccessUrls/                # Access URL display for stack instances
    BranchSelector/            # Git branch picker with autocomplete
    ConfirmDialog/             # Reusable confirmation modal
    DeploymentLogViewer/       # Real-time deployment log display
    EmptyState/                # Placeholder for empty lists
    EntityLink/                # Clickable link to related entities
    ErrorBoundary/             # React error boundary wrapper
    FavoriteButton/            # Toggle bookmark on templates/instances
    LoadingState/              # Centered CircularProgress wrapper
    PodStatusDisplay/          # Kubernetes pod status visualization
    ProtectedRoute/            # Auth-gated route wrapper
    NotificationCenter/        # Notification bell dropdown in app bar
    QuickDeployDialog/         # One-click template deploy modal
    QuotaConfigDialog/         # Resource quota configuration modal
    StatusBadge/               # Colored status chip
    TtlSelector/               # TTL duration picker
    YamlEditor/                # YAML text editor with syntax support
  pages/
    Login/                     # Authentication page
    AuthCallback/              # OIDC authentication callback handler
    StackInstances/            # Dashboard — instance list, deploy, stop, clean
    StackDefinitions/          # Definition CRUD + chart management
    Templates/                 # Template CRUD + publish, instantiate
    AuditLog/                  # Filterable audit log viewer
    Admin/                     # Orphaned namespace detection/cleanup
    Profile/                   # User profile and settings
    Analytics/                 # Usage overview, template stats, user stats
    CleanupPolicies/           # Cron-based cleanup policy management
    ClusterHealth/             # Multi-cluster health monitoring
    Notifications/             # Full notification list page with filters and pagination
    SharedValues/              # Per-cluster shared Helm values
    NotFound/                  # 404 page
  context/
    AuthContext.tsx             # Authentication state + JWT token management
    NotificationContext.tsx     # Toast/snackbar notification provider
    ThemeContext.tsx            # Light/dark theme toggle provider
  hooks/
    useCountdown.ts            # Countdown timer hook (e.g., TTL display)
    useUnsavedChanges.ts       # Unsaved changes warning hook
    useWebSocket.ts            # WebSocket hook for real-time updates
  theme/
    index.ts                   # MUI theme export (combines palette, typography, components)
    palette.ts                 # Color palette definitions
    typography.ts              # Typography variant overrides
    components.ts              # MUI component default prop/style overrides
  types/                       # Shared TypeScript type definitions
  utils/                       # Utility functions (timeAgo, role helpers, notification helpers, recently used tracking)
```

**Patterns**: MUI components (no raw HTML), `sx` prop for styling, functional components only, `useState`/`useEffect` for state, service objects with async methods for API calls. All service objects and methods in `api/client.ts` must have TSDoc comments with `@param`, `@returns`, and `@see` (HTTP method + route).

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
  deployer/              # Helm CLI wrapper for deploy/undeploy/status (multi-cluster via registry), cleanup executor, expiry stopper
  k8s/                   # Kubernetes cluster client, status monitoring, resource quota management
  notifier/              # Notification dispatch (creates notifications on deploy/stop/clean events)
  scheduler/             # Cron-based cleanup policy execution with condition parsing
  ttl/                   # TTL reaper: background goroutine auto-expiring instances
  auth/                  # OIDC provider: OpenID Connect authentication, state store
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
- **Cleanup policies**: Cron-scheduled actions (stop/clean/delete) on instances matching a condition. Policies target a cluster (or "all"). Scheduler reloads on policy changes. Manual run supported with dry-run mode.
- **TTL auto-expiry**: Instances with `TTLMinutes > 0` get `ExpiresAt` set on deploy. Background reaper checks every minute and stops expired instances.
- **Favorites**: Users can bookmark templates and instances. Stored as `UserFavorite` entities.
- **Shared values**: Per-cluster Helm values applied to all instances in that cluster, merged by priority (lowest first) before instance-specific overrides.
- **Analytics**: Read-only aggregation of instance counts, deployment stats, template usage, and user activity.
- **Quick deploy**: One-click flow: template → new instance → deploy. Generates instance name and namespace automatically.
- **Per-chart branch overrides**: Instances can override the branch per chart (default uses the definition's `DefaultBranch`). Substituted in Helm values via `{{.Branch}}`.
- **Notifications**: In-app notifications for deploy/stop/clean events. Per-user notification preferences. Unread count for badge display. Notification dispatch via `notifier` package.
- **Template versioning**: Templates maintain version history; snapshots are created automatically on publish. Version diff compares chart configs between snapshots.
- **Resource quotas**: Per-cluster resource quotas (CPU, memory, storage, pods) enforced via Kubernetes ResourceQuota and LimitRange objects. Admin-configurable via API.
- **Bulk operations**: Bulk deploy/stop/clean/delete supports up to 50 instances per request. Returns per-instance success/failure results.
- **Bulk template operations**: Bulk delete/publish/unpublish supports up to 50 templates per request. Returns per-template success/failure results. Requires DevOps+ role.
- **Instance comparison**: Side-by-side comparison of two stack instances including merged Helm values per chart.
- **Import/export**: Stack definitions can be exported as JSON bundles and re-imported to create new definitions with charts.

### API Route Groups

| Group | Prefix | Description |
|-------|--------|-------------|
| Auth | `/api/v1/auth` | Login, register, current user |
| Templates | `/api/v1/templates` | CRUD + publish, unpublish, instantiate, clone |
| Template Versions | `/api/v1/templates/:id/versions` | Version history listing, detail, diff |
| Stack Definitions | `/api/v1/stack-definitions` | CRUD + nested chart management + import/export |
| Stack Instances | `/api/v1/stack-instances` | CRUD + clone, deploy, stop, clean, status, logs, compare, extend TTL |
| Bulk Operations | `/api/v1/stack-instances/bulk` | Bulk deploy, stop, clean, delete (up to 50 instances) |
| Bulk Template Ops | `/api/v1/templates/bulk` | Bulk delete, publish, unpublish templates (up to 50) |
| Value Overrides | `/api/v1/stack-instances/:id/overrides` | Per-chart value overrides |
| Git | `/api/v1/git` | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Filterable audit log viewer |
| Users | `/api/v1/users` | User management (admin) |
| API Keys | `/api/v1/users/:id/api-keys` | API key management |
| Admin | `/api/v1/admin` | Orphaned namespace detection and cleanup |
| Clusters | `/api/v1/clusters` | Multi-cluster registration, health, test-connection, quotas, utilization |
| Branch Overrides | `/api/v1/stack-instances/:id/branches` | Per-chart branch overrides |
| Favorites | `/api/v1/favorites` | User bookmark management |
| Notifications | `/api/v1/notifications` | List, read/unread, count, preferences |
| Cleanup Policies | `/api/v1/admin/cleanup-policies` | Cron-based cleanup policy management |
| Shared Values | `/api/v1/clusters/:id/shared-values` | Per-cluster shared Helm values |
| Analytics | `/api/v1/analytics` | Usage overview, template stats, user stats |
| Quick Deploy | `/api/v1/templates/:id/quick-deploy` | One-click template deployment |
| OIDC Auth | `/api/v1/auth/oidc` | OpenID Connect config, authorize, callback |
| Quota Overrides | `/api/v1/stack-instances/:id/quota-overrides` | Per-instance resource quota overrides |
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
- Always implement pagination for list endpoints — use `ListPaged(limit, offset)` with default 25, max 100. Select only columns needed for list views (omit TEXT fields like `description`). Use batch queries (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops for enrichment data.
- Struct field ordering: optimize for memory alignment (8-byte fields first)

## Project Plan

See `PLAN.md` for the complete phased plan, data models, API specifications, and agent mapping.
