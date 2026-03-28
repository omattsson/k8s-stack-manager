---
description: "Use when creating or modifying Go API handler functions in the backend"
applyTo: "backend/internal/api/handlers/**/*.go"
---

# Go Handler Guidelines

- Every handler must have Swagger annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router
- Use `c.ShouldBindJSON()` for request body parsing with struct `binding` tags
- Return structured error responses: `c.JSON(statusCode, gin.H{"error": "message"})`
- Log errors with `slog.Error()` including context fields
- Audit logging is handled by `middleware.NewAuditMiddleware` applied to route groups — handlers do NOT call the audit service directly
- Extract user from Gin context via helper functions: `middleware.GetUserIDFromContext(c)`, `middleware.GetUsernameFromContext(c)`, `middleware.GetRoleFromContext(c)`
- Use proper HTTP status codes: 200 OK, 201 Created, 204 No Content, 400 Bad Request, 401 Unauthorized, 403 Forbidden, 404 Not Found, 409 Conflict, 500 Internal Server Error
- Group related handlers in the same file (e.g., all stack definition handlers in `stack_definitions.go`)
- For handlers that should broadcast real-time events, use `NewHandlerWithHub(repo, hub)` and call `h.broadcast(msgType, payload)` after successful mutations
- Use `handleDBError(err)` for all repository errors — it maps DB errors to correct HTTP status codes and never leaks internal details
- Domain handlers (non-Items) use `mapError(err, entityName)` from `errors.go` — it provides contextual entity names in error messages
