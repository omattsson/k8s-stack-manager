# Copilot Instructions

## Architecture Overview

K8s Stack Manager is a full-stack application.
- Backend: Go (Gin). Frontend: React (TypeScript, Vite, MUI). Data store: MySQL (GORM).
- Docker Compose runs all services.
- Versions: Go 1.26 (`backend/go.mod`; the images build with `golang:1.27`), Node 22.12 or later (Vitest needs `^22.12 || ^24 || >=26`; the images use `node:26-alpine`).

**Bootstrap flow.** `backend/api/main.go` calls helpers in `backend/api/bootstrap.go` in this order:

```
config.LoadConfig()
telemetry.Init(cfg.Otel)
leader.NewInCluster          # fails the start when LEADER_ELECTION_ENABLED=true outside a pod
initDatabase                 # database.NewRepositoryWithGormDB(cfg)
initRepositories             # all domain repositories
buildWSFanout                # WebSocket fan-out, when WS_FANOUT_ENABLED=true
buildDomainServices          # git, cluster registry, deployer, hooks, notifier, ...
replica heartbeat            # replica_heartbeats row, every replica
session store                # sessionstore
buildHandlers
hub.StartRevalidation        # closes revoked sockets, every minute
buildRouter                  # gin.SetMode, then routes.SetupRoutes(router, routes.Deps{...})
buildLeaderWorkers + startLeaderElection   # leader-only background workers
startHTTPServer              # main server, optional pprof and metrics servers
```

**Graceful shutdown** (`SIGINT` or `SIGTERM`), in this order:
1. Stop the HTTP servers.
2. Stop the leader workers, release the lease, stop the cleanup scheduler.
3. Wait for in-flight deploys. Stop the heartbeat and delete its row.
4. Flush the fan-out queue. Stop the hub, the cluster registry, the notifier, the session store and the dashboard handler.
5. Flush telemetry.
6. Close the database.

**Gin mode.** `GIN_MODE` wins when it is set. Otherwise the mode is debug when `APP_DEBUG` is true, and release when it is false. The code default of `APP_DEBUG` is `true`. The Helm chart sets `APP_DEBUG: "false"`.

**Ports.**
- Backend: `:8081` on the host (`SERVER_PORT`). Frontend: `:3000` in dev.
- Docker: nginx (`location /api/` with `proxy_pass http://backend:8081/api/`) and the Vite dev proxy (`/api` to the backend, no rewrite) both keep the `/api` prefix.
- The backend registers routes under `/api/v1`.
- Local development without Docker calls `localhost:8081` directly (`frontend/src/api/config.ts`).
- Optional servers: Prometheus metrics (`METRICS_ENABLED`, `METRICS_ADDR`, default `:9090`) and pprof (`PPROF_ENABLED`, `PPROF_ADDR`, default `127.0.0.1:6060`).
- `make dev-otel`: Grafana on `:3001`, Prometheus on `:9090`.

## Development Commands

| Task | Command |
|---|---|
| Full stack (Docker) | `make dev` |
| Full stack with local Kubernetes cluster access (Rancher Desktop, Docker Desktop Kubernetes) | `make dev-k8s` |
| Full stack with the OpenTelemetry stack (collector, Prometheus, Tempo, Grafana) | `make dev-otel` |
| Backend and MySQL only (for `stackctl` workflows) | `make dev-api-only` |
| Backend and frontend on the host with hot reload (Go `air`, Vite HMR) | `make dev-local` |
| Seed sample data into a running dev stack | `make seed` |
| Backend tests (unit, no database) | `cd backend && go test ./... -v -short` |
| All backend tests (unit and integration) | `make test-backend-all` (starts MySQL) |
| Frontend tests | `cd frontend && npm test` |
| Unit tests (backend and frontend) | `make test` |
| End-to-end tests | `make test-e2e` (starts infrastructure, backend and Playwright) |
| Swagger docs | `make docs` (runs `swag init -g api/main.go` in `backend`) |
| Coverage (80% threshold) | `cd backend && make test-coverage` |
| Format | `make fmt` |
| Lint | `make lint` (`go vet` and `npm run lint`) |
| Install dependencies | `make install` |
| Helm lint | `make helm-lint` |
| Helm dry-run render | `make helm-template` |
| Helm render checks (default values, secrets, External Secrets, Traefik priority) | `make helm-test` |
| Helm install, upgrade, uninstall | `make helm-install`, `make helm-upgrade`, `make helm-uninstall` |
| Helm package for release | `make helm-release` |
| Load tests | `make loadtest` (see the `loadtest-*` targets) |

## Helm Chart (Kubernetes Deployment)

The Helm chart is in `helm/k8s-stack-manager/`. `Chart.yaml` holds the chart version and the app version.
- The chart deploys the backend and the frontend.
- It can also deploy a bundled MySQL and an OpenTelemetry collector.
- `values.yaml` has toggles for the workload kind, the ingress type, the secrets source and observability.

### Prerequisites
- Kubernetes cluster with a configured `kubectl` context
- Helm 3 or later
- Argo Rollouts controller, only if `argoRollouts.enabled=true`: `kubectl create namespace argo-rollouts && kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml`
- Traefik with CRDs, only if `ingress.type=traefik`
- External Secrets Operator, only if `externalSecrets.enabled=true`
- Kubernetes 1.30 or later, only if `backend.rbac.admissionPolicy.enabled=true` (default)

### Chart Structure

```
helm/k8s-stack-manager/
  Chart.yaml                              # Chart metadata (version, appVersion; source of truth)
  values.yaml                             # All configurable values
  tests/                                  # Values files for `make helm-test`: backend-extra, branding, external-secrets, namespace-labels
  templates/_helpers.tpl                  # Reusable named templates
  templates/NOTES.txt                     # Post-install instructions
  templates/analysis-template.yaml        # Argo AnalysisTemplate for canary steps
  templates/ingress.yaml                  # Standard Ingress (ingress.type=ingress)
  templates/backend/
    configmap.yaml                        # Non-secret environment variables
    secret.yaml                           # Secret environment variables (skipped when externalSecrets.enabled)
    serviceaccount.yaml, clusterrole.yaml # Identity and RBAC (see "Namespace-scoped RBAC"); clusterrole.yaml also holds the ValidatingAdmissionPolicy
    leader-election-rbac.yaml             # Role and RoleBinding: leases get, create, update (backend.leaderElection.enabled)
    deployment.yaml                       # Used when argoRollouts.enabled=false (default)
    rollout.yaml                          # Argo Rollout, canary 20%, 50%, 80% (argoRollouts.enabled=true)
    service.yaml, service-canary.yaml     # Stable and canary services
    hpa.yaml, pdb.yaml                    # Autoscaling (off by default), PodDisruptionBudget (on)
    hooks-configmap.yaml                  # Hook subscriptions file (hooks.enabled)
    servicemonitor.yaml                   # Prometheus Operator scrape (metrics.serviceMonitor.enabled)
  templates/frontend/
    configmap.yaml                        # nginx.conf (SPA only)
    branding-configmap.yaml               # Branding files served at /branding/ (branding.files, branding.binaryFiles)
    serviceaccount.yaml, deployment.yaml, rollout.yaml, service.yaml, service-canary.yaml, hpa.yaml, pdb.yaml
  templates/mysql/                        # Bundled MySQL (mysql.enabled, default true): deployment, service, pvc, configmap, secret, networkpolicy
  templates/otel/                         # OpenTelemetry collector (otel.enabled): collector-configmap, collector-deployment, collector-service
  templates/external-secrets/             # SecretStore and ExternalSecret (externalSecrets.enabled)
  templates/traefik/                      # IngressRoute, StripPrefix and secure-headers Middleware, Traefik Service (ingress.type=traefik)
```

### Key Design Decisions
- **Deployments by default, Argo Rollouts opt-in.** `argoRollouts.enabled=true` swaps the Deployments for Rollouts. A Rollout uses a canary strategy and an AnalysisTemplate. Weight-based traffic splitting needs `ingress.type=traefik`.
- **Three ingress modes** (`ingress.type`):
  - `traefik`: IngressRoute. It routes `/api`, `/ws`, `/health` and `/swagger` to the backend, and `/` to the frontend.
  - `ingress`: standard Ingress, any controller.
  - `none`: use port-forward.
