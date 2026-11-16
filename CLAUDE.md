# K8s Stack Manager

Developers use this application to configure, store and deploy multi-service Helm-based application stacks to shared Kubernetes clusters.

## Architecture Overview

- Backend: Go (Gin). Frontend: React (TypeScript, Vite, MUI). Data store: MySQL (GORM). Docker Compose runs all services.
- Versions: Go 1.27.3 (`backend/go.mod`; CI reads the version from `backend/go.mod`, and the images build with `golang:1.27.3`), Node 22.12 or later (Vitest needs `^22.12 || ^24 || >=26`; the images use `node:26-alpine`).
- Ports: backend `:8081` (`SERVER_PORT`), frontend `:3000` in dev. The backend registers routes under `/api/v1`. nginx and the Vite dev proxy keep the `/api` prefix.
- Bootstrap order, graceful shutdown, Gin mode, optional servers, middleware order, telemetry and WebSocket messages: `docs/claude/backend-runtime.md`.

## Development Commands

| Task | Command |
|---|---|
| Full stack (Docker) | `make dev` |
| Backend and frontend on the host with hot reload | `make dev-local` |
| Backend tests (unit, no database) | `cd backend && go test ./... -short` (add `-v` for one package) |
| All backend tests (unit and integration) | `make test-backend-all` (starts MySQL) |
| Frontend tests | `cd frontend && npm test` |
| End-to-end tests | `make test-e2e` (`E2E_SPECS=e2e/x.spec.ts` runs only these specs) |
| Swagger docs | `make docs` |
| Format, lint | `make fmt`, `make lint` |
| Security scan (backend) | `make gosec` |
| Helm render checks, values schema | `make helm-test`, `make helm-schema` (after a change of `values.yaml`) |

All other commands (dev variants, seed, coverage, repeat runs, Helm install and release, load tests, e2e details): `docs/claude/commands.md`.

## Reference Docs

These files do not load on every request. Read the matching file before you work in its area.

| Area | File |
|---|---|
| Domain rules: instances, TTL, deploy and Helm values, rollback, quotas, access control, templates, definitions, clusters, cleanup policies, notifications, dashboard, branding, port-forward, exec, audit log, API route groups | `docs/claude/domain.md` |
| Sessions, refresh tokens, login, WebSocket auth and revocation | `docs/claude/auth-sessions.md` |
| Lifecycle hooks and custom actions | `docs/claude/hooks-actions.md` |
| Helm chart: structure, design decisions, values | `docs/claude/helm-chart.md` |
| Backend and frontend file trees | `docs/claude/structure.md` |
| Scalability, replicas, interrupted operation recovery, log retention | `docs/claude/operations.md` |
| Bootstrap, shutdown, ports, middleware order, client IP, telemetry, WebSocket messages | `docs/claude/backend-runtime.md` |
| All development commands | `docs/claude/commands.md` |
| A new entity, repository or handler: steps, handler struct, optimistic locking, filter whitelist | `docs/claude/new-resource.md` |

Directory rules: `backend/CLAUDE.md`, `frontend/CLAUDE.md`, `backend/internal/api/handlers/CLAUDE.md`.

## Key Backend Patterns

**Repository interface.**
- The generic `models.Repository` interface has `Create`, `FindByID`, `Update`, `Delete`, `List`. All take `context.Context` first. Domain repositories (for example `StackInstanceRepository`) have their own interfaces in their model files.
- `GenericRepository` calls `Validate()` on create and update when the model implements `Validator`. Domain repositories call it where they choose; check the repository before you rely on it.
- Paginated lists use `ListPaged(..., limit, offset)`, which returns `([]T, total, error)`, with a filter struct (`StackInstanceFilter`, `StackDefinitionFilter`) that the count query shares. New or changed list endpoints must use this pattern. Small admin lists (users, clusters, cleanup policies, favorites, API keys) still return unpaged `List()` results.
- Batch methods (`CountByTemplateIDs`, `FindByIDs`, `NamesByIDs`) remove N+1 queries. Computed response fields are model fields with `gorm:"-"`.

