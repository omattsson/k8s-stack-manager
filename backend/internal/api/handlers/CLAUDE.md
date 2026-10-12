# Handler Guidelines

- Every handler needs these Swagger annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router.
- Parse the request body with `c.ShouldBindJSON()` and struct `binding` tags.
- Return structured error responses: `c.JSON(statusCode, gin.H{"error": "message"})`.
- Log errors with `slog.Error()` and context fields.
- The audit middleware `middleware.NewAuditMiddleware` writes the audit log. It is applied to route groups. Handlers do not call the audit service directly. One exception: `quick_deploy.go` writes its own AuditLog, because it creates and deploys in one request. Its route has `Skip: true` in `auditRoutes`.
- Get the user from the Gin context with these helpers: `middleware.GetUserIDFromContext(c)`, `middleware.GetUsernameFromContext(c)`, `middleware.GetRoleFromContext(c)`.
- Check instance access with `requireInstanceModify(c, inst)` (owner, devops or admin; `authz.go`). Check definition access with `requireDefinitionModify(c, def)`.
- Use these HTTP status codes: 200 OK, 201 Created, 204 No Content, 400 Bad Request, 401 Unauthorized, 403 Forbidden, 404 Not Found, 409 Conflict, 500 Internal Server Error.
- Put related handlers in the same file.
- For real-time events, use `NewHandlerWithHub(repo, hub)`. Call `h.broadcast(msgType, payload)` after a successful mutation.
- Map repository errors to an HTTP status: use `handleDBError(err)` in the Items reference handler, and `mapError(err, "EntityName")` (in `errors.go`) in all domain handlers. Never leak internal details. A 500 returns `"Internal server error"`.
- Domain handlers use separate structs with specialized repositories (for example `InstanceHandler`, `DefinitionHandler`, `AdminHandler`). Each struct has its own constructor that takes the repositories it needs. Follow this pattern for new resources.
- The generic `Handler` struct with `models.Repository` is only for the Items reference implementation.