- **Traefik priority.** `ingress.traefik.priority` (default `10000`) is the router priority of every route. The frontend route `/` gets `priority - 1`, so the backend routes win over `/`. The value `0` uses the Traefik default (rule length).
- **nginx in the frontend pod** serves the SPA only: the chart ConfigMap (`templates/frontend/configmap.yaml`) has no API proxy, and the ingress does all routing. Note: `frontend/nginx.conf` in the image (Docker Compose) proxies `/api/` and `/ws` to the backend; the chart replaces it.
- **Bundled MySQL** is for dev and small installs (`mysql.enabled`, optional PVC). For production, point `backend.env.DB_*` at an external database.
- **Secrets** come from a Secret or from External Secrets Operator. `externalSecrets.enabled` renders a SecretStore and an ExternalSecret and skips the chart Secret.
- **ConfigMap and Secret checksums** in pod annotations restart the pods on a config change.
- **Security contexts.** The backend runs as non-root (uid 65532) with a read-only root file system. The frontend drops all capabilities.
- **Observability.** `otel.enabled` deploys a collector. `metrics.enabled` exposes Prometheus metrics. `metrics.serviceMonitor.enabled` adds a ServiceMonitor.
- **Namespace-scoped RBAC.** `backend.rbac.scope: namespace` is the default.
  - The backend (and Helm, which runs with its service account) has rights on namespaced objects only in stack namespaces.
  - The RoleBinding `k8s-stack-manager-access` in each stack namespace binds the service account to the ClusterRole `<fullname>-stack-access`.
  - Cluster-wide, the backend keeps: nodes (read), namespaces (manage), pods (read), RoleBinding writes, and `bind` on the stack-access ClusterRole and on the ClusterRoles of `backend.namespaceRoleBindings`.
  - The chart sets `STACK_ACCESS_CLUSTER_ROLE` and `STACK_ACCESS_SERVICE_ACCOUNT` (`<namespace>/<name>`) for this mode. The backend applies the RoleBinding with `k8s.AccessBinding` (see "Namespace access binding").
  - A ValidatingAdmissionPolicy (`backend.rbac.admissionPolicy.enabled`, Kubernetes 1.30 or later) limits the backend. It may write Roles and RoleBindings only in namespaces with `managed-by=k8s-stack-manager`. It may create only namespaces with that label. It may change or delete only namespaces whose name starts with `stack-` or that have the label.
  - When `WILDCARD_TLS_SOURCE_NAMESPACE` and `WILDCARD_TLS_SOURCE_SECRET` are set, a Role gives `get` on that one Secret.
  - `backend.rbac.scope: cluster` keeps the old rules (namespaced objects in all namespaces).
- **Leader election.** `backend.leaderElection.enabled` (default true) and `backend.leaderElection.leaseName` (Lease `<fullname>-workers`) set `LEADER_ELECTION_ENABLED` and `POD_NAME` / `POD_NAMESPACE` (downward API, Deployment and Rollout). It also creates the Lease Role. Only the leader replica runs the background workers. Every replica serves HTTP and WebSocket.
- **WebSocket fan-out.** `backend.wsFanout.enabled` (default true) sets `WS_FANOUT_ENABLED`. `backend.wsFanout.pollInterval` (500ms) and `retention` (5m) set `WS_FANOUT_POLL_INTERVAL` and `WS_FANOUT_RETENTION`. The replicas share WebSocket messages through the `ws_events` table. Clients on every replica get deploy logs and leader events.

### Configuration
Key values in `values.yaml`:
- `argoRollouts.enabled`: Rollouts (canary) instead of Deployments
- `backend.env.*`: non-secret environment variables (ConfigMap)
- `backend.secrets.*`: secret environment variables, for example `JWT_SECRET` (required), `ADMIN_PASSWORD`, `KUBECONFIG_ENCRYPTION_KEY`, `DB_PASSWORD` (Secret or ExternalSecret)
- `backend.replicas`, `frontend.replicas`, `*.autoscaling`, `*.pdb`: independent scaling
- `backend.allowedChartRepos`: chart repository URL prefixes (`ALLOWED_CHART_REPOS`). Empty means all sources.
- `backend.rbac.scope` (`namespace` or `cluster`), `backend.rbac.admissionPolicy.enabled`: backend RBAC (see Key Design Decisions)
- `backend.namespaceRoleBindings`: RoleBindings that the deployer applies to every stack namespace (`NAMESPACE_ROLE_BINDINGS_JSON`)
- `backend.namespaceLabels`: map of extra stack namespace labels, rendered as sorted `NAMESPACE_LABELS`. The chart renders it only when it is not empty. The keys `managed-by` and `k8s-stack-manager.io/instance-name` fail the render.
- `mysql.enabled`, `mysql.auth.*`, `mysql.persistence.enabled`: bundled database
- `ingress.type`, `ingress.host`, `ingress.traefik.*` (`priority`), `ingress.className`, `ingress.tls`: ingress settings
- `otel.enabled`, `metrics.enabled`, `metrics.serviceMonitor.enabled`: observability
- `hooks.enabled`, `hooks.subscriptions`, `hooks.actions`: outbound webhook subscriptions and custom actions (see `EXTENDING.md`)
- `branding.title`, `branding.logoUrl`, `branding.faviconUrl`: set `APP_TITLE`, `APP_LOGO_URL`, `APP_FAVICON_URL` in the backend ConfigMap
- `branding.files`, `branding.binaryFiles`: ConfigMap that the frontend nginx serves at `/branding/` (nosniff header, sandbox CSP, names `^[-._a-zA-Z0-9]+$`, at most 900 KiB in total)
- `externalSecrets.enabled`, `externalSecrets.secretStore.*`: External Secrets Operator

## Backend Structure

