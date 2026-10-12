---
description: "Use when you create or change Go API handler functions in the backend"
applyTo: "backend/internal/api/handlers/**/*.go"
---

# Go Handler Guidelines

- Add Swagger annotations to each handler: `@Summary`, `@Description`, `@Tags`, `@Accept`, `@Produce`, `@Param`, `@Success`, `@Failure`, `@Router`. Run `cd backend && make docs` after a change.
- Parse request bodies with `c.ShouldBindJSON()` and struct `binding` tags. Then check required fields.
- Return errors as `c.JSON(statusCode, gin.H{"error": "message"})`.
- A 500 returns `"Internal server error"`. Never return `err.Error()` for a 500.
- Map repository errors with `mapError(err, "EntityName")` in domain handlers. `handleDBError(err)` is only for the `Item` handlers in `items.go`. Both return safe messages.
- Log errors with `slog.Error()` and context fields.
- Do not call the audit repository in a handler. `middleware.NewAuditMiddleware` writes the entries. A route that is not plain create, read, update, delete (CRUD) needs an entry in `auditRoutes` (`middleware/audit.go`). Exception: `quick_deploy.go` writes its own entry because it creates and deploys in one request.
- Register new handlers in `routes.go` under the `authed` group inside an `if deps.XHandler != nil` block. The group applies `CombinedAuth`, `SpanEnrichUser()`, and the audit middleware.
- Get the user from the Gin context with `middleware.GetUserIDFromContext(c)`, `middleware.GetUsernameFromContext(c)`, and `middleware.GetRoleFromContext(c)`.
- Check access before any side effect. Instance changes: `requireInstanceModify(c, inst)` (owner, `devops`, `admin`). Definition changes: owner, `devops`, or `admin`. A user without `devops` or `admin` cannot change a chart name, repository, path, or version (403).
- Use these status codes: 200, 201, 204, 400, 401, 403, 404, 409, 500.
- Keep related handlers in one file (for example all stack definition handlers in `stack_definitions.go`).
- The generic `Handler` (`Item`) broadcasts events with `NewHandlerWithHub(repo, hub)` and `h.broadcast(msgType, payload)` after a successful change. Domain handlers use the hub or the deploy manager (`BroadcastInstanceCreated`, `BroadcastInstanceDeleted`).
- Domain handlers use dedicated structs with their own repositories and constructors. Entity IDs are strings.
- List endpoints take `page` and `pageSize` and call `ListPaged`. Use batch lookups for names and counts.
