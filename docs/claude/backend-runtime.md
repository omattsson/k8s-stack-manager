# Backend Runtime

Reference for Claude Code and other agents. The root `CLAUDE.md` links here. Read this file before you work on the bootstrap, shutdown, middleware, telemetry or WebSocket messages.

## Bootstrap flow

`backend/api/main.go` calls helpers in `backend/api/bootstrap.go` in this order:

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

## Graceful shutdown

`SIGINT` or `SIGTERM`, in this order:
1. Stop the HTTP servers. Then close the port-forward tunnels and the exec sessions (1001, both `podstream.Manager`s at once) and wait for them: `http.Server.Shutdown` does not close hijacked connections (`podstream.Manager.Shutdown`). Then wait for the asynchronous audit writes (`middleware.WaitAuditWrites`, 10 s budget; a later write runs inline).
2. Stop the leader workers, release the lease, stop the cleanup scheduler.
3. Wait for in-flight deploys. Stop the heartbeat and delete its row.
4. Flush the fan-out queue. Stop the hub, the cluster registry, the notifier, the session store and the dashboard handler.
5. Flush telemetry.
6. Close the database.

## Gin mode

`GIN_MODE` wins when it is set. Otherwise the mode is debug when `APP_DEBUG` is true, and release when it is false. The code default of `APP_DEBUG` is `true`. The Helm chart sets `APP_DEBUG: "false"`.

## Ports

- Backend: `:8081` on the host (`SERVER_PORT`). Frontend: `:3000` in dev.
- Docker: nginx (`location /api/` with `proxy_pass http://backend:8081/api/`) and the Vite dev proxy (`/api` to the backend, no rewrite) both keep the `/api` prefix.
- The backend registers routes under `/api/v1`.
- Local development without Docker calls `localhost:8081` directly (`frontend/src/api/config.ts`).
- Optional servers: Prometheus metrics (`METRICS_ENABLED`, `METRICS_ADDR`, default `:9090`) and pprof (`PPROF_ENABLED`, `PPROF_ADDR`, default `127.0.0.1:6060`; `PPROF_MUTEX_FRACTION` and `PPROF_BLOCK_RATE` turn on the mutex and block profiles, default 0 = off).
- `make dev-otel`: Grafana on `:3001`, Prometheus on `:9090`.

## Middleware and route groups

- Global middleware order: RequestID, RedactWSToken, RedactGitRepoQuery, HTTPMetrics (if `OTEL_ENABLED` or `METRICS_ENABLED`), otelgin (if `OTEL_ENABLED`), Logger, Recovery, SecurityHeaders, CORS, MaxBodySize (1 MB).
- `/api/v1` adds the API rate limiter (`RATE_LIMIT`, default 100 requests per minute per IP). The login route gets a stricter limiter when `LOGIN_RATE_LIMIT` is greater than 0 (default 10 per minute). `POST /auth/refresh` uses the login limiter too.
- Client IP: `SetupRoutes` calls `SetTrustedProxies` with `TRUSTED_PROXIES` (IPs or CIDRs, `config.ParseTrustedProxies`; an invalid entry stops the start). Only those peers may set the client IP with `X-Forwarded-For` or `X-Real-IP`. Code default: no trusted proxy (the TCP peer). `0.0.0.0/0`, `::/0` and IPv4-mapped IPv6 entries are refused. The Helm chart (`backend.trustedProxies`) trusts the private IPv4 ranges and `fc00::/7`. The rate limiters, the `/ws` API-key limit and the refresh token IP use `c.ClientIP()`.
- The authenticated group adds `CombinedAuth` (JWT and API key, checks the token blocklist and disabled users), `SpanEnrichUser` (if `OTEL_ENABLED`) and `NewAuditMiddleware` (if an audit logger is configured).
- WebSocket: `/ws` (outside the `/api/v1` rate limiter). Health: `/health/*`. Swagger UI: `/swagger/*` only when `ENABLE_SWAGGER=true`.

## Telemetry

`telemetry.Init` configures OTLP traces, metrics and logs (`OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`). `StartDBMetrics`, `StartBusinessMetrics` and `StartLeaderMetric` register gauges. Middleware records HTTP and auth metrics. Add tracing for new outbound calls in a `tracing.go` next to the package (see `cluster/`, `deployer/`, `gitprovider/`, `hooks/`). With `OTEL_ENABLED`, the backend also sends its logs to the collector: `setupOTelLogging` (`api/bootstrap.go`) bridges `slog` to the OTel log pipeline (`telemetry.LogHandler`, `otelslog`) and to a slog text handler on stderr, both at `LOG_LEVEL`. Log records with a context carry its trace and span ID. Never wrap the package default slog handler in a new default: it writes through the `log` package, which then writes back through the new default and deadlocks. GORM SQL errors and slow queries also go through slog with the query context (`internal/database/gorm_logger.go`, #653).

## WebSocket messages

- Client to server: `subscribe` and `unsubscribe` with `{"instance_id": "..."}`.
- Server to client: `deployment.status`, `deployment.log` (only to the subscribers of the instance, if the sender supports it), `instance.created`, `instance.deleted`, `notification.new` (only to the sockets of the notified user). The Items reference handler (`handlers/items.go`) also sends `item.*`, but no route registers it (#534).
- Stream route: `GET /api/v1/stack-instances/:id/port-forward` is a WebSocket upgrade that tunnels raw bytes to a stack service (`internal/podstream`, `STACK_PORT_FORWARD_*`). It uses header auth only (no `?token=`) and the hub can revoke it. `GET /api/v1/stack-instances/:id/exec?chart=<chart>&container=<name>` runs one command in a container (`STACK_EXEC_*`, per-cluster `allow_exec`, own `podstream.Manager` with kind `exec`). Rules: `docs/claude/domain.md`, `docs/claude/auth-sessions.md`.