```
backend/
  api/main.go                    # Entry point: bootstrap order, graceful shutdown, default cluster
  api/bootstrap.go               # Bootstrap helpers: repositories, services, handlers, router, leader workers, HTTP servers, Gin mode
  docs/                          # Generated swagger (swagger.json, swagger.yaml), hooks.md (webhook contract)
  internal/
    api/routes/routes.go         # All route registration and middleware order; returns *RateLimiters
    api/handlers/
      items.go                   # CRUD handler pattern (reference implementation)
      handlers.go                # Health handlers (closure injection, no Handler struct)
      rate_limiter.go            # Per-IP sliding window (API limiter and optional login limiter)
      errors.go                  # mapError() maps repository errors to HTTP status for domain handlers
      authz.go                   # Role names, canModifyInstance / requireInstanceModify (owner, devops, admin)
      admin.go                   # AdminHandler: orphaned namespace detection and cleanup
      analytics.go               # AnalyticsHandler: overview, template and user stats
      api_keys.go                # APIKeyHandler: API key management
      audit_logs.go              # AuditLogHandler: filterable audit log viewer and CSV export
      auth.go                    # AuthHandler: login, register, current user, refresh, logout, logout-all
      auth_recheck.go            # recheckUserActive: re-reads the user before a token is returned
      oidc.go                    # OIDCHandler: OpenID Connect authentication flow
      branch_overrides.go        # BranchOverrideHandler: per-chart branch overrides
      bulk_operations.go         # BulkHandler: bulk deploy, stop, clean, delete (up to 50 instances)
      bulk_template_operations.go # TemplateHandler: bulk delete, publish, unpublish (up to 50 templates)
      instance_quota_overrides.go # InstanceQuotaOverrideHandler: per-instance resource quota overrides
      chart_configs.go           # Chart config management (nested under definitions), chartSourceChanged
      cleanup_policies.go        # CleanupPolicyHandler: CRUD and manual run
      clusters.go                # ClusterHandler: CRUD, test-connection, health, quotas, utilization
      dashboard.go               # DashboardHandler: aggregated overview for the landing page
      definition_names.go        # Definition name uniqueness per owner, generated names
      display_names.go           # List filters (owner, cluster, definition), pagination parsing, owner/cluster/definition names
      favorites.go               # FavoriteHandler: user bookmarks
      git.go                     # GitHandler: branch listing, validation
      notifications.go           # NotificationHandler: list, read/unread, preferences
      notification_channels.go   # NotificationChannelHandler: outbound webhook channels, event subscriptions, test send, delivery logs
      quick_deploy.go            # QuickDeployHandler: template quick deploy (writes its own AuditLog)
      shared_values.go           # SharedValuesHandler: per-cluster shared values
      shared_values_loader.go    # Loads the shared values of the cluster that the deploy uses
      values_builder.go          # valuesBuilder: the one Helm values pipeline (deploy, bulk, quick deploy, preview, export, compare)
      stack_definitions.go       # DefinitionHandler: CRUD, chart management, import and export, requireDefinitionModify
      stack_instances.go         # InstanceHandler: CRUD, clone, deploy, stop, clean, deploy-preview, compare, extend TTL
      instance_rollback.go       # Rollback response type and values-drift message
      instance_actions.go        # InstanceHandler: custom action list (UI metadata, can_invoke) and async job log proxy
      instance_followers.go      # InstanceHandler: follow and unfollow
      stack_templates.go         # TemplateHandler: CRUD, publish, instantiate, clone
      template_releases.go       # Template draft and release rules (latest published snapshot)
      template_charts.go         # Template chart config management
      template_versions.go       # TemplateVersionHandler: version history listing and diff
      users.go                   # UserHandler: user management, role change (local users only)
      ui_config.go               # Public GET /ui-config: branding (APP_TITLE, APP_LOGO_URL, APP_FAVICON_URL)
      value_overrides.go         # Per-chart value overrides
      websocket.go               # WebSocket upgrade handler
      content_disposition.go     # Safe Content-Disposition header for downloads
      mock_repository.go         # In-memory mock for Item tests (same package)
      mock_broadcast_sender.go   # Test double for websocket.BroadcastSender
      mock_domain_repositories_test.go # Mocks for domain repositories (instances, clusters, users, ...)
      mock_session_store_test.go # Mock sessionstore
      mock_tx_runner_test.go     # Mock transaction runner
    api/middleware/
      middleware.go              # RequestID, Logger, Recovery, SecurityHeaders, CORS, MaxBodySize
      auth.go                    # JWT authentication and token generation
      combined_auth.go           # Combined JWT and API key auth; checks the sessionstore blocklist and User.Disabled
      audit.go                   # Audit logging middleware (applied to route groups) and the auditRoutes table
      role.go                    # RequireAdmin, RequireDevOps role-based access
      metrics.go                 # HTTPMetrics: OpenTelemetry HTTP request metrics
      auth_metrics.go            # Auth outcome counters (login, refresh, OIDC, API key)
      otel.go                    # SpanEnrichUser: adds user attributes to the active span
      ws_redact.go               # RedactWSToken: strips ?token= from /ws URLs before logging and tracing
    config/                      # config.go (env vars with godotenv .env fallback, typed config structs); branding.go, chart_repos.go, namespace_labels.go, stack_access.go (parsers for the matching env vars)
    database/database.go         # Database struct that wraps gorm.DB, NewDatabase(), Transaction, Ping
    database/config.go           # Database connection Config struct, loaded from env vars
    database/schema.go           # SchemaManager interface for table creation, dropping and inspection
    database/factory.go          # MySQL connection with retry (5 attempts, 2s delay)
    database/repository.go       # NewRepository() factory; repository_factory.go wires all domain repositories
    database/*_repository.go     # One GORM repository per domain model
    database/instance_delete.go  # DeleteInstanceRecord, DeleteInstanceWithOwnedDefinition
    database/migrations.go       # Versioned migrations through schema.Migrator, run on startup
    database/errors.go           # Re-exports from pkg/dberrors (single source of truth)
    database/schema/             # Migrator and versioned migration structs
    models/                      # One file per domain model plus the repository interface; models.go has Base, Item, Validator, Versionable, the generic Repository interface, GenericRepository, Filter, Pagination; validation.go; chart_source.go (chart repository allowlist)
    health/health.go             # Dependency health checks (liveness, readiness)
    auth/                        # OIDC provider (PKCE): discovery, authorize, token exchange (sessionstore keeps OIDC and CLI state)
    cache/                       # Generic concurrent in-memory TTL cache
    cluster/                     # ClusterRegistry (multi-cluster coordination), health poller, quota monitor, secret monitor and refresher, AccessReconciler
    deployer/                    # Helm CLI wrapper for deploy, undeploy, rollback, status (multi-cluster through the registry), cleanup executor, expiry stopper, interrupted operation recovery
    gitprovider/                 # Azure DevOps and GitLab branch listing, URL detection, 5-minute cache
    helm/                        # Values deep-merge, template variable substitution
    hooks/                       # Outbound lifecycle webhooks: dispatcher, HMAC-signed client, actions, config file (see docs/hooks.md and EXTENDING.md)
    k8s/                         # Kubernetes cluster client, status watcher, resource quotas, pod exec, scaling, AccessBinding
    leader/                      # Leader election (coordination.k8s.io Lease through client-go) and leader worker group (Run(ctx) for each term)
    replica/                     # Process identity (POD_NAME or host name plus random suffix) and heartbeat writer (replica_heartbeats, every replica)
    notifier/                    # Notification dispatch (in-app notifications and outbound notification channels)
    sessionstore/                # Token blocklist and OIDC state persistence (mysql default, memory)
    telemetry/                   # OpenTelemetry bootstrap, DB pool metrics, business metrics, leader gauge
    websocket/                   # WebSocket hub, client, message types, fan-out between replicas (ws_events writer and poller, cleanup worker)
    scheduler/                   # Cron-based cleanup policy execution
    ttl/                         # TTL reaper and expiry warner for stack instances
  pkg/dberrors/errors.go         # Canonical error types: ErrNotFound, ErrDuplicateKey, ErrValidation, ErrConnectionFailed, ErrNotImplemented
  pkg/crypto/                    # AES-GCM encryption and decryption of kubeconfig data at rest (key derived with SHA-256)
  pkg/utils/                     # Shared utilities (for example cryptographic random strings)
  internal/test/test_helpers.go  # Shared test utilities (test server setup)
```

## Key Backend Patterns

**Repository interface.**
- The generic `models.Repository` interface has `Create`, `FindByID`, `Update`, `Delete`, `List`. All take `context.Context` first. `GenericRepository` (GORM, MySQL) implements it.
- Domain repositories (for example `StackInstanceRepository`, `UserRepository`, `AuditLogRepository`) have their own interfaces in their model files. Their method signatures differ.
- `GenericRepository` calls `Validate()` on create and update when the model implements `Validator`. Domain repositories call it where they choose (for example chart configs on create and update); check the repository before you rely on it.
- Paginated list endpoints (stack instances, definitions, templates) use `ListPaged(..., limit, offset)`. It returns `([]T, total, error)`. GORM uses `SELECT` with column projection and `LIMIT/OFFSET`.
- List filters go into the paged query through a filter struct (`StackInstanceFilter`, `StackDefinitionFilter`). The count query uses the same conditions, so `total` counts the matching rows.
- New or changed list endpoints must use this pattern. Small admin lists (users, clusters, cleanup policies, favorites, API keys) still return unpaged `List()` results.
- Batch methods (`CountByTemplateIDs`, `FindByIDs`, `NamesByIDs`) remove N+1 queries for enrichment lookups.
- Computed response fields are model fields with `gorm:"-"` (for example `ValuesDrift`, `OwnerUsername`).

**Handler struct.**
- `handlers.Handler` holds `models.Repository` and an optional `websocket.BroadcastSender`. Use constructor injection: `NewHandler(repo)` or `NewHandlerWithHub(repo, hub)`.
- Domain handlers (for example `InstanceHandler`, `DefinitionHandler`, `AdminHandler`, `DashboardHandler`) use separate structs. Each has its own constructor with the repositories it needs.
- Health handlers are factory functions that return a `gin.HandlerFunc` closure.

**Error flow.**
- A repository returns `*dberrors.DatabaseError` that wraps a sentinel error.
- Two functions map it to an HTTP status:
  - `handleDBError()` in `handlers/items.go` (Items reference implementation, uses `errors.As` and `errors.Is`).
  - `mapError()` in `handlers/errors.go` (all domain handlers, takes the entity name for the message).
- Both map: 400 validation, 404 not found, 409 duplicate or conflict, 501 not implemented, 500 internal.
- Never expose a raw error message for a 500. Always return `"Internal server error"`.

**Optimistic locking.** Only the `Item` reference model has `Version uint` (`default:1`). `GenericRepository.Update()` uses `WHERE version = ?`. On a conflict it returns a `"version mismatch"` error. The Items `UpdateItem` handler maps it to 409. The domain models have no version column. They use targeted or conditional writes (for example `UserRepository.UpdateRole`, the conditional instance status updates).

**Filter whitelist.** `GenericRepository` has an `allowedFilterFields` map. A new entity needs `NewRepositoryWithFilterFields()`, or you must extend the existing repository.

