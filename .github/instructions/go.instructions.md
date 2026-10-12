---
applyTo: "**/*.go"
---

# Go Backend Instructions

## Project layout

All backend code is in `backend/`. The module is `backend` (`go.mod`, Go 1.26). Use `backend/internal/` for private packages and `backend/pkg/` for shared packages (`dberrors`, `crypto`, `utils`).

Packages in `internal/`: `api` (handlers, middleware, routes), `auth` (OpenID Connect, OIDC), `cache` (in-memory cache with a time to live, TTL), `cluster` (`ClusterRegistry`, health poller, quota monitor, secret refresher), `config`, `database` (GORM repositories, migrations), `deployer` (Helm CLI), `gitprovider`, `health`, `helm` (values merge), `hooks` (outbound webhooks, actions), `k8s`, `leader` (leader election), `models`, `notifier`, `replica` (identity, heartbeat), `scheduler`, `sessionstore` (token blocklist, OIDC state), `telemetry` (OpenTelemetry, metrics), `ttl`, `websocket`.

## Handlers

Handlers are in `internal/api/handlers/`. The `Item` reference handler uses the generic `Handler` struct with repository injection. Domain handlers use dedicated structs with the repositories they need and `mapError(err, "Entity")` for errors:

```go
type Handler struct {
    repository models.Repository
    hub        websocket.BroadcastSender
}
func NewHandler(repository models.Repository) *Handler { ... }
func NewHandlerWithHub(repository models.Repository, hub websocket.BroadcastSender) *Handler { ... }
func (h *Handler) CreateItem(c *gin.Context) { ... }
```

Use `NewHandlerWithHub` when a handler broadcasts WebSocket events. Register new handlers in `internal/api/routes/routes.go` (see `go-handlers.instructions.md`).

## Repository interface

The generic `models.Repository` (`internal/models/models.go`) serves `Item`:

```go
type Repository interface {
    Create(ctx context.Context, entity interface{}) error
    FindByID(ctx context.Context, id uint, dest interface{}) error
    Update(ctx context.Context, entity interface{}) error
    Delete(ctx context.Context, entity interface{}) error
    List(ctx context.Context, dest interface{}, conditions ...interface{}) error
    Ping(ctx context.Context) error
    Close() error
}
```

`GenericRepository` (GORM and MySQL) implements it. It calls `Validate()` on create and update. It uses `Version` locking for `Versionable` models. Domain resources have their own repository interfaces in `internal/models/<entity>.go` and GORM implementations in `internal/database/<entity>_repository.go`. They are wired in `repository_factory.go`. Newer domain methods take `context.Context` first; older methods do not.

## Models

Put each model in its own file `internal/models/<entity>.go` with its repository interface. `models.go` holds the shared types.

- `Item` embeds `Base` (ID, timestamps, `DeletedAt`) and has `Version`. `Base.DeletedAt` is a plain `*time.Time`, so GORM deletes rows for real (no soft delete).
- Domain models use a string `ID` (`gorm:"primaryKey;size:36"`) that the repository sets with `uuid.New().String()`. They do not embed `Base` and have no `Version` column.

```go
type Item struct {
    Base
    Name    string  `gorm:"size:255;not null" json:"name"`
    Price   float64 `json:"price"`
    Version uint    `gorm:"not null;default:1" json:"version"` // 1 = initial; 0 = not sent
}
```

Implement `Validator` in `internal/models/validation.go`. A model that supports optimistic locking implements `Versionable` (`GetVersion`, `SetVersion`).

## Error handling

Use the error types in `pkg/dberrors/errors.go` (`internal/database/errors.go` re-exports the main ones):

- Sentinel errors: `ErrNotFound`, `ErrDuplicateKey`, `ErrValidation`, `ErrConnectionFailed`, `ErrNotImplemented`.
- Wrap with context: `NewDatabaseError("operation", err)`.
- Check with `errors.Is` (and `errors.As(err, &dbErr)`).
- `mapError()` (`handlers/errors.go`) maps these to HTTP statuses for domain handlers. `handleDBError()` (`handlers/items.go`) does it for `Item`.

