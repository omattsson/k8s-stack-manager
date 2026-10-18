---
applyTo: "**"
---

# Scalability Instructions

## Connection pooling

The MySQL connection pool uses environment variables (set in `docker-compose.yml`) and is applied in `internal/database/factory.go`:

- `DB_MAX_OPEN_CONNS` (default 25): maximum open connections.
- `DB_MAX_IDLE_CONNS` (default 5): idle connections that stay open.
- `DB_CONN_MAX_LIFETIME` (default 5m): maximum lifetime of a connection.

The factory sets them with `SetMaxOpenConns`, `SetMaxIdleConns`, and `SetConnMaxLifetime`. Idle connections reduce latency for bursts. MySQL has a `max_connections` limit (see `backend/config/mysql/my.cnf`). Remember that each replica has its own pool.

## Database retry

`NewFromAppConfig()` in `internal/database/factory.go` tries to connect up to 5 times with a delay of 2 seconds. This handles container start order and short network problems. Use the same pattern for new external service connections.

## Docker network isolation

`docker-compose.yml` has two networks:

- `backend-net` connects MySQL and the backend.
- `frontend-net` connects the backend and the frontend.

The frontend container cannot reach the database. Keep this separation when you add services.

## Health checks

`internal/health/health.go` supports many dependency checks:

```go
healthChecker.AddCheck("database", func(ctx context.Context) error { return repo.Ping(ctx) })
healthChecker.AddCheck("helm", deployer.HelmHealthCheck(cfg.Deployment.HelmBinary))
```

Docker and Kubernetes use `/health/live` (liveness) and `/health/ready` (readiness). Readiness depends on all registered checks. It does not depend on leadership. Register a health check for each new external dependency.

## Optimistic locking

`Item` has a `Version` field. The repository (`GenericRepository.Update`) updates with `WHERE version = ?`. A mismatch returns `"version mismatch"`, and the handler returns 409. The client sends the version it read. Use this pattern only for models that implement `Versionable`:

```go
type MyModel struct {
    Base
    Version uint `gorm:"not null;default:1" json:"version"`
}
```

Domain models (stack instances, definitions, users, and others) have no version column. For concurrent writers, use targeted updates (`UpdateRole`, `SetDisabled`, `MarkExpiryWarned`) or a conditional update (compare-and-set on status or `updated_at`). Keep state that must survive a leader change in the database.

## Database indexing

Add indexes in versioned migrations in `internal/database/migrations.go`. Name them `idx_<table>_<columns>`. Add an index for each new query pattern (filter, sort, join). Check that an index does not exist before you create it (`Migrator().HasIndex` or `information_schema.statistics`, see migration `20231201000002`). Check the plan with EXPLAIN. Do not leave an index that another index covers.

## Server timeouts

Server timeouts are in `internal/config/config.go`:

- `SERVER_READ_TIMEOUT` (default 10s): maximum time to read a request.
- `SERVER_WRITE_TIMEOUT` (default 0, off): maximum time to write a response. It is off to support long-lived WebSocket connections. The WebSocket write pump sets its own deadlines. Set a positive value (for example 30s) when no WebSocket is used and slow clients are a risk. `docker-compose.yml` sets 15s for local development.
- `SERVER_IDLE_TIMEOUT` (default 30s): maximum idle time of a keep-alive connection.
- `SERVER_SHUTDOWN_TIMEOUT` (default 30s): time for a graceful shutdown.

## Pagination

New and changed list endpoints take `?page=N&pageSize=M` (default 25, maximum 100). Stack instances and definitions also accept `limit` and `offset`. Templates take `page` and `pageSize` only. Use the `listPagination(c)` helper (`handlers/display_names.go`). Call `ListPaged(..., limit, offset)` with a filter struct and column projection. The count query uses the same filter, so `total` counts the matching rows.

Older endpoints (items, notifications, delivery logs) take `limit` and `offset`. Audit logs take `limit` and `offset` or a `cursor`. Small admin lists (users, clusters, cleanup policies, favorites, API keys) are not paged.

Use batch lookups (`FindByIDs`, `NamesByIDs`, `CountByTemplateIDs`) instead of N+1 loops.

## Replicas and background work

- Run each periodic job as a `leader.Worker` with a restartable `Run(ctx)`. Only the leader replica runs the workers. Every replica serves HTTP and WebSocket.
- WebSocket messages and socket revocations reach all replicas through the hub fan-out (`WS_FANOUT_ENABLED`, table `ws_events`). Send with `Broadcast`, `BroadcastToInstance`, or `BroadcastToUser`. Revoke with `DisconnectUser` or `DisconnectToken`. The hub send never blocks. A full buffer drops the message.
- Deploy, rollback, stop, and clean run on the replica that took the request. Each replica writes a heartbeat (`replica_heartbeats`). The leader worker for interrupted operations ends operations of replicas that have no heartbeat.
- Respect the shutdown signal in each new goroutine.

## Caching and metrics

Use `internal/cache` (generic in-memory cache with a time to live, TTL) for short-lived data. Combined auth, login (`LOGIN_CACHE_TTL`), dashboard, and analytics use it. `gitprovider.Registry` keeps its own 5-minute branch list cache. HTTP, DB pool, auth, and business metrics are exported with OpenTelemetry (`internal/telemetry`, `api/middleware/metrics.go`). Add a metric when you add a hot path.