**Routes registration.**
- `SetupRoutes()` takes a `Deps` struct with all handlers and repositories. Handlers are optional. Register each one inside an `if deps.XHandler != nil` block.
- It returns `*RateLimiters` (API limiter and optional login limiter). The caller must call `Stop()` on shutdown.
- Global middleware order: RequestID, RedactWSToken, HTTPMetrics (if `OTEL_ENABLED` or `METRICS_ENABLED`), otelgin (if `OTEL_ENABLED`), Logger, Recovery, SecurityHeaders, CORS, MaxBodySize (1 MB).
- `/api/v1` adds the API rate limiter (`RATE_LIMIT`, default 100 requests per minute per IP). The login route gets a stricter limiter when `LOGIN_RATE_LIMIT` is greater than 0 (default 10 per minute). `POST /auth/refresh` uses the login limiter too.
- The authenticated group adds `CombinedAuth` (JWT and API key, checks the token blocklist and disabled users), `SpanEnrichUser` (if `OTEL_ENABLED`) and `NewAuditMiddleware` (if an audit logger is configured).
- Role gates use `RequireAdmin()` and `RequireDevOps()`.
- WebSocket: `/ws` (outside the `/api/v1` rate limiter). Health: `/health/*`. Swagger UI: `/swagger/*` only when `ENABLE_SWAGGER=true`.

**Sessions.**
- Login returns a JWT (with a `jti` and a `sid` session claim) and a refresh token.
- `POST /auth/refresh` rotates the refresh token through `RefreshTokenRepository`. Each login starts a session family (`family_id`, `session_started_at`).
- A rotated token that comes again within `REFRESH_REUSE_GRACE` (default 30s, for concurrent tabs) gets a new access token and no new cookie. Later reuse revokes the family.
- The idle limit (`SESSION_IDLE_TIMEOUT`, default 30m) counts from the last authenticated request. The JWT middleware touches the `last_activity` of the family, at most once per minute.
- `SESSION_MAX_LIFETIME` (default 12h) caps a session. Rotation never extends it.
- Exceptions: the SSO CLI token has no session and lives for `JWT_EXPIRATION`. API keys have no session. `/ws` traffic is not activity.
- `/auth/logout` blocklists the `jti` of the current access token in `sessionstore` and revokes the presented refresh token. `/auth/logout-all` also revokes every refresh token of the user.
- JWT auth checks `IsTokenBlocked` and `IsUserBlocked(ctx, userID, issueTime)`.
  - A user block rejects only tokens issued at or before the block time (401).
  - The precision is one millisecond. Access tokens carry `iat_ms`. The block time is stored in ms. A legacy token without `iat_ms` keeps the rule "same second or before".
  - `BlockUser` writes two rows: `user_block` in seconds (older versions read it during a rolling update) and `user_block_ms`. See `userBlockTime` and `Claims.IssueTime`.
  - The check fails open on a store error (log and continue).
- API-key auth skips the blocklist.
- Delete, disable, password reset and role change block earlier tokens and revoke refresh tokens. Delete also removes the API keys. A role change keeps them, because API-key auth reads the current role.
- A login cache hit (`LOGIN_CACHE_TTL`) skips only bcrypt. After the password check, the login reads the user again and uses that record (disabled check, token role). A changed password hash gives 401.
- All auth paths (login, refresh, OIDC, API key) reject `User.Disabled`.
- Register (`POST /auth/register`) needs an admin, or `SELF_REGISTRATION=true`.
- WebSocket auth:
  - The `/ws` upgrade accepts a JWT in the `Sec-WebSocket-Protocol: bearer, <jwt>` subprotocol, in the `Authorization` header or in `?token=`. It accepts an API key in the `X-API-Key` header only.
  - It runs the same checks (`middleware.CheckRevocation`, fail open) and also rejects a deleted or disabled user (401). A user lookup error gives 500.
  - An API-key upgrade counts against the per-IP API rate limit.
- WebSocket revocation:
  - The hub (`websocket.ClientRevoker`) closes sockets with code 1008 on delete, disable, password reset, role change and logout-all (`DisconnectUser`), on logout (`DisconnectToken`) and on API key delete (`DisconnectAPIKey`).
  - Each socket also closes when its access token (or API key) expires.
  - The frontend reconnects. It refreshes the token first after a 1008 close or near expiry. A refresh is not idle activity: the rotated refresh token keeps `LastActivity`.
  - With fan-out, a revocation reaches the other replicas through `ws_events` within about one poll interval.
  - `Hub.StartRevalidation` re-checks all open sockets every minute (fail open). It closes sockets that a direct database change or a blocklist entry made invalid.

**Hooks.**
- `hooks.Dispatcher.Fire` sends HTTP webhooks for the lifecycle events that the core fires:
  - Deploy: `pre-deploy`, `post-deploy`, `deploy-finalized`, `deploy-timeout`.
  - Rollback: `pre-rollback`, `post-rollback`, `rollback-completed`.
  - Instance create and delete: `pre-instance-create`, `post-instance-create`, `pre-instance-delete`, `post-instance-delete` (single, bulk, after-clean and cleanup policy deletes).
  - Completion: `stop-completed`, `clean-completed` (`metadata.operation=delete` when `delete-completed` follows), `delete-completed`.
  - Cleanup policy: `cleanup-policy-executed` (once per policy run with a match; the summary is in `cleanup_policy`).
- The config accepts the constants `instance-created`, `stack-expiring`, `stack-expired`, `quota-warning` and `secret-expiring`, but the core does not fire them. `pre-namespace-create` and `post-namespace-create` are reserved.
- Signature: when a subscription sets `secret_env`, the request has `X-StackManager-Signature`. A `secret_env` that resolves to an empty value is a config-load error. Omit `secret_env` for unsigned dispatch.
- Envelopes carry `trigger`: `user` (ID and username), `cleanup-policy` (policy ID and name) or `ttl`. The caller sets it with `hooks.WithTrigger`. The deploy manager keeps it for each deploy log.
- A `post-deploy` subscription with `blocking: true` runs after the releases are ready. The instance stays `stabilizing`.
  - `LOG:` lines go into the deploy log. The time limit is the sum of the blocking timeouts plus 1 minute, separate from the Helm budget. The wait holds no concurrency slot.
  - Only a stop ends the wait early. `stack_instances.post_deploy_hook_until` makes the status watcher of the leader keep the status.
  - A shutdown sets `error` with "Interrupted by a server restart. Deploy again." After a hard kill, the interrupted operation recovery sets `error` (see Scalability Notes).
  - After the wait: status `running`, notification "Deployment succeeded", then `deploy-finalized`. A failure gives `error` (`failure_policy: fail`) or a `deployment.warning` notification (`ignore`).
- A `pre-*` subscriber with `failure_policy: fail` can abort the operation.
  - `hooks.UserMessage` builds the safe reason (hook name and subscriber message, never the URL).
  - The reason goes into the `error_message` of the instance and of the deploy log, and into an `ERROR:` line of the log output.
- Event hooks never follow redirects (`noRedirectClient`, as actions). A 3xx answer is a subscriber failure (`hooks.RedirectError`, the user message has no URL). `failure_policy` applies.
- Subscriptions come from `HOOKS_CONFIG_FILE` (Helm: `hooks.subscriptions`). The contract is in `backend/docs/hooks.md`.

**Actions.**
- `hooks.ActionRegistry` holds the custom actions of `HOOKS_CONFIG_FILE` (Helm: `hooks.actions`).
- `GET /stack-instances/:id/actions` lists them for any user who can view the instance. The list has UI metadata (`label`, `confirm`, `parameters`) and `can_invoke` (the instance modify rule). A parameter has: name, label, type (`string`, `bool` or `enum`), required, default, options. The list never returns `url` or `secret_env`.
- `POST /stack-instances/:id/actions/:name` checks the declared parameters. Undeclared parameters pass through. When the action has `log_path`, a valid `result.job_id` of a 2xx answer goes into the envelope.
- `GET /stack-instances/:id/actions/:name/jobs/:job_id/log?offset=N` proxies the job log. It needs the modify rule, because a job log can hold sensitive output.
  - `log_path` resolves on the host of the action URL only.
  - `job_id` must match `^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`.
  - The GET is signed (HMAC of the request URI). It carries `instance_id` and `ts` (Unix seconds). Subscribers check a 5-minute window and must answer 404 for a job of another instance.
  - Limits: 10s timeout, 256 KiB (cut at a line end, then `truncated: true`).
  - Redirects are not followed (also on invoke). An upstream failure gives 502 without the URL.
  - The response is JSON `{status, log, offset, next_offset, done, truncated}`.
  - `status` comes from `X-Job-Status` (preferred) or, as a fallback, from a line `===<NAME>-END=== status=<s>`.
  - `X-Log-Offset` counts only when it is not smaller than the offset.
