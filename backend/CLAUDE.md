# Backend — Go Instructions

## Project Layout
Module is `backend` (see `go.mod`, Go 1.26). Use `internal/` for private packages and `pkg/` for shared utilities (`pkg/dberrors/`, `pkg/crypto/`, `pkg/utils/`). Shared test utilities live in `internal/test/test_helpers.go`.

Domain packages under `internal/`: `auth` (OIDC provider, PKCE), `cache` (generic in-memory TTL cache), `cluster` (ClusterRegistry, health poller, quota monitor, secret refresher), `deployer` (Helm CLI wrapper, cleanup executor, expiry stopper), `gitprovider` (Azure DevOps + GitLab), `helm` (values deep-merge), `hooks` (outbound lifecycle webhooks with HMAC signing; see `docs/hooks.md` and `EXTENDING.md` at the repo root), `k8s` (cluster client, watcher, quotas, pod exec, scaling), `leader` (Kubernetes Lease leader election + leader worker group: only the leader replica runs the background workers), `notifier` (notification dispatch + outbound channels), `scheduler` (cron cleanup policies), `sessionstore` (token blocklist + OIDC state; `SESSION_STORE=mysql` default, `memory` for tests), `telemetry` (OpenTelemetry bootstrap, DB-pool and business metrics; HTTP and auth request metrics live in `api/middleware`), `ttl` (instance expiry reaper), `websocket` (hub, clients, message types).

## Handler Pattern
The Items reference handler uses the generic `Handler` struct with repository injection. Domain handlers use dedicated structs (`InstanceHandler`, `DefinitionHandler`, `ClusterHandler`, `DashboardHandler`, `NotificationChannelHandler`, ...) with the repositories they need. Use `NewHandlerWithHub` when a handler needs to broadcast WebSocket events. Register in `internal/api/routes/routes.go` under `/api/v1`.

