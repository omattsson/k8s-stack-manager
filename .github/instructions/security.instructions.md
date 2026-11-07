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

The CORS middleware is in `internal/api/middleware/middleware.go`. `CORS_ALLOWED_ORIGINS` is `*` (all origins; code default, development only), `same-origin` (no cross-origin requests; Helm chart default), or a comma-separated list of origins. With `same-origin` or a list, a request whose `Origin` host equals its `Host` header (the web UI behind the ingress) always passes. Another `Origin` gets 403. The WebSocket upgrade (`/ws`) uses the same rules (`middleware.OriginPolicy`). `*` or `same-origin` mixed with other entries stops the startup. The backend logs a warning for `*` when `GO_ENV=production`. `docker-compose.yml` sets `http://localhost:3000`. In production, keep `same-origin` or set the exact domains. Never use `Access-Control-Allow-Origin: *` in production.

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

The client IP is `c.ClientIP()`: the TCP peer, or the client named in `X-Forwarded-For` or `X-Real-IP` by a peer in `TRUSTED_PROXIES` (`config.ParseTrustedProxies`; an invalid entry stops the start). `0.0.0.0/0`, `::/0` and IPv4-mapped IPv6 entries are refused. Never trust all proxies: a client could pick a new IP for each request and skip the limiters. Never read the forwarded headers directly.

## Sessions, tokens, and headers

- Logout revokes the refresh token family (`RefreshTokenRepository`), blocks the session `sid` (`BlockSessions`) and adds the access token `jti` to the blocklist in `sessionstore`. Reuse of a rotated refresh token revokes the whole family and blocks the session. Logout-all also writes a user block. Details: `docs/claude/auth-sessions.md`.
- JSON Web Token (JWT) auth checks the token, session and user blocklists (`middleware.RevocationStatus`). It fails open on store errors (log and continue); keep it that way unless the docs change. API key auth does not use the blocklist; revoking a key closes its sockets with `DisconnectAPIKey`.
- Every auth path (login, refresh, OpenID Connect (OIDC), and API key) and the `/ws` upgrade reject `User.Disabled`.
- `middleware.SecurityHeaders()` sets baseline security headers. `middleware.RedactWSToken()` removes `?token=` from `/ws` URLs and the stream routes before logging, metrics, and tracing. Keep it first after RequestID. Keep both.
- Outbound hooks carry a hash-based message authentication code (HMAC) signature in `X-StackManager-Signature` when the subscription or action sets `secret_env` (`internal/hooks/client.go`). Omitting `secret_env` is the only unsigned mode. A `secret_env` that resolves to an empty value is rejected at config load (fail closed). Hooks do not follow redirects.
- Kubeconfig data, registry passwords, notification channel secrets and notification channel webhook URLs are encrypted at rest with AES-GCM (`pkg/crypto`, `KUBECONFIG_ENCRYPTION_KEY`). Without the key, the backend refuses `kubeconfig_data`, `registry_password` and a non-empty channel `secret` (400). It stores a channel `webhook_url` in plaintext.

## Stream routes (port-forward and exec)

`GET /api/v1/stack-instances/:id/port-forward` and `GET /api/v1/stack-instances/:id/exec` tunnel bytes into a pod. The kubelet connects inside the pod network, so port-forward bypasses NetworkPolicies, and exec shows the container environment and its secrets.

- Both are off by default: `STACK_PORT_FORWARD_ENABLED` and the cluster `allow_port_forward`; `STACK_EXEC_ENABLED`, the cluster `allow_exec` and a container name in `STACK_EXEC_ALLOWED_CONTAINERS`.
- Header auth only, never `?token=`. API keys are refused unless `STACK_STREAM_ALLOW_API_KEYS=true`. The caller is the owner, `devops` or `admin`.
- Refuse a namespace without `managed-by=k8s-stack-manager`, and the backend namespace.
- Exec takes the pod from the release pods in the instance namespace, never from the request. It never wraps the command in a shell. It refuses a container that holds a service-account token unless `STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN=true`. The start audit entry is written before the command and fails closed.
- Build executors only from the cluster `rest.Config` with the stock client-go constructors. A hand-built upgrade transport without a TLS config skips the certificate check.
- The hub revalidation closes open streams when the access, the role or the cluster switch changes. A new stream kind needs its own switch in the access checker.
- Audit entries never hold payload bytes.

## Contexts and deadlines

- A write whose side effects follow the commit uses a detached context with a deadline (`detachedCtx(c)`, `writeCtx(c)`, `ctxutil.Detach`), so a client that leaves cannot skip them and a stalled database cannot hold a request forever.
- Never use a bare `context.WithoutCancel` (`ctxutil.TestNoUnboundedDetach` fails).

## Recovery middleware

`Recovery()` (`internal/api/middleware/middleware.go`) catches panics and returns a generic 500. `routes.go` applies it globally. Never remove it.

## Graceful shutdown

`api/main.go` handles `SIGINT` and `SIGTERM` with a shutdown context (`SERVER_SHUTDOWN_TIMEOUT`, default 30s). Each new goroutine or worker must respect the shutdown signal.
