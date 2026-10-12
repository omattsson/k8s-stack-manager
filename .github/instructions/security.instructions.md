---
applyTo: "**"
---

# Security Instructions

## Input validation

Validate all user input before you process it. The project validates in two layers:

1. Handler layer: `ShouldBindJSON` rejects malformed payloads. Then the handler checks the required fields before it calls the repository:

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

2. Model layer: models implement `Validator` (`internal/models/validation.go`). The generic repository calls `Validate()` before create and update. Add validation for each new model. Instance names must be RFC 1123 labels (`models.ValidateInstanceName`).

## Errors: never leak internals

`handleDBError()` (`handlers/items.go`) and `mapError()` (`handlers/errors.go`) map database errors to safe responses. A 500 always returns `"Internal server error"`. Never return raw error messages, stack traces, or database details to clients:

```go
return http.StatusInternalServerError, "Internal server error"  // correct
return http.StatusInternalServerError, err.Error()              // wrong: leaks internals
```

## Authorization

- Role-gated routes use `RequireAdmin()` or `RequireDevOps()` (`middleware/role.go`).
- Instance changes (deploy, stop, clean, delete, rollback, actions, overrides, extend) need the owner, `devops`, or `admin`. Call `requireInstanceModify` before any side effect.
- Definition changes: create, import, add chart, and remove chart need `devops` or `admin`. Update, delete, upgrade, and chart updates need the owner, `devops`, or `admin`. A user without `devops` or `admin` can change chart default values and deploy order. That user cannot change the chart name, repository, path, or version (403).
- A user without `devops` or `admin` cannot set an instance quota override above the cluster quota (403).
- A role change, disable, delete, or password reset revokes the sessions of the user (`revokeUserAccess`). The last enabled admin cannot be demoted, disabled, or deleted.

## Chart source allowlist

Helm installs charts with the identity of the backend. `ALLOWED_CHART_REPOS` (Helm value `backend.allowedChartRepos`) sets the allowed chart repository URL prefixes. Without a list, every source is allowed.

- `models.CheckChartSource(repositoryURL, chartPath, chartName)` checks a chart config.
- Chart config validation calls it on create and update. The deployer calls it again at deploy and rollback.
- A chart path with a scheme, a leading `/`, or `..` is rejected when the list is set.
- Call it in each new path that stores or deploys a chart config.

## Cluster access of the backend

- With `backend.rbac.scope=namespace` (chart default), the backend has namespaced rights only in stack namespaces. It gets them through the RoleBinding `k8s-stack-manager-access` to the ClusterRole `<fullname>-stack-access`. `k8s.Client.EnsureNamespace` applies it.
- A ValidatingAdmissionPolicy (`backend.rbac.admissionPolicy`, Kubernetes 1.30 or later) limits the backend to Roles and RoleBindings in namespaces with the label `managed-by=k8s-stack-manager`. It allows namespace creation only with that label.
- Helm gets no `--create-namespace` when the deployer created the namespace (`InstallRequest.NamespaceReady`). The admission policy refuses that flag for an existing namespace.
- Do not add code or chart rules that need cluster-scoped rights. `backend.rbac.scope=cluster` is only for stack charts that need cluster-scoped objects.
- Test chart and Role-Based Access Control (RBAC) changes with the Helm version of the backend image (`HELM_VERSION` in `backend/Dockerfile`), not with the local Helm.

## Cross-Origin Resource Sharing (CORS)

The CORS middleware is in `internal/api/middleware/middleware.go`. The default of `CORS_ALLOWED_ORIGINS` in `config.go` is `*`. `docker-compose.yml` sets `http://localhost:3000`. In production, set `CORS_ALLOWED_ORIGINS` to the exact domains. Never use `Access-Control-Allow-Origin: *` in production.

## Credentials

- All secrets come from environment variables, loaded by `config.LoadConfig()` with a `.env` fallback (godotenv).
- Never hardcode credentials. `docker-compose.yml` has `${VAR:-default}` defaults for local development only.
- Never log hook secrets, provider tokens, or kubeconfig data.

## SQL injection

GORM parameterizes queries. In raw SQL (including migrations), never put user input in the query string. Use parameters. Filter field names on the generic repository come from the `allowedFilterFields` whitelist.

## Deletes

`Base.DeletedAt` is a plain `*time.Time`, not `gorm.DeletedAt`, so GORM deletes rows for real. There is no automatic soft delete. If you add a soft delete, make it explicit and document why.

## Rate limiting

`RateLimiter` (`handlers/rate_limiter.go`) limits requests for each IP with a sliding window. `SetupRoutes` applies it to the `/api/v1` group (`RATE_LIMIT`, default 100 per minute). It adds a stricter login limiter (`LOGIN_RATE_LIMIT`, default 10 per minute):

```go
rateLimiter := handlers.NewRateLimiter(int(cfg.Server.RateLimit), time.Minute)
v1.Use(rateLimiter.RateLimit())
```

## Sessions, tokens, and headers

- Logout adds the access token `jti` to the blocklist in `sessionstore` (`BlockToken`). It revokes the refresh token through `RefreshTokenRepository`. Reuse of a rotated refresh token revokes the whole session family.
- JSON Web Token (JWT) auth checks `IsTokenBlocked` and `IsUserBlocked`. It fails open on store errors (log and continue). API key auth does not use the blocklist.
- Every auth path (login, refresh, OpenID Connect (OIDC), and API key) and the `/ws` upgrade reject `User.Disabled`.
- `middleware.SecurityHeaders()` sets baseline security headers. `middleware.RedactWSToken()` removes `?token=` from `/ws` URLs before logging, metrics, and tracing. Keep both.
- Outbound hooks carry a hash-based message authentication code (HMAC) signature in `X-StackManager-Signature` when the subscription or action sets `secret_env` (`internal/hooks/client.go`). Omitting `secret_env` is the only unsigned mode. A `secret_env` that resolves to an empty value is rejected at config load (fail closed). Hooks do not follow redirects.
- Kubeconfig data is encrypted at rest with AES-GCM (`pkg/crypto`, `KUBECONFIG_ENCRYPTION_KEY`).

## Recovery middleware

`Recovery()` (`internal/api/middleware/middleware.go`) catches panics and returns a generic 500. `routes.go` applies it globally. Never remove it.

## Graceful shutdown

`api/main.go` handles `SIGINT` and `SIGTERM` with a shutdown context (`SERVER_SHUTDOWN_TIMEOUT`, default 30s). Each new goroutine or worker must respect the shutdown signal.
