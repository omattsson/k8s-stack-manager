# Handler Guidelines

- Every handler must have Swagger annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router
- Use `c.ShouldBindJSON()` for request body parsing with struct `binding` tags
- Return structured error responses: `c.JSON(statusCode, gin.H{"error": "message"})`
- Log errors with `slog.Error()` including context fields
- Audit logging is handled by `middleware.NewAuditMiddleware` applied to route groups — handlers do NOT call the audit service directly (one exception: `quick_deploy.go` writes its own AuditLog because it creates and deploys in a single request)
- Extract user from Gin context via helper functions: `middleware.GetUserIDFromContext(c)`, `middleware.GetUsernameFromContext(c)`, `middleware.GetRoleFromContext(c)`
- Use proper HTTP status codes: 200 OK, 201 Created, 204 No Content, 400 Bad Request, 401 Unauthorized, 403 Forbidden, 404 Not Found, 409 Conflict, 500 Internal Server Error
- Group related handlers in the same file
- For real-time events, use `NewHandlerWithHub(repo, hub)` and call `h.broadcast(msgType, payload)` after successful mutations
- Map repository errors to HTTP status: `handleDBError(err)` in the Items reference handler; `mapError(err, "EntityName")` (in `errors.go`) in all domain handlers — never leak internal details; 500s return `"Internal server error"`
- Domain handlers use separate structs with specialized repositories (e.g., `InstanceHandler`, `DefinitionHandler`, `AdminHandler`). Each has its own constructor accepting the repositories it needs. Follow the existing domain handler pattern when creating new resources.
- The generic `Handler` struct with `models.Repository` is used only for the Items reference implementation