- The web UI shows the custom actions as the first group of the instance "Actions" menu (`useInstanceActions` in `components/InstanceActionsMenu`). The menu also holds export values, clone, clean namespace and delete.
- The job log dialog polls every 3 s. It backs off on 429, 502, 503, 504 and network errors (`Retry-After`, else a doubling delay up to 30 s). It stops on done, on close, on 401, 403 or 404, and after 60 minutes.

**Telemetry.** `telemetry.Init` configures OTLP traces and metrics (`OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`). `StartDBMetrics`, `StartBusinessMetrics` and `StartLeaderMetric` register gauges. Middleware records HTTP and auth metrics. Add tracing for new outbound calls in a `tracing.go` next to the package (see `cluster/`, `deployer/`, `gitprovider/`, `hooks/`).

**WebSocket messages.**
- Client to server: `subscribe` and `unsubscribe` with `{"instance_id": "..."}`.
- Server to client: `deployment.status`, `deployment.log` (only to the subscribers of the instance, if the sender supports it), `instance.created`, `instance.deleted`, `notification.new` (only to the sockets of the notified user), and `item.*` for the Items example.

## Frontend Structure

```
frontend/src/
  api/config.ts                # API_BASE_URL: http://localhost:8081 (dev) or '' same-origin (prod); client paths already include /api/v1
  api/client.ts                # Axios instance and service objects
  api/refresh.ts               # Access token refresh (one refresh for all tabs, Web Locks)
  routes.tsx                   # Route definitions
  App.tsx                      # Root component with providers
  main.tsx                     # Entry point
  components/
    Layout/                    # AppBar, navigation, footer shell
    AccessUrls/                # Access URL display for stack instances
    BrandLogo/                 # Logo from the branding config (falls back to the built-in /logo.svg)
    BranchSelector/            # Git branch picker with autocomplete
    ChartDiffList/             # Chart changes of a template version diff
    ConfirmDialog/             # Reusable confirmation modal
    DeployPreviewDialog/       # Merged values preview before deploy (GET /stack-instances/:id/deploy-preview)
    DeploymentLogViewer/       # Real-time deployment log display
    EmptyState/                # Placeholder for empty lists
    EntityLink/                # Link to a related entity
    ErrorBoundary/             # React error boundary wrapper
    ExtendTtlMenu/             # Menu that adds time to the expiry of an instance
    FavoriteButton/            # Bookmark toggle for templates and instances
    FollowButton/              # Follow toggle for an instance
    InstanceActionsMenu/       # Custom actions (`useInstanceActions`: first group of the instance "Actions" menu), parameter form, result, job log polling
    JsonResultView/            # Readable view of a JSON action result
    LoadingState/              # Centered CircularProgress wrapper
    PodStatusDisplay/          # Kubernetes pod status display
    ProtectedRoute/            # Auth-gated route wrapper (optional requiredRole)
    NotificationCenter/        # Notification bell dropdown in the app bar
    QuickDeployDialog/         # One-click template deploy modal
    QuotaConfigDialog/         # Resource quota configuration modal
    SetupWizard/               # First-run checklist (cluster, template, instance), can be dismissed
    StatusBadge/               # Colored status chip
    TemplateFieldDiffList/     # Template field changes of a version diff
    TtlSelector/               # TTL duration picker
    YamlEditor/                # YAML text editor (Monaco)
  pages/                       # Simple pages export from index.tsx; domain pages use named files
    Login/                     # Authentication page
    AuthCallback/              # OIDC authentication callback handler
    StackInstances/            # Dashboard.tsx, Detail.tsx, Form.tsx, Compare.tsx, CloneDialog.tsx, RollbackDialog.tsx, ExpiryChip.tsx, widgets/: instance list, deploy, stop, clean, rollback
    StackDefinitions/          # List.tsx, Form.tsx, ImportDefinitionDialog.tsx, UpgradeDialog.tsx
    Templates/                 # Gallery.tsx, Builder.tsx, Instantiate.tsx, Preview.tsx, VersionHistory.tsx, PublishDialog.tsx, CloneTemplateDialog.tsx
    AuditLog/                  # Filterable audit log viewer
    Admin/                     # Clusters/, Users/, OrphanedNamespaces/, NotificationChannels/
    Profile/                   # User profile and settings
    Analytics/                 # Usage overview, template stats, user stats
    CleanupPolicies/           # Cron-based cleanup policy management
    ClusterHealth/             # Multi-cluster health monitoring
    Notifications/             # Full notification list with filters and pagination
    SharedValues/              # Per-cluster shared Helm values
    NotFound/                  # 404 page
  context/
    AuthContext.tsx            # Authentication state and JWT management
    BrandingContext.tsx        # Renders the cached ui-config (localStorage) or the built-in defaults at once; loads GET /api/v1/ui-config once (5s timeout), swaps the values in and caches them; sets document.title and the favicon
    NotificationContext.tsx    # Toast and snackbar provider
    ThemeContext.tsx           # Light and dark theme toggle provider
  hooks/
    useCountdown.ts            # Countdown timer hook (for example TTL display)
    useUnsavedChanges.ts       # Unsaved changes warning hook
    useWebSocket.ts            # WebSocket hook for real-time updates (one shared connection)
  theme/
    index.ts                   # MUI theme export (palette, typography, components)
    palette.ts                 # Color palette definitions
    typography.ts              # Typography variant overrides
    components.ts              # MUI component default prop and style overrides
  types/                       # Shared TypeScript type definitions
  utils/                       # roles, timeAgo, expiry, instanceName, instanceState, jwt, quantity, apiError, download, deployHistory, templateVersion, notificationEventTypes, notificationHelpers, recentTemplates, setupWizard
```

**Stack:** React 19, MUI v9, react-router-dom v7, Vite 8, TypeScript 6, Vitest 5, Playwright (26 specs in `frontend/e2e/`).

**Patterns.**
- Use MUI components (no raw HTML) and the `sx` prop for styling.
- Use functional components only, and `useState` and `useEffect` for state.
- Use service objects with async methods for API calls.
- Each service object and method in `api/client.ts` needs a TSDoc comment with `@param`, `@returns` and `@see` (HTTP method and route).

## Testing Conventions

**Backend.**
- Use `testify/assert`. Write table-driven tests (`tests := []struct{...}` with `t.Run`).
- Call `t.Parallel()` in the parent test and in the subtests. Capture the loop variable: `tt := tt`.
- Use `MockRepository` (in-memory, Item type only) or the domain mocks in `mock_domain_repositories_test.go`.
- Test setup: `setupTestRouter()` returns `(*gin.Engine, *MockRepository)`.
- Validate JSON responses against the schemas in `test_schemas.go` with `gojsonschema`.

**Frontend.** Vitest and Testing Library (unit), Playwright (e2e, `frontend/e2e/*.spec.ts`).

**Integration test names:** `TestDatabase*` (MySQL).

## Adding a New API Resource

1. Model: create `internal/models/<entity>.go`. Domain models have a string UUID `ID` (`gorm:"primaryKey;size:36"`, set by the repository `Create`) and their own timestamps. Do not embed `Base` (uint ID, only for `Item`). Deletes are hard deletes: `Base.DeletedAt` is a plain field, not `gorm.DeletedAt`. Define the repository interface in the same file.
2. Validation: add it in `internal/models/validation.go` (implement the `Validator` interface).
3. Repository: create `internal/database/<entity>_repository.go`. Wire it in `repository_factory.go`.
4. Migration: add it in `internal/database/migrations.go` (the next version string).
5. Handler: create a file in `internal/api/handlers/` with a dedicated handler struct. Use `mapError()` for errors. Follow `items.go` for the CRUD shape.
6. Routes: register them in `internal/api/routes/routes.go` under the authenticated `/api/v1` group, inside an `if deps.XHandler != nil` block. Add the handler to `Deps` and to `api/bootstrap.go` (`buildHandlers`, `buildRouter`).
7. Swagger: add annotations to the handlers. Then run `make docs`.
8. Tests: use the mocks in `mock_domain_repositories_test.go` and the table-driven pattern.
9. Frontend: add service methods in `api/client.ts`, a new page in `pages/`, register it in `routes.tsx`, and add navigation in `components/Layout/`.