## Repository Interface
Generic data access through `models.Repository` with `context.Context` as first parameter. Domain-specific repositories have dedicated interfaces: some (e.g., `StackInstanceRepository`, `UserRepository`) omit the context parameter, while newer ones (e.g., `NotificationRepository`, `ResourceQuotaRepository`) take `context.Context` first — follow the interface you are implementing. Implemented by `GenericRepository` (GORM/MySQL). Factory in `internal/database/repository.go` initializes the repository based on config. New or changed list endpoints must use `ListPaged(limit, offset)` with column projection (omit TEXT fields) — never add new unbounded `List()` calls from handlers. Today only stack instances, definitions and templates use the `ListPaged` repository API; items, audit logs, notifications and delivery logs paginate through their own `limit`/`offset` parameters; users, clusters, cleanup policies, favorites and API keys return unpaged lists. Use batch methods (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops for enrichment.

## Error Handling
- Sentinel errors: `ErrNotFound`, `ErrDuplicateKey`, `ErrValidation`, `ErrConnectionFailed`
- Wrap with context: `NewDatabaseError("operation", err)`
- Check with: `errors.As(err, &dbErr)` and `errors.Is(dbErr.Err, database.ErrNotFound)`
- Items reference handler uses `handleDBError()` in `handlers/items.go` for HTTP status mapping
- Domain handlers use `mapError()` in `handlers/errors.go` — takes `(err, entityName)` and returns `(statusCode, message)` with contextual entity names

## Testing
- `testify/assert` — never `testing` alone for assertions
- Table-driven tests with `t.Parallel()` on parent and subtests
- Capture range variables: `tt := tt` before `t.Run`
- Use `MockRepository` from `handlers/mock_repository.go` (Item type only)
- Domain handler mocks live in `handlers/mock_domain_repositories_test.go`; session store mock in `mock_session_store_test.go`; transaction runner mock in `mock_tx_runner_test.go`
- Test setup: `gin.SetMode(gin.TestMode)` + `httptest.NewRecorder()` + `setupTestRouter()`
- Validate JSON responses with `gojsonschema` against schemas in `test_schemas.go`
- Run unit tests: `go test ./... -v -short`
- Coverage (80% threshold): `make test-coverage`

## Swagger/OpenAPI
Every handler must have annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router. Regenerate: `make docs`.

## Struct Field Alignment
Optimize field ordering for memory alignment. Place 8-byte fields before smaller fields.

## Configuration
All config via env vars with `.env` fallback (godotenv). See `internal/config/config.go`. Key groups: `DB_*`, `JWT_*`, `OIDC_*`, `SESSION_STORE` / `SESSION_IDLE_TIMEOUT`, `RATE_LIMIT` (default 100/min) / `LOGIN_RATE_LIMIT` (default 10/min), `KUBECONFIG_PATH` / `KUBECONFIG_ENCRYPTION_KEY`, `HELM_BINARY`, `DEPLOYMENT_TIMEOUT`, `MAX_CONCURRENT_DEPLOYS`, `HOOKS_CONFIG_FILE`, `OTEL_ENABLED` / `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_SERVICE_NAME` / `OTEL_TRACE_SAMPLE_RATE`, `METRICS_ENABLED`, `LEADER_ELECTION_*` (`ENABLED` default false = always leader; `LEASE_NAME`, `NAMESPACE`, `LEASE_DURATION`, `RENEW_DEADLINE`, `RETRY_PERIOD`) with `POD_NAME` / `POD_NAMESPACE`.

## Background Workers
Leader-only workers (TTL reaper, expiry warner, cleanup scheduler, quota and secret monitors, secret refresher, cluster health poller, k8s status watcher, refresh token cleanup) are built in `api/bootstrap.go` `buildLeaderWorkers` and run in a `leader.Group` for each leadership term. A worker exposes `Run(ctx)`: it blocks until ctx is done and must work again after it returned (no closed stop channels, no one-shot state). State that must survive a leader change goes to the database (example: `stack_instances.expiry_warned_at`). Add a new periodic job as a `leader.Worker`, not as a goroutine in `main.go`.

## WebSocket Fan-out
With `WS_FANOUT_ENABLED=true`, `websocket.Fanout` (`internal/websocket/fanout.go`) shares hub messages between replicas through `ws_events` (`WSEventRepository`). Every hub send (`Broadcast`, `BroadcastToInstance`, `BroadcastToUser`) and revocation (`DisconnectUser`, `DisconnectToken`) acts locally and queues one row; a writer goroutine inserts batches; a poller delivers rows of other origins (origin = `POD_NAME` plus a per-process suffix), skips rows older than 30s, and re-reads skipped ids for 10s (out-of-order commits, also the 1000 ids below `MAX(id)` at start). A remote revocation calls the local disconnect only (no new row); the per-replica revalidation never writes rows. The leader worker `ws-event-cleanup` deletes old rows. Send through the hub methods only; never write `ws_events` directly. Per-user messages use `BroadcastToUser`, not `Broadcast`.

## Middleware Order
`routes.SetupRoutes()` applies: RequestID → RedactWSToken → HTTPMetrics (if `OTEL_ENABLED` or `METRICS_ENABLED`) → otelgin (if `OTEL_ENABLED`) → Logger → Recovery → SecurityHeaders → CORS → MaxBodySize (1 MB). The `/api/v1` group adds the API rate limiter. The authenticated group adds `CombinedAuth` (JWT + API key), `SpanEnrichUser` (if `OTEL_ENABLED`), and `NewAuditMiddleware` (if an audit logger is configured). `SetupRoutes` returns `*RateLimiters` (API + optional login limiter); call `Stop()` on shutdown.

## Observability
`telemetry.Init(cfg.Otel)` in `api/main.go` sets up OTLP tracing and metrics. `telemetry.StartDBMetrics`, `telemetry.StartBusinessMetrics` and `telemetry.StartLeaderMetric` (`stackmanager_leader`) register gauges. `internal/websocket/tracing.go` holds the hub and fan-out metrics (`websocket.fanout.events_written_total`, `_dropped_total`, `_delivered_total`, `_skipped_total`, `poll_errors_total`, `delivery_lag`). `middleware/metrics.go` records HTTP metrics; `middleware/auth_metrics.go` records auth outcomes; `middleware/otel.go` enriches spans with the user. `middleware/ws_redact.go` strips `?token=` from `/ws` URLs before logging and tracing.

## Sessions and Tokens
Login issues a JWT (with `jti`) plus a refresh token (`models/refresh_token.go`, hashed at rest). Routes: `POST /api/v1/auth/refresh`, `/logout`, `/logout-all`. Refresh rotates the token through `RefreshTokenRepository` within a session family (`family_id`, `session_started_at`, `rotated_at`). Reuse of a rotated token within `REFRESH_REUSE_GRACE` (30s) returns an access token only (no cookie, no revoke); later reuse revokes the family. Access tokens carry `sid` (the family); the JWT middleware calls `OnSessionActivity` (throttled, async) so `SESSION_IDLE_TIMEOUT` counts from the last request. `SESSION_MAX_LIFETIME` (12h) caps the family. User blocks are issued-at based: `IsUserBlocked(ctx, userID, iat)` rejects only tokens issued at or before the block (401). Logout blocklists the access-token `jti` in `sessionstore` (`BlockToken`) and revokes the refresh token in the repository; logout-all revokes all refresh tokens. JWT auth checks `IsTokenBlocked` and fails open on store errors (log + continue). `sessionstore` also holds user blocks (`BlockUser`), OIDC state and CLI auth sessions. All auth paths (login, refresh, OIDC, API key) reject `User.Disabled`.

## OIDC Authentication
OpenID Connect support is in `internal/auth/`. The `auth.Provider` handles OIDC discovery, authorization, token exchange and validation. CSRF-safe OIDC state (and CLI-auth state) is persisted by `sessionstore`, not `auth`. Enable via `OIDC_ENABLED=true` + related `OIDC_*` env vars in config. The `OIDCHandler` in `internal/api/handlers/oidc.go` exposes `/api/v1/auth/oidc/config`, `/authorize`, and `/callback` endpoints.
