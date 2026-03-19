# Backend — Go Instructions

## Project Layout
Module is `backend` (see `go.mod`). Use `internal/` for private packages and `pkg/` for shared utilities.

## Handler Pattern
Resource handlers use the `Handler` struct with repository injection. Use `NewHandlerWithHub` when the handler needs to broadcast WebSocket events. Register in `internal/api/routes/routes.go` under `/api/v1`.

## Repository Interface
All data access through `models.Repository` with `context.Context` as first parameter. Two implementations: `GenericRepository` (GORM/MySQL) and `azure.TableRepository`. Factory in `internal/database/repository.go` selects based on config.

## Error Handling
- Sentinel errors: `ErrNotFound`, `ErrDuplicateKey`, `ErrValidation`, `ErrConnectionFailed`
- Wrap with context: `NewDatabaseError("operation", err)`
- Check with: `errors.As(err, &dbErr)` and `errors.Is(dbErr.Err, database.ErrNotFound)`
- See `handleDBError()` in `handlers/items.go` for HTTP status mapping

## Testing
- `testify/assert` — never `testing` alone for assertions
- Table-driven tests with `t.Parallel()` on parent and subtests
- Capture range variables: `tt := tt` before `t.Run`
- Use `MockRepository` from `handlers/mock_repository.go`
- Test setup: `gin.SetMode(gin.TestMode)` + `httptest.NewRecorder()` + `setupTestRouter()`
- Validate JSON responses with `gojsonschema` against schemas in `test_schemas.go`
- Run unit tests: `go test ./... -v -short`
- Coverage (80% threshold): `make test-coverage`

## Swagger/OpenAPI
Every handler must have annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router. Regenerate: `make docs`.

## Struct Field Alignment
Optimize field ordering for memory alignment. Place 8-byte fields before smaller fields.

## Configuration
All config via env vars with `.env` fallback (godotenv). See `internal/config/config.go`.