## Domain: K8s Stack Manager

Developers use this application to configure, store and deploy multi-service Helm-based application stacks to shared Kubernetes clusters.

### Domain Conventions

**Audit trail**
- Every mutating API endpoint (POST, PUT, DELETE) must create an AuditLog entry. `middleware.NewAuditMiddleware` writes it.
- An operation that is not plain CRUD (deploy, stop, publish, test, bulk, ...) needs an entry in the route table `auditRoutes` (`middleware/audit.go`: action, entity type, ID parameter, details). Without an entry, the action comes from the HTTP method and the entity type from the last route segment.
- Add new actions and entity types to `KnownAuditActions` and `KnownAuditEntityTypes`, and to the filter lists of `pages/AuditLog`.
- `routes/audit_routes_test.go` fails for a mutating route with an unknown entity type.

**Instances**
- **Branch default:** `master` (`DEFAULT_BRANCH`), unless the stack definition sets `DefaultBranch`.
- **Namespace naming:** the handler generates `stack-{instance-name}-{owner}` (`buildNamespace`).
- **Instance names:** a new or changed name must be an RFC 1123 label (lowercase `a-z`, `0-9`, `-`; start and end alphanumeric; at most 50 characters). `models.ValidateInstanceName` checks it on create, rename, clone and quick deploy. `StackInstance.Validate` does not check it, so older instances keep working.
- **Clone:** `POST /stack-instances/:id/clone` takes an optional `{name, branch, ttl_minutes}`.
  - Without a name, it picks `<name>-copy`, `<name>-copy-2`, and so on.
  - It copies the cluster, TTL, value overrides and branch overrides.
  - It copies the quota override only when the caller may modify the source (owner, admin, devops). An owner without the admin or devops role gets the copy only when the override stays within the cluster quota. Otherwise the clone uses the cluster quota.
- **Per-chart branch overrides:** an instance can override the branch of each chart (default: `DefaultBranch` of the definition). Helm values use it through `{{.Branch}}` and `{{.ImageTag}}`.
- **Instance comparison:** `GET /stack-instances/compare` compares two instances side by side, including the merged Helm values of each chart.
- **List filters and names:**
  - `GET /stack-instances` filters by `name`, `status` (unknown value: 400), `cluster_id`, `definition_id` and `owner` (`me`, a username or a user ID). The filters combine with AND. `total` counts the matches.
  - `GET /stack-definitions` filters by `name` and `owner` the same way.
  - Instance responses add `owner_username`, `definition_name` and `cluster_name`. Definition and template responses add `owner_username`.
  - Each response uses one batch lookup for each kind. A missing owner, definition or cluster omits the field.
- **Primary URL:** an Ingress with the annotation `k8s-stack-manager.io/primary-url: "true"` (`k8s.PrimaryURLAnnotation`) is the main entry point of the stack. `GET /stack-instances/:id/status` lists it first (`primary: true`), and the stack list card shows it. Without the annotation, the first Ingress by name is the primary URL.
- **TTL auto-expiry:**
  - An instance with `TTLMinutes > 0` gets `ExpiresAt` on deploy: now plus the TTL, or the current expiry when that is later. A redeploy never shortens an extended expiry. Bulk deploy does the same.
  - The reaper (leader replica only) checks every minute and stops expired instances.
  - The expiry warner sends one warning 30 minutes before expiry. It sets `stack_instances.expiry_warned_at` with a conditional update before it notifies. The repository `Update` never writes that column. It clears the column when `ExpiresAt` changes (deploy, extend).
  - `POST /:id/extend` with `{"minutes": N}` adds N minutes to the expiry. It never shortens the expiry, never changes `ttl_minutes`, and caps the result at now plus 30 days (`MaxTTLMinutes`, 43200). An empty body adds one TTL. The deprecated body `{"ttl_minutes": N}` alone keeps the old reset behavior.
- **Follow:**
  - Any user who can view an instance can follow it: `POST` and `DELETE /stack-instances/:id/follow` (idempotent, audited as `follow` and `unfollow`).
  - `GET /stack-instances/:id` adds `following` and `follower_count`.
  - Table `instance_followers` (primary key `user_id, instance_id`, index on `instance_id`).
  - Every instance delete path removes the followers (`DeleteInstanceRecord`). A user delete removes the follows of the user.
  - Followers get in-app notifications only.
- **Quick deploy:** one-click flow from a template to a new instance and a deploy. It generates the instance name and the namespace.
  - The definition that it creates has `OwnerInstanceID` set.
  - Deleting the instance deletes that definition when no other instance uses it. This also works when the owner instance is already gone.
  - `database.DeleteInstanceWithOwnedDefinition` does it: instance and overrides in one transaction, then definition cleanup in a second, best-effort transaction that locks the definition row.
- **Bulk operations:** bulk deploy, stop, clean and delete accept up to 50 instances for each request, and need `RequireDevOps`. They return a success or failure result for each instance.
  - Bulk delete uses the single-delete path (`InstanceHandler.deleteInstance`). It checks the status before `pre-instance-delete`.
  - It cleans first for instances that have cluster resources, through `Manager.CleanForDelete`. One conditional update sets `cleaning` and `stack_instances.delete_after_clean` (migration 57). The loser of two concurrent deletes gets 409.
  - `Update` never writes the flag. A failed clean and the interrupt recovery clear it.
  - The WebSocket message `instance.deleted` follows the row delete.

**Deploy and Helm**
- **Helm values merge order** (a later step overrides an earlier step):
  1. Shared values (cluster-scoped, by priority).
  2. Chart default values.
  3. Instance overrides.
  4. Template locked values (locked values always win).
  After the merge, the backend substitutes the template variables `{{.Branch}}`, `{{.ImageTag}}` (Docker-safe tag of the branch), `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}` and `{{.Owner}}`. `valuesBuilder` is the one pipeline for all values paths.
- **Deploy preview:** `GET /stack-instances/:id/deploy-preview` returns the merged values of each chart without a deploy.
- **Namespace and Helm:** the deployer calls `k8s.Client.EnsureNamespace` before the first Helm install. If it succeeds, the install request has `NamespaceReady` and Helm gets no `--create-namespace`. With that flag, Helm always sends a namespace create. An admission policy that requires the `managed-by` label refuses that create. Without a ready namespace (no k8s client, or the check failed), Helm creates the namespace.
- **Namespace labels:** `k8s.Client.EnsureNamespace(ctx, namespace, instanceName)` sets these labels on create. It adds or corrects them on an existing namespace (deploy, rollback to a target). Other labels stay.
  - `managed-by=k8s-stack-manager`.
  - `k8s-stack-manager.io/instance-name=<instance name>` (the exact stack name for admission policies, because a namespace prefix is ambiguous). The backend sets it only when the name is a valid label value. Otherwise it logs a warning. A rename overwrites it.
  - The `NAMESPACE_LABELS` labels (`k=v,k=v`). `config.ParseNamespaceLabels` parses and validates them at startup. The keys `managed-by` and the instance-name key are reserved. `cluster.RegistryOptions.NamespaceLabels` passes them to each client.
- **Namespace access binding:** with `STACK_ACCESS_CLUSTER_ROLE` and `STACK_ACCESS_SERVICE_ACCOUNT` set (`<namespace>/<name>`), `EnsureNamespace` also applies the RoleBinding `k8s-stack-manager-access` (`k8s.AccessBinding`). This works for in-cluster clusters only. Both variables must be set, or neither. The leader worker `stack-access-reconcile` (`cluster.AccessReconciler`) applies it to existing stack namespaces once per term and every 10 minutes.
- **Chart sources:**
  - `ALLOWED_CHART_REPOS` is a comma list of `oci`, `https` or `http` URL prefixes (`models.NormalizeChartSourcePrefix`). Empty means all sources.
  - `models.CheckChartSource` checks the chart location: an OCI reference, a `--repo` URL plus the chart name, or a chart path. The location must equal a prefix or start with a prefix plus `/`. It must not contain `..`. A chart path with a scheme or a leading `/` next to a repository URL is rejected.
  - The check runs in `ChartConfig.Validate` and `TemplateChartConfig.Validate` (repository create and update).
  - It runs again before each Helm install: a deploy marks the chart failed, and a rollback to a target returns an error.