**Error flow.**
- A repository returns `*dberrors.DatabaseError` that wraps a sentinel error. `mapError()` in `handlers/errors.go` (domain handlers) and `handleDBError()` in `handlers/items.go` (Items reference) map it: 400 validation, 404 not found, 409 duplicate or conflict, 501 not implemented, 500 internal.
- A cancelled request context (`context.Canceled`) gives 499 and a debug log, not 500. `context.DeadlineExceeded` stays 500.
- Context rule: a repository read on the request path uses the request context (`c.Request.Context()`). A write whose side effects follow the commit uses a detached context, so a client that leaves cannot skip them: `detachedCtx(c)` (`writeCtx(c)` in instance handlers, `ctxutil.Detach(ctx, timeout)` elsewhere). Each one has a deadline (`ctxutil.WriteTimeout`, 30 s) and returns a cancel function that the caller must call, so a stalled database cannot hold a request forever (#674); a timeout gives 500. Never use a bare `context.WithoutCancel` anywhere in the backend: background jobs detach with `ctxutil.Detach` too (`ctxutil.TestNoUnboundedDetach` fails). A post-commit hook (`InstanceHandler.firePostHook`) detaches again with `WriteTimeout` plus the subscriber timeouts, so the write deadline never cuts a hook short. Never keep the request context after the handler returns.

**Routes registration.**
- `SetupRoutes()` takes a `Deps` struct. Handlers are optional. Register each one inside an `if deps.XHandler != nil` block.
- It returns `*RateLimiters`. The caller must call `Stop()` on shutdown.
- Role gates use `RequireAdmin()` and `RequireDevOps()`.

**Telemetry.** Add tracing for new outbound calls in a `tracing.go` next to the package (see `cluster/`, `deployer/`, `gitprovider/`, `hooks/`). Never wrap the package default slog handler in a new default: it deadlocks (`docs/claude/backend-runtime.md`).

## Frontend

**Stack:** React 19, MUI v9, react-router-dom v7, Vite 8, TypeScript 6, Vitest 5, Playwright (`frontend/e2e/`).

**Patterns.**
- Use MUI components (no raw HTML) and the `sx` prop for styling.
- Use functional components only, and `useState` and `useEffect` for state.
- Use service objects with async methods for API calls.
- Each service object and method in `api/client.ts` needs a TSDoc comment with `@param`, `@returns` and `@see` (HTTP method and route).

## Testing Conventions

**Backend.**
- Use `testify/assert`. Write table-driven tests (`tests := []struct{...}` with `t.Run`).
- Call `t.Parallel()` in the parent test and in the subtests. Capture the loop variable: `tt := tt`.
- Use `MockRepository` (in-memory, Item type only) or the domain mocks in `mock_domain_repositories_test.go`.
- Test setup: `setupTestRouter()` returns `(*gin.Engine, *MockRepository)`.
- Validate JSON responses against the schemas in `test_schemas.go` with `gojsonschema`.

**Frontend.** Vitest and Testing Library (unit), Playwright (e2e, `frontend/e2e/*.spec.ts`).

**Integration test names:** `TestDatabase*` (MySQL).

## Audit Trail

- Every mutating API endpoint (POST, PUT, DELETE) must create an AuditLog entry. `middleware.NewAuditMiddleware` writes it.
- A handler calls `middleware.SkipAudit(c)` for a successful request that changed nothing (a no-op), so no entry is written.
- An operation that is not plain CRUD (deploy, stop, publish, test, bulk, ...) needs an entry in the route table `auditRoutes` (`middleware/audit.go`: action, entity type, ID parameter, details). Without an entry, the action comes from the HTTP method and the entity type from the last route segment.
- Add new actions and entity types to `KnownAuditActions` and `KnownAuditEntityTypes`, and to the filter lists of `pages/AuditLog`.
- `routes/audit_routes_test.go` fails for a mutating route with an unknown entity type.
- Each entry carries the request context and the entity name; a delete handler calls `middleware.SetAuditEntityName`, an update handler calls `middleware.SetAuditChanges` with `auditlog.Fields` (values only for an allow-list; secret fields `auditlog.Redacted`). Code that writes an entry after the handler returned uses `middleware.CaptureRequestContext`. Sign-in events and `AUDIT_LOG_STDOUT`: `docs/claude/domain.md` (Audit log).

## Security Rules

- Never expose a raw error message for a 500. Return `"Internal server error"`.
- Set all secrets through environment variables. Never hard-code credentials.
- GORM parameterizes queries. Never interpolate user input into raw SQL.
- CORS allows `*` in dev only (code default; a startup warning when `GO_ENV=production`). The Helm chart default is `same-origin`: no cross-origin requests, the web UI on the same host passes. Set a list of origins only for other browser clients.
- Rate limiting: a per-IP sliding window on API routes, and a stricter login limiter (`LOGIN_RATE_LIMIT`). The IP is the TCP peer, or the client behind a proxy of `TRUSTED_PROXIES`. Never trust all proxies: a client could then pick a new IP for each request.
- The `SecurityHeaders` middleware sets baseline headers on every response. Never remove it.
- The Recovery middleware catches panics globally. Never remove it.
- `sessionstore` holds the revoked access-token `jti` values, the revoked sessions (`sid`) and the blocked users. `RefreshTokenRepository` holds the revoked refresh tokens. JWT auth and the `/ws` upgrade check the blocklist. Every auth path (login, refresh, OIDC, API key) rejects `User.Disabled`.
- `RedactWSToken` removes `?token=` from `/ws` URLs and from the stream routes before logging, metrics and tracing. Keep it first after RequestID.
- Outbound hooks carry an HMAC signature (`X-StackManager-Signature`) when a subscription has a secret. A subscription without a secret is sent unsigned. Never log hook secrets or provider tokens.
- Kubeconfig data, registry passwords, notification channel secrets and notification channel webhook URLs are encrypted at rest with AES-GCM (`KUBECONFIG_ENCRYPTION_KEY`). Without the key, the backend refuses `kubeconfig_data`, `registry_password` and a non-empty channel `secret` (400). It stores a channel `webhook_url` in plaintext, because the URL is required and the Helm default key is empty.
- Port-forward bypasses NetworkPolicies and the ingress auth of tools. Keep `allow_port_forward` off on clusters with real data. Stream routes take header auth only and refuse API keys unless `STACK_STREAM_ALLOW_API_KEYS=true`. Rules: `docs/claude/domain.md` (Port-forward).
- Exec shows the container environment and its secrets. The pod comes only from a list of the release pods, never from the request. Never wrap the command in a shell. Build the executors only from the cluster `rest.Config` with the stock client-go constructors (a hand-built upgrade transport without a TLS config skips the certificate check). Rules: `docs/claude/domain.md` (Exec).
- Helm runs with the backend identity. On shared clusters: keep `ALLOWED_CHART_REPOS` set, keep `backend.rbac.scope: namespace` and its admission policy, and never give a role `user` path a way to change a chart source.

## Scalability Rules

Details: `docs/claude/operations.md`.

- Paginate every new list endpoint: `page` and `pageSize` (default 25, maximum 100) with `ListPaged(..., limit, offset)`. Select only the columns that the list needs. Give the query an index that covers its filter and its order. Use batch queries (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops.
- The notification counts are capped (#654): `total` is exact up to offset + 1000, then `total_capped: true`; `unread_count` is exact up to 1000, then `unread_count_capped: true`.
- Add a periodic job as a `leader.Worker` with a restartable `Run(ctx)`. Keep state that must survive a leader change in the database.
- Send WebSocket messages through `Broadcast`, `BroadcastToInstance` or `BroadcastToUser`. Revoke through `DisconnectUser`, `DisconnectToken` or `DisconnectAPIKey`. Fan-out reaches all replicas.
- Register every external dependency with `healthChecker.AddCheck()`.
- A new log table that grows needs a retention in `internal/retention`.
- Use `internal/cache` for short-lived in-memory caching instead of ad-hoc maps.
- Struct field order: put 8-byte fields first for memory alignment.

## Project Plan

`ROADMAP.md` tracks upcoming work. `ARCHITECTURE.md` describes the system. `EXTENDING.md` and `backend/docs/hooks.md` describe the hooks and actions extension contract. `WIKI.md` covers user-facing concepts. `docs/getting-started.md` is the end-user walkthrough. `RELEASING.md` describes the release steps and the Release Drafter notes.
