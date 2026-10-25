# K8s Stack Manager

## Architecture Overview

K8s Stack Manager is a full-stack application.
- Backend: Go (Gin). Frontend: React (TypeScript, Vite, MUI). Data store: MySQL (GORM).
- Docker Compose runs all services.
- Versions: Go 1.27.3 (`backend/go.mod`; CI reads the version from `backend/go.mod`, and the images build with `golang:1.27.3`), Node 22.12 or later (Vitest needs `^22.12 || ^24 || >=26`; the images use `node:26-alpine`).

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
- Optional servers: Prometheus metrics (`METRICS_ENABLED`, `METRICS_ADDR`, default `:9090`) and pprof (`PPROF_ENABLED`, `PPROF_ADDR`, default `127.0.0.1:6060`; `PPROF_MUTEX_FRACTION` and `PPROF_BLOCK_RATE` turn on the mutex and block profiles, default 0 = off).
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
| Changed backend packages with `-race -count=3` (order-dependent tests) | `make test-backend-repeat` (`REPEAT_BASE`, default `origin/main`; `REPEAT_COUNT`) |
| Frontend tests | `cd frontend && npm test` |
| Unit tests (backend and frontend) | `make test` |
| End-to-end tests | `make test-e2e` (starts infrastructure, backend and Playwright; no cluster access unless `E2E_KUBECONFIG` names a local kubeconfig; starts the test stub `frontend/e2e/stub/`: the subscriber of the actions in `frontend/e2e/fixtures/hooks-e2e.json` on http://127.0.0.1:18090, and an HTTPS webhook receiver on port 18443, which the backend trusts on Linux only; `E2E_SPECS=e2e/x.spec.ts` runs only these specs and skips the isolated phase; then restarts the backend with short session limits and runs the `isolated` project; CI: `.github/workflows/e2e.yml`) |
| Isolated e2e tests only | `make test-e2e-isolated` (project `isolated`, one worker; backend with `E2E_SESSION_*` limits: access token 10s, idle 20s, maximum lifetime 60s, reuse grace 3s) |
| Swagger docs | `make docs` (runs `swag init -g api/main.go` in `backend`) |
| Coverage (80% threshold) | `cd backend && make test-coverage` |
| Format | `make fmt` |
| Lint | `make lint` (`go vet` and `npm run lint`) |
| Security scan (backend) | `make gosec` (`scripts/gosec.sh`: pinned gosec built with the local Go, run inside `backend/`, as in CI) |
| Install dependencies | `make install` |
| Helm lint | `make helm-lint` |
| Helm dry-run render | `make helm-template` |
| Helm render checks (default values, secrets, External Secrets, Traefik priority, Grafana dashboard ConfigMap) | `make helm-test` |
| Helm install, upgrade, uninstall | `make helm-install`, `make helm-upgrade`, `make helm-uninstall` |
| Helm package for release | `make helm-release` |
| Load tests | `make loadtest` (see the `loadtest-*` targets; `LOADTEST_PORT`, `LOADTEST_MYSQL=external`; memory and CPU: `make loadtest-capture` or `LOADTEST_CAPTURE=1`, see `loadtest/README.md`) |

## Reference Docs

These files do not load on every request. Read the matching file before you work in its area.

| Area | File |
|---|---|
| Domain rules: instances, TTL, deploy and Helm values, rollback, quotas, access control, templates, definitions, clusters, cleanup policies, notifications, dashboard, branding, API route groups | `docs/claude/domain.md` |
| Sessions, refresh tokens, login, WebSocket auth and revocation | `docs/claude/auth-sessions.md` |
| Lifecycle hooks and custom actions | `docs/claude/hooks-actions.md` |
| Helm chart: structure, design decisions, values | `docs/claude/helm-chart.md` |
| Backend and frontend file trees | `docs/claude/structure.md` |
| Scalability, replicas, interrupted operation recovery, log retention | `docs/claude/operations.md` |

Directory rules: `backend/CLAUDE.md`, `frontend/CLAUDE.md`, `backend/internal/api/handlers/CLAUDE.md`.

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
- A cancelled request context (`context.Canceled`) gives 499 (client closed request) and a debug log, not 500. `context.DeadlineExceeded` stays 500.
- Context rule: a repository read on the request path uses the request context (`c.Request.Context()`). A write whose side effects follow the commit uses a detached context, so a client that leaves cannot skip them: `detachedCtx(c)` (`writeCtx(c)` in instance handlers, `ctxutil.Detach(ctx, timeout)` elsewhere). Each one has a deadline (`ctxutil.WriteTimeout`, 30 s) and returns a cancel function that the caller must call, so a stalled database cannot hold a request forever (#674); a timeout gives 500. Never use a bare `context.WithoutCancel` anywhere in the backend: background jobs detach with `ctxutil.Detach` too (`ctxutil.TestNoUnboundedDetach` fails). A post-commit hook (`InstanceHandler.firePostHook`) detaches again with `WriteTimeout` plus the subscriber timeouts, so the write deadline never cuts a hook short. Never keep the request context after the handler returns.
- Never expose a raw error message for a 500. Always return `"Internal server error"`.

**Optimistic locking.** Only the `Item` reference model has `Version uint` (`default:1`). `GenericRepository.Update()` uses `WHERE version = ?`. On a conflict it returns a `"version mismatch"` error. The Items `UpdateItem` handler maps it to 409. The domain models have no version column. They use targeted or conditional writes (for example `UserRepository.UpdateRole`, the conditional instance status updates).

**Filter whitelist.** `GenericRepository` has an `allowedFilterFields` map. A new entity needs `NewRepositoryWithFilterFields()`, or you must extend the existing repository.

**Routes registration.**
- `SetupRoutes()` takes a `Deps` struct with all handlers and repositories. Handlers are optional. Register each one inside an `if deps.XHandler != nil` block.
- It returns `*RateLimiters` (API limiter and optional login limiter). The caller must call `Stop()` on shutdown.
- Global middleware order: RequestID, RedactWSToken, RedactGitRepoQuery, HTTPMetrics (if `OTEL_ENABLED` or `METRICS_ENABLED`), otelgin (if `OTEL_ENABLED`), Logger, Recovery, SecurityHeaders, CORS, MaxBodySize (1 MB).
- `/api/v1` adds the API rate limiter (`RATE_LIMIT`, default 100 requests per minute per IP). The login route gets a stricter limiter when `LOGIN_RATE_LIMIT` is greater than 0 (default 10 per minute). `POST /auth/refresh` uses the login limiter too.
- Client IP: `SetupRoutes` calls `SetTrustedProxies` with `TRUSTED_PROXIES` (IPs or CIDRs, `config.ParseTrustedProxies`; an invalid entry stops the start). Only those peers may set the client IP with `X-Forwarded-For` or `X-Real-IP`. Code default: no trusted proxy (the TCP peer). `0.0.0.0/0`, `::/0` and IPv4-mapped IPv6 entries are refused. The Helm chart (`backend.trustedProxies`) trusts the private IPv4 ranges and `fc00::/7`. The rate limiters, the `/ws` API-key limit and the refresh token IP use `c.ClientIP()`.
- The authenticated group adds `CombinedAuth` (JWT and API key, checks the token blocklist and disabled users), `SpanEnrichUser` (if `OTEL_ENABLED`) and `NewAuditMiddleware` (if an audit logger is configured).
- Role gates use `RequireAdmin()` and `RequireDevOps()`.
- WebSocket: `/ws` (outside the `/api/v1` rate limiter). Health: `/health/*`. Swagger UI: `/swagger/*` only when `ENABLE_SWAGGER=true`.

**Telemetry.** `telemetry.Init` configures OTLP traces, metrics and logs (`OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`). `StartDBMetrics`, `StartBusinessMetrics` and `StartLeaderMetric` register gauges. Middleware records HTTP and auth metrics. Add tracing for new outbound calls in a `tracing.go` next to the package (see `cluster/`, `deployer/`, `gitprovider/`, `hooks/`). With `OTEL_ENABLED`, the backend also sends its logs to the collector: `setupOTelLogging` (`api/bootstrap.go`) bridges `slog` to the OTel log pipeline (`telemetry.LogHandler`, `otelslog`) and to a slog text handler on stderr, both at `LOG_LEVEL`. Log records with a context carry its trace and span ID. Never wrap the package default slog handler in a new default: it writes through the `log` package, which then writes back through the new default and deadlocks. GORM SQL errors and slow queries also go through slog with the query context (`internal/database/gorm_logger.go`, #653).

**WebSocket messages.**
- Client to server: `subscribe` and `unsubscribe` with `{"instance_id": "..."}`.
- Server to client: `deployment.status`, `deployment.log` (only to the subscribers of the instance, if the sender supports it), `instance.created`, `instance.deleted`, `notification.new` (only to the sockets of the notified user). The Items reference handler (`handlers/items.go`) also sends `item.*`, but no route registers it (#534).

## Frontend

**Stack:** React 19, MUI v9, react-router-dom v7, Vite 8, TypeScript 6, Vitest 5, Playwright (`frontend/e2e/`).

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

## Domain

Developers use this application to configure, store and deploy multi-service Helm-based application stacks to shared Kubernetes clusters.

**Audit trail**
- Every mutating API endpoint (POST, PUT, DELETE) must create an AuditLog entry. `middleware.NewAuditMiddleware` writes it.
- A handler calls `middleware.SkipAudit(c)` for a successful request that changed nothing (a no-op), so no entry is written.
- An operation that is not plain CRUD (deploy, stop, publish, test, bulk, ...) needs an entry in the route table `auditRoutes` (`middleware/audit.go`: action, entity type, ID parameter, details). Without an entry, the action comes from the HTTP method and the entity type from the last route segment.
- Add new actions and entity types to `KnownAuditActions` and `KnownAuditEntityTypes`, and to the filter lists of `pages/AuditLog`.
- `routes/audit_routes_test.go` fails for a mutating route with an unknown entity type.

## Security Rules

- Never expose a raw error message for a 500. Return `"Internal server error"`.
- Set all secrets through environment variables. Never hard-code credentials.
- GORM parameterizes queries. Never interpolate user input into raw SQL.
- CORS allows `*` in dev only (code default; a startup warning when `GO_ENV=production`). The Helm chart default is `same-origin`: no cross-origin requests, the web UI on the same host passes. Set a list of origins only for other browser clients.
- Rate limiting: a per-IP sliding window on API routes, and a stricter login limiter (`LOGIN_RATE_LIMIT`). The IP is the TCP peer, or the client behind a proxy of `TRUSTED_PROXIES`. Never trust all proxies: a client could then pick a new IP for each request.
- The `SecurityHeaders` middleware sets baseline headers on every response. Never remove it.
- The Recovery middleware catches panics globally. Never remove it.
- `sessionstore` holds the revoked access-token `jti` values, the revoked sessions (`sid`) and the blocked users. `RefreshTokenRepository` holds the revoked refresh tokens. JWT auth and the `/ws` upgrade check the blocklist. Every auth path (login, refresh, OIDC, API key) rejects `User.Disabled`.
- `RedactWSToken` removes `?token=` from `/ws` URLs before logging, metrics and tracing. Keep it first after RequestID.
- Outbound hooks carry an HMAC signature (`X-StackManager-Signature`) when a subscription has a secret. A subscription without a secret is sent unsigned. Never log hook secrets or provider tokens.
- Kubeconfig data, registry passwords, notification channel secrets and notification channel webhook URLs are encrypted at rest with AES-GCM (`KUBECONFIG_ENCRYPTION_KEY`). Without the key, the backend refuses `kubeconfig_data`, `registry_password` and a non-empty channel `secret` (400). It stores a channel `webhook_url` in plaintext, because the URL is required and the Helm default key is empty.
- Helm runs with the backend identity. On shared clusters: keep `ALLOWED_CHART_REPOS` set, keep `backend.rbac.scope: namespace` and its admission policy, and never give a role `user` path a way to change a chart source.

## Scalability Rules

Details: `docs/claude/operations.md`.

- Paginate every new list endpoint: `page` and `pageSize` (default 25, maximum 100) with `ListPaged(..., limit, offset)`. Select only the columns that the list needs. Give the query an index that covers its filter and its order. Use batch queries (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops.
- The notification counts are capped (#654): `total` is exact up to offset + 1000, then `total_capped: true`; `unread_count` is exact up to 1000, then `unread_count_capped: true`.
- Add a periodic job as a `leader.Worker` with a restartable `Run(ctx)`. Keep state that must survive a leader change in the database.
- Send WebSocket messages through `Broadcast`, `BroadcastToInstance` or `BroadcastToUser`. Revoke through `DisconnectUser`, `DisconnectToken` or `DisconnectAPIKey`. Fan-out reaches all replicas.
- Register every external dependency with `healthChecker.AddCheck()`.
- A new log table that grows needs a retention in `internal/retention`.
- Use `internal/cache` for short-lived in-memory caching instead of ad-hoc maps.
- Struct field order: put 8-byte fields first for memory alignment.

## Project Plan

`ROADMAP.md` tracks upcoming work. `ARCHITECTURE.md` describes the system. `EXTENDING.md` and `backend/docs/hooks.md` describe the hooks and actions extension contract. `WIKI.md` covers user-facing concepts. `docs/getting-started.md` is the end-user walkthrough. `RELEASING.md` describes the release steps and the Release Drafter notes.
