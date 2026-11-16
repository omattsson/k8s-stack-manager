# Adding a New API Resource

Reference for Claude Code and other agents. The root `CLAUDE.md` links here. Read this file before you add an entity, a repository or a handler.

## Steps

1. Model: create `internal/models/<entity>.go`. Domain models have a string UUID `ID` (`gorm:"primaryKey;size:36"`, set by the repository `Create`) and their own timestamps. Do not embed `Base` (uint ID, only for `Item`). Deletes are hard deletes: `Base.DeletedAt` is a plain field, not `gorm.DeletedAt`. Define the repository interface in the same file.
2. Validation: add it in `internal/models/validation.go` (implement the `Validator` interface).
3. Repository: create `internal/database/<entity>_repository.go`. Wire it in `repository_factory.go`.
4. Migration: add it in `internal/database/migrations.go` (the next version string).
5. Handler: create a file in `internal/api/handlers/` with a dedicated handler struct. Use `mapError()` for errors. Follow `items.go` for the CRUD shape.
6. Routes: register them in `internal/api/routes/routes.go` under the authenticated `/api/v1` group, inside an `if deps.XHandler != nil` block. Add the handler to `Deps` and to `api/bootstrap.go` (`buildHandlers`, `buildRouter`).
7. Swagger: add annotations to the handlers. Then run `make docs`.
8. Tests: use the mocks in `mock_domain_repositories_test.go` and the table-driven pattern.
9. Frontend: add service methods in `api/client.ts`, a new page in `pages/`, register it in `routes.tsx`, and add navigation in `components/Layout/`.

## Handler struct

- `handlers.Handler` holds `models.Repository` and an optional `websocket.BroadcastSender`. Use constructor injection: `NewHandler(repo)` or `NewHandlerWithHub(repo, hub)`.
- Domain handlers (for example `InstanceHandler`, `DefinitionHandler`, `AdminHandler`, `DashboardHandler`) use separate structs. Each has its own constructor with the repositories it needs.
- Health handlers are factory functions that return a `gin.HandlerFunc` closure.

## Optimistic locking

Only the `Item` reference model has `Version uint` (`default:1`). `GenericRepository.Update()` uses `WHERE version = ?`. On a conflict it returns a `"version mismatch"` error. The Items `UpdateItem` handler maps it to 409. The domain models have no version column. They use targeted or conditional writes (for example `UserRepository.UpdateRole`, the conditional instance status updates).

## Filter whitelist

`GenericRepository` has an `allowedFilterFields` map. A new entity needs `NewRepositoryWithFilterFields()`, or you must extend the existing repository.
