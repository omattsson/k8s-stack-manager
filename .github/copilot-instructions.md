# Copilot Review Guide

K8s Stack Manager: developers configure, store and deploy multi-service Helm stacks to shared Kubernetes clusters.

- Backend: Go (Gin), GORM on MySQL, in `backend/`. Frontend: React 19, TypeScript, Vite, MUI v9, in `frontend/`. Helm chart in `helm/`.
- Several backend replicas can run at once. One leader replica runs the background workers. Every replica serves HTTP and WebSocket.

This file holds the rules to check in a review. The full rules are in `CLAUDE.md`, `backend/CLAUDE.md`, `frontend/CLAUDE.md`, `backend/internal/api/handlers/CLAUDE.md` and `docs/claude/*.md` (domain, auth and sessions, hooks, Helm chart, operations). Those files are the source of truth. When this guide and those files differ, follow those files and say that this guide is out of date.

Path-specific rules are in `.github/instructions/*.instructions.md` (security, scalability, Go, handlers, TypeScript, React pages, adding an API resource).

## What to flag first

Report these in this order. Skip style nits that `gofmt`, `go vet` and ESLint already catch.

1. Security: a raw error message in a 500, a missing role or ownership check, user input in raw SQL, a secret in a log, a weakened auth, CORS, header or rate-limit rule. See `security.instructions.md`.
2. Correctness: races, a goroutine that ignores shutdown, a lost error, a wrong HTTP status, a context used after the handler returns, a write without a deadline.
3. A mutating route without an audit entry, or a no-op that still writes one.
4. Scalability: an unpaged new list, an N+1 loop, a query without an index, in-memory state that must survive a leader change or reach other replicas.
5. Tests: new behavior without a test, a test that cannot fail, a mock that hides the bug under test.
6. Docs: a changed rule, route, env var or Helm value without an update of `CLAUDE.md`, `docs/claude/*`, Swagger or `values.yaml` and its schema.

Name the file and line, say what breaks and when, and propose a fix. Do not repeat a comment that an earlier review round already made and the author answered.

## Backend rules

**Layers.**
- Domain models live in `internal/models/<entity>.go`, with their repository interface in the same file. They have a string UUID `ID` (`gorm:"primaryKey;size:36"`) and their own timestamps. Only the `Item` reference model embeds `Base`, has a numeric ID and a `Version` column.
- Deletes are hard deletes. `Base.DeletedAt` is a plain field, not `gorm.DeletedAt`.
- Repository methods take `context.Context` first and return `*dberrors.DatabaseError` that wraps a sentinel error (`ErrNotFound`, `ErrDuplicateKey`, `ErrValidation`, ...).
- Domain handlers are dedicated structs (`InstanceHandler`, `DefinitionHandler`, ...) with constructor injection. The generic `Handler` is only for `Item`.
- A new migration goes in `internal/database/migrations.go` with the next version string. `Up` and `Down` are idempotent.
- A new route is registered under the authenticated `/api/v1` group inside `if deps.XHandler != nil`, and wired in `api/bootstrap.go` (`buildHandlers`, `buildRouter`).
- Struct fields: 8-byte fields first.

**Errors.**
- Domain handlers map errors with `mapError(err, "Entity")` (`handlers/errors.go`). Only the Items handler uses `handleDBError()`.
- The mapping is 400 validation, 404 not found, 409 duplicate or conflict, 501 not implemented, 500 internal. A cancelled request context (`context.Canceled`) gives 499 and a debug log. `context.DeadlineExceeded` stays 500.
- A 500 always returns `{"error": "Internal server error"}`. Flag any `err.Error()` in a 500 response.
- Error responses are `gin.H{"error": "message"}`. Log with `slog` and context fields.

**Contexts.** This rule causes real bugs; check it on every handler and worker change.
- A repository read on the request path uses `c.Request.Context()`.
- A write whose side effects follow the commit uses a detached context with a deadline: `detachedCtx(c)`, `writeCtx(c)` in instance handlers, or `ctxutil.Detach(ctx, timeout)` elsewhere. The caller must call the returned cancel function.
- Never use a bare `context.WithoutCancel` in the backend. `ctxutil.TestNoUnboundedDetach` fails on it. Background jobs detach with `ctxutil.Detach` too.
- Never keep the request context after the handler returns (goroutines, hooks, broadcasts).
- Final writes of deploy, stop, clean and rollback use `Manager.finalWriteCtx`, never `m.shutdownCtx`.

