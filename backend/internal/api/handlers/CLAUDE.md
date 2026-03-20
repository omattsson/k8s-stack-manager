# Handler Guidelines

- Every handler must have Swagger annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router
- Use `c.ShouldBindJSON()` for request body parsing with struct `binding` tags
- Return structured error responses: `c.JSON(statusCode, gin.H{"error": "message"})`
- Log errors with `slog.Error()` including context fields
- Audit logging is handled by `middleware.NewAuditMiddleware` applied to route groups — handlers do NOT call the audit service directly
- Extract user from Gin context via helper functions: `middleware.GetUserIDFromContext(c)`, `middleware.GetUsernameFromContext(c)`, `middleware.GetRoleFromContext(c)`
- Use proper HTTP status codes: 200 OK, 201 Created, 204 No Content, 400 Bad Request, 401 Unauthorized, 403 Forbidden, 404 Not Found, 409 Conflict, 500 Internal Server Error
- Group related handlers in the same file
- For real-time events, use `NewHandlerWithHub(repo, hub)` and call `h.broadcast(msgType, payload)` after successful mutations
- Use `handleDBError(err)` for all repository errors — never leaks internal details
- Domain handlers use separate structs with specialized repositories (e.g., `InstanceHandler`, `DefinitionHandler`, `AdminHandler`). Each has its own constructor accepting the repositories it needs. Follow the existing domain handler pattern when creating new resources.
- The generic `Handler` struct with `models.Repository` is used only for the Items reference implementation
