---
applyTo: "**"
---

# Security Instructions

## Input Validation
All user input must be validated before processing. This project validates at two layers:

1. **Handler layer** — Gin's `ShouldBindJSON` rejects malformed payloads, then handlers check required fields explicitly before passing to the repository:
   ```go
   if err := c.ShouldBindJSON(&item); err != nil {
       c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
       return
   }
   if item.Name == "" {
       c.JSON(http.StatusBadRequest, gin.H{"error": "Name is required"})
       return
   }
   ```
2. **Model layer** — models implement the `Validator` interface (`internal/models/validation.go`). The repository calls `Validate()` automatically before Create/Update. Add validation for every new model.

## Error Handling — Never Leak Internals
The `handleDBError()` function in `handlers/items.go` maps database errors to safe HTTP responses. For `500` errors, always return `"Internal server error"` — never expose raw error messages, stack traces, or database details to clients:
```go
return http.StatusInternalServerError, "Internal server error"  // correct
return http.StatusInternalServerError, err.Error()              // only for non-DB errors
```

## CORS Configuration
CORS middleware is in `internal/api/middleware/middleware.go`. Default development config allows all origins (`*`). For production, restrict `CORS_ALLOWED_ORIGINS` in `docker-compose.yml` to specific domains. Never deploy with `Access-Control-Allow-Origin: *` in production.

## Database Credentials
- All secrets are passed via environment variables, loaded by `config.LoadConfig()` with `.env` fallback (godotenv).
- Never hardcode credentials in source files. Use `docker-compose.yml` env var substitution with defaults only for local dev.

## SQL Injection Prevention
GORM parameterizes all queries automatically. When writing raw SQL in migrations (`internal/database/migrations.go`), never interpolate user input — use parameterized queries exclusively.

## Soft Delete
The `Base` model uses `DeletedAt *time.Time` with a GORM index. Deletes are soft by default — rows are marked with a timestamp, not removed. GORM automatically filters soft-deleted records from queries. If implementing hard delete, be explicit and document why.

## Rate Limiting
The `RateLimiter` in `handlers/rate_limiter.go` provides per-IP request throttling using a sliding window. `SetupRoutes` applies it to the whole `/api/v1` group (`RATE_LIMIT`, default 100/min) and adds a stricter login limiter (`LOGIN_RATE_LIMIT`, default 10/min):
```go
rateLimiter := handlers.NewRateLimiter(int(cfg.Server.RateLimit), time.Minute)
v1.Use(rateLimiter.RateLimit())
```

## Sessions, Tokens and Headers
- Logout blocklists the access-token `jti` in `sessionstore` (`BlockToken`); refresh tokens are revoked through `RefreshTokenRepository`, and reuse of a rotated refresh token revokes the whole family. JWT auth checks `IsTokenBlocked` (fail open on store errors: log + continue); API-key auth does not use the blocklist. Disabling a user blocks the user in `sessionstore`, and every auth path (login, refresh, OIDC, API key) rejects `User.Disabled`.
- `middleware.SecurityHeaders()` sets baseline security headers; `middleware.RedactWSToken()` strips `?token=` from `/ws` URLs before logging, metrics and tracing. Keep both.
- Outbound hooks are HMAC-signed (`X-StackManager-Signature`, `internal/hooks/client.go`). Never log hook secrets, provider tokens, or kubeconfig data.
- Kubeconfig data is encrypted at rest with AES-GCM (`pkg/crypto`, `KUBECONFIG_ENCRYPTION_KEY`).

## Recovery Middleware
The `Recovery()` middleware in `internal/api/middleware/middleware.go` catches panics and returns a generic 500. This is applied globally in `routes.go`. Never remove it — it prevents unhandled panics from crashing the server and potentially leaking stack traces.

## Graceful Shutdown
`api/main.go` handles `SIGINT`/`SIGTERM` with a 30-second context for in-flight requests (configurable via `SERVER_SHUTDOWN_TIMEOUT`). Ensure any new background goroutines or workers respect shutdown signals.
