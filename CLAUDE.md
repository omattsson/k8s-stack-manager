# K8s Stack Manager

## Architecture Overview

Full-stack app: **Go (Gin) backend** + **React (TypeScript, Vite, MUI) frontend**, with **MySQL** (GORM) as the data store. Docker Compose orchestrates all services. Go 1.26 (`backend/go.mod`), Node 22+ (images use `node:26-alpine`).

**Bootstrap flow**: `backend/api/main.go` → `config.LoadConfig()` → `telemetry.Init(cfg.Otel)` → `leader.NewInCluster` (fails startup when `LEADER_ELECTION_ENABLED=true` outside a pod) → `database.NewRepositoryWithGormDB(cfg)` → `buildWSFanout` (WebSocket fan-out, when `WS_FANOUT_ENABLED`) → session store (`sessionstore`) → `routes.SetupRoutes(router, routes.Deps{...})` → `buildLeaderWorkers` + `startLeaderElection` (leader-only background workers) → `http.Server` with graceful shutdown (`SIGINT`/`SIGTERM`: stops HTTP, stops the leader workers, releases the lease, flushes the fan-out queue, stops rate limiters and flushes telemetry).

**Ports**: Backend `:8081` on host, frontend `:3000` in dev. Inside Docker, nginx (`location /api/` → `proxy_pass http://backend:8081/api/`) and the Vite dev proxy (`/api` → backend, no rewrite) both keep the `/api` prefix; backend routes are registered under `/api/v1`. Local non-Docker dev hits `localhost:8081` directly (`frontend/src/api/config.ts`). With `make dev-otel`: Grafana `:3001`, Prometheus `:9090`.

## Development Commands

| Task | Command |
|---|---|
| Full stack (Docker) | `make dev` |
| Full stack + local K8s cluster access (Rancher Desktop, Docker Desktop K8s) | `make dev-k8s` |
| Full stack + OpenTelemetry stack (collector, Prometheus, Tempo, Grafana) | `make dev-otel` |
| Backend + MySQL only (for `stackctl` workflows) | `make dev-api-only` |
| Backend + frontend locally with hot reload (Go `air` + Vite HMR) | `make dev-local` |
| Seed sample data into a running dev stack | `make seed` |
| Backend tests (unit, no DB) | `cd backend && go test ./... -v -short` |
| All backend tests (unit + integration) | `make test-backend-all` (starts MySQL) |
| Frontend tests | `cd frontend && npm test` |
| Unit tests (backend + frontend) | `make test` |
| E2E tests | `make test-e2e` (starts infra + backend + Playwright) |
| Swagger docs | `cd backend && make docs` (runs `swag init -g api/main.go`) |
| Coverage (80% threshold) | `cd backend && make test-coverage` |
| Lint | `make lint` (`go vet` + `npm run lint`) |
| Install deps | `make install` |
| Helm lint | `make helm-lint` |
| Helm dry-run render | `make helm-template` |
| Helm render checks (default + External Secrets values) | `make helm-test` |
| Helm install / upgrade / uninstall | `make helm-install` / `make helm-upgrade` / `make helm-uninstall` |
| Helm package for release | `make helm-release` |
| Load tests | `make loadtest` (see `loadtest-*` targets) |

## Helm Chart (Kubernetes Deployment)

The Helm chart lives in `helm/k8s-stack-manager/` (chart version and appVersion in `Chart.yaml`). It deploys backend, frontend, and optionally a bundled MySQL and an OpenTelemetry collector. Workload kind, ingress type, secrets source, and observability are all toggles in `values.yaml`.

### Prerequisites
- Kubernetes cluster with `kubectl` context configured
- Helm 3+
- Argo Rollouts controller only if `argoRollouts.enabled=true` (`kubectl create namespace argo-rollouts && kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml`)
- Traefik with CRDs only if `ingress.type=traefik`
- External Secrets Operator only if `externalSecrets.enabled=true`

### Chart Structure

```
helm/k8s-stack-manager/
  Chart.yaml                              # Chart metadata (version, appVersion — source of truth)
  values.yaml                             # All configurable values
  tests/external-secrets-values.yaml      # Values used by `make helm-test`
  templates/_helpers.tpl                   # Reusable named templates
  templates/NOTES.txt                      # Post-install instructions
  templates/analysis-template.yaml         # Argo AnalysisTemplate for canary steps
  templates/ingress.yaml                   # Standard Ingress (ingress.type=ingress)
  templates/backend/
    configmap.yaml                         # Non-secret env vars
    secret.yaml                            # Secret env vars (skipped when externalSecrets.enabled)
    serviceaccount.yaml, clusterrole.yaml  # Identity + RBAC for managing target clusters
    leader-election-rbac.yaml              # Role + RoleBinding: leases get/create/update (backend.leaderElection.enabled)
    deployment.yaml                        # Used when argoRollouts.enabled=false (default)
    rollout.yaml                           # Argo Rollout, canary 20%→50%→80% (argoRollouts.enabled=true)
    service.yaml, service-canary.yaml      # Stable + canary services
    hpa.yaml, pdb.yaml                     # Autoscaling (off by default), PodDisruptionBudget (on)
    hooks-configmap.yaml                   # Hook subscriptions file (hooks.enabled)
    servicemonitor.yaml                    # Prometheus Operator scrape (metrics.serviceMonitor.enabled)
  templates/frontend/
    configmap.yaml                         # nginx.conf (SPA-only routing)
    serviceaccount.yaml, deployment.yaml, rollout.yaml, service.yaml, service-canary.yaml, hpa.yaml, pdb.yaml
  templates/mysql/                         # Bundled MySQL (mysql.enabled, default true): deployment, service, pvc, configmap, secret, networkpolicy
  templates/otel/                          # OpenTelemetry collector (otel.enabled): collector-configmap, collector-deployment, collector-service
  templates/external-secrets/              # SecretStore + ExternalSecret (externalSecrets.enabled)
  templates/traefik/                       # IngressRoute, StripPrefix + secure-headers Middleware, Traefik Service (ingress.type=traefik)
```

