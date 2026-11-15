# K8s Stack Manager

Developers use this application to configure, store and deploy multi-service Helm-based application stacks to shared Kubernetes clusters.

## Architecture Overview

- Backend: Go (Gin). Frontend: React (TypeScript, Vite, MUI). Data store: MySQL (GORM). Docker Compose runs all services.
- Versions: Go 1.27.4 (`backend/go.mod`; CI reads the version from `backend/go.mod`, and the images build with `golang:1.27.4`), Node 22.12 or later (Vitest needs `^22.12 || ^24 || >=26`; the images use `node:26-alpine`).
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
| Security rules: errors, secrets, CORS, rate limits, middleware, revocation, encryption at rest, port-forward, exec, Helm identity | `docs/claude/security.md` |

Directory rules: `backend/CLAUDE.md`, `frontend/CLAUDE.md`, `backend/internal/api/handlers/CLAUDE.md`.

## Key Rules

Details: `backend/CLAUDE.md`, `frontend/CLAUDE.md`, `docs/claude/operations.md`, `docs/claude/security.md`.

- List endpoints: a new or changed list uses `ListPaged(..., limit, offset)` with `page` and `pageSize` (default 25, maximum 100). Use batch queries (`CountByTemplateIDs`, `FindByIDs`), not N+1 loops.
- Errors: map repository errors with `mapError()` (`handlers/errors.go`). A 500 returns `"Internal server error"`, never the raw error.
- Context: a repository read uses the request context. A write whose side effects follow the commit uses `detachedCtx(c)`, `writeCtx(c)` or `ctxutil.Detach`. Never use a bare `context.WithoutCancel`.
- Routes: register a handler in `SetupRoutes()` inside `if deps.XHandler != nil`. Role gates: `RequireAdmin()`, `RequireDevOps()`.
- Telemetry: add tracing for a new outbound call in a `tracing.go` next to the package. Never wrap the default slog handler in a new default: it deadlocks.
- Audit: every POST, PUT and DELETE writes an AuditLog entry. Non-CRUD operations need an `auditRoutes` entry. Rules: `docs/claude/domain.md` (Audit log).
- Security: never hard-code a secret, never interpolate user input into SQL, never remove the `SecurityHeaders`, Recovery or `RedactWSToken` middleware.
- Tests: backend uses `testify/assert`, table-driven tests and `t.Parallel()`. Frontend uses Vitest and Playwright.

## Project Plan

`ROADMAP.md` tracks upcoming work. `ARCHITECTURE.md` describes the system. `EXTENDING.md` and `backend/docs/hooks.md` describe the hooks and actions extension contract. `WIKI.md` covers user-facing concepts. `docs/getting-started.md` is the end-user walkthrough. `RELEASING.md` describes the release steps and the Release Drafter notes.