- **Rollback:** `POST /stack-instances/:id/rollback`
  - Without `target_log_id`, it rolls the Helm releases back one revision.
  - With `target_log_id` (a successful deploy of the instance), it upgrades each chart to the values snapshot and the recorded chart version of that deploy (`DeploymentLog.ChartVersions`). This restores values, not images.
  - `pre-rollback` fires in the rollback goroutine, with progress streaming like `pre-deploy`. It carries the pre-deploy chart list, so image gates subscribe to it. A rejection restores the previous instance status.
  - `rollback-completed` fires on either outcome. `post-rollback` fires only on success.
  - A successful rollback sets `LastDeployedValues` to the running values.
  - `values_drift` (rollback response, deploy preview, GET instance) shows that the stored overrides differ. The next deploy then undoes the rollback.
- **Pod problems:**
  - During the Helm and stabilize phases of a deploy or rollback, `deployer.podEventWatch` polls namespace Warning events and container waiting reasons every 10s (`k8s.Client.ListPodProblems`).
  - It writes new ones as `Pod event:` lines to the deploy log, grouped by reason and object with a count (`(x18)`). The most relevant one (quota first) goes into the error message.
  - The namespace ResourceQuota and LimitRange are applied after the namespace is ensured and before the first Helm install (deploy, redeploy, rollback to a target).
  - A partial deploy gives the deploy log status `partial` (`models.DeployLogPartial`).
- **Resource quotas:** per-cluster quotas (CPU, memory, storage, ephemeral storage, pods) use Kubernetes ResourceQuota and LimitRange objects. An admin configures them through the API.
  - `k8s.Client.EnsureLimitRange` takes the per-container fields: `container_{cpu,memory,ephemeral_storage}_{request,limit}` and `container_{cpu,memory}_max` (migration 58 adds the columns).
  - It never copies a namespace total into a container default. Without a container default it uses a fallback (CPU 100m/500m, memory 128Mi/512Mi, ephemeral storage 128Mi/1Gi), capped at the namespace total and the container max.
  - Validation (`quotaOrderRules` in `models/quota_merge.go`): request <= limit <= max for each container, and a container default <= the namespace total.
  - `ephemeral_storage_limit` sets `requests.ephemeral-storage` and `limits.ephemeral-storage`.
  - A user without the admin or devops role can set an instance quota override only at or below each cluster quota value, also for the per-container fields (`models.CheckOverrideWithinClusterQuota`, 403). The check runs on write and on clone. A value equal to the stored override passes. `pod_limit: 0` counts as no limit.
  - The quota monitor raises quota warnings through the notifier. The `quota-warning` hook constant is defined but not dispatched.

**Access control**
- **Auth:** JWT with the header `Authorization: Bearer <token>`. The middleware puts `userID`, `username` and `role` into the Gin context. Refresh tokens rotate through `/auth/refresh`. Logout blocks tokens in `sessionstore`. See "Sessions".
- **Roles:** `user`, `devops`, `admin`.
- **User roles API:** `PUT /users/:id/role` (admin) changes the role of a local user.
  - An SSO user gives 409 (the identity provider sets the role at each login). The own role gives 403. An unchanged role is a no-op.
  - Delete, disable, enable and role change are guarded (`UserRepository.UpdateRole`, `SetDisabled`, `DeleteGuarded`). One transaction locks the admin rows (by ID), then the target.
  - The caller must still be an enabled admin (403). The last enabled admin cannot be demoted, disabled or deleted (409).
  - Handlers use the stored ID after `FindByID` (MySQL compares IDs without case).
  - `UserRepository` has no full-row update. Use the targeted writes (`UpdatePassword`, `UpdateProfile` for the SSO sync and link, `UpdateRole`, `SetDisabled`), so concurrent changes are not lost.
  - Migration 50 indexes `users.role`.
  - A change runs `revokeUserAccess` (user block, refresh tokens, WebSocket disconnect). The audit action is `change_role` with `old_role` and `new_role`.
- **Definition access:**
  - Create definition, import, export and add or remove chart need `RequireDevOps`.
  - Update, delete, upgrade and chart update need the owner, devops or admin (`requireDefinitionModify`, 403).
  - A user without devops or admin can change chart default values and deploy order. That user cannot change the chart name, repository, path or version (`chartSourceChanged`, 403).
  - The UI hides Create Definition, Import and Add/Remove Chart for the role `user`. It shows the chart source read-only.
- **Template access:** edit, delete, publish, unpublish and chart changes need `RequireDevOps` and the template owner or an admin. Bulk template operations need `RequireDevOps`. Instantiate and quick deploy are open to every authenticated user.
- **Instance access:** deploy, stop, clean, delete, rollback, overrides, TTL and custom actions need the instance owner, a devops user or an admin.

**Templates and definitions**
- **Template versioning (draft and release):**
  - The template row and its template charts are the working copy (draft). You can edit them while the template is published. Edits never change what users get.
  - `POST /templates/:id/publish` (optional `{"version","change_summary"}`, default: the version of the working copy) stores a snapshot. An existing version gives 409. An unchanged working copy creates no snapshot (idempotent). Publish runs in one transaction that locks the template row.
  - Use Template, Quick Deploy, definition upgrades and deploy-time locked values read the latest snapshot (`template_releases.go`). An unpublished template, or one that was never published, gives 409 "Template has no published version".
  - `GET /templates/:id` adds `published_version`, `published_version_id`, `published_charts` and `has_unpublished_changes`.
  - The version diff accepts `working` on either side.
  - `PUT /templates/:id` is a partial update.
- **Definition names:** unique for each owner (application check, no unique index). Create, rename and template instantiate return 409 for a duplicate. Import renames to `<name> (imported)`, `<name> (imported 2)`, and so on. Quick deploy uses `<name> (2)`, and so on.
- **Import and export:** you can export a definition as a JSON bundle and import it to create a new definition with charts.
- **Shared values:** per-cluster Helm values for all instances in that cluster. They merge by priority (lowest first) before the chart defaults and the instance overrides.
- **Bulk template operations:** bulk delete, publish and unpublish accept up to 50 templates for each request. They return a result for each template.

**Clusters and operations**
- **Multi-cluster:** register clusters through `/api/v1/clusters` with kubeconfig data (encrypted at rest through `pkg/crypto`) or a kubeconfig path.
  - `ClusterRegistry` manages the K8s and Helm clients of each cluster. The health poller monitors the status.
  - A cluster with `use_in_cluster` uses the backend service account and has no API server URL.
  - An instance targets one cluster (or the default).
  - Per-cluster registry credentials (`registry_url`, `registry_username`, `registry_password`) give automatic image pull secret provisioning at deploy time and a refresh every 4 hours (`SecretRefresher`).
- **Git provider detection:** by URL. `dev.azure.com` and `visualstudio.com` mean Azure DevOps. `gitlab.com` or a custom host means GitLab.
- **Cleanup policies:**
  - `PUT /admin/cleanup-policies/:id` is a partial update. Only the fields in the body change. The backend validates the merged policy.
  - A policy runs a cron-scheduled action (stop, clean, delete) on instances that match a condition: `status:`, `idle_days:` (days without a deploy), `age_days:` (creation age), `stopped_days:` (time since `StoppedAt`), `ttl_expired`.
  - A policy targets one cluster or "all".
  - Only the leader replica runs the cron jobs. It reloads on the policy changes it handles, when it becomes leader and every minute. A scheduled run reads the policy again.
  - Any replica can run a manual run. A manual run supports dry-run mode.
  - `StoppedAt` is set when a stop completes (API, TTL reaper, policy). A deploy or a clean clears it.
- **Orphaned namespaces:** `/admin/orphaned-namespaces` lists `stack-*` namespaces without an instance. Each entry has a `managed` flag (label `managed-by=k8s-stack-manager`). Delete of an unmanaged namespace needs `?confirm=<full name>`, else 409.
- **Cluster health units:** `GET /clusters/:id/health/summary` adds `requested_cpu` and `requested_memory`. `GetClusterRequests` sums the pod requests with the scheduler rule. It reads pods from the API server cache. This is not the real use, and the dashboard does not list pods. The frontend parses quantities with `utils/quantity.ts` and shows cores and binary units.

**Notifications and UI**
- **Notification channels:** DevOps and admin users register outbound webhook channels (`/admin/notification-channels`, `RequireDevOps`). Each channel has event subscriptions, a test send and delivery logs.
  - Optional `filters`: `instance_name_patterns` (globs through `path.Match`), `owner_ids`, `definition_ids`, `cluster_ids`. Filters combine with AND, values inside a filter with OR. An empty filter means all.
  - `channel.Dispatcher` matches the filters against `EventPayload.Instances`. An event without an instance goes only to channels without filters. `cleanup.policy.executed` goes to a channel when one affected instance matches.
  - A bad glob gives 400. An unknown ID adds a `warnings` entry.