### Key Design Decisions
- **Deployments by default, Argo Rollouts opt-in** — `argoRollouts.enabled=true` swaps Deployments for Rollouts with a canary strategy and an AnalysisTemplate. Weight-based traffic splitting needs `ingress.type=traefik`.
- **Three ingress modes** — `ingress.type`: `traefik` (IngressRoute, routes `/api/*` and `/ws` to backend, `/` to frontend), `ingress` (standard Ingress, any controller), `none` (port-forward).
- **nginx in frontend pod** serves SPA only (no API proxy); the ingress handles all routing.
- **Bundled MySQL** for dev and small installs (`mysql.enabled`, PVC optional); point `backend.env.DB_*` at an external database for production.
- **Secrets from Secret or External Secrets Operator** — `externalSecrets.enabled` renders SecretStore + ExternalSecret and skips the chart Secret.
- **ConfigMap/Secret checksums** in pod annotations trigger a rollout on config changes.
- **Security contexts** — backend runs as non-root (uid 65532), readOnlyRootFilesystem; frontend drops all capabilities.
- **Observability** — `otel.enabled` deploys a collector; `metrics.enabled` exposes Prometheus metrics, `metrics.serviceMonitor.enabled` adds a ServiceMonitor.
- **Leader election** — `backend.leaderElection.enabled` (default true) sets `LEADER_ELECTION_ENABLED`, `POD_NAME`/`POD_NAMESPACE` (downward API, Deployment and Rollout) and a Lease Role; only the leader replica runs the background workers, every replica serves HTTP and WebSocket.
- **WebSocket fan-out** — `backend.wsFanout.enabled` (default true) sets `WS_FANOUT_ENABLED`; `backend.wsFanout.pollInterval` (500ms) and `retention` (5m) set `WS_FANOUT_POLL_INTERVAL` and `WS_FANOUT_RETENTION`. The replicas share WebSocket messages through the `ws_events` table, so clients on every replica get deploy logs and leader events.

### Configuration
Key values in `values.yaml`:
- `argoRollouts.enabled` — Rollouts (canary) instead of Deployments
- `backend.env.*` — Non-secret env vars (ConfigMap)
- `backend.secrets.*` — Secret env vars like `JWT_SECRET` (required), `ADMIN_PASSWORD`, `KUBECONFIG_ENCRYPTION_KEY`, `DB_PASSWORD` (Secret or ExternalSecret)
- `backend.replicas` / `frontend.replicas`, `*.autoscaling`, `*.pdb` — Independently scalable
- `backend.leaderElection.enabled`, `backend.leaderElection.leaseName` — Leader election for the background workers (Lease `<fullname>-workers`)
- `backend.wsFanout.enabled`, `backend.wsFanout.pollInterval`, `backend.wsFanout.retention` — WebSocket fan-out between replicas (`ws_events`)
- `mysql.enabled`, `mysql.auth.*`, `mysql.persistence.enabled` — Bundled database
- `ingress.type`, `ingress.host`, `ingress.traefik.*`, `ingress.className`, `ingress.tls` — Ingress settings
- `otel.enabled`, `metrics.enabled`, `metrics.serviceMonitor.enabled` — Observability
- `hooks.enabled`, `hooks.subscriptions` — Outbound webhook subscriptions (see `EXTENDING.md`)
- `branding.title`, `branding.logoUrl`, `branding.faviconUrl` — `APP_TITLE`, `APP_LOGO_URL`, `APP_FAVICON_URL` in the backend ConfigMap; `branding.files` / `branding.binaryFiles` — ConfigMap served by the frontend nginx at `/branding/` (nosniff, sandbox CSP; names `^[-._a-zA-Z0-9]+$`, max 900 KiB total)
- `externalSecrets.enabled`, `externalSecrets.secretStore.*` — External Secrets Operator

## Backend Structure

