---
name: pre-pr
description: Run the pre-push checks of this repository on the current branch (format, lint, unit tests, gosec, generated files, Helm) and report what fails. Use before a push or a pull request. It never pushes or fixes code on its own.
disable-model-invocation: true
---

# Pre-PR checks

Run the checks that CI runs, so a push does not fail on something found locally. Report the result. Do not push, do not open a pull request, and do not fix failures unless the user asks.

## 1. Scope

1. `git fetch origin main`, then `git diff origin/main...HEAD --name-only`. Run only the checks that match the changed paths:
   - `backend/` changed: backend checks.
   - `frontend/` changed: frontend checks.
   - `helm/` changed: Helm checks.
   - `backend/docs/` or a handler annotation changed: the Swagger check.
   - `.trivyignore` changed: `scripts/check-trivyignore.sh`.
2. A change to shared code (`routes.go`, `middleware/`, `models/`, `database/migrations.go`, `api/bootstrap.go`, `frontend/src/api/client.ts`) always runs both test suites.

## 2. Checks

Run these in order. Put the output of a long run in a file (`> "$TMPDIR/check.log" 2>&1`) and read only the failures.

| Area | Command |
|---|---|
| Format | `make fmt`, then `git status --short`: report files that `make fmt` changed |
| Lint | `make lint` |
| Backend tests | `cd backend && go test ./... -short` |
| Frontend tests | `cd frontend && npm test` |
| Security scan | `make gosec` (backend changes) |
| Swagger drift | `make docs`, then `git diff --exit-code backend/docs` |
| Values schema drift | `make helm-schema`, then `git diff --exit-code helm/k8s-stack-manager/values.schema.json` (when `values.yaml` changed) |
| Helm | `make helm-lint` and `make helm-test` (chart changes) |

Audit entries are covered by the routes test in the backend suite: it fails when a mutating route has no known action and entity type. When it fails, point to `auditRoutes` in `backend/internal/api/middleware/audit.go`.

## 3. Report

One line per check: pass, fail or skipped (with the reason). For each failure give the file, line and error message, and the command that reproduces it. Then say plainly whether the branch is ready to push.

Also check the pull request rules:

- The branch is not `main`.
- The commit titles and the future pull request title follow the conventional commit format (`feat(scope): ...`, `fix(ui): ...`). Release Drafter groups the release notes by this type (see `RELEASING.md`).