**Concurrent writes.** Domain models have no version column. Concurrent writers use targeted or conditional updates (`UpdateRole`, `SetDisabled`, compare-and-set on status). Flag a read-modify-write of a whole domain row that another request or worker can change at the same time.

**Audit trail.**
- Every successful POST, PUT and DELETE writes an audit entry through `middleware.NewAuditMiddleware`. Handlers do not write audit logs directly. Exceptions: `quick_deploy.go`, and the stream handlers (`instance_stream.go`, `instance_port_forward.go`, `instance_exec.go`): the middleware does not audit GET, so they write start and end entries themselves, and exec writes its start entry before the command (fail closed).
- A successful request that changed nothing calls `middleware.SkipAudit(c)`.
- An operation that is not plain CRUD (deploy, stop, publish, test, bulk, ...) needs an entry in `auditRoutes` (`middleware/audit.go`).
- New actions and entity types go in `KnownAuditActions` and `KnownAuditEntityTypes`, and in the filter lists of `frontend/src/pages/AuditLog`.

**Lists.**
- A new or changed list endpoint takes `page` and `pageSize` (default 25, maximum 100) through `listPagination(c)`, and calls `ListPaged(..., limit, offset)` with a filter struct and column projection. The count query uses the same filter.
- Small admin lists (users, clusters, cleanup policies, favorites, API keys) stay unpaged.
- Use batch lookups (`FindByIDs`, `NamesByIDs`, `CountByTemplateIDs`) instead of N+1 loops.
- Computed response fields are model fields with `gorm:"-"`.

**Background work and replicas.**
- A periodic job is a `leader.Worker` with a restartable `Run(ctx)`, built in `buildLeaderWorkers`. Not a goroutine in `main.go`. No closed stop channels or one-shot state. Exception: work that every replica must do for itself, such as the replica heartbeat (`replica.Heartbeat`, which interrupted-operation recovery relies on) and the hub revalidation of its own sockets (`hub.StartRevalidation`); `main` starts these on every replica and stops them on shutdown.
- State that must survive a leader change lives in the database.
- WebSocket sends go through `Broadcast`, `BroadcastToInstance` or `BroadcastToUser`. Revocations go through `DisconnectUser`, `DisconnectToken` or `DisconnectAPIKey`. Never write `ws_events` directly. Per-user messages use `BroadcastToUser`, not `Broadcast`.
- Every new goroutine respects the shutdown signal.

**Swagger.** Every handler has the annotations `@Summary`, `@Description`, `@Tags`, `@Accept`, `@Produce`, `@Param`, `@Success`, `@Failure`, `@Router`. A changed annotation needs the regenerated `backend/docs/` files (`make docs`).

**Telemetry.** A new outbound call gets tracing in a `tracing.go` next to its package. Never wrap the package default slog handler in a new default (it deadlocks).

## Domain rules that are easy to break

**Access control.** Roles are `user`, `devops`, `admin`. The backend is the authority; the UI only hides controls.
- Instance deploy, stop, clean, delete, rollback, overrides, TTL and custom actions: owner, `devops` or `admin` (`requireInstanceModify`, before any side effect).
- Bulk routes (`/stack-instances/bulk/*`: deploy, stop, clean, delete; `/templates/bulk/*`: delete, publish, unpublish) need `RequireDevOps` on the route group; a plain owner never gets them. Template bulk also checks each template: owner or admin. Flag a bulk route outside the `devops` group.
- Definition create, import, export and add or remove chart: `RequireDevOps`. Update, delete, upgrade and chart update: owner, `devops` or `admin` (`requireDefinitionModify`).
- A `user` may change chart default values and deploy order, but not the chart name, repository, path or version (`chartSourceChanged`, 403).
- Template edit, delete, publish, unpublish and chart changes: `RequireDevOps` and the template owner or an admin. Instantiate and quick deploy are open to every authenticated user.
- A `user` cannot set an instance quota override above the cluster quota (403).
- A role change, disable, delete or password reset revokes the user's sessions. The last enabled admin cannot be demoted, disabled or deleted.
- Bulk operations accept at most 50 items per request and return a result for each item.

**Charts and Helm.**
- `models.CheckChartSource` checks each chart source against `ALLOWED_CHART_REPOS`. Every new path that stores or deploys a chart config must call it. Never give a role `user` path a way to change a chart source.
- Helm runs with the backend identity. Flag code or chart rules that need cluster-scoped rights.
- Template locked values always win in the values merge.