- **Notifications:**
  - In-app notifications for deploy, stop and clean events. Each user has notification preferences and an unread count for the badge.
  - Dispatch uses the `notifier` package. The WebSocket message `notification.new` goes only to the sockets of the notified user (`Hub.BroadcastToUser`).
  - Instance events use `Notifier.NotifyInstance(models.NotificationTarget, ...)`. The receivers are the owner and the followers (no duplicates). Each receiver is filtered by the own event-type preferences (`DisabledUserIDs`, one query, fail open). The actor is not excluded. One channel dispatch for each event.
  - `NotifySystem` and `NotifySystemForInstances` (admin and devops) apply the preferences too.
  - Read the followers with `FollowerIDs` before an instance delete, and put them in `NotificationTarget.FollowerIDs`.
- **Favorites:** users can bookmark templates and instances (`UserFavorite` entities). A favorite does not follow the instance.
- **Analytics and dashboard:**
  - `/analytics/*` (devops) gives read-only aggregation of instance counts, deploy stats, template usage and user activity. `/analytics/users` needs admin.
  - `/dashboard` serves the landing page overview. Cluster health is for admin and devops only. The widget is hidden for the role `user`.
  - Per-user deploy counts use `DeploymentLog.UserID` (who started the deploy). Migration 49 backfills the instance owner. The counts survive instance deletes.
  - The stack list (Dashboard) updates live. `deployment.status` changes a row.
  - An event for an instance that is not in the list reloads the list once for each instance ID, 500 ms after the first event. The events are `deployment.status` and `instance.created` (`Manager.BroadcastInstanceCreated` sends it to all clients after create, clone and quick deploy).
  - A `deployment.status` with status `running` fetches the card URL again and reloads the list once (new deploy time).
  - `instance.deleted` removes the row. The backend sends it after every delete: after a clean by the deploy manager, and after a direct delete (API, bulk delete or cleanup policy) by `Manager.BroadcastInstanceDeleted`.
- **Branding:** `APP_TITLE` (default `K8s Stack Manager`), `APP_LOGO_URL` (empty means the built-in `/logo.svg`), `APP_FAVICON_URL` (default `/favicon.svg`; both files are in `frontend/public/`). `config.BrandingConfig` validates them at startup: a same-origin path or an https URL. It refuses `http:`, `javascript:`, `data:`, `//host` and whitespace. The public `GET /api/v1/ui-config` is rate-limited and has `Cache-Control: public, max-age=300`.
- **Setup wizard:** the frontend shows a first-run checklist until a cluster, a template and an instance exist, or until the user dismisses it (localStorage).
- **Generic design:** no company-specific hard-coding. Branding and all configurable values use environment variables.
- **Hooks:** put organization-specific behavior (Slack, database refresh, approval gates) in hook subscribers, not in the core. See "Hooks" above and `EXTENDING.md`.

### API Route Groups

| Group | Prefix | Description |
|-------|--------|-------------|
| Auth | `/api/v1/auth` | Login, register, current user, refresh, logout, logout-all |
| OIDC Auth | `/api/v1/auth/oidc` | OpenID Connect config, authorize, callback, CLI auth and token |
| Dashboard | `/api/v1/dashboard` | Landing page overview aggregation |
| Templates | `/api/v1/templates` | CRUD, publish, unpublish, instantiate, clone (optional `name`) |
| Template Versions | `/api/v1/templates/:id/versions` | Version history listing, detail, diff |
| Quick Deploy | `/api/v1/templates/:id/quick-deploy` | One-click template deployment |
| Bulk Template Ops | `/api/v1/templates/bulk` | Bulk delete, publish, unpublish templates (up to 50, devops) |
| Stack Definitions | `/api/v1/stack-definitions` | CRUD, nested chart management, import and export |
| Stack Instances | `/api/v1/stack-instances` | CRUD, clone, values export, deploy, deploy-preview, stop, clean, rollback, status, pods, deploy-log, recent, compare, extend TTL, follow and unfollow, custom actions (list, invoke, job log) |
| Bulk Operations | `/api/v1/stack-instances/bulk` | Bulk deploy, stop, clean, delete (up to 50 instances, devops) |
| Value Overrides | `/api/v1/stack-instances/:id/overrides` | Per-chart value overrides |
| Branch Overrides | `/api/v1/stack-instances/:id/branches` | Per-chart branch overrides (list, get, set, delete for each chart) |
| Quota Overrides | `/api/v1/stack-instances/:id/quota-overrides` | Per-instance resource quota overrides (`PUT` replaces all fields) |
| Git | `/api/v1/git` | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Filterable audit log viewer (devops or admin) and export (admin) |
| Users | `/api/v1/users` | User management (admin): list, delete, disable, enable, password reset, role change |
| API Keys | `/api/v1/users/:id/api-keys` | API key management |
| Admin | `/api/v1/admin` | Orphaned namespace detection and cleanup (admin) |
| Cleanup Policies | `/api/v1/admin/cleanup-policies` | Cron-based cleanup policy management (admin) |
| Notification Channels | `/api/v1/admin/notification-channels` | Outbound webhook channels, event types, subscriptions, test, delivery logs (devops) |
| Clusters | `/api/v1/clusters` | Multi-cluster registration (admin), health, test-connection, quotas, utilization |
| Shared Values | `/api/v1/clusters/:id/shared-values` | Per-cluster shared Helm values (admin) |
| Favorites | `/api/v1/favorites` | User bookmarks |
| Notifications | `/api/v1/notifications` | List, read and unread, count, preferences |
| Analytics | `/api/v1/analytics` | Usage overview, template stats, user stats |
| UI Config | `/api/v1/ui-config` | Public branding values (title, logo, favicon) |
| Health | `/health/*` | Liveness and readiness |
| WebSocket | `/ws` | Real-time status and log events. Replicas share them through `ws_events` when `WS_FANOUT_ENABLED=true`. `RedactWSToken` removes the token from logs. |

### Frontend Pages

| Page | Route | Description |
|------|-------|-------------|
| Login | `/login` | Username and password sign-in (single sign-on when OIDC is enabled) |
| Auth Callback | `/auth/callback` | OIDC authentication callback handler |
| Dashboard | `/` | Stack instances overview with deploy and stop actions, widgets, setup wizard on first run |
| Templates | `/templates`, `/templates/new`, `/templates/:id`, `/templates/:id/edit`, `/templates/:id/use` | Gallery, builder, preview, version history, instantiate |
| Stack Definitions | `/stack-definitions`, `/stack-definitions/new`, `/stack-definitions/:id/edit` | List, create, edit definitions; import and export |
| Stack Instance | `/stack-instances/new`, `/stack-instances/:id`, `/stack-instances/compare` | Form, detail (values editor, branch selector, deploy preview, deploy, stop, clean, rollback, Actions menu), comparison |
| Audit Log | `/audit-log` | Filterable audit log viewer and export (DevOps and admin) |
| Admin Users | `/admin/users` | User management (admin only) |
| Orphaned Namespaces | `/admin/orphaned-namespaces` | Namespace cleanup (admin only) |
| Clusters | `/admin/clusters` | Cluster registration and health (admin only) |
| Cluster Health | `/admin/cluster-health` | Cluster health dashboard (DevOps and admin) |
| Analytics | `/admin/analytics` | Usage stats and deploy metrics (DevOps and admin) |
| Shared Values | `/admin/shared-values` | Per-cluster shared Helm values (admin only) |
| Cleanup Policies | `/admin/cleanup-policies` | Cron-based cleanup policy editor (admin only) |
| Notification Channels | `/admin/notification-channels` | Outbound webhook channels, subscriptions, delivery logs (DevOps and admin) |
| Notifications | `/notifications` | Full notification list with filters and pagination |
| Profile | `/profile` | User profile and API key management |

### Project Docs

`ROADMAP.md` tracks upcoming work. `ARCHITECTURE.md` describes the system. `EXTENDING.md` and `backend/docs/hooks.md` describe the hooks and actions extension contract. `WIKI.md` covers user-facing concepts. `docs/getting-started.md` is the end-user walkthrough. Security and scalability rules are in `.github/instructions/security.instructions.md` and `.github/instructions/scalability.instructions.md`.
