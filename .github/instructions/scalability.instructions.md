---
applyTo: "**"
---

# Scalability Instructions

## Connection Pooling
MySQL connection pool is configured via env vars in `docker-compose.yml` and applied in `internal/database/factory.go`:
- `DB_MAX_OPEN_CONNS` (default: 25) — maximum concurrent connections
- `DB_MAX_IDLE_CONNS` (default: 5) — kept-alive idle connections
- `DB_CONN_MAX_LIFETIME` (default: 5m) — max lifetime per connection

These are set on the `sql.DB` pool via `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`. Tune based on expected load — idle conns reduce latency for burst traffic, but MySQL has a `max_connections` limit (see `config/mysql/my.cnf`).

## Database Retry Logic
`NewFromAppConfig()` in `internal/database/factory.go` retries DB connections up to 5 times with 2-second delays. This handles container startup ordering and transient network issues. Extend the retry pattern for any new external service connections.

## Swappable Data Store
The `models.Repository` interface enables switching between MySQL (GORM) and Azure Table Storage without changing handler code. The factory in `internal/database/repository.go` selects based on `USE_AZURE_TABLE` config. When adding new storage backends, implement the `Repository` interface and add a case in `NewRepository()`.

## Docker Network Isolation
`docker-compose.yml` uses two networks:
- `backend-net` — connects db, backend, and azurite (backend services only)
- `frontend-net` — connects backend and frontend

This prevents the frontend container from directly accessing the database. Maintain this separation when adding new services.

## Health Checks for Orchestration
The health system in `internal/health/health.go` supports multiple dependency checks:
```go
healthChecker.AddCheck("database", db.Ping)
healthChecker.AddCheck("cache", cacheClient.Ping)  // example for new dependency
```
Docker and Kubernetes use `/health/live` (liveness) and `/health/ready` (readiness). Readiness gates on all registered dependency checks. Always register health checks for new external dependencies.

## Optimistic Locking
`Item` has a `Version` field for optimistic locking — concurrent updates are detected via version mismatch, returning `409 Conflict`. Use this pattern for any model that may be updated concurrently:
```go
type MyModel struct {
    Base
    Version uint `gorm:"not null;default:0" json:"version"`
    // ... fields
}
```
The handler checks `updateItem.Version != currentItem.Version` before persisting.

## Database Indexing
Indexes are added via versioned migrations in `internal/database/migrations.go`. The existing `idx_items_name_price` composite index optimizes the Items list filter queries. When adding new query patterns, add corresponding indexes as new migration versions. Always check `information_schema.statistics` for existence before creating (see the idempotent pattern in migration `20231201000002`).

## Server Timeouts
HTTP server timeouts are configurable in `internal/config/config.go`:
- `SERVER_READ_TIMEOUT` (default: 10s) — max time to read request
- `SERVER_WRITE_TIMEOUT` (default: 0, disabled) — max time to write response. Disabled by default to support long-lived WebSocket connections. Per-write deadlines are enforced in the WebSocket write pump. Set to a positive value (e.g. 30s) in environments without WebSocket to protect against slow clients.
- `SERVER_IDLE_TIMEOUT` (default: 30s) — max time an idle keep-alive connection stays open
- `SERVER_SHUTDOWN_TIMEOUT` (default: 30s) — graceful shutdown window

These prevent slow clients from consuming connections. Set via env vars in production based on expected request sizes.

## Pagination
The `models.Pagination` struct and List handler support server-side pagination via `?limit=N&offset=M` query params. Always implement pagination for list endpoints to avoid unbounded result sets.