```
backend/
  api/main.go                    # Bootstrap: config → telemetry → repo → session store → router → server → shutdown
  docs/                          # Generated swagger (swagger.json/yaml), hooks.md (webhook contract)
  internal/
    api/routes/routes.go         # All route registration + middleware ordering; returns *RateLimiters
    api/handlers/
      items.go                   # CRUD handler pattern (reference implementation)
      handlers.go                # Health handlers (closure-injection, not Handler struct)
      rate_limiter.go            # Per-IP sliding window (API limiter + optional login limiter)
      errors.go                  # mapError() maps repo errors to HTTP status for domain handlers
      admin.go                   # AdminHandler: orphaned namespace detection/cleanup
      analytics.go               # AnalyticsHandler: overview, template, user stats
      api_keys.go                # APIKeyHandler: API key management
      audit_logs.go              # AuditLogHandler: filterable audit log viewer + CSV export
      auth.go                    # AuthHandler: login, register, current user, refresh, logout, logout-all
      oidc.go                    # OIDCHandler: OpenID Connect authentication flow
      branch_overrides.go        # BranchOverrideHandler: per-chart branch overrides
      bulk_operations.go         # BulkHandler: bulk deploy/stop/clean/delete (up to 50 instances)
      bulk_template_operations.go # TemplateHandler: bulk delete/publish/unpublish (up to 50 templates)
      instance_quota_overrides.go  # InstanceQuotaOverrideHandler: per-instance resource quota overrides
      chart_configs.go           # Chart config management (nested under definitions)
      cleanup_policies.go        # CleanupPolicyHandler: CRUD + manual run
      clusters.go                # ClusterHandler: CRUD + test-connection + health + quotas + utilization
      dashboard.go               # DashboardHandler: aggregated overview for the landing page
      favorites.go               # FavoriteHandler: user bookmark management
      git.go                     # GitHandler: branch listing, validation
      notifications.go           # NotificationHandler: list, read/unread, preferences
      notification_channels.go   # NotificationChannelHandler: outbound webhook channels, event subscriptions, test send, delivery logs
      quick_deploy.go            # QuickDeployHandler: template quick-deploy (writes its own AuditLog)
      shared_values.go           # SharedValuesHandler: per-cluster shared values
      stack_definitions.go       # DefinitionHandler: CRUD + chart management + import/export
      stack_instances.go         # InstanceHandler: CRUD + clone + deploy/stop/clean/rollback + deploy-preview + compare + extend TTL
      instance_actions.go        # InstanceHandler: custom action list (UI metadata, can_invoke) + async job log proxy
      stack_templates.go         # TemplateHandler: CRUD + publish + instantiate + version snapshots
      template_charts.go         # Template chart config management
      template_versions.go       # TemplateVersionHandler: version history listing + diff
      users.go                   # UserHandler: user management, role change (local users only)
      ui_config.go               # Public GET /ui-config: branding (APP_TITLE, APP_LOGO_URL, APP_FAVICON_URL)
      value_overrides.go         # Per-chart value overrides
      websocket.go               # WebSocket upgrade handler
      mock_repository.go         # In-memory mock for Item tests (same package)
      mock_broadcast_sender.go   # Test double for websocket.BroadcastSender
      mock_domain_repositories_test.go # Mocks for domain repositories (instances, clusters, users, ...)
      mock_session_store_test.go # Mock sessionstore
      mock_tx_runner_test.go     # Mock transaction runner
    api/middleware/
      middleware.go              # RequestID, Logger, Recovery, SecurityHeaders, CORS, MaxBodySize
      auth.go                    # JWT authentication + token generation
      combined_auth.go           # Combined JWT + API key auth; checks sessionstore blocklist and User.Disabled
      audit.go                   # Audit logging middleware (applied to route groups)
      role.go                    # RequireAdmin, RequireDevOps role-based access
      metrics.go                 # HTTPMetrics: OpenTelemetry HTTP request metrics
      auth_metrics.go            # Auth outcome counters (login, refresh, OIDC, API key)
      otel.go                    # SpanEnrichUser: adds user attributes to the active span
      ws_redact.go               # RedactWSToken: strips ?token= from /ws URLs before logging/tracing
    config/config.go             # Env vars with godotenv .env fallback, typed config structs
    database/database.go         # Database struct wrapping gorm.DB, NewDatabase(), Transaction, Ping
    database/config.go           # Database connection Config struct, loaded from env vars
    database/schema.go           # SchemaManager interface for table creation/dropping/inspection
    database/factory.go          # MySQL connection with retry (5x, 2s delay)
    database/repository.go       # NewRepository() factory; repository_factory.go wires all domain repos
    database/*_repository.go     # One GORM repository per domain model
    database/migrations.go       # Versioned migrations via schema.Migrator, auto-run on startup
    database/errors.go           # Re-exports from pkg/dberrors (single source of truth)
    database/schema/             # Migrator and versioned migration structs
    models/                      # One file per domain model + repository interface; models.go has Base, Item, Validator, Versionable, the generic Repository interface + GenericRepository, Filter, Pagination; validation.go
    health/health.go             # Dependency health checks (liveness/readiness)
    auth/                        # OIDC provider (PKCE) — discovery, authorize, token exchange (OIDC/CLI state is persisted in sessionstore)
    cache/                       # Generic concurrent in-memory TTL cache
    cluster/                     # ClusterRegistry (multi-cluster coordination), health poller, quota monitor, secret refresher
    deployer/                    # Helm CLI wrapper for deploy/undeploy/rollback/status (multi-cluster via registry), cleanup executor, expiry stopper
    gitprovider/                 # Azure DevOps + GitLab branch listing, URL detection, 5-minute cache
    helm/                        # Values deep-merge, template variable substitution
    hooks/                       # Outbound lifecycle webhooks: dispatcher, HMAC-signed client, actions, config file (see docs/hooks.md, EXTENDING.md)
    k8s/                         # Kubernetes cluster client, status watcher, resource quotas, pod exec, scaling
    leader/                      # Leader election (coordination.k8s.io Lease via client-go) + leader worker group (Run(ctx) per term)
    notifier/                    # Notification dispatch (in-app notifications + outbound notification channels)
    sessionstore/                # Token blocklist + OIDC state persistence (mysql default, memory)
    telemetry/                   # OpenTelemetry bootstrap, DB pool metrics, business metrics, leader gauge
    websocket/                   # WebSocket hub, client, message types, fan-out between replicas (ws_events writer + poller, cleanup worker)
    scheduler/                   # Cron-based cleanup policy execution
    ttl/                         # TTL reaper for auto-expiring stack instances
  pkg/dberrors/errors.go         # Canonical error types: ErrNotFound, ErrDuplicateKey, ErrValidation, ErrConnectionFailed, ErrNotImplemented
  pkg/crypto/                    # AES-GCM encryption/decryption for kubeconfig data at rest (key derived via SHA-256)
  pkg/utils/                     # Shared utilities (e.g., cryptographic random string generation)
  internal/test/test_helpers.go  # Shared test utilities (test server setup)
```

## Key Backend Patterns

**Repository interface**: The generic `models.Repository` interface uses `Create`, `FindByID`, `Update`, `Delete`, `List` — all take `context.Context` first. Implemented by `GenericRepository` (GORM/MySQL). Domain-specific repositories (e.g., `StackInstanceRepository`, `UserRepository`, `AuditLogRepository`) have dedicated interfaces in their model files with custom method signatures. The repository auto-calls `Validate()` on create/update if the model implements `Validator`. Paginated list endpoints (stack instances, definitions, templates) use `ListPaged(..., limit, offset)` returning `([]T, total, error)` — GORM uses `SELECT`+column projection+`LIMIT/OFFSET`. List filters go into the paged query through a filter struct (`StackInstanceFilter`, `StackDefinitionFilter`); the count query uses the same conditions, so `total` counts the matching rows. New or changed list endpoints must use this pattern; small admin lists (users, clusters, cleanup policies, favorites, API keys) still return unpaged `List()` results. Batch methods (`CountByTemplateIDs`, `FindByIDs`, `NamesByIDs`) eliminate N+1 queries for enrichment lookups. Computed response fields are model fields with `gorm:"-"` (for example `ValuesDrift`, `OwnerUsername`).

**Handler struct**: `handlers.Handler` holds `models.Repository` and optional `websocket.BroadcastSender` via constructor injection (`NewHandler(repo)` or `NewHandlerWithHub(repo, hub)`). Domain handlers (e.g., `InstanceHandler`, `DefinitionHandler`, `AdminHandler`, `DashboardHandler`) use separate structs with specialized repository dependencies injected via their own constructors. Health handlers use factory functions returning `gin.HandlerFunc` via closure.

**Error flow**: Repository returns `*dberrors.DatabaseError` wrapping sentinel errors → two mapping functions translate to HTTP status: `handleDBError()` in `handlers/items.go` (Items reference implementation, uses `errors.As`/`errors.Is`) and `mapError()` in `handlers/errors.go` (all domain handlers, takes entity name for contextual messages). Both map to 400 validation, 404 not found, 409 duplicate/conflict, 501 not implemented, 500 internal. **Never expose raw error messages for 500s** — always return `"Internal server error"`.

**Optimistic locking**: Models embed `Version uint` field (`default:1`). Repository `Update()` uses `WHERE version = ?` — returns `"version mismatch"` error (mapped to 409).

**Filter whitelist**: `GenericRepository` has `allowedFilterFields` map. New entities need `NewRepositoryWithFilterFields()` or the existing repo must be extended.

