# Backend: Go Instructions

## Project Layout
The module is `backend` (see `go.mod`, Go 1.26). Use `internal/` for private packages and `pkg/` for shared utilities (`pkg/dberrors/`, `pkg/crypto/`, `pkg/utils/`). Shared test utilities are in `internal/test/test_helpers.go`.

Domain packages in `internal/`:
- `auth`: OIDC provider (PKCE).
- `cache`: generic in-memory TTL cache.
- `cluster`: ClusterRegistry, health poller, quota monitor, secret monitor and refresher, `AccessReconciler`.
- `deployer`: Helm CLI wrapper, cleanup executor, expiry stopper, interrupted operation recovery.
- `gitprovider`: Azure DevOps and GitLab.
- `helm`: values deep-merge and template variables.
- `hooks`: outbound lifecycle webhooks with HMAC signing and custom actions. See `docs/hooks.md` and `EXTENDING.md` in the repository root.
- `k8s`: cluster client, watcher, quotas, pod exec, scaling, `AccessBinding`.
- `leader`: Kubernetes Lease leader election and the leader worker group. Only the leader replica runs the background workers.
- `notifier`: notification dispatch and outbound channels.
- `replica`: process identity and heartbeat.
- `scheduler`: cron cleanup policies.
- `sessionstore`: token blocklist and OIDC state. `SESSION_STORE=mysql` is the default. Use `memory` for tests.
- `telemetry`: OpenTelemetry bootstrap, DB-pool and business metrics. HTTP and auth request metrics are in `api/middleware`.
- `ttl`: instance expiry reaper and expiry warner.
- `websocket`: hub, clients, message types, fan-out.

The entry point is `api/main.go`. The helpers (repositories, services, handlers, router, leader workers, HTTP servers) are in `api/bootstrap.go`.

## Handler Pattern
- The Items reference handler uses the generic `Handler` struct with repository injection.
- Domain handlers use dedicated structs (`InstanceHandler`, `DefinitionHandler`, `ClusterHandler`, `DashboardHandler`, `NotificationChannelHandler`, ...). Each struct has the repositories it needs.
- Use `NewHandlerWithHub` when a handler must broadcast WebSocket events.
- Register routes in `internal/api/routes/routes.go` under `/api/v1`.

