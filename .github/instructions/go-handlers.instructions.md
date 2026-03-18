---
description: "Use when creating or modifying Go API handler functions in the backend"
applyTo: "backend/internal/api/handlers/**/*.go"
---

# Go Handler Guidelines

- Every handler must have Swagger annotations: @Summary, @Description, @Tags, @Accept, @Produce, @Param, @Success, @Failure, @Router
- Use `c.ShouldBindJSON()` for request body parsing with struct `binding` tags
- Return structured error responses: `c.JSON(statusCode, gin.H{"error": "message"})`
- Log errors with `slog.Error()` including context fields
- Every mutating handler (POST/PUT/DELETE) must call the audit log service after successful operation
- Extract user from Gin context via `c.GetString("userID")` and `c.GetString("username")` (set by auth middleware)
- Use proper HTTP status codes: 200 OK, 201 Created, 204 No Content, 400 Bad Request, 401 Unauthorized, 403 Forbidden, 404 Not Found, 409 Conflict, 500 Internal Server Error
- Group related handlers in the same file (e.g., all stack definition handlers in `stack_definitions.go`)