**Routes registration**: `SetupRoutes()` accepts a `Deps` struct with all handler and repository dependencies; handlers are optional and registered inside `if deps.XHandler != nil` blocks. Returns `*RateLimiters` (API limiter + optional login limiter; caller must call `Stop()` on shutdown). Global middleware order: RequestID → RedactWSToken → HTTPMetrics (if `OTEL_ENABLED` or `METRICS_ENABLED`) → otelgin (if `OTEL_ENABLED`) → Logger → Recovery → SecurityHeaders → CORS → MaxBodySize (1MB). `/api/v1` adds the API rate limiter (`RATE_LIMIT`, default 100 req/min per IP); login gets a stricter limiter when `LOGIN_RATE_LIMIT` > 0 (default 10/min). The authenticated group adds `CombinedAuth` (JWT + API key, checks token blocklist and disabled users), `SpanEnrichUser` (if `OTEL_ENABLED`), and `NewAuditMiddleware` (if an audit logger is configured). Role-based access via `RequireAdmin()`/`RequireDevOps()`. WebSocket at `/ws`, health at `/health/*`.

**Sessions**: Login returns a JWT (with a `jti` and a `sid` session claim) and a refresh token. `POST /auth/refresh` rotates the refresh token through `RefreshTokenRepository`; each login starts a session family (`family_id`, `session_started_at`). A rotated token presented again within `REFRESH_REUSE_GRACE` (default 30s, concurrent tabs) gets a new access token without a new cookie; later reuse revokes the family. The idle limit (`SESSION_IDLE_TIMEOUT`, 30m) counts from the last authenticated request (the JWT middleware touches the family's `last_activity`, throttled to once per minute); `SESSION_MAX_LIFETIME` (default 12h) caps a session, and rotation never extends it (exceptions: the SSO CLI token has no session and lives `JWT_EXPIRATION`; API keys have no session; `/ws` traffic is not activity). `/auth/logout` blocklists the current access-token `jti` in `sessionstore` and revokes the presented refresh token; `/auth/logout-all` also revokes every refresh token of the user. JWT auth checks `IsTokenBlocked` and `IsUserBlocked(ctx, userID, issueTime)` (only tokens issued at or before a user block are rejected, with 401; millisecond precision: access tokens carry `iat_ms` and the block time is stored in ms, legacy tokens without `iat_ms` keep the "same second or before" rule; `BlockUser` dual-writes a `user_block` row in seconds (read by older versions during a rolling update) and a `user_block_ms` row, see `userBlockTime` and `Claims.IssueTime`) and fails open on store errors (log + continue); API-key auth skips the blocklist. Delete, disable, password reset and role change block earlier tokens and revoke refresh tokens (delete also removes API keys; a role change keeps them, API-key auth reads the current role); a login cache hit (`LOGIN_CACHE_TTL`) skips only bcrypt; after the password check the login re-reads the user and uses that record (disabled check, token role; a changed password hash gives 401); all auth paths (login, refresh, OIDC, API key) reject `User.Disabled`. The `/ws` upgrade runs the same checks through `middleware.CheckRevocation` (blocklists, fail open) and also rejects a deleted or disabled user (401; a user lookup error gives 500). The hub (`websocket.ClientRevoker`) closes open sockets on delete, disable, password reset, role change and logout-all (`DisconnectUser`) and on logout (`DisconnectToken`); each socket also closes when its access token expires (close code 1008), and the frontend reconnects, refreshing the token first after a 1008 close or near expiry (a refresh is not idle activity: the rotated refresh token keeps `LastActivity`). `Hub.StartRevalidation` re-checks all open sockets every minute (fail open), so other replicas close revoked sockets within about one minute (until #428).

**Hooks**: `hooks.Dispatcher.Fire` sends HTTP webhooks (HMAC-signed with `X-StackManager-Signature` when a subscription sets `secret_env`; omit it for unsigned dispatch, and a `secret_env` that resolves to empty is a config-load error) for the lifecycle events it actually dispatches: `pre-deploy`, `post-deploy`, `deploy-finalized`, `deploy-timeout`, `pre-rollback`, `post-rollback`, `rollback-completed`, `pre/post-instance-create`, `pre/post-instance-delete` (single, bulk, after-clean and cleanup policy deletes), `stop-completed`, `clean-completed` (`metadata.operation=delete` when `delete-completed` follows), `delete-completed`, `cleanup-policy-executed` (once per policy run with a match; summary in `cleanup_policy`). Other event constants (`instance-created`, `stack-expiring`, `stack-expired`, `quota-warning`, `secret-expiring`) are accepted in subscription config but not yet fired; `pre/post-namespace-create` are reserved. Envelopes carry `trigger` (`user` with ID and username, `cleanup-policy` with policy ID and name, `ttl`), passed from the caller with `hooks.WithTrigger` and kept per deploy log by the deploy manager. A `post-deploy` subscription with `blocking: true` runs after the releases are ready while the instance stays `stabilizing` (`LOG:` lines stream into the deploy log; own time limit = sum of the blocking timeouts + 1 minute, separate from the Helm budget; no concurrency slot held; only a Stop ends the wait early; `stack_instances.post_deploy_hook_until` makes the leader's k8s status watcher keep the status; a shutdown gives `error` "Interrupted by a server restart. Deploy again."); `running`, "Deployment succeeded" and `deploy-finalized` follow; a failure gives `error` (`fail`) or a `deployment.warning` notification (`ignore`). `pre-*` subscribers can abort with `failure_policy: fail`; the user-safe reason (`hooks.UserMessage`: hook name + subscriber message, never the URL) goes to the instance and deploy log `error_message` and an `ERROR:` line in the log output. Subscriptions come from `HOOKS_CONFIG_FILE` (Helm: `hooks.subscriptions`). Contract in `backend/docs/hooks.md`.

**Actions**: `hooks.ActionRegistry` holds the custom actions of `HOOKS_CONFIG_FILE` (Helm: `hooks.actions`). `GET /stack-instances/:id/actions` lists them for any user who can view the instance, with UI metadata (`label`, `confirm`, `parameters`: name, label, type `string|bool|enum`, required, default, options) and `can_invoke` (instance modify rule); it never returns `url`, `secret_env` or headers. `POST /stack-instances/:id/actions/:name` checks declared parameters (undeclared ones pass through) and lifts a valid `result.job_id` to the envelope when the action has `log_path`. `GET /stack-instances/:id/actions/:name/jobs/:job_id/log?offset=N` (modify rule, because job logs can hold sensitive output) proxies the job log: `log_path` resolves on the action URL host only, `job_id` must match `^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`, the GET is signed (HMAC of the request URI) and carries `instance_id` and `ts` (Unix seconds; subscribers check a 5-minute window and must 404 a job of another instance), 10s timeout, 256 KiB cap (cut at a line end, `truncated: true`), redirects are not followed (also on invoke), upstream failures give 502 without the URL. The response is JSON `{status, log, offset, next_offset, done, truncated}`; status comes from `X-Job-Status` (preferred) or, as a fallback, an `===<NAME>-END=== status=<s>` line; `X-Log-Offset` counts only when >= offset; `job_id` is lifted only from a 2xx invoke. The web UI (`components/InstanceActionsMenu`) polls it every 3 s and backs off on 429/502/503/504/network errors (`Retry-After`, else doubling up to 30 s); it stops on done, close, 401, 403, 404.

**Telemetry**: `telemetry.Init` configures OTLP traces and metrics (`OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`). `StartDBMetrics` and `StartBusinessMetrics` register gauges; HTTP and auth metrics come from middleware. Add tracing for new outbound calls in a `tracing.go` next to the package (see `cluster/`, `deployer/`, `gitprovider/`, `hooks/`).

## Frontend Structure

```
frontend/src/
  api/config.ts                # API_BASE_URL: http://localhost:8081 (dev) | '' same-origin (prod); client paths already include /api/v1
  api/client.ts                # Axios instance + service objects
  routes.tsx                   # Route definitions
  App.tsx                      # Root component with providers
  main.tsx                     # Entry point
  components/
    Layout/                    # AppBar + nav + footer shell
    AccessUrls/                # Access URL display for stack instances
    BrandLogo/                 # Logo from the branding config (falls back to the built-in /logo.svg)
    BranchSelector/            # Git branch picker with autocomplete
    ConfirmDialog/             # Reusable confirmation modal
    DeployPreviewDialog/       # Merged values preview before deploy (GET /stack-instances/:id/deploy-preview)
    DeploymentLogViewer/       # Real-time deployment log display
    EmptyState/                # Placeholder for empty lists
    EntityLink/                # Clickable link to related entities
    ErrorBoundary/             # React error boundary wrapper
    FavoriteButton/            # Toggle bookmark on templates/instances
    InstanceActionsMenu/       # "Actions" menu on the instance detail page: parameter form, result, job log polling
    LoadingState/              # Centered CircularProgress wrapper
    PodStatusDisplay/          # Kubernetes pod status visualization
    ProtectedRoute/            # Auth-gated route wrapper
    NotificationCenter/        # Notification bell dropdown in app bar
    QuickDeployDialog/         # One-click template deploy modal
    QuotaConfigDialog/         # Resource quota configuration modal
    SetupWizard/               # First-run checklist (cluster → template → instance), dismissible
    StatusBadge/               # Colored status chip
    TtlSelector/               # TTL duration picker
    YamlEditor/                # YAML text editor (Monaco)
  pages/                       # Simple pages export from index.tsx; domain pages use named files
    Login/                     # Authentication page
    AuthCallback/              # OIDC authentication callback handler
    StackInstances/            # Dashboard.tsx, Detail.tsx, Form.tsx, Compare.tsx, widgets/ — instance list, deploy, stop, clean, rollback
    StackDefinitions/          # List.tsx, Form.tsx, ImportDefinitionDialog.tsx, UpgradeDialog.tsx
    Templates/                 # Gallery.tsx, Builder.tsx, Instantiate.tsx, Preview.tsx, VersionHistory.tsx
    AuditLog/                  # Filterable audit log viewer
    Admin/                     # Clusters/, Users/, OrphanedNamespaces/, NotificationChannels/
    Profile/                   # User profile and settings
    Analytics/                 # Usage overview, template stats, user stats
    CleanupPolicies/           # Cron-based cleanup policy management
    ClusterHealth/             # Multi-cluster health monitoring
    Notifications/             # Full notification list page with filters and pagination
    SharedValues/              # Per-cluster shared Helm values
    NotFound/                  # 404 page
  context/
    AuthContext.tsx             # Authentication state + JWT token management
    BrandingContext.tsx         # Renders the cached ui-config (localStorage) or built-in defaults at once, loads GET /api/v1/ui-config once (5s timeout), swaps the values in and caches them; sets document.title and favicon
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
  utils/                       # timeAgo, roles, notificationHelpers, recentTemplates, setupWizard (dismissed flag)
```

**Stack**: React 19, MUI v9, react-router-dom v7, Vite 8, TypeScript 6, Vitest 5, Playwright (26 specs in `frontend/e2e/`).

**Patterns**: MUI components (no raw HTML), `sx` prop for styling, functional components only, `useState`/`useEffect` for state, service objects with async methods for API calls. All service objects and methods in `api/client.ts` must have TSDoc comments with `@param`, `@returns`, and `@see` (HTTP method + route).

## Testing Conventions

**Backend**: `testify/assert`, table-driven (`tests := []struct{...}` + `t.Run`), `t.Parallel()` on parent and subtests, capture loop var `tt := tt`. Use `MockRepository` (in-memory, Item type only) or the domain mocks in `mock_domain_repositories_test.go`. Test setup: `setupTestRouter()` returns `(*gin.Engine, *MockRepository)`. JSON responses validated against schemas in `test_schemas.go` via `gojsonschema`.

**Frontend**: Vitest + Testing Library (unit), Playwright (e2e, `frontend/e2e/*.spec.ts`).

**Integration test naming**: `TestDatabase*` (MySQL).

## Adding a New API Resource

1. Model in a new file `internal/models/<entity>.go` (embed `Base` for ID/timestamps/soft-delete; add `Version uint` with `default:1`; define the repository interface in the same file)
2. Validation in `internal/models/validation.go` (implement `Validator` interface)
3. Repository in `internal/database/<entity>_repository.go`; wire it in `repository_factory.go`
4. Migration in `internal/database/migrations.go` (incrementing version string)
5. Handler file in `internal/api/handlers/` (dedicated handler struct; `mapError()` for errors; follow `items.go` for the CRUD shape)
6. Routes in `internal/api/routes/routes.go` under the authenticated `/api/v1` group, inside an `if deps.XHandler != nil` block; add the handler to `Deps` and to `api/main.go`
7. Swagger annotations on handlers, then `cd backend && make docs`
8. Tests with mocks in `mock_domain_repositories_test.go` + table-driven pattern
9. Frontend service methods in `api/client.ts`, new page in `pages/`, register in `routes.tsx`, nav in `components/Layout/`

## Domain — K8s Stack Manager

This application enables developers to configure, store, and deploy multi-service Helm-based application stacks to shared Kubernetes clusters.

### Domain Packages

```
backend/internal/
  cluster/               # ClusterRegistry: multi-cluster client management, health poller, quota monitor, secret refresher
  gitprovider/           # Azure DevOps + GitLab branch listing, URL detection, caching
  helm/                  # Values deep-merge, template variable substitution, YAML export
  deployer/              # Helm CLI wrapper for deploy/undeploy/rollback/status (multi-cluster via registry), cleanup executor, expiry stopper
  k8s/                   # Kubernetes cluster client, status watcher, resource quota management, pod exec, scaling
  leader/                # Leader election: only the leader replica runs the background workers
  hooks/                 # Outbound lifecycle webhooks (HMAC signed), actions
  notifier/              # Notification dispatch (in-app + outbound notification channels on deploy/stop/clean events)
  scheduler/             # Cron-based cleanup policy execution with condition parsing
  ttl/                   # TTL reaper: background goroutine auto-expiring instances
  auth/                  # OIDC provider: OpenID Connect authentication (PKCE); OIDC/CLI state persisted in sessionstore
  sessionstore/          # Token blocklist + OIDC state (MySQL or memory)
  cache/                 # In-memory TTL cache (login cache, branch lists)
  telemetry/             # OpenTelemetry setup, DB and business metrics
```

### Domain Conventions

- **Audit trail**: Every mutating API endpoint (POST, PUT, DELETE) must create an AuditLog entry. `middleware.NewAuditMiddleware` writes it; operations that are not plain CRUD (deploy, stop, publish, test, bulk, ...) need an entry in the route table `auditRoutes` (`middleware/audit.go`: action, entity type, ID param, details), else the action comes from the method and the entity type from the last route segment. New actions or entity types go into `KnownAuditActions` / `KnownAuditEntityTypes` and the filter lists of `pages/AuditLog`; `routes/audit_routes_test.go` fails for a mutating route with an unknown entity type
- **Branch default**: "master" unless overridden per stack definition's `DefaultBranch` field
- **Namespace naming**: Auto-generated as `stack-{instance-name}-{owner}`
- **Instance names**: A new or changed name must be an RFC 1123 label (lowercase `a-z`, `0-9`, `-`, start and end alphanumeric, max 50 characters) — `models.ValidateInstanceName` on create, rename, clone and quick deploy. `StackInstance.Validate` does not check it, so older instances keep working
- **Clone**: `POST /stack-instances/:id/clone` takes an optional `{name, branch, ttl_minutes}`; without a name it picks `<name>-copy`, `<name>-copy-2`, …; copies cluster, TTL, value overrides and branch overrides; the quota override only when the caller may modify the source (owner, admin, devops), and for an owner without the admin or devops role only when it stays within the cluster quota (else the clone uses the cluster quota)
- **Multi-cluster**: Clusters are registered via `/api/v1/clusters` with kubeconfig data (encrypted at rest via `pkg/crypto`) or kubeconfig path. `ClusterRegistry` manages per-cluster K8s/Helm clients. Health poller monitors cluster status. A cluster with `use_in_cluster` uses the backend service account and has no API server URL. Stack instances target a specific cluster (or the default). Per-cluster container registry credentials (`registry_url`, `registry_username`, `registry_password`) enable automatic image pull secret provisioning at deploy time and periodic refresh (4h) via `SecretRefresher`.
- **Git provider detection**: URL-based — `dev.azure.com`/`visualstudio.com` → Azure DevOps; `gitlab.com` or custom → GitLab
- **Helm values merge**: Merge order shared values (cluster-scoped, by priority) → chart default values → instance overrides → template locked values (locked always wins). Then substitute template vars `{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`
- **Deploy preview**: `GET /stack-instances/:id/deploy-preview` returns the merged values per chart without deploying
- **Rollback**: `POST /stack-instances/:id/rollback` rolls Helm releases back one revision, or with `target_log_id` (a successful deploy of the instance) upgrades each chart to that deploy's values snapshot and recorded chart version (`DeploymentLog.ChartVersions`) — values, not images; `pre-rollback` fires in the rollback goroutine with progress streaming (like `pre-deploy`) and carries the pre-deploy chart list, so image gates subscribe to it; a rejection restores the previous instance status; fires `pre-rollback`, then `rollback-completed` on either outcome and `post-rollback` only on success. A successful rollback sets `LastDeployedValues` to the running values; `values_drift` (rollback response, deploy preview, GET instance) shows that the stored overrides differ and the next deploy undoes the rollback
- **Auth**: JWT with `Authorization: Bearer <token>` header; middleware injects `userID`, `username`, `role` into Gin context. Refresh tokens rotate via `/auth/refresh`; logout blocks tokens in `sessionstore`
- **Hooks**: Lifecycle events go to HMAC-signed webhook subscribers; `pre-*` events can gate the operation (`failure_policy: fail`). Organization-specific behaviour (Slack, DB refresh, approval gates) belongs in hook subscribers, not in core (see `EXTENDING.md`)
- **Notification channels**: DevOps and admin users register outbound webhook channels (`/admin/notification-channels`, guarded by `RequireDevOps`) with per-event subscriptions, test send, and delivery logs. Optional `filters` (`instance_name_patterns` globs via `path.Match`, `owner_ids`, `definition_ids`, `cluster_ids`; AND across filters, OR inside; empty = all) are matched in `channel.Dispatcher` against `EventPayload.Instances`; events without an instance go only to channels without filters, `cleanup.policy.executed` when one affected instance matches. Bad globs give 400, unknown IDs a `warnings` entry
- **Generic design**: No company-specific hardcoding; all branding and configurable values via environment variables
- **Branding**: `APP_TITLE` (default `K8s Stack Manager`), `APP_LOGO_URL` (empty = built-in `/logo.svg`), `APP_FAVICON_URL` (default `/favicon.svg`, both in `frontend/public/`), validated at startup (`config.BrandingConfig`: a same-origin path or an https URL; no `http:`, `javascript:`, `data:`, `//host`, whitespace). Public `GET /api/v1/ui-config` (rate-limited, `Cache-Control: public, max-age=300`)
- **User roles**: `PUT /users/:id/role` (admin) changes the role of a local user. SSO users give 409 (the IdP sets the role at each login), the own role 403; unchanged = no-op. Delete, disable, enable and role change are guarded (`UserRepository.UpdateRole`/`SetDisabled`/`DeleteGuarded`): one transaction locks the admin rows (by ID) then the target; the caller must still be an enabled admin (403), the last enabled admin cannot be demoted, disabled or deleted (409). Handlers use the stored ID after `FindByID` (MySQL IDs compare without case). `UserRepository` has no full-row update: use the targeted writes (`UpdatePassword`, `UpdateProfile` for the SSO sync/link, `UpdateRole`, `SetDisabled`) so concurrent changes are not lost. Migration 50 indexes `users.role`. A change runs `revokeUserAccess` (user block, refresh tokens, WebSocket disconnect); audit action `change_role` with `old_role`/`new_role`
- **Cleanup policies**: `PUT /admin/cleanup-policies/:id` is a partial update (only fields in the body change; the merged policy is validated). Cron-scheduled actions (stop/clean/delete) on instances matching a condition (`status:`, `idle_days:`, `age_days:` = creation age, `stopped_days:` = time since `StoppedAt`, `ttl_expired`). Policies target a cluster (or "all"). Only the leader replica runs cron jobs; it reloads on policy changes it handles, when it becomes leader and every minute (a scheduled run reads the policy again). Manual run (any replica) supported with dry-run mode. `StoppedAt` is set when a stop completes (API, TTL reaper, policy) and cleared on deploy and clean.
- **TTL auto-expiry**: Instances with `TTLMinutes > 0` get `ExpiresAt` set on deploy. Background reaper (leader replica only) checks every minute and stops expired instances. The expiry warner sends one warning 30 minutes before expiry: it marks `stack_instances.expiry_warned_at` with a conditional update before it notifies; the repository `Update` never writes that column and clears it when `ExpiresAt` changes (deploy, extend). `POST /:id/extend` with `{"minutes": N}` adds N minutes to the expiry (never shortens, never changes `ttl_minutes`, capped at now + 30 days); an empty body adds one TTL; the deprecated `{"ttl_minutes": N}` alone keeps the old reset behaviour.
- **Favorites**: Users can bookmark templates and instances. Stored as `UserFavorite` entities. A favorite does not follow the instance.
- **Follow**: Any user who can view an instance can follow it (`POST`/`DELETE /stack-instances/:id/follow`, idempotent, audited as `follow`/`unfollow`); `GET /stack-instances/:id` adds `following` and `follower_count`. Table `instance_followers` (PK `user_id, instance_id`, index on `instance_id`). Every instance delete path removes the followers (`DeleteInstanceRecord`), a user delete removes the user's follows. Followers get in-app notifications only
- **Shared values**: Per-cluster Helm values applied to all instances in that cluster, merged by priority (lowest first) before chart defaults and instance overrides.
- **Analytics and dashboard**: Read-only aggregation of instance counts, deployment stats, template usage, and user activity (`/analytics/*`, devops; `/analytics/users` admin only); `/dashboard` serves the landing page overview (cluster health only for admin and devops; the widget is hidden for role `user`). Per-user deploy counts use `DeploymentLog.UserID` (who started the deploy; migration 49 backfills the instance owner), so they survive instance deletes.
- **Orphaned namespaces**: `/admin/orphaned-namespaces` lists `stack-*` namespaces without an instance with a `managed` flag (label `managed-by=k8s-stack-manager`). Delete of an unmanaged namespace needs `?confirm=<full name>` (else 409).
- **Cluster health units**: `GET /clusters/:id/health/summary` adds `requested_cpu`/`requested_memory` (`GetClusterRequests`: sum of pod requests with the scheduler rule, pods from the API server cache; not real use; the dashboard does not list pods); the frontend parses quantities with `utils/quantity.ts` and shows cores and binary units.
- **Quick deploy**: One-click flow: template → new instance → deploy. Generates instance name and namespace automatically. The definition it creates has `OwnerInstanceID` set; deleting the instance deletes that definition when no other instance uses it, also when its owner instance is already gone (`database.DeleteInstanceWithOwnedDefinition`: instance + overrides in one transaction, definition cleanup in a second, best-effort transaction that locks the definition row).
- **Per-chart branch overrides**: Instances can override the branch per chart (default uses the definition's `DefaultBranch`). Substituted in Helm values via `{{.Branch}}`.
- **Notifications**: In-app notifications for deploy/stop/clean events. Per-user notification preferences. Unread count for badge display. Notification dispatch via `notifier` package; the WebSocket `notification.new` message goes only to the sockets of the notified user (`Hub.BroadcastToUser`). Instance events use `Notifier.NotifyInstance(models.NotificationTarget, ...)`: receivers are the owner and the followers (no duplicates), each filtered by the own event-type preferences (`DisabledUserIDs`, one query; fail open); the actor is not excluded; one channel dispatch per event. `NotifySystem`/`NotifySystemForInstances` (admin and devops) apply the preferences too. Read followers with `FollowerIDs` before an instance delete and put them in `NotificationTarget.FollowerIDs`
- **Template versioning (draft and release)**: The template row and its template charts are the working copy (draft); edits are allowed while published and never change what users get. `POST /templates/:id/publish` (optional `{"version","change_summary"}`, default the working copy version) stores a snapshot; an existing version gives 409, an unchanged working copy creates no snapshot (idempotent). Use Template, Quick Deploy, definition upgrades and deploy-time locked values read the latest snapshot (`template_releases.go`); unpublished or never published gives 409 "Template has no published version". `GET /templates/:id` adds `published_version`, `published_version_id`, `published_charts`, `has_unpublished_changes`; version diff accepts `working` on either side. Publish runs in one transaction (template row locked). Template edit/delete/publish/unpublish/chart changes require the owner or an admin (plus `RequireDevOps`); `PUT /templates/:id` is a partial update.
- **Resource quotas**: Per-cluster resource quotas (CPU, memory, storage, pods) enforced via Kubernetes ResourceQuota and LimitRange objects. Admin-configurable via API. An instance quota override by a user without the admin or devops role must stay at or below each cluster quota value (`models.CheckOverrideWithinClusterQuota`, 403; checked on write and on clone; a value equal to the stored override passes; `pod_limit: 0` counts as no limit). The quota monitor raises quota warnings through the notifier (the `quota-warning` hook constant is defined but not yet dispatched).
- **Bulk operations**: Bulk deploy/stop/clean/delete supports up to 50 instances per request. Returns per-instance success/failure results.
- **Bulk template operations**: Bulk delete/publish/unpublish supports up to 50 templates per request. Returns per-template success/failure results. Requires DevOps+ role.
- **Instance comparison**: Side-by-side comparison of two stack instances including merged Helm values per chart.
- **List filters and names**: `GET /stack-instances` filters by `name`, `status` (unknown value: 400), `cluster_id`, `definition_id` and `owner` (`me`, a username or a user ID); the filters combine (AND) and `total` counts the matches. `GET /stack-definitions` filters by `name` and `owner` the same way. Instance responses add `owner_username`, `definition_name` and `cluster_name`; definition and template responses add `owner_username`. One batch lookup per kind and response; a missing owner, definition or cluster omits the field.
- **Import/export**: Stack definitions can be exported as JSON bundles and re-imported to create new definitions with charts.
- **Definition names**: Unique per owner (application check, no unique index): create, rename and template instantiate return 409; import renames to `<name> (imported)`, `<name> (imported 2)`, …; quick deploy uses `<name> (2)`, …
- **Setup wizard**: The frontend shows a first-run checklist until a cluster, a template, and an instance exist, or until the user dismisses it (localStorage).

### API Route Groups

| Group | Prefix | Description |
|-------|--------|-------------|
| Auth | `/api/v1/auth` | Login, register, current user, refresh, logout, logout-all |
| OIDC Auth | `/api/v1/auth/oidc` | OpenID Connect config, authorize, callback |
| Dashboard | `/api/v1/dashboard` | Landing page overview aggregation |
| Templates | `/api/v1/templates` | CRUD + publish, unpublish, instantiate, clone (optional `name`) |
| Template Versions | `/api/v1/templates/:id/versions` | Version history listing, detail, diff |
| Quick Deploy | `/api/v1/templates/:id/quick-deploy` | One-click template deployment |
| Bulk Template Ops | `/api/v1/templates/bulk` | Bulk delete, publish, unpublish templates (up to 50) |
| Stack Definitions | `/api/v1/stack-definitions` | CRUD + nested chart management + import/export |
| Stack Instances | `/api/v1/stack-instances` | CRUD + clone, deploy, deploy-preview, stop, clean, rollback, status, logs, compare, extend TTL, follow/unfollow, custom actions (list, invoke, job log) |
| Bulk Operations | `/api/v1/stack-instances/bulk` | Bulk deploy, stop, clean, delete (up to 50 instances) |
| Value Overrides | `/api/v1/stack-instances/:id/overrides` | Per-chart value overrides |
| Branch Overrides | `/api/v1/stack-instances/:id/branches` | Per-chart branch overrides (list, get, set, delete per chart) |
| Quota Overrides | `/api/v1/stack-instances/:id/quota-overrides` | Per-instance resource quota overrides (`PUT` replaces all fields) |
| Git | `/api/v1/git` | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Filterable audit log viewer + export |
| Users | `/api/v1/users` | User management (admin): list, delete, disable, enable, password reset, role change |
| API Keys | `/api/v1/users/:id/api-keys` | API key management |
| Admin | `/api/v1/admin` | Orphaned namespace detection and cleanup |
| Cleanup Policies | `/api/v1/admin/cleanup-policies` | Cron-based cleanup policy management |
| Notification Channels | `/api/v1/admin/notification-channels` | Outbound webhook channels, event types, subscriptions, test, delivery logs |
| Clusters | `/api/v1/clusters` | Multi-cluster registration, health, test-connection, quotas, utilization |
| Shared Values | `/api/v1/clusters/:id/shared-values` | Per-cluster shared Helm values |
| Favorites | `/api/v1/favorites` | User bookmark management |
| Notifications | `/api/v1/notifications` | List, read/unread, count, preferences |
| Analytics | `/api/v1/analytics` | Usage overview, template stats, user stats |
| UI Config | `/api/v1/ui-config` | Public branding values (title, logo, favicon) |
| Health | `/health/*` | Liveness + readiness |
| WebSocket | `/ws` | Real-time status and log events (token via query param, redacted in logs); shared between replicas through `ws_events` when `WS_FANOUT_ENABLED` |

## Security Rules

- Never expose raw error messages for 500s — return `"Internal server error"`
- All secrets via environment variables, never hardcode credentials
- GORM parameterizes queries — never interpolate user input in raw SQL
- CORS allows `*` in dev only; restrict `CORS_ALLOWED_ORIGINS` for production
- Rate limiting: per-IP sliding window on API routes; stricter login limiter (`LOGIN_RATE_LIMIT`)
- `SecurityHeaders` middleware sets baseline headers on every response — never remove it
- Recovery middleware catches panics globally — never remove it
- Revoked access-token `jti`s and blocked users live in `sessionstore`; revoked refresh tokens live in `RefreshTokenRepository`. JWT auth and the `/ws` upgrade check the blocklist; every auth path (login, refresh, OIDC, API key) rejects `User.Disabled`
- `RedactWSToken` strips `?token=` from `/ws` URLs before logging, metrics, and tracing — keep it first after RequestID
- Outbound hooks are HMAC-signed (`X-StackManager-Signature`) when a subscription has a secret; subscriptions without a secret are sent unsigned. Never log hook secrets or provider tokens
- Kubeconfig data is encrypted at rest with AES-GCM (`KUBECONFIG_ENCRYPTION_KEY`)

## Scalability Notes

- MySQL connection pool: `DB_MAX_OPEN_CONNS` (25), `DB_MAX_IDLE_CONNS` (5), `DB_CONN_MAX_LIFETIME` (5m)
- DB retry: 5 attempts, 2s delay on startup
- Docker networks: `backend-net` (db, backend) and `frontend-net` (backend, frontend) — maintain separation
- Health checks: register for all external dependencies via `healthChecker.AddCheck()`
- Background workers run on the leader replica only (`internal/leader`, Kubernetes Lease); add a periodic job as a `leader.Worker` with a restartable `Run(ctx)`, and keep state that must survive a leader change in the database. WebSocket messages and socket revocations reach all replicas through the hub fan-out (`WS_FANOUT_ENABLED`, `ws_events`, 500ms poll; the hub send never blocks, a full buffer drops the row; ws_events writes also go to the MySQL binary log); send through `Broadcast`, `BroadcastToInstance` or `BroadcastToUser`, revoke through `DisconnectUser` or `DisconnectToken`. Readiness does not depend on leadership
- Always implement pagination for new list endpoints — `page`/`pageSize` query params (default 25, max 100) backed by `ListPaged(..., limit, offset)`, as in stack instances, definitions and templates (stack instances and definitions also accept a `limit`/`offset` fallback; templates take `page`/`pageSize` only). Older endpoints (items, audit logs, notifications, delivery logs) take `limit`/`offset`; small admin lists are unpaged. Select only columns needed for list views (omit TEXT fields like `description`). Use batch queries (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops for enrichment data.
- Use `internal/cache` for short-lived in-memory caching instead of ad-hoc maps (used by combined auth, login, dashboard and analytics handlers; `LOGIN_CACHE_TTL`). `gitprovider.Registry` keeps its own 5-minute branch cache
- Struct field ordering: optimize for memory alignment (8-byte fields first)

## Project Plan

`ROADMAP.md` tracks upcoming work. `ARCHITECTURE.md` describes the system; `EXTENDING.md` and `backend/docs/hooks.md` describe the hooks/actions extension contract; `WIKI.md` covers user-facing concepts; `docs/getting-started.md` is the end-user walkthrough.
