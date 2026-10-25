# Backend: Go Instructions

## Project Layout
The module is `backend` (see `go.mod`, Go 1.27). Use `internal/` for private packages and `pkg/` for shared utilities (`pkg/dberrors/`, `pkg/crypto/`, `pkg/utils/`). Shared test utilities are in `internal/test/test_helpers.go`.

Domain packages in `internal/`:
- `auth`: OIDC provider (PKCE).
- `cache`: generic in-memory TTL cache.
- `cluster`: ClusterRegistry, health poller, quota monitor, secret monitor and refresher, `AccessReconciler`.
- `deployer`: Helm CLI wrapper, cleanup executor, expiry stopper, interrupted operation recovery.
- `gitprovider`: Azure DevOps, GitHub and GitLab.
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
- Domain repositories have their own interfaces. Every method of a domain repository takes `context.Context` first (for example `NotificationRepository`, `ResourceQuotaRepository`, `APIKeyRepository`, `UserRepository`, `StackInstanceRepository` with `CleanStartMarker`, `InstanceErrorMarker`, `InstanceNameLookup` and `TxRunner.RunInTx`, `StackDefinitionRepository`, `RefreshTokenRepository`, `ClusterRepository`, `ChartConfigRepository`, `ChartBranchOverrideRepository`, `ValueOverrideRepository`, `SharedValuesRepository`, `StackTemplateRepository`, `TemplateChartConfigRepository`, `UserFavoriteRepository`, `CleanupPolicyRepository`, `AuditLogRepository`). `BaseRepository` has no method without a context, so a repository cannot write without one. Follow the interface that you implement.
- Pass `c.Request.Context()` to a repository read on the request path. A write whose side effects follow the commit (token block, refresh-token revoke, WebSocket disconnect, audit entry) uses `context.WithoutCancel(c.Request.Context())`, so a client that leaves cannot skip them. Instance handlers use `writeCtx(c)` from the first write on; definition and template handlers use `context.WithoutCancel(c.Request.Context())`. Auth handlers detach after the identity check: login from the refresh-token issue, OIDC from user provisioning, refresh at each write and for everything after the rotation except the user re-check (a cancel gives 499 and revokes nothing). Logout and logout-all run fully detached. Cluster handlers detach from the first write on (create, update, delete, set default, quota upsert and delete). The cluster registry reads the cluster row on its own context with a 10s timeout (`registryLoadTimeout`), so one request cancel cannot fail a shared load. Chart config writes (add, update, delete, upgrade, import, instantiate), branch override writes (set, delete), value override writes (set, delete) and shared values writes (create, update, delete) detach from the write on; favorite writes (add, remove) and cleanup policy writes (create, update, delete, with the scheduler reload that follows) detach the same way; the audit middleware and quick deploy write their audit entry on the request context without its cancel (`context.WithoutCancel`), and so do the audit entries of the TTL reaper, the cleanup scheduler and the interrupted operation recovery, so the end of a term cannot skip an entry; a manual cleanup policy run reads its policy with the request context and then runs on the scheduler context, so a client that leaves does not stop a run that has started; the audit log list and export answer 499 on a cancel; template writes (create, update, delete, publish, unpublish, clone, bulk delete, bulk publish, bulk unpublish) and template chart writes (add, update, delete) detach the same way, and publish runs its whole transaction on the detached context; a committed upgrade reads its charts back on the detached context. A rollback to a target that the client leaves during the drift values build gets 499 and starts nothing. A failed or cancelled read before a guarded write (running instances, required charts, locked values, linked definitions, the existing override of an upsert) stops the write. Never keep the request context after the handler returns.
- New or changed list endpoints must use `ListPaged(limit, offset)` with column projection (omit TEXT fields). Never add a new unbounded `List()` call from a handler.
- Today only stack instances, definitions and templates use the `ListPaged` repository API. Items, audit logs, notifications and delivery logs paginate through their own `limit` and `offset` parameters. Users, clusters, cleanup policies, favorites and API keys return unpaged lists.
- Use batch methods (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops for enrichment.

## Error Handling
- Sentinel errors: `ErrNotFound`, `ErrDuplicateKey`, `ErrValidation`, `ErrConnectionFailed`, `ErrNotImplemented`.
- Wrap errors with context: `NewDatabaseError("operation", err)`.
- Check errors with `errors.As(err, &dbErr)` and `errors.Is(dbErr.Err, database.ErrNotFound)`.
- The Items reference handler uses `handleDBError()` in `handlers/items.go` for the HTTP status.
- Domain handlers use `mapError()` in `handlers/errors.go`. It takes `(err, entityName)` and returns `(statusCode, message)` with the entity name in the message.
- Both functions map a cancelled request context (`context.Canceled`) to 499 (client closed request) with a debug log, not 500. `context.DeadlineExceeded` stays 500.

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
- `TRUSTED_PROXIES`: IPs or CIDRs that may set the client IP with `X-Forwarded-For` or `X-Real-IP` (`config.ParseTrustedProxies`, applied in `routes.SetupRoutes`). Empty (code default): the client IP is the TCP peer.
- `KUBECONFIG_PATH`, `KUBECONFIG_ENCRYPTION_KEY`.
- `IN_CLUSTER_FALLBACK_KUBECONFIG` (local development only): the kubeconfig of an in-cluster cluster when the backend does not run in a pod. Empty (default): such a cluster is unreachable; the backend never falls back to `~/.kube/config`.
- `HELM_BINARY`, `DEPLOYMENT_TIMEOUT`, `MAX_CONCURRENT_DEPLOYS` (default 5).
- `K8S_CLIENT_QPS`, `K8S_CLIENT_BURST` (default 50, 100; `0` = default): the client-side request limit of each cluster client (`k8s.ClientOptions`). The client-go default (5, 10) throttled the stack page and the deploy readiness polls in the load test. `GET /stack-instances/:id/pods` and the `/status` fallback share one namespace status read for concurrent callers (`InstanceHandler.namespaceStatus`, single-flight, no cache).
- `LOG_LEVEL`: `debug`, `info` (default), `warn` or `error`. `main` applies it to the default `slog` logger. An invalid value stops the startup. With `OTEL_ENABLED`, it also applies to the logs that go to the collector.
- GORM SQL logging goes through `slog` (`newGormLogger`, `internal/database/gorm_logger.go`, #653), so `LOG_LEVEL` applies. With `OTEL_ENABLED`, the lines go to the collector with the trace and span ID of the query context. Every domain repository runs its queries with the context of its caller (`WithContext`: the request, or the worker context of a background job), so each line carries the trace ID. A SQL error has level error (`sql`, `rows`, `duration`, `error`). A cancelled query (`context.Canceled`) has level debug ("SQL query cancelled"). A slow cancelled query keeps the slow query line and adds `error`. A duplicate key (MySQL 1062) has level warn ("SQL duplicate key"), because the repositories map it to 409. A slow query (200 ms or more) has level warn (`sql`, `rows`, `duration`, `threshold`). With `APP_DEBUG=true`, every other statement has level debug: set `LOG_LEVEL=debug` to see them. With `APP_DEBUG=false`, `sql` and `error` have `?` instead of the values, also for `db.Scan` (`useParameterizedScanSQL` marks the query for `logger.RecorderParamsFilter`). The error number stays. For 1062, 1292 and 1366, the key or column name also stays. With `APP_DEBUG=true` (the code default), they hold the values, and with `OTEL_ENABLED` the values also go to the collector: keep `APP_DEBUG=false` in production (the Helm chart sets it). `gorm.ErrRecordNotFound` is never logged: the repositories map it to `dberrors.ErrNotFound`. The logger reads `slog.Default()` at each call and never wraps a handler.
- `AZURE_DEVOPS_PAT`, `AZURE_DEVOPS_AUTH` (`pat` or `workload-identity`), `AZURE_DEVOPS_DEFAULT_ORG` (the organization of the provider health check), `GITHUB_TOKEN`, `GITHUB_ALLOWED_REPOSITORIES` (required with `GITHUB_TOKEN`), `GITLAB_TOKEN`, `GITLAB_BASE_URL` (an origin without a path).
- `NAMESPACE_LABELS`: extra stack namespace labels, `k=v,k=v`.
- `ALLOWED_CHART_REPOS`: chart repository URL prefixes (`config.ParseAllowedChartRepos`). `models.SetAllowedChartSources` sets them at startup.
- `UNIQUE_INSTANCE_NAMES` (default false): an instance name is unique on its cluster (`handlers/instance_names.go`).
- `DEFAULT_INSTANCE_TTL_MINUTES` (default 0): TTL of a create request without `ttl_minutes`. `MAX_INSTANCE_TTL_DAYS` (default 30, 1 to 365; another value stops the start): maximum TTL and stop-time distance (`config/instance_ttl.go`, `handlers/instance_ttl.go`).
- `STACK_ACCESS_CLUSTER_ROLE`, `STACK_ACCESS_SERVICE_ACCOUNT` (`<namespace>/<name>`): namespace-scoped access (`k8s.AccessBinding`, in-cluster clusters only). Set both or neither. The leader worker `stack-access-reconcile` applies it to existing namespaces.
- `GIN_MODE`: wins when set. Otherwise the mode is debug when `APP_DEBUG` is true, and release when it is false. The code default of `APP_DEBUG` is `true`. The Helm chart sets `false`.
- `ENABLE_SWAGGER` (default false), `PPROF_ENABLED`, `PPROF_ADDR`, `PPROF_MUTEX_FRACTION` (default 0 = off), `PPROF_BLOCK_RATE` (nanoseconds, default 0 = off; both read only when pprof is on).
- `HOOKS_CONFIG_FILE`.
- `APP_PUBLIC_URL`: public base URL of the web UI (an https URL with a host; http, a same-origin path, user info, a query or a fragment stop the start; `config.ParsePublicURL`, trailing slash removed). `notifier.WithPublicURL` (wired in `api/bootstrap.go`) sets `instance_url` = `<APP_PUBLIC_URL>/stack-instances/<id>` in the notification channel payload (not for `instance.deleted`). Empty (default): no `instance_url` and no "Stack Manager" button on Teams cards.
- `OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`, `METRICS_ENABLED`, `METRICS_ADDR`.
- `LEADER_ELECTION_*` (`ENABLED` default false means this process is always the leader; `LEASE_NAME`, `NAMESPACE`, `LEASE_DURATION`, `RENEW_DEADLINE`, `RETRY_PERIOD`) with `POD_NAME` and `POD_NAMESPACE`.
- `WS_FANOUT_ENABLED`, `WS_FANOUT_POLL_INTERVAL`, `WS_FANOUT_RETENTION`.
- `DEPLOY_LOG_RETENTION_DAYS` (180), `AUDIT_LOG_RETENTION_DAYS` (365), `NOTIFICATION_RETENTION_DAYS` (90), `DELIVERY_LOG_RETENTION_DAYS` (30), `ACTION_RUN_RETENTION_DAYS` (180), `LOG_RETENTION_INTERVAL` (1h). `0` keeps the rows forever. A value that is not a number stops the start (`config/retention.go`).

## Background Workers
- The leader-only workers are built in `api/bootstrap.go`, function `buildLeaderWorkers`. They run in a `leader.Group` for each leadership term. The workers are:
  - TTL reaper, expiry warner, cleanup scheduler.
  - Quota monitor, secret monitor, secret refresher.
  - Cluster health poller, k8s status watcher.
  - Refresh token cleanup.
  - `stack-access-reconcile`, only when `STACK_ACCESS_*` is set.
  - `interrupted-operation-recovery`.
  - `ws-event-cleanup`, only when fan-out is enabled.
  - `log-retention` (`internal/retention`), only when a `*_RETENTION_DAYS` is greater than 0 (`buildLogRetention`). It deletes old rows of `deployment_logs`, `audit_logs`, `notifications`, `notification_delivery_logs` and `action_runs` through `RetentionRepository`, in batches of 1000 IDs (100 for `deployment_logs`: large rows) with a 100ms pause, by database time. For each existing instance it keeps the running deploy logs, the newest `deploy` or `rollback` log (any status), the newest successful `deploy` log and the two newest successful `deploy` or `rollback` logs (values drift, rollback branch). Counter `log_retention.deleted_rows_total{table}`.
- A worker has `Run(ctx)`. It blocks until `ctx` is done. It must work again after it returned. Do not use closed stop channels or one-shot state.
- Keep state that must survive a leader change in the database. Example: `stack_instances.expiry_warned_at`.
- Add a new periodic job as a `leader.Worker`, not as a goroutine in `main.go`.
- Exception: `replica.Heartbeat` runs on every replica. It writes `replica_heartbeats` every 30s. `main.go` starts it and deletes the row on shutdown.
- The deploy manager stores the same process identity on each deploy log (`replica_id`).
- `deployer.InterruptRecovery` (leader) ends operations that are past their `deadline_at` and whose process has no heartbeat for 2 minutes (database time).
- `DeploymentLogRepository.Update` writes only a running log (`models.ErrDeployLogNotRunning`). Finalize through `Manager.writeFinal`.
- Final writes of deploy, stop, clean and rollback use `Manager.finalWriteCtx` (`WithoutCancel(shutdownCtx)` plus a timeout), never `m.shutdownCtx`: `Shutdown` cancels it before it waits.
- A shutdown after the Helm installs ends the deploy with `deployer.ErrDeployInterrupted` (status `error`), never `running`. Exception: one last status read (5 s) finds the pods healthy and no blocking `post-deploy` subscriber must run. A shutdown during the Helm installs stops Helm (`exec.CommandContext`), so the charts fail (`all charts failed` or `partial`).
- A failed instance read at the start of the blocking `post-deploy` phase fails the deploy (`errPostDeployNotStarted`). It never skips the subscribers silently.

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
`routes.SetupRoutes()` applies this order: RequestID, RedactWSToken, RedactGitRepoQuery, HTTPMetrics (if `OTEL_ENABLED` or `METRICS_ENABLED`), otelgin (if `OTEL_ENABLED`), Logger, Recovery, SecurityHeaders, CORS, MaxBodySize (1 MB).
- The `/api/v1` group adds the API rate limiter.
- The authenticated group adds `CombinedAuth` (JWT and API key), `SpanEnrichUser` (if `OTEL_ENABLED`) and `NewAuditMiddleware` (if an audit logger is configured).
- `SetupRoutes` returns `*RateLimiters` (API limiter and optional login limiter). Call `Stop()` on shutdown.

## Observability
- `telemetry.Init(cfg.Otel)` in `api/main.go` sets up OTLP tracing, metrics and logs. `setupOTelLogging` (`api/bootstrap.go`) then makes the default `slog` logger write to stderr (slog text format) and to the OTel log pipeline (`telemetry.LogHandler`, the `otelslog` bridge), both at `LOG_LEVEL`. Do not wrap the package default slog handler: it writes through the `log` package, which writes back through the new default and deadlocks.
- `telemetry.StartDBMetrics`, `telemetry.StartBusinessMetrics` and `telemetry.StartLeaderMetric` (`stackmanager_leader`) register gauges.
- `internal/websocket/tracing.go` holds the hub and fan-out metrics: `websocket.fanout.events_written_total`, `events_dropped_total`, `events_delivered_total`, `events_skipped_total`, `poll_errors_total`, `delivery_lag`.
- `internal/notifier/channel/metrics.go` holds the counter `notification.channel.dropped` (#649).
- `middleware/metrics.go` records HTTP metrics. `middleware/auth_metrics.go` records auth outcomes. `middleware/otel.go` adds the user to spans.
- `middleware/ws_redact.go` removes `?token=` from `/ws` URLs before logging and tracing.

## Sessions and Tokens
- Login issues a JWT (with `jti`) and a refresh token (`models/refresh_token.go`, hashed at rest). Routes: `POST /api/v1/auth/refresh`, `/logout`, `/logout-all`.
- Refresh rotates the token through `RefreshTokenRepository` inside a session family (`family_id`, `session_started_at`, `rotated_at`).
- Reuse of a rotated token within `REFRESH_REUSE_GRACE` (30s) returns an access token only (no cookie, no revoke). Later reuse revokes the family.
- Access tokens carry `sid` (the family). The JWT middleware calls `OnSessionActivity` (throttled, asynchronous), so `SESSION_IDLE_TIMEOUT` counts from the last request. `SESSION_MAX_LIFETIME` (12h) caps the family.
- User blocks use the issue time. `IsUserBlocked(ctx, userID, iat)` rejects only tokens issued at or before the block (401).
- Logout revokes the session family of the refresh token cookie and of the `sid` of a valid bearer token (`RevokeFamilyForUser` when the credential names the user, else `RevokeFamily`), then blocks each session (`BlockSessions`: until the access-token lifetime plus 1 minute, the long block, now plus `SESSION_MAX_LIFETIME` plus 1 minute, when its revoke failed) and the `jti` of the access token (`BlockToken`). Revoke comes before block. A failed lookup or revoke answers 500 after all other steps. Logout-all revokes all refresh tokens, lists the recent session families (revoked rows too), blocks each (long block when the revoke failed), then writes a user block (`BlockUser`, no clock-skew margin).
- JWT auth checks `IsTokenBlocked`, `IsSessionBlocked` (token with a `sid`) and `IsUserBlocked` (`middleware.RevocationStatus`, also used by `/ws` and the hub revalidation). It fails open on store errors (log and continue).
- `sessionstore` also holds session blocks (`BlockSession`; `BlockSessions` writes many in batched upserts, as logout-all does), user blocks (`BlockUser`), OIDC state and CLI auth sessions.
- All auth paths (login, refresh, OIDC, API key) reject `User.Disabled`.

## OIDC Authentication
- OpenID Connect support is in `internal/auth/`. `auth.Provider` handles OIDC discovery, authorization, token exchange and validation.
- `sessionstore` persists the CSRF-safe OIDC state and the CLI-auth state, not `auth`.
- Enable it with `OIDC_ENABLED=true` and the related `OIDC_*` variables.
- `OIDCHandler` in `internal/api/handlers/oidc.go` serves `/api/v1/auth/oidc/config`, `/authorize`, `/callback`, `/cli-auth` and `/cli-token`.