## Repository Interface
- Generic data access uses `models.Repository` with `context.Context` as the first parameter. `GenericRepository` (GORM, MySQL) implements it. The factory in `internal/database/repository.go` builds it from the config.
- Domain repositories have their own interfaces. Some (for example `StackInstanceRepository`, `UserRepository`) have no context parameter. Newer ones (for example `NotificationRepository`, `ResourceQuotaRepository`) take `context.Context` first. Follow the interface that you implement.
- New or changed list endpoints must use `ListPaged(limit, offset)` with column projection (omit TEXT fields). Never add a new unbounded `List()` call from a handler.
- Today only stack instances, definitions and templates use the `ListPaged` repository API. Items, audit logs, notifications and delivery logs paginate through their own `limit` and `offset` parameters. Users, clusters, cleanup policies, favorites and API keys return unpaged lists.
- Use batch methods (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops for enrichment.

## Error Handling
- Sentinel errors: `ErrNotFound`, `ErrDuplicateKey`, `ErrValidation`, `ErrConnectionFailed`, `ErrNotImplemented`.
- Wrap errors with context: `NewDatabaseError("operation", err)`.
- Check errors with `errors.As(err, &dbErr)` and `errors.Is(dbErr.Err, database.ErrNotFound)`.
- The Items reference handler uses `handleDBError()` in `handlers/items.go` for the HTTP status.
- Domain handlers use `mapError()` in `handlers/errors.go`. It takes `(err, entityName)` and returns `(statusCode, message)` with the entity name in the message.

## Testing
- Use `testify/assert`. Never use `testing` alone for assertions.
- Write table-driven tests with `t.Parallel()` in the parent test and in the subtests.
- Capture range variables: `tt := tt` before `t.Run`.
- Use `MockRepository` from `handlers/mock_repository.go` (Item type only).
- Domain handler mocks are in `handlers/mock_domain_repositories_test.go`. The session store mock is in `mock_session_store_test.go`. The transaction runner mock is in `mock_tx_runner_test.go`.
- Test setup: `gin.SetMode(gin.TestMode)`, `httptest.NewRecorder()` and `setupTestRouter()`.
- Validate JSON responses with `gojsonschema` against the schemas in `test_schemas.go`.
- Run unit tests: `go test ./... -v -short`.
- Coverage (80% threshold): `make test-coverage`.

## Swagger/OpenAPI
Every handler needs these annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router. Regenerate the docs with `make docs`.

## Struct Field Alignment
Order the fields for memory alignment. Put 8-byte fields before smaller fields.

## Configuration
All configuration uses environment variables with a `.env` fallback (godotenv). See `internal/config/config.go`. Key groups:
- `DB_*`, `JWT_*`, `OIDC_*`.
- `SESSION_STORE`, `SESSION_IDLE_TIMEOUT`.
- `RATE_LIMIT` (default 100 per minute), `LOGIN_RATE_LIMIT` (default 10 per minute).
- `KUBECONFIG_PATH`, `KUBECONFIG_ENCRYPTION_KEY`.
- `HELM_BINARY`, `DEPLOYMENT_TIMEOUT`, `MAX_CONCURRENT_DEPLOYS` (default 5).
- `NAMESPACE_LABELS`: extra stack namespace labels, `k=v,k=v`.
- `ALLOWED_CHART_REPOS`: chart repository URL prefixes (`config.ParseAllowedChartRepos`). `models.SetAllowedChartSources` sets them at startup.
- `STACK_ACCESS_CLUSTER_ROLE`, `STACK_ACCESS_SERVICE_ACCOUNT` (`<namespace>/<name>`): namespace-scoped access (`k8s.AccessBinding`, in-cluster clusters only). Set both or neither. The leader worker `stack-access-reconcile` applies it to existing namespaces.
- `GIN_MODE`: wins when set. Otherwise the mode is debug when `APP_DEBUG` is true, and release when it is false. The code default of `APP_DEBUG` is `true`. The Helm chart sets `false`.
- `ENABLE_SWAGGER` (default false), `PPROF_ENABLED`, `PPROF_ADDR`.
- `HOOKS_CONFIG_FILE`.
- `OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`, `METRICS_ENABLED`, `METRICS_ADDR`.
- `LEADER_ELECTION_*` (`ENABLED` default false means this process is always the leader; `LEASE_NAME`, `NAMESPACE`, `LEASE_DURATION`, `RENEW_DEADLINE`, `RETRY_PERIOD`) with `POD_NAME` and `POD_NAMESPACE`.
- `WS_FANOUT_ENABLED`, `WS_FANOUT_POLL_INTERVAL`, `WS_FANOUT_RETENTION`.

## Background Workers
- The leader-only workers are built in `api/bootstrap.go`, function `buildLeaderWorkers`. They run in a `leader.Group` for each leadership term. The workers are:
  - TTL reaper, expiry warner, cleanup scheduler.
  - Quota monitor, secret monitor, secret refresher.
  - Cluster health poller, k8s status watcher.
  - Refresh token cleanup.
  - `stack-access-reconcile`, only when `STACK_ACCESS_*` is set.
  - `interrupted-operation-recovery`.
  - `ws-event-cleanup`, only when fan-out is enabled.
- A worker has `Run(ctx)`. It blocks until `ctx` is done. It must work again after it returned. Do not use closed stop channels or one-shot state.
- Keep state that must survive a leader change in the database. Example: `stack_instances.expiry_warned_at`.
- Add a new periodic job as a `leader.Worker`, not as a goroutine in `main.go`.
- Exception: `replica.Heartbeat` runs on every replica. It writes `replica_heartbeats` every 30s. `main.go` starts it and deletes the row on shutdown.
- The deploy manager stores the same process identity on each deploy log (`replica_id`).
- `deployer.InterruptRecovery` (leader) ends operations that are past their `deadline_at` and whose process has no heartbeat for 2 minutes (database time).
- `DeploymentLogRepository.Update` writes only a running log (`models.ErrDeployLogNotRunning`). Finalize through `Manager.writeFinal`.

## WebSocket Fan-out
- With `WS_FANOUT_ENABLED=true`, `websocket.Fanout` (`internal/websocket/fanout.go`) shares hub messages between replicas through `ws_events` (`WSEventRepository`).
- Every hub send (`Broadcast`, `BroadcastToInstance`, `BroadcastToUser`) and every revocation (`DisconnectUser`, `DisconnectToken`, `DisconnectAPIKey`) acts locally and queues one row.
- A writer goroutine inserts the rows in batches.
- A poller delivers the rows of other origins. The origin is `POD_NAME` plus a suffix for each process.
  - The poller skips rows older than 30s.
  - It reads skipped IDs again for 10s (out-of-order commits). This includes the 1000 IDs below `MAX(id)` at start.
- A remote revocation calls the local disconnect only (no new row). The revalidation of each replica never writes rows.
- The leader worker `ws-event-cleanup` deletes old rows.
- Send through the hub methods only. Never write `ws_events` directly. Use `BroadcastToUser` for per-user messages, not `Broadcast`.

## Middleware Order
`routes.SetupRoutes()` applies this order: RequestID, RedactWSToken, HTTPMetrics (if `OTEL_ENABLED` or `METRICS_ENABLED`), otelgin (if `OTEL_ENABLED`), Logger, Recovery, SecurityHeaders, CORS, MaxBodySize (1 MB).
- The `/api/v1` group adds the API rate limiter.
- The authenticated group adds `CombinedAuth` (JWT and API key), `SpanEnrichUser` (if `OTEL_ENABLED`) and `NewAuditMiddleware` (if an audit logger is configured).
- `SetupRoutes` returns `*RateLimiters` (API limiter and optional login limiter). Call `Stop()` on shutdown.

## Observability
- `telemetry.Init(cfg.Otel)` in `api/main.go` sets up OTLP tracing and metrics.
- `telemetry.StartDBMetrics`, `telemetry.StartBusinessMetrics` and `telemetry.StartLeaderMetric` (`stackmanager_leader`) register gauges.
- `internal/websocket/tracing.go` holds the hub and fan-out metrics: `websocket.fanout.events_written_total`, `events_dropped_total`, `events_delivered_total`, `events_skipped_total`, `poll_errors_total`, `delivery_lag`.
- `middleware/metrics.go` records HTTP metrics. `middleware/auth_metrics.go` records auth outcomes. `middleware/otel.go` adds the user to spans.
- `middleware/ws_redact.go` removes `?token=` from `/ws` URLs before logging and tracing.

## Sessions and Tokens
- Login issues a JWT (with `jti`) and a refresh token (`models/refresh_token.go`, hashed at rest). Routes: `POST /api/v1/auth/refresh`, `/logout`, `/logout-all`.
- Refresh rotates the token through `RefreshTokenRepository` inside a session family (`family_id`, `session_started_at`, `rotated_at`).
- Reuse of a rotated token within `REFRESH_REUSE_GRACE` (30s) returns an access token only (no cookie, no revoke). Later reuse revokes the family.
- Access tokens carry `sid` (the family). The JWT middleware calls `OnSessionActivity` (throttled, asynchronous), so `SESSION_IDLE_TIMEOUT` counts from the last request. `SESSION_MAX_LIFETIME` (12h) caps the family.
- User blocks use the issue time. `IsUserBlocked(ctx, userID, iat)` rejects only tokens issued at or before the block (401).
- Logout blocklists the `jti` of the access token in `sessionstore` (`BlockToken`) and revokes the refresh token in the repository. Logout-all revokes all refresh tokens.
- JWT auth checks `IsTokenBlocked`. It fails open on store errors (log and continue).
- `sessionstore` also holds user blocks (`BlockUser`), OIDC state and CLI auth sessions.
- All auth paths (login, refresh, OIDC, API key) reject `User.Disabled`.

## OIDC Authentication
- OpenID Connect support is in `internal/auth/`. `auth.Provider` handles OIDC discovery, authorization, token exchange and validation.
- `sessionstore` persists the CSRF-safe OIDC state and the CLI-auth state, not `auth`.
- Enable it with `OIDC_ENABLED=true` and the related `OIDC_*` variables.
- `OIDCHandler` in `internal/api/handlers/oidc.go` serves `/api/v1/auth/oidc/config`, `/authorize`, `/callback`, `/cli-auth` and `/cli-token`.
