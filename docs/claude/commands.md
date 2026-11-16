# Development Commands

Reference for Claude Code and other agents. The root `CLAUDE.md` lists the common commands and links here for the rest.

| Task | Command |
|---|---|
| Full stack (Docker) | `make dev` |
| Full stack with local Kubernetes cluster access (Rancher Desktop, Docker Desktop Kubernetes) | `make dev-k8s` |
| Full stack with the OpenTelemetry stack (collector, Prometheus, Tempo, Grafana) | `make dev-otel` |
| Backend and MySQL only (for `stackctl` workflows) | `make dev-api-only` |
| Backend and frontend on the host with hot reload (Go `air`, Vite HMR) | `make dev-local` |
| Seed sample data into a running dev stack | `make seed` |
| Backend tests (unit, no database) | `cd backend && go test ./... -short` (add `-v` for one package) |
| All backend tests (unit and integration) | `make test-backend-all` (starts MySQL) |
| Changed backend packages with `-race -count=3` (order-dependent tests) | `make test-backend-repeat` (`REPEAT_BASE`, default `origin/main`; `REPEAT_COUNT`) |
| Frontend tests | `cd frontend && npm test` |
| Unit tests (backend and frontend) | `make test` |
| End-to-end tests | `make test-e2e` (starts infrastructure, backend and Playwright; no cluster access unless `E2E_KUBECONFIG` names a local kubeconfig; starts the test stub `frontend/e2e/stub/`: the subscriber of the actions in `frontend/e2e/fixtures/hooks-e2e.json` on http://127.0.0.1:18090, and an HTTPS webhook receiver on port 18443, which the backend trusts on Linux only; `E2E_SPECS=e2e/x.spec.ts` runs only these specs and skips the isolated phase; `E2E_SHARD=2/4` runs one part of the main suite and skips the isolated phase; then restarts the backend with short session limits and runs the `isolated` project; CI: `.github/workflows/e2e.yml`, 4 shard jobs and one `isolated` job in parallel) |
| Isolated e2e tests only | `make test-e2e-isolated` (project `isolated`, one worker; backend with `E2E_SESSION_*` limits: access token 10s, idle 20s, maximum lifetime 60s, reuse grace 3s) |
| Swagger docs | `make docs` (runs `swag init -g api/main.go` in `backend`) |
| Coverage (80% threshold) | `cd backend && make test-coverage` |
| Format | `make fmt` |
| Lint | `make lint` (`go vet` and `npm run lint`) |
| Security scan (backend) | `make gosec` (`scripts/gosec.sh`: pinned gosec built with the local Go, run inside `backend/`, as in CI) |
| Install dependencies | `make install` |
| Helm lint | `make helm-lint` |
| Helm dry-run render | `make helm-template` |
| Helm render checks (default values, secrets, External Secrets, Traefik priority, Grafana dashboard ConfigMap, values schema current) | `make helm-test` |
| Helm values schema (after a change of `values.yaml`; descriptions come from its comments, extra rules in `backend/tools/helmschema`) | `make helm-schema` |
| Helm install, upgrade, uninstall | `make helm-install`, `make helm-upgrade`, `make helm-uninstall` |
| Helm package for release | `make helm-release` |
| Load tests | `make loadtest` (see the `loadtest-*` targets; `LOADTEST_PORT`, `LOADTEST_MYSQL=external`; memory and CPU: `make loadtest-capture` or `LOADTEST_CAPTURE=1`, see `loadtest/README.md`) |