**Namespaces.**
- `k8s.Client.EnsureNamespace` sets `managed-by=k8s-stack-manager` and `k8s-stack-manager.io/instance-name`. The keys are reserved in `NAMESPACE_LABELS`.
- When the namespace is ready, Helm gets no `--create-namespace` (`NamespaceReady`). The admission policy refuses a namespace create without the label.
- Instance names are RFC 1123 labels of at most 50 characters (`models.ValidateInstanceName`) on create, rename, clone and quick deploy.

**Stream routes (port-forward and exec).**
- Both are off by default and need a global switch and a per-cluster switch (`allow_port_forward`, `allow_exec`). Exec also needs the container name in `STACK_EXEC_ALLOWED_CONTAINERS`.
- Header auth only (no `?token=`). API keys are refused unless `STACK_STREAM_ALLOW_API_KEYS=true`. A namespace without `managed-by=k8s-stack-manager`, and the backend namespace, are refused.
- Exec picks the pod from the release pods in the instance namespace, never from the request. It never wraps the command in a shell. It refuses a container with a service-account token unless `STACK_EXEC_ALLOW_SERVICE_ACCOUNT_TOKEN=true`. The start audit entry is written before the command runs and fails closed.
- Executors are built only from the cluster `rest.Config` with the stock client-go constructors.
- Audit entries never hold payload bytes (port-forward: service, port and pod).
- After a hijack, clear the connection deadline and use the session deadlines only.

**Encryption at rest.** Kubeconfig data, registry passwords and notification channel secrets are encrypted with AES-GCM (`KUBECONFIG_ENCRYPTION_KEY`). Without the key, the backend refuses `kubeconfig_data`, `registry_password` and a non-empty channel `secret` (400). Never return these values in a response or a log. Channel webhook URLs are encrypted when the key is set and stored in plaintext without it (by design: the URL is required and the Helm default key is empty). The channel API returns `webhook_url` to its DevOps and admin users for the editor; error texts, delivery logs and log lines never contain it.

**Hooks.** Organization-specific behavior belongs in hook subscribers, not in the core. Outbound hooks are HMAC-signed when `secret_env` is set and do not follow redirects. Never log hook secrets or provider tokens.

**Notifications.** The list `total` is exact up to offset + 1000, then `total_capped: true`. `unread_count` is exact up to 1000, then `unread_count_capped: true`. A new log table that grows needs a retention in `internal/retention`.

## Backend tests

- `testify/assert`, table-driven tests (`tests := []struct{...}` with `t.Run`).
- `t.Parallel()` in the parent test and in each subtest; `tt := tt` before `t.Run`.
- Domain handler mocks are in `handlers/mock_domain_repositories_test.go`. `MockRepository` is for `Item` only.
- Validate JSON responses with `gojsonschema` against `test_schemas.go`.
- Integration tests that need MySQL are named `TestDatabase*` and skip with `-short`.
- Flag a mock that returns success where the real repository would return an error, and a test that asserts only the status code of a security check.

## Frontend rules

- MUI components and the `sx` prop; no raw HTML elements, no CSS files.
- Functional components with `useState` and `useEffect`; no global state library.
- TypeScript interfaces for props and API types; no `any` without a reason.
- API calls go through service objects in `src/api/client.ts`. Each service object and method has TSDoc with `@param`, `@returns` and `@see` (HTTP method and route).
- Pages render loading (`CircularProgress`), then error (`Alert severity="error"`), then content. Page titles are `Typography variant="h4" component="h1"`.
- New pages are registered in `src/routes.tsx` and the navigation in `src/components/Layout/`.
- Hide or disable controls that the backend refuses for the role (`hasAtLeastRole`). The backend stays the authority.
- Accessibility: never show a state by colour alone; an accessible name contains the visible text in the same order; use theme tokens such as `action.hover` behind text, not a fixed `grey.*`; in `sx`, `width: 1` means 100%. A new page goes in `pageList` (`e2e/a11y.ts`), a new dialog in `accessibility-dialogs.spec.ts`.
- Tests: Vitest and Testing Library for units, Playwright in `frontend/e2e/` for end-to-end.

## Helm chart and infrastructure

- A change of `helm/k8s-stack-manager/values.yaml` needs the regenerated values schema (`make helm-schema`); `make helm-test` checks it.
- Keep `backend.rbac.scope: namespace` and its admission policy as the defaults. Keep CORS `same-origin` as the chart default.
- A new env var needs a default in `internal/config/config.go`, a Helm value where operators need it, and a line in the docs.
- Docker images pin exact versions (Go 1.27.3, `node:26-alpine`).
