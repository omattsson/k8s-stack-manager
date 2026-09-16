---
applyTo: "**/*.go"
---

# Go Backend Instructions

## Project Layout
All backend code lives under `backend/`. The module is `backend` (see `go.mod`, Go 1.26). Use `backend/internal/` for private packages and `backend/pkg/` for shared utilities.

Packages under `internal/`: `api` (handlers, middleware, routes), `auth` (OIDC), `cache` (in-memory TTL cache), `cluster` (ClusterRegistry, health poller, quota monitor, secret refresher), `config`, `database` (GORM repositories, migrations), `deployer` (Helm CLI), `gitprovider`, `health`, `helm` (values merge), `hooks` (outbound webhooks, HMAC), `k8s`, `models`, `notifier`, `scheduler`, `sessionstore` (token blocklist + OIDC state), `telemetry` (OpenTelemetry, metrics), `ttl`, `websocket`.

## Handler Pattern
HTTP handlers live in `internal/api/handlers/`. The Items reference handler uses the generic `Handler` struct with repository injection; domain handlers use dedicated structs with the repositories they need and `mapError(err, "Entity")` for errors:
```go
type Handler struct {
    repository models.Repository
    hub        websocket.BroadcastSender
}
func NewHandler(repository models.Repository) *Handler { ... }
func NewHandlerWithHub(repository models.Repository, hub websocket.BroadcastSender) *Handler { ... }
func (h *Handler) CreateItem(c *gin.Context) { ... }
```
Use `NewHandlerWithHub` when the handler needs to broadcast WebSocket events on mutations. Register new handlers in `internal/api/routes/routes.go` under the `/api/v1` Gin route group.

## Repository Interface
All data access goes through `models.Repository` (defined in `internal/models/models.go`):
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
All methods take `context.Context` as the first parameter. Implemented by `GenericRepository` (GORM/MySQL). The factory in `internal/database/repository.go` initializes the repository based on config.

## Models
Define each model in its own file `internal/models/<entity>.go` together with its repository interface (`models.go` holds the shared types: `Base`, `Item`, `Validator`, `Versionable`, the generic `Repository` interface, `GenericRepository`, `Filter`, `Pagination`). Embed `Base` for ID, timestamps, and soft-delete:
```go
type Item struct {
    Base
    Name    string  `gorm:"size:255;not null" json:"name"`
    Price   float64 `json:"price"`
    Version uint    `gorm:"not null;default:1" json:"version"` // 1 = initial; 0 = not provided
}
```
Add validation methods implementing `Validator` interface in `internal/models/validation.go`.

Models supporting optimistic locking should also implement the `Versionable` interface:
```go
type Versionable interface {
    GetVersion() uint
    SetVersion(v uint)
}
```

## Error Handling
Use the custom error types in `internal/database/errors.go` and `pkg/dberrors/errors.go`:
- Sentinel errors: `ErrNotFound`, `ErrDuplicateKey`, `ErrValidation`, `ErrConnectionFailed`
- Wrap with context: `NewDatabaseError("operation", err)`
- Check with: `errors.As(err, &dbErr)` and `errors.Is(dbErr.Err, database.ErrNotFound)`
- See `handleDBError()` in `handlers/items.go` for the HTTP status mapping pattern.

## Testing
- Use `testify/assert` — never `testing` alone for assertions.
- Use **table-driven tests** with `tests := []struct{...}` and `t.Run(tt.name, ...)`.
- Always call `t.Parallel()` on both the parent test and subtests.
- Capture range variables: `tt := tt` before `t.Run`.
- Use `MockRepository` from `handlers/mock_repository.go` for unit tests (no DB needed).
- Test setup pattern: `gin.SetMode(gin.TestMode)` + `httptest.NewRecorder()` + `setupTestRouter()`.
- Validate JSON responses with `gojsonschema` against schemas in `handlers/test_schemas.go`.
- Integration test naming: `TestDatabase*` (MySQL).
- Run unit tests: `cd backend && go test ./... -v -short`
- Run with coverage (80% threshold): `cd backend && make test-coverage`

## Swagger/OpenAPI
Add godoc-style annotations above handler methods:
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
Regenerate docs: `cd backend && make docs` (runs `swag init -g api/main.go`).

## Struct Field Alignment
This project optimizes struct field ordering for memory alignment. Place 8-byte fields (pointers, `time.Duration`, strings, `int64`) before smaller fields. Use `//nolint:govet` when intentional.

## Migrations
Add versioned migrations in `internal/database/migrations.go` using `schema.Migrator`:
```go
migrator.AddMigration(schema.Migration{
    Version: "20231201000001",
    Name:    "create_base_tables",
    Up:      func(tx *gorm.DB) error { return tx.AutoMigrate(&models.Item{}) },
    Down:    func(tx *gorm.DB) error { return tx.Migrator().DropTable(&models.Item{}) },
})
```
Migrations run automatically on startup.

## Configuration
All config via env vars, loaded by `config.LoadConfig()` with `.env` fallback (godotenv). Key vars: `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, `PORT`, `APP_ENV`, `JWT_SECRET`, `OIDC_*`, `SESSION_STORE` (mysql | memory), `SESSION_IDLE_TIMEOUT`, `LOGIN_CACHE_TTL`, `RATE_LIMIT`, `LOGIN_RATE_LIMIT`, `KUBECONFIG_PATH`, `KUBECONFIG_ENCRYPTION_KEY`, `HELM_BINARY`, `HOOKS_CONFIG_FILE`, `OTEL_ENABLED`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `OTEL_TRACE_SAMPLE_RATE`, `METRICS_ENABLED`. See `internal/config/config.go` for all fields and defaults.