## Testing

- Use `testify/assert` and `require`.
- Use table-driven tests (`tests := []struct{...}` and `t.Run(tt.name, ...)`).
- Call `t.Parallel()` on the parent test and each subtest. Write `tt := tt` before `t.Run`. Exception: a test that sets process-wide state (for example `models.SetAllowedChartSources`) does not run in parallel.
- `Item` tests: `MockRepository` (`handlers/mock_repository.go`) and `setupTestRouter()` (`items_test.go`).
- Domain handler tests: the mocks in `handlers/mock_domain_repositories_test.go` and a router helper with `callerID` and `callerRole` in each test file.
- Validate JSON responses with `gojsonschema` (`handlers/test_schemas.go`).
- Integration tests use the build tag `integration` and run with `make test-backend-all` (starts MySQL in Docker).
- Run unit tests: `cd backend && go test ./... -v -short`.
- Run with coverage (80% minimum): `cd backend && make test-coverage`.

## Swagger and OpenAPI

Add godoc annotations above handler methods:

```go
// CreateItem godoc
// @Summary Create a new item
// @Tags items
// @Accept json
// @Produce json
// @Param item body models.Item true "Item object"
// @Success 201 {object} models.Item
// @Router /api/v1/items [post]
```

Regenerate: `cd backend && make docs` (runs `swag init -g api/main.go`). Commit the generated files in `backend/docs/`.

## Struct field alignment

The project orders struct fields for memory alignment (`golangci-lint` runs `fieldalignment`: `cd backend && make lint`). Put pointers, strings, `time.Time`, `time.Duration`, and `int64` before smaller fields. Use `//nolint:govet` with a reason when the order is intentional.

## Formatting

Many existing files have `gofmt` differences that continuous integration (CI) accepts. Format only the lines that you change. Do not run `gofmt -w` or `make fmt` on whole files.

## Migrations

Add a named function that returns `schema.Migration` in `internal/database/migrations.go`. Register it with `migrator.AddMigration(...)`. The version is a date plus a 6-digit sequence (the last one is `20261012000058`). Make `Up` and `Down` idempotent. Migrations run at startup.

```go
migrator.AddMigration(schema.Migration{
    Version: "20231201000001",
    Name:    "create_base_tables",
    Up:      func(tx *gorm.DB) error { return tx.AutoMigrate(&models.Item{}) },
    Down:    func(tx *gorm.DB) error { return tx.Migrator().DropTable(&models.Item{}) },
})
```

## Configuration

All configuration comes from environment variables. `config.LoadConfig()` reads them, with a `.env` fallback (godotenv). Main variables:

- Database: `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`.
- Server: `SERVER_PORT` (default 8081), `GO_ENV`, `GIN_MODE`, `RATE_LIMIT`, `LOGIN_RATE_LIMIT`, `CORS_ALLOWED_ORIGINS`.
- Auth: `JWT_SECRET`, `JWT_EXPIRATION`, `OIDC_*`, `SESSION_STORE` (`mysql` or `memory`), `SESSION_IDLE_TIMEOUT`, `SESSION_MAX_LIFETIME`, `LOGIN_CACHE_TTL`.
- Deploy: `KUBECONFIG_PATH`, `KUBECONFIG_ENCRYPTION_KEY`, `HELM_BINARY`, `DEPLOYMENT_TIMEOUT`, `MAX_CONCURRENT_DEPLOYS`, `NAMESPACE_LABELS`, `ALLOWED_CHART_REPOS`, `STACK_ACCESS_CLUSTER_ROLE`, `STACK_ACCESS_SERVICE_ACCOUNT`.
- Hooks: `HOOKS_CONFIG_FILE`.
- Telemetry: `OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`, `METRICS_ENABLED`.
- Replicas: `LEADER_ELECTION_ENABLED`, `LEADER_ELECTION_LEASE_NAME`, `LEADER_ELECTION_NAMESPACE` (else `POD_NAMESPACE`), `POD_NAME`, `WS_FANOUT_ENABLED`.

See `internal/config/config.go` for all fields and defaults.
