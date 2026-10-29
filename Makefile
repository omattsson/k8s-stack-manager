.PHONY: loadtest-stackctl gosec e2e-prepare test-e2e-isolated dev dev-otel dev-api-only compose-api-only seed dev-backend dev-frontend dev-local dev-local-backend dev-local-frontend prod prod-backend prod-frontend build clean prune test test-backend test-backend-integration test-backend-all test-backend-repeat test-frontend test-e2e integration-infra-start integration-infra-stop install docs fmt lint loadtest loadtest-start loadtest-start-backend loadtest-start-frontend loadtest-stop loadtest-stop-backend loadtest-stop-frontend loadtest-backend loadtest-backend-run loadtest-stress loadtest-stress-run loadtest-frontend loadtest-frontend-run loadtest-capture loadtest-k8s-config loadtest-start-replica loadtest-stop-replica loadtest-users loadtest-users-run loadtest-mysql-stats-reset loadtest-mysql-stats loadtest-full loadtest-ramp

# Development mode for both services
dev:
	NODE_ENV=development GO_ENV=development PORT=3000 BACKEND_PORT=8081 GIN_MODE=debug docker compose --profile full up --build --remove-orphans

# Development mode with local K8s cluster access (rancher-desktop, Docker Desktop K8s, etc.)
dev-k8s:
	NODE_ENV=development GO_ENV=development PORT=3000 BACKEND_PORT=8081 GIN_MODE=debug docker compose --profile full -f docker-compose.yml -f docker-compose.k8s.yml up --build --remove-orphans

# Development mode with OpenTelemetry observability stack (Jaeger + Prometheus)
dev-otel: ## Start full stack with OpenTelemetry (Jaeger UI: :16686, Prometheus: :9090)
	NODE_ENV=development GO_ENV=development PORT=3000 BACKEND_PORT=8081 GIN_MODE=debug docker compose --profile full --profile otel -f docker-compose.yml -f docker-compose.otel.yml up --build --remove-orphans

# Headless / API-only mode (no frontend container) — pair with stackctl as the client.
# COMPOSE_PROFILES=api-only on the recipe line so an existing .env with
# COMPOSE_PROFILES=full cannot accidentally pull in the frontend.
dev-api-only: ## Start backend + mysql only, no frontend (for stackctl-driven workflows)
	COMPOSE_PROFILES=api-only GO_ENV=development BACKEND_PORT=8081 GIN_MODE=debug docker compose up --build --remove-orphans

# Same as dev-api-only without the dev-mode env vars — exact API-only baseline
compose-api-only: ## Start backend + mysql only with default env (matches Helm frontend.enabled=false)
	COMPOSE_PROFILES=api-only docker compose up --build --remove-orphans

seed: ## Seed dev environment with sample data (requires running dev stack)
	@./scripts/seed-dev-data.sh

# Development mode for backend only
dev-backend:
	NODE_ENV=development GO_ENV=development BACKEND_PORT=8081 GIN_MODE=debug docker compose up --build --remove-orphans backend

# Development mode for frontend only
dev-frontend:
	NODE_ENV=development PORT=3000 docker compose up --build frontend

# Production mode for both services
prod:
	NODE_ENV=production GO_ENV=production PORT=80 BACKEND_PORT=8080 GIN_MODE=release docker compose --profile full up --build --remove-orphans

# Production mode for backend only
prod-backend:
	NODE_ENV=production GO_ENV=production BACKEND_PORT=8080 GIN_MODE=release docker compose up --build backend

# Production mode for frontend only
prod-frontend:
	NODE_ENV=production PORT=80 docker compose up --build frontend

# Stop and remove all containers, networks, and volumes
clean:
	docker compose down -v

# Remove all unused Docker resources
prune:
	docker system prune -f

# Run all tests
test: test-backend test-frontend

# Run backend unit tests only (no external dependencies needed)
test-backend:
	cd backend && go test ./... -v

# Start infrastructure for integration tests (MySQL via Docker)
integration-infra-start:
	@echo "Starting MySQL..."
	@docker compose up -d mysql
	@echo "Waiting for MySQL to be ready..."
	@n=0; while ! docker compose exec -T mysql mysqladmin ping -h localhost -uroot -p"$${DB_PASSWORD:-rootpassword}" --silent 2>/dev/null; do \
		n=$$((n+1)); \
		if [ $$n -ge 90 ]; then echo "ERROR: MySQL failed to start after 90s"; exit 1; fi; \
		sleep 1; \
	done
	@echo "MySQL is ready on :3306"

# Stop infrastructure for integration tests
integration-infra-stop:
	@docker compose stop mysql
	@echo "Integration test infrastructure stopped."

# Run backend integration tests (starts/stops infra automatically)
test-backend-integration: integration-infra-start
	cd backend && go test -tags integration ./... -v
	$(MAKE) integration-infra-stop

# Run all backend tests (unit + integration)
test-backend-all: integration-infra-start
	cd backend && go test -tags integration ./... -v
	$(MAKE) integration-infra-stop

# Run the tests of the changed backend packages repeatedly (-race -count) to catch order-dependent tests.
# Changed = .go files vs the merge-base of REPEAT_BASE and HEAD (this includes uncommitted changes), plus untracked files.
# -race needs cgo (CGO_ENABLED=1 and a C toolchain).
# The variables are exported and read from the environment in the recipe, never expanded into the shell text.
REPEAT_BASE ?= origin/main
REPEAT_COUNT ?= 3
export REPEAT_BASE REPEAT_COUNT
test-backend-repeat:
	@case "$$REPEAT_COUNT" in ''|*[!0-9]*|0|0[0-9]*) echo "ERROR: REPEAT_COUNT must be a positive integer, got '$$REPEAT_COUNT'"; exit 1;; esac; \
	base=$$(git merge-base "$$REPEAT_BASE" HEAD) || { echo "ERROR: cannot find merge-base with REPEAT_BASE=$$REPEAT_BASE; fetch it or set REPEAT_BASE"; exit 1; }; \
	tracked=$$(git diff --name-only "$$base" -- backend) || { echo "ERROR: git diff failed"; exit 1; }; \
	untracked=$$(git ls-files --others --exclude-standard -- backend) || { echo "ERROR: git ls-files failed"; exit 1; }; \
	all=$$(printf '%s\n%s\n' "$$tracked" "$$untracked"); \
	files=$$(printf '%s\n' "$$all" | grep '\.go$$' | sort -u); \
	if printf '%s\n' "$$all" | grep -E '^backend/go\.(mod|sum)$$' >/dev/null; then \
		echo "NOTICE: go.mod or go.sum changed; dependency changes select no package. For a full check run: cd backend && go test -race -count=3 -short ./..."; \
	fi; \
	pkgs=""; \
	for f in $$files; do \
		[ -f "$$f" ] && grep -q '^//go:build' "$$f" && echo "NOTICE: $$f has a //go:build constraint; go test -short without tags may not compile it"; \
		d=$$(dirname "$${f#backend/}"); \
		[ -d "backend/$$d" ] || continue; \
		ls backend/$$d/*.go >/dev/null 2>&1 || continue; \
		[ "$$d" = "." ] || d="./$$d"; \
		pkgs="$$pkgs $$d"; \
	done; \
	pkgs=$$(printf '%s\n' $$pkgs | sort -u | tr '\n' ' '); \
	if [ -z "$$(echo $$pkgs)" ]; then echo "No changed backend Go packages vs $$REPEAT_BASE; nothing to repeat."; exit 0; fi; \
	echo "Repeating tests ($$REPEAT_COUNT x, -race) for: $$pkgs"; \
	cd backend && go test -short -race -count="$$REPEAT_COUNT" $$pkgs

test-frontend:
	cd frontend && npm test

# Run frontend e2e tests (requires MySQL running, starts backend natively)
# Logs are written to frontend/test-logs/ for post-mortem troubleshooting.
#
# Isolation from the developer's cluster and data:
# - The run uses its own database (E2E_DB_NAME, default app_e2e) and creates
#   it fresh on each run. The dev database can hold cluster rows with an
#   absolute kubeconfig path (make dev-local stores ~/.kube/config).
# - The backend runs with HOME=backend/tmp/e2e-home, and KUBECONFIG and
#   KUBECONFIG_PATH point only at the E2E_KUBECONFIG copy (or are empty). An
#   in-cluster cluster outside a pod is unreachable (no fallback to
#   ~/.kube/config, #582), unless IN_CLUSTER_FALLBACK_KUBECONFIG is set.
# - UNIQUE_INSTANCE_NAMES=true: unique-instance-names.spec.ts (#587) checks the
#   409 for a taken name. The other specs use unique generated names.
# - Set E2E_KUBECONFIG only to a local cluster (Rancher Desktop, kind): the
#   specs create and delete stack namespaces. Without it, the specs that need
#   a cluster are skipped.
E2E_DB_NAME ?= app_e2e
# Run only some specs of the main suite, for example
# make test-e2e E2E_SPECS=e2e/custom-actions.spec.ts (default: all). With
# E2E_SPECS, the isolated phase does not run.
E2E_SPECS ?=
E2E_HOME := $(CURDIR)/backend/tmp/e2e-home
E2E_PID := $(CURDIR)/backend/tmp/e2e-backend.pid
# Database access for the specs that change rows directly (helpers.ts, for
# example to mark a user as an SSO user; there is no API for that).
E2E_DB_PLAYWRIGHT_ENV = E2E_DB_NAME=$(E2E_DB_NAME) E2E_DB_USER=$${DB_USER:-root} E2E_DB_PASSWORD=$${DB_PASSWORD:-rootpassword}
E2E_STOP_BACKEND = kill $$(cat $(E2E_PID) 2>/dev/null) 2>/dev/null; rm -rf $(E2E_HOME) $(E2E_PID)

# Test stub of the specs (frontend/e2e/stub/server.ts; global-setup.ts starts it):
# - Custom actions (#588): the backend gets HOOKS_CONFIG_FILE=$(E2E_HOOKS_CONFIG). Its actions call
#   the stub on http://127.0.0.1:18090 and sign with E2E_ACTION_SECRET.
# - Notification channels (#592): the backend sends only to https:// webhooks
#   and checks the certificate. e2e-prepare makes a self-signed certificate for
#   127.0.0.1 in E2E_STUB_DIR. On Linux the backend trusts it through
#   SSL_CERT_DIR, and the specs get E2E_STUB_TLS=1. Go on macOS ignores
#   SSL_CERT_DIR, so the specs that need a successful delivery skip there.
E2E_STUB_DIR := $(CURDIR)/backend/tmp/e2e-stub
E2E_HOOKS_CONFIG := $(CURDIR)/frontend/e2e/fixtures/hooks-e2e.json
E2E_ACTION_SECRET ?= e2e-action-secret
# Public web UI URL of the e2e backend: notification channel cards get the
# "Stack Manager" button (instance_url) with it (#642).
E2E_APP_PUBLIC_URL ?= https://stacks.e2e.example
E2E_STUB_BACKEND_ENV = HOOKS_CONFIG_FILE=$(E2E_HOOKS_CONFIG) E2E_ACTION_SECRET=$(E2E_ACTION_SECRET) \
	APP_PUBLIC_URL=$(E2E_APP_PUBLIC_URL)
E2E_STUB_PLAYWRIGHT_ENV = E2E_ACTION_SECRET=$(E2E_ACTION_SECRET) E2E_APP_PUBLIC_URL=$(E2E_APP_PUBLIC_URL)
ifeq ($(shell uname -s),Linux)
E2E_STUB_BACKEND_ENV += SSL_CERT_DIR=/etc/ssl/certs:$(E2E_STUB_DIR)/trust
E2E_STUB_PLAYWRIGHT_ENV += E2E_STUB_TLS=1
endif

# Isolated phase: the Playwright project `isolated` runs specs that must not run
# next to the main suite, serially, against a restarted backend.
# - session-lifecycle.spec.ts (#584) needs short session limits; it reads the
#   same values from the E2E_SESSION_* variables.
# - last-admin.spec.ts (#585) disables the built-in `admin` account for a moment.
#   It is the teardown project `isolated-last-admin`, so `--project=isolated`
#   runs it after session-lifecycle, also when that fails.
# `make test-e2e` runs this phase after the main suite; `make test-e2e-isolated`
# runs only this phase. It uses one worker: the specs must never overlap (the
# last-admin spec disables `admin`, which the other specs log in as).
E2E_SESSION_ACCESS_TTL ?= 10s
E2E_SESSION_IDLE_TIMEOUT ?= 20s
E2E_SESSION_MAX_LIFETIME ?= 60s
E2E_SESSION_REUSE_GRACE ?= 3s
E2E_SESSION_BACKEND_ENV = ACCESS_TOKEN_EXPIRATION=$(E2E_SESSION_ACCESS_TTL) \
	SESSION_IDLE_TIMEOUT=$(E2E_SESSION_IDLE_TIMEOUT) SESSION_MAX_LIFETIME=$(E2E_SESSION_MAX_LIFETIME) \
	REFRESH_REUSE_GRACE=$(E2E_SESSION_REUSE_GRACE)
E2E_ISOLATED_PLAYWRIGHT_ENV = E2E_ISOLATED=1 E2E_SESSION_ACCESS_TTL=$(E2E_SESSION_ACCESS_TTL) \
	E2E_SESSION_IDLE_TIMEOUT=$(E2E_SESSION_IDLE_TIMEOUT) E2E_SESSION_MAX_LIFETIME=$(E2E_SESSION_MAX_LIFETIME) \
	E2E_SESSION_REUSE_GRACE=$(E2E_SESSION_REUSE_GRACE)

# Recreate the e2e HOME and start the built backend in the background.
# $(1): extra environment variables. $(2): log file name in frontend/test-logs.
define e2e_start_backend
	@rm -rf $(E2E_HOME) && mkdir -p $(E2E_HOME)/.kube
	@cd backend && \
		export HOME="$(E2E_HOME)" KUBECONFIG= KUBECONFIG_PATH=; \
		if [ -n "$${E2E_KUBECONFIG:-}" ]; then \
			case "$$E2E_KUBECONFIG" in /*) kc="$$E2E_KUBECONFIG" ;; *) kc="$(CURDIR)/$$E2E_KUBECONFIG" ;; esac; \
			[ -f "$$kc" ] || { echo "ERROR: E2E_KUBECONFIG file not found: $$kc"; exit 1; }; \
			cp "$$kc" "$$HOME/.kube/config"; \
			export KUBECONFIG="$$HOME/.kube/config" KUBECONFIG_PATH="$$HOME/.kube/config"; \
			echo "Cluster access: a copy of $$kc"; \
		else \
			echo "E2E_KUBECONFIG is not set: the backend runs without cluster access."; \
		fi; \
		DB_HOST=$${DB_HOST:-127.0.0.1} DB_PORT=$${DB_PORT:-3306} \
		DB_USER=$${DB_USER:-root} DB_PASSWORD=$${DB_PASSWORD:-rootpassword} DB_NAME=$(E2E_DB_NAME) \
		JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
		KUBECONFIG_ENCRYPTION_KEY=$${KUBECONFIG_ENCRYPTION_KEY:-dev-only-encryption-key-do-not-use-in-production} \
		ADMIN_USERNAME=admin ADMIN_PASSWORD=admin SELF_REGISTRATION=true \
		HELM_BINARY=$${HELM_BINARY:-helm} \
		CORS_ALLOWED_ORIGINS=http://localhost:3000 \
		RATE_LIMIT=10000 LOGIN_RATE_LIMIT=10000 PORT=8081 UNIQUE_INSTANCE_NAMES=true $(E2E_STUB_BACKEND_ENV) $(1) \
		./tmp/main > ../frontend/test-logs/$(2) 2>&1 & echo $$! > $(E2E_PID)
	@echo "Waiting for backend to be healthy..."
	@n=0; until curl -sf http://localhost:8081/health/live >/dev/null 2>&1; do \
		if ! kill -0 $$(cat $(E2E_PID)) 2>/dev/null; then \
			echo "ERROR: the backend stopped. Last lines of frontend/test-logs/$(2):"; \
			tail -20 frontend/test-logs/$(2); $(E2E_STOP_BACKEND); $(MAKE) integration-infra-stop; exit 1; \
		fi; \
		n=$$((n+1)); if [ $$n -ge 90 ]; then \
			echo "ERROR: the backend is not healthy after 90s"; $(E2E_STOP_BACKEND); $(MAKE) integration-infra-stop; exit 1; \
		fi; \
		sleep 1; \
	done
endef

# PIDs that listen on the backend port (client connections to it do not count).
E2E_PORT_LISTENERS = lsof -nP -iTCP:8081 -sTCP:LISTEN -t 2>/dev/null

# Stop the backend and wait until nothing listens on port 8081 (before a
# restart). After 30s, kill the listener; if the port is still taken, stop
# the infrastructure and fail.
define e2e_stop_backend_and_wait
	@$(E2E_STOP_BACKEND) || true
	@n=0; while [ -n "$$($(E2E_PORT_LISTENERS))" ]; do \
		n=$$((n+1)); if [ $$n -ge 30 ]; then \
			echo "WARNING: port 8081 is still in use after 30s; killing the listener."; \
			$(E2E_PORT_LISTENERS) | xargs kill -9 2>/dev/null; sleep 2; \
			if [ -n "$$($(E2E_PORT_LISTENERS))" ]; then \
				echo "ERROR: port 8081 is still in use"; $(MAKE) -C $(CURDIR) integration-infra-stop; exit 1; \
			fi; \
			break; \
		fi; \
		sleep 1; \
	done
endef

# Run the isolated project against the running (short-limit) backend.
# It writes its HTML report and traces to separate folders, because Playwright
# clears playwright-report/ and test-results/ at the start of a run, and the
# main suite's report must survive for the CI artifact.
define e2e_run_isolated_specs
	@echo "Running the isolated specs (access token $(E2E_SESSION_ACCESS_TTL), idle $(E2E_SESSION_IDLE_TIMEOUT), max lifetime $(E2E_SESSION_MAX_LIFETIME), reuse grace $(E2E_SESSION_REUSE_GRACE))..."
	cd frontend && $(E2E_ISOLATED_PLAYWRIGHT_ENV) $(E2E_DB_PLAYWRIGHT_ENV) $(E2E_STUB_PLAYWRIGHT_ENV) PLAYWRIGHT_HTML_OUTPUT_DIR=playwright-report-isolated \
		bash -o pipefail -c 'npx playwright test --project=isolated --workers=1 --output=test-results-isolated 2>&1 | tee test-logs/playwright-isolated.log' || \
		($(E2E_STOP_BACKEND); $(MAKE) -C $(CURDIR) integration-infra-stop; exit 1)
endef

# Check the port, create a fresh database and build the backend.
e2e-prepare: integration-infra-start
	@if [ -n "$$($(E2E_PORT_LISTENERS))" ]; then \
		echo "ERROR: port 8081 is in use. Stop make dev (or the other backend) first."; exit 1; \
	fi
	@mkdir -p frontend/test-logs backend/tmp
	@echo "Creating a fresh database $(E2E_DB_NAME)..."
	@# On a fresh volume, mysqladmin ping already answers during the init of the
	@# entrypoint (temporary server), so retry until the statement works.
	@n=0; until docker compose exec -T mysql mysql -uroot -p"$${DB_PASSWORD:-rootpassword}" \
		-e 'DROP DATABASE IF EXISTS `$(E2E_DB_NAME)`; CREATE DATABASE `$(E2E_DB_NAME)`' 2>$(CURDIR)/backend/tmp/e2e-db.err; do \
		n=$$((n+1)); if [ $$n -ge 30 ]; then \
			echo "ERROR: could not create the database $(E2E_DB_NAME):"; cat $(CURDIR)/backend/tmp/e2e-db.err; exit 1; \
		fi; \
		sleep 2; \
	done; rm -f $(CURDIR)/backend/tmp/e2e-db.err
	@echo "Creating the certificate of the e2e stub..."
	@rm -rf $(E2E_STUB_DIR) && mkdir -p $(E2E_STUB_DIR)/trust
	@openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=127.0.0.1" \
		-addext "subjectAltName=IP:127.0.0.1" -keyout $(E2E_STUB_DIR)/stub.key \
		-out $(E2E_STUB_DIR)/trust/stub.pem >$(E2E_STUB_DIR)/openssl.log 2>&1 || \
		{ echo "WARNING: openssl could not create the stub certificate (see $(E2E_STUB_DIR)/openssl.log). The specs that need the HTTPS stub skip."; \
		rm -f $(E2E_STUB_DIR)/trust/stub.pem; }
	@echo "Building backend..."
	@cd backend && mkdir -p tmp && go build -o tmp/main ./api

# A failed main suite does not skip the isolated phase: its exit status goes to
# E2E_MAIN_STATUS, and the target fails at the end when either phase failed.
E2E_MAIN_STATUS := $(CURDIR)/backend/tmp/e2e-main.status

test-e2e: e2e-prepare
	@echo "Starting backend..."
	$(call e2e_start_backend,,backend.log)
	@echo "Backend is ready, running Playwright tests..."
	@cd frontend && $(E2E_DB_PLAYWRIGHT_ENV) $(E2E_STUB_PLAYWRIGHT_ENV) bash -o pipefail -c 'npx playwright test $(E2E_SPECS) 2>&1 | tee test-logs/playwright.log'; \
		echo $$? > $(E2E_MAIN_STATUS)
ifeq ($(strip $(E2E_SPECS)),)
	@echo "Restarting backend for the isolated specs (short session limits)..."
	$(call e2e_stop_backend_and_wait)
	$(call e2e_start_backend,$(E2E_SESSION_BACKEND_ENV),backend-isolated.log)
	$(call e2e_run_isolated_specs)
else
	@echo "E2E_SPECS is set: the isolated phase does not run."
endif
	@echo "Stopping backend..."
	@$(E2E_STOP_BACKEND) || true
	$(MAKE) integration-infra-stop
	@status=$$(cat $(E2E_MAIN_STATUS) 2>/dev/null || echo 1); rm -f $(E2E_MAIN_STATUS); \
		if [ "$$status" != 0 ]; then echo "ERROR: the main e2e suite failed (see frontend/test-logs/playwright.log)."; exit 1; fi

test-e2e-isolated: e2e-prepare ## Run only the isolated e2e specs (serial, short session limits)
	@echo "Starting backend with short session limits..."
	$(call e2e_start_backend,$(E2E_SESSION_BACKEND_ENV),backend-isolated.log)
	$(call e2e_run_isolated_specs)
	@echo "Stopping backend..."
	@$(E2E_STOP_BACKEND) || true
	$(MAKE) integration-infra-stop

# Install dependencies
install:
	cd backend && go mod download
	cd frontend && npm install

# Generate API documentation
docs:
	cd backend && swag init -g api/main.go

# Format and lint code
fmt:
	cd backend && go fmt ./...
	cd frontend && npm run format

lint:
	cd backend && go vet ./...
	cd frontend && npm run lint

# gosec security scan of the backend, as in CI (pull-request.yml).
gosec:
	scripts/gosec.sh

# Shared env vars for local backend development
DEV_LOCAL_ENV = \
	DB_HOST=$${DB_HOST:-127.0.0.1} DB_PORT=$${DB_PORT:-3306} \
	DB_USER=$${DB_USER:-root} DB_PASSWORD=$${DB_PASSWORD:-rootpassword} DB_NAME=$${DB_NAME:-app} \
	JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
	KUBECONFIG_ENCRYPTION_KEY=$${KUBECONFIG_ENCRYPTION_KEY:-dev-only-encryption-key-do-not-use-in-production} \
	ADMIN_USERNAME=admin ADMIN_PASSWORD=admin \
	SELF_REGISTRATION=true \
	HELM_BINARY=$${HELM_BINARY:-helm} \
	KUBECONFIG_PATH=$${KUBECONFIG_PATH:-$$HOME/.kube/config} \
	RATE_LIMIT=$${RATE_LIMIT:-1000} \
	CORS_ALLOWED_ORIGINS=$${CORS_ALLOWED_ORIGINS:-http://localhost:3000,http://localhost:3001} \
	PORT=8081 GIN_MODE=debug

# Run backend locally with hot reload.
# Uses 'air' (Go live reload) if installed, otherwise falls back to 'go run'.
# Install air: go install github.com/air-verse/air@latest
GO_AIR := $(shell go env GOPATH 2>/dev/null)/bin/air
dev-local-backend:
	@if [ -x "$(GO_AIR)" ]; then \
		echo "Using air for hot reload..."; \
		cd backend && $(DEV_LOCAL_ENV) $(GO_AIR); \
	else \
		echo "air not found — using go run (no hot reload). Install: go install github.com/air-verse/air@latest"; \
		cd backend && $(DEV_LOCAL_ENV) go run ./api; \
	fi

# Run frontend dev server locally with HMR (port 3000)
dev-local-frontend:
	cd frontend && npm run dev

# Run both backend and frontend locally with hot reload (no Docker for app code)
# Starts MySQL in Docker automatically. Ctrl+C stops backend/frontend processes.
# Backend: port 8081, Frontend: port 3000
dev-local: mysql-start
	@echo "Starting backend (port 8081) and frontend (port 3000)..."
	@trap 'kill 0' EXIT; \
	$(MAKE) dev-local-backend & \
	$(MAKE) dev-local-frontend & \
	wait

# ── Load Testing ─────────────────────────────────────────────────────
# Starts the backend as a local process in release mode, runs the tests, then
# stops it. Requires: k6 (brew install k6) for the backend tests.
# See loadtest/README.md.

# Port of the load test backend. The health checks and the k6 API_URL use it.
LOADTEST_PORT ?= 8081
LOADTEST_URL = http://localhost:$(LOADTEST_PORT)
# docker: start the MySQL container (mysql-start) and stop the Docker backend.
# external: use the MySQL server of the DB_* variables; run no Docker command.
LOADTEST_MYSQL ?= docker
# 1: run each k6 test through loadtest/capture-resources.sh $(CAPTURE_ARGS).
LOADTEST_CAPTURE ?=
CAPTURE_ARGS ?=
# 1: the k6-users.js tests deploy a small chart to a local cluster (see loadtest-k8s-config).
LOADTEST_K8S ?=
LOADTEST_KUBE_CONTEXT ?= rancher-desktop
# 1: run a second backend (loadtest-start-replica) with WS fan-out on both.
LOADTEST_FANOUT ?=
LOADTEST_PORT_2 ?= 8082
LOADTEST_PPROF_ADDR_2 ?= 127.0.0.1:6061
LOADTEST_URL_2 = http://localhost:$(LOADTEST_PORT_2)
# Extra k6 options, for example --summary-export FILE.
LOADTEST_K6_EXTRA ?=
# Folder of the MySQL report (loadtest-mysql-stats).
LOADTEST_STATS_DIR ?= loadtest/results
LOADTEST_KUBECONFIG = $(CURDIR)/backend/tmp/loadtest-kubeconfig.yaml
LOADTEST_CHART = $(CURDIR)/loadtest/chart/loadtest-tiny
LOADTEST_CAPTURE_REPLICA = $(if $(filter 1 true yes,$(LOADTEST_FANOUT)),-A $(LOADTEST_PPROF_ADDR_2))
LOADTEST_K6 = $(if $(filter 1 true yes,$(LOADTEST_CAPTURE)),loadtest/capture-resources.sh $(CAPTURE_ARGS) $(LOADTEST_CAPTURE_REPLICA) --) \
	k6 run $(LOADTEST_K6_EXTRA) --env API_URL=$(LOADTEST_URL)

# Env vars of the load test backends. All k6 users share one IP address, so
# both rate limits are high. The empty kubeconfig and git token variables keep
# the backend away from real clusters and git hosts.
LOADTEST_ENV = \
	APP_DEBUG=false GIN_MODE=release \
	SERVER_HOST=$${SERVER_HOST:-127.0.0.1} SERVER_PORT=$(LOADTEST_PORT) \
	DB_HOST=$${DB_HOST:-127.0.0.1} DB_PORT=$${DB_PORT:-3306} \
	DB_USER=$${DB_USER:-root} DB_PASSWORD=$${DB_PASSWORD:-rootpassword} DB_NAME=$${DB_NAME:-app} \
	JWT_SECRET=$${JWT_SECRET:-dev-secret-change-in-production-minimum-16-chars} \
	ADMIN_USERNAME=$${ADMIN_USERNAME:-admin} ADMIN_PASSWORD=$${ADMIN_PASSWORD:-admin} \
	SELF_REGISTRATION=true \
	HELM_BINARY=$${HELM_BINARY:-helm} \
	KUBECONFIG= KUBECONFIG_PATH= IN_CLUSTER_FALLBACK_KUBECONFIG= ALLOWED_CHART_REPOS= \
	AZURE_DEVOPS_PAT= GITHUB_TOKEN= GITLAB_TOKEN= \
	PPROF_ENABLED=$${PPROF_ENABLED:-true} PPROF_ADDR=$${PPROF_ADDR:-127.0.0.1:6060} \
	PPROF_MUTEX_FRACTION=$${PPROF_MUTEX_FRACTION:-5} PPROF_BLOCK_RATE=$${PPROF_BLOCK_RATE:-1000000} \
	TRUSTED_PROXIES=$${TRUSTED_PROXIES:-127.0.0.1,::1} \
	$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),WS_FANOUT_ENABLED=true POD_NAME=loadtest-a) \
	OTEL_TRACE_SAMPLE_RATE=$${OTEL_TRACE_SAMPLE_RATE:-0.05} \
	RATE_LIMIT=$${RATE_LIMIT:-1000000} LOGIN_RATE_LIMIT=$${LOGIN_RATE_LIMIT:-1000000}

# Env vars for MySQL + OTel load testing
LOADTEST_MYSQL_ENV = $(LOADTEST_ENV) \
	DB_MAX_OPEN_CONNS=$${DB_MAX_OPEN_CONNS:-200} DB_MAX_IDLE_CONNS=$${DB_MAX_IDLE_CONNS:-20} \
	OTEL_ENABLED=true OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
	OTEL_SERVICE_NAME=k8s-stack-manager

# Fail when the backend port or the pprof port is in use: the health check or
# the capture script would otherwise talk to another process.
define loadtest_check_ports
	@ports="$(LOADTEST_PORT)"; addr=$${PPROF_ADDR:-127.0.0.1:6060}; \
	if [ "$${PPROF_ENABLED:-true}" = true ]; then ports="$$ports $${addr##*:}"; fi; \
	if command -v lsof >/dev/null 2>&1; then tool=lsof; \
	elif command -v ss >/dev/null 2>&1; then tool=ss; \
	else echo "WARNING: lsof not found, port check skipped"; ports=; fi; \
	for p in $$ports; do \
		if [ $$tool = lsof ]; then used=$$(lsof -nP -iTCP:$$p -sTCP:LISTEN -t 2>/dev/null); \
		else used=$$(ss -ltn "sport = :$$p" 2>/dev/null | tail -n +2); fi; \
		if [ -n "$$used" ]; then \
			echo "ERROR: port $$p is in use. Stop the other process, or set LOADTEST_PORT or PPROF_ADDR."; exit 1; \
		fi; \
	done
endef

# Wait until the load test backend is healthy. $(1): PID file, $(2): log file (both in backend/tmp).
define loadtest_wait_backend
	@n=0; until curl -sf $(LOADTEST_URL)/health/live >/dev/null 2>&1; do \
		if ! kill -0 $$(cat backend/tmp/$(1)) 2>/dev/null; then \
			echo "ERROR: the backend stopped. Last lines of backend/tmp/$(2):"; tail -20 backend/tmp/$(2); \
			rm -f backend/tmp/$(1); exit 1; \
		fi; \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then \
			echo "ERROR: Backend failed to start after 30s. See backend/tmp/$(2)"; \
			kill $$(cat backend/tmp/$(1)) 2>/dev/null; rm -f backend/tmp/$(1); exit 1; \
		fi; \
		sleep 1; \
	done
	@echo "Backend is ready on $(LOADTEST_URL) (logs: backend/tmp/$(2))"
endef

loadtest-start-backend: $(if $(filter docker,$(LOADTEST_MYSQL)),mysql-start) ## Build and start backend in release mode for load testing
ifeq ($(LOADTEST_MYSQL),docker)
	@echo "Stopping Docker backend (load test uses a local binary instead)..."
	@docker compose stop backend 2>/dev/null || true
endif
	$(call loadtest_check_ports)
	@echo "Building backend (release mode)..."
	@cd backend && mkdir -p tmp && go build -o tmp/main ./api
	@echo "Starting backend on port $(LOADTEST_PORT) (GIN_MODE=release, APP_DEBUG=false)..."
	@cd backend && ( $(LOADTEST_ENV) ./tmp/main > tmp/loadtest.log 2>&1 & echo $$! > tmp/loadtest-backend.pid )
	$(call loadtest_wait_backend,loadtest-backend.pid,loadtest.log)

loadtest-start-frontend: ## Start frontend dev server for load testing
	@if [ "$(LOADTEST_PORT)" != 8081 ]; then \
		echo "ERROR: the frontend dev server calls the backend on port 8081. Use LOADTEST_PORT=8081."; exit 1; \
	fi
	@echo "Starting frontend dev server..."
	@cd frontend && ( npm run dev > /tmp/loadtest-frontend.log 2>&1 & echo $$! > /tmp/loadtest-frontend.pid )
	@n=0; while ! curl -sf http://localhost:3000 >/dev/null 2>&1; do \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then echo "ERROR: Frontend failed to start after 30s. See /tmp/loadtest-frontend.log"; exit 1; fi; \
		sleep 1; \
	done
	@echo "Frontend is ready on :3000"

loadtest-start: loadtest-start-backend loadtest-start-frontend ## Start backend and frontend for load testing

loadtest-stop-backend: ## Stop load test backend
	@if [ -f backend/tmp/loadtest-backend.pid ]; then \
		echo "Stopping backend (PID: $$(cat backend/tmp/loadtest-backend.pid))..."; \
		kill $$(cat backend/tmp/loadtest-backend.pid) 2>/dev/null || true; \
		rm -f backend/tmp/loadtest-backend.pid; \
	else \
		echo "No backend PID file found; skipping backend stop."; \
	fi
	@if [ -f backend/tmp/loadtest.log ]; then echo "Backend log: backend/tmp/loadtest.log"; fi

loadtest-stop-frontend: ## Stop load test frontend
	@if [ -f /tmp/loadtest-frontend.pid ]; then \
		echo "Stopping frontend (PID: $$(cat /tmp/loadtest-frontend.pid))..."; \
		kill $$(cat /tmp/loadtest-frontend.pid) 2>/dev/null || true; \
		rm -f /tmp/loadtest-frontend.pid; \
	else \
		echo "No frontend PID file found; skipping frontend stop."; \
	fi

loadtest-stop: loadtest-stop-backend loadtest-stop-frontend ## Stop load test backend and frontend

loadtest: loadtest-start ## Run all load tests (starts/stops backend automatically)
	@$(MAKE) loadtest-backend-run || ($(MAKE) loadtest-stop; exit 1)
	@$(MAKE) loadtest-frontend-run || ($(MAKE) loadtest-stop; exit 1)
	@$(MAKE) loadtest-stop

loadtest-backend: loadtest-start-backend ## Run k6 backend load tests (starts/stops backend only)
	@$(MAKE) loadtest-backend-run || ($(MAKE) loadtest-stop-backend; exit 1)
	@$(MAKE) loadtest-stop-backend

loadtest-backend-run: ## Run k6 tests (assumes backend already running)
	@command -v k6 >/dev/null 2>&1 || { echo "k6 not found. Install: brew install k6"; exit 1; }
	@echo "Running backend API load test against $(LOADTEST_URL)..."
	$(LOADTEST_K6) loadtest/backend/k6-api.js
	@echo ""
	@echo "Running backend WebSocket load test..."
	$(LOADTEST_K6) loadtest/backend/k6-websocket.js

loadtest-stress: loadtest-start-backend ## Run stress/optimization load tests (starts/stops backend)
	@$(MAKE) loadtest-stress-run || ($(MAKE) loadtest-stop-backend; exit 1)
	@$(MAKE) loadtest-stop-backend

loadtest-stress-run: ## Run k6 stress tests (assumes backend already running)
	@command -v k6 >/dev/null 2>&1 || { echo "k6 not found. Install: brew install k6"; exit 1; }
	@echo "Running stress/optimization load tests against $(LOADTEST_URL)..."
	$(LOADTEST_K6) loadtest/backend/k6-stress.js

loadtest-capture: ## Record RSS, CPU and pprof profiles of the running load test backend (Ctrl-C stops)
	loadtest/capture-resources.sh $(CAPTURE_ARGS)

loadtest-k8s-config: ## Write a single-context kubeconfig of the local cluster (LOADTEST_KUBE_CONTEXT, default rancher-desktop)
	@command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found."; exit 1; }
	@command -v helm >/dev/null 2>&1 || { echo "ERROR: helm not found."; exit 1; }
	@kubectl config get-contexts -o name 2>/dev/null | grep -qx "$(LOADTEST_KUBE_CONTEXT)" || { \
		echo "ERROR: kubectl context \"$(LOADTEST_KUBE_CONTEXT)\" not found. Start Rancher Desktop with Kubernetes on, or set LOADTEST_KUBE_CONTEXT."; exit 1; }
	@mkdir -p backend/tmp
	@umask 077; kubectl config view --minify --flatten --context $(LOADTEST_KUBE_CONTEXT) > backend/tmp/loadtest-kubeconfig.yaml
	@chmod 600 backend/tmp/loadtest-kubeconfig.yaml
	@kubectl --kubeconfig backend/tmp/loadtest-kubeconfig.yaml get nodes || { \
		echo "ERROR: the cluster of context \"$(LOADTEST_KUBE_CONTEXT)\" does not answer. Is Rancher Desktop running?"; exit 1; }
	@helm version --short
	@echo "Kubeconfig: backend/tmp/loadtest-kubeconfig.yaml"

loadtest-start-replica: ## Start a second backend on LOADTEST_PORT_2 (fan-out on, same database; run loadtest-start-backend with LOADTEST_FANOUT=1 first)
	@if [ ! -x backend/tmp/main ]; then echo "ERROR: backend/tmp/main not found. Run make loadtest-start-backend LOADTEST_FANOUT=1 first."; exit 1; fi
	@if [ -f backend/tmp/loadtest-2.pid ] && kill -0 $$(cat backend/tmp/loadtest-2.pid) 2>/dev/null; then \
		echo "ERROR: the replica runs already (PID $$(cat backend/tmp/loadtest-2.pid))."; exit 1; fi
	@if command -v lsof >/dev/null 2>&1; then addr=$(LOADTEST_PPROF_ADDR_2); for p in $(LOADTEST_PORT_2) $${addr##*:}; do \
		if [ -n "$$(lsof -nP -iTCP:$$p -sTCP:LISTEN -t 2>/dev/null)" ]; then echo "ERROR: port $$p is in use."; exit 1; fi; done; fi
	@echo "Starting replica on port $(LOADTEST_PORT_2) (WS_FANOUT_ENABLED=true, POD_NAME=loadtest-b)..."
	@cd backend && ( $(LOADTEST_ENV) SERVER_PORT=$(LOADTEST_PORT_2) PPROF_ADDR=$(LOADTEST_PPROF_ADDR_2) \
		WS_FANOUT_ENABLED=true POD_NAME=loadtest-b ./tmp/main > tmp/loadtest-2.log 2>&1 & echo $$! > tmp/loadtest-2.pid )
	@n=0; until curl -sf $(LOADTEST_URL_2)/health/live >/dev/null 2>&1; do \
		if ! kill -0 $$(cat backend/tmp/loadtest-2.pid) 2>/dev/null; then \
			echo "ERROR: the replica stopped. Last lines of backend/tmp/loadtest-2.log:"; tail -20 backend/tmp/loadtest-2.log; \
			rm -f backend/tmp/loadtest-2.pid; exit 1; fi; \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then echo "ERROR: replica failed to start after 30s. See backend/tmp/loadtest-2.log"; \
			kill $$(cat backend/tmp/loadtest-2.pid) 2>/dev/null; rm -f backend/tmp/loadtest-2.pid; exit 1; fi; \
		sleep 1; \
	done
	@echo "Replica is ready on $(LOADTEST_URL_2) (logs: backend/tmp/loadtest-2.log)"

loadtest-stop-replica: ## Stop the second load test backend
	@if [ -f backend/tmp/loadtest-2.pid ]; then \
		echo "Stopping replica (PID: $$(cat backend/tmp/loadtest-2.pid))..."; \
		kill $$(cat backend/tmp/loadtest-2.pid) 2>/dev/null || true; \
		rm -f backend/tmp/loadtest-2.pid; \
	else \
		echo "No replica PID file found."; \
	fi

loadtest-users: ## Run k6-users.js (many users with API keys; starts/stops the backend, and the replica with LOADTEST_FANOUT=1)
	@rc=0; \
	$(if $(filter 1 true yes,$(LOADTEST_K8S)),$(MAKE) loadtest-k8s-config || exit 1;) \
	$(MAKE) loadtest-start-backend || exit 1; \
	$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),$(MAKE) loadtest-start-replica || { $(MAKE) loadtest-stop-backend; exit 1; };) \
	trap '$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),$(MAKE) loadtest-stop-replica;) $(MAKE) loadtest-stop-backend; exit 130' INT TERM; \
	$(MAKE) loadtest-users-run || rc=$$?; \
	$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),$(MAKE) loadtest-stop-replica;) \
	$(MAKE) loadtest-stop-backend; \
	exit $$rc

loadtest-users-run: ## Run k6-users.js (assumes backend running; LOADTEST_K8S=1 deploys, LOADTEST_FANOUT=1 adds the replica URL)
	@command -v k6 >/dev/null 2>&1 || { echo "k6 not found. Install: brew install k6"; exit 1; }
	@echo "Running the users load test against $(LOADTEST_URL)..."
	$(LOADTEST_K6) \
		$(if $(filter 1 true yes,$(LOADTEST_K8S)),--env K8S_KUBECONFIG_PATH=$(LOADTEST_KUBECONFIG) --env DEPLOY_CHART_PATH=$(LOADTEST_CHART)) \
		$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),--env API_URL_2=$(LOADTEST_URL_2)) \
		loadtest/backend/k6-users.js

loadtest-mysql-stats-reset: ## Reset the MySQL statement statistics (before a test)
	loadtest/mysql-stats.sh reset

loadtest-mysql-stats: ## Write the MySQL statement report into LOADTEST_STATS_DIR (default loadtest/results)
	loadtest/mysql-stats.sh report $(LOADTEST_STATS_DIR)

# stackctl workers (loadtest/stackctl-load.sh). loadtest-full runs them next to k6.
LOADTEST_STACKCTL ?= $(if $(shell command -v stackctl 2>/dev/null),1,)
STACKCTL_WORKERS ?= 10
STACKCTL_DURATION ?= 330

loadtest-stackctl: ## Run the stackctl workers against the running load test backend
	@loadtest/stackctl-load.sh -w $(STACKCTL_WORKERS) -d $(STACKCTL_DURATION) -u $(LOADTEST_URL) $(if $(STACKCTL_OUT),-o $(STACKCTL_OUT))

loadtest-full: ## One run: backend(s), MySQL stats, k6-users.js with capture, reports in loadtest/results/<timestamp>/
	@dir=loadtest/results/$$(date +%Y%m%d-%H%M%S); mkdir -p $$dir; rc=0; sc=; \
	trap '[ -z "$$sc" ] || { pkill -P $$sc 2>/dev/null; kill $$sc 2>/dev/null; }; \
		$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),$(MAKE) loadtest-stop-replica;) $(MAKE) loadtest-stop-backend; exit 130' INT TERM; \
	$(if $(filter 1 true yes,$(LOADTEST_K8S)),$(MAKE) loadtest-k8s-config || exit 1;) \
	$(MAKE) loadtest-start-backend || exit 1; \
	$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),$(MAKE) loadtest-start-replica || { $(MAKE) loadtest-stop-backend; exit 1; };) \
	$(MAKE) loadtest-mysql-stats-reset || echo "WARNING: the MySQL statistics reset failed. The MySQL report can include older statements."; \
	$(if $(filter 1 true yes,$(LOADTEST_STACKCTL)),$(MAKE) loadtest-stackctl STACKCTL_OUT=$$dir > $$dir/stackctl.log 2>&1 & sc=$$!;) \
	$(MAKE) loadtest-users-run LOADTEST_CAPTURE=1 CAPTURE_ARGS="$(CAPTURE_ARGS) -o $$dir" \
		LOADTEST_K6_EXTRA="$(LOADTEST_K6_EXTRA) --summary-export $$dir/k6-summary.json" || rc=$$?; \
	if [ -n "$$sc" ]; then wait $$sc; scrc=$$?; \
		if [ $$scrc != 0 ]; then echo "stackctl workers failed (exit $$scrc), see $$dir/stackctl.log"; fi; sc=; fi; \
	$(MAKE) loadtest-mysql-stats LOADTEST_STATS_DIR=$$dir || true; \
	$(if $(LOADTEST_RAMP_REPORT),loadtest/ramp-report.sh $$dir/k6-summary.json $(RAMP_HOLD) $(RAMP_STEPS) > $$dir/ramp.md && cat $$dir/ramp.md || true;) \
	$(if $(filter 1 true yes,$(LOADTEST_FANOUT)),$(MAKE) loadtest-stop-replica;) \
	$(MAKE) loadtest-stop-backend; \
	echo "Results: $$dir"; exit $$rc

# Ramp: the user journey steps up through RAMP_STEPS VUs (rise RAMP_RISE s,
# hold RAMP_HOLD s). No deploys, fanout or stackctl, so the steps compare.
RAMP_STEPS ?= 50,100,200,300,400
RAMP_HOLD ?= 60
RAMP_RISE ?= 20

loadtest-ramp: ## Step the user journey up through RAMP_STEPS VUs to find the limit; table in loadtest/results/<timestamp>/ramp.md
	@$(MAKE) loadtest-full LOADTEST_K8S= LOADTEST_FANOUT= LOADTEST_STACKCTL= LOADTEST_RAMP_REPORT=1 \
		LOADTEST_K6_EXTRA="$(LOADTEST_K6_EXTRA) --env SCENARIO=user-journey --env RAMP_STEPS=$(RAMP_STEPS) --env RAMP_HOLD=$(RAMP_HOLD) --env RAMP_RISE=$(RAMP_RISE)"

loadtest-frontend: loadtest-start ## Run Playwright frontend load tests (starts/stops backend + frontend)
	@$(MAKE) loadtest-frontend-run || ($(MAKE) loadtest-stop; exit 1)
	@$(MAKE) loadtest-stop

loadtest-frontend-run: ## Run Playwright load tests (assumes backend already running)
	@echo "Running frontend load tests (workers=$${LOAD_WORKERS:-5})..."
	cd frontend && NODE_PATH=node_modules npx playwright test --config=../loadtest/frontend/playwright.config.ts

mysql-start: ## Start MySQL container
	docker compose up -d mysql
	@echo "Waiting for MySQL to be ready..."
	@n=0; while ! docker compose exec -T mysql mysqladmin ping -h localhost -uroot -p"$${DB_PASSWORD:-rootpassword}" --silent 2>/dev/null; do \
		n=$$((n+1)); \
		if [ $$n -ge 90 ]; then echo "ERROR: MySQL failed to start after 90s"; exit 1; fi; \
		sleep 1; \
	done
	@echo "MySQL is ready on :$${DB_PORT:-3306}"

mysql-stop: ## Stop MySQL container
	docker compose stop mysql
	docker compose rm -f mysql

otel-start: ## Start OTel observability stack (Collector, Prometheus, Tempo, Grafana)
	docker compose --profile otel up -d otel-collector prometheus tempo grafana
	@echo "Waiting for OTel Collector..."
	@n=0; while ! docker compose --profile otel exec -T otel-collector /otelcol-contrib validate --config /etc/otelcol-contrib/config.yaml >/dev/null 2>&1; do \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then echo "ERROR: OTel Collector failed to start after 30s"; exit 1; fi; \
		sleep 1; \
	done
	@echo "OTel stack ready: Grafana http://localhost:3001, Prometheus http://localhost:9090"

otel-stop: ## Stop OTel stack
	docker compose --profile otel stop otel-collector prometheus tempo grafana
	docker compose rm -f otel-collector prometheus tempo grafana

loadtest-mysql-start: ## Start MySQL + OTel + mysqld-exporter for load testing
	RATE_LIMIT=1000000 docker compose --profile otel --profile mysql-otel up -d mysql otel-collector prometheus tempo grafana mysqld-exporter
	@echo "Stopping Docker backend (load test uses a local binary instead)..."
	@docker compose stop backend 2>/dev/null || true
	@echo "Waiting for MySQL to be ready..."
	@n=0; while ! docker compose exec -T mysql mysqladmin ping -h localhost -uroot -p"$${DB_PASSWORD:-rootpassword}" --silent 2>/dev/null; do \
		n=$$((n+1)); \
		if [ $$n -ge 90 ]; then echo "ERROR: MySQL failed to start after 90s"; exit 1; fi; \
		sleep 1; \
	done
	@echo "Waiting for OTel Collector..."
	@n=0; while ! docker compose --profile otel exec -T otel-collector /otelcol-contrib validate --config /etc/otelcol-contrib/config.yaml >/dev/null 2>&1; do \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then echo "ERROR: OTel Collector failed to start after 30s"; exit 1; fi; \
		sleep 1; \
	done
	$(call loadtest_check_ports)
	@echo "Building backend (release mode)..."
	@cd backend && mkdir -p tmp && go build -o tmp/main ./api
	@echo "Starting backend on port $(LOADTEST_PORT) (MySQL + OTel, OTEL_TRACE_SAMPLE_RATE=$${OTEL_TRACE_SAMPLE_RATE:-0.05})..."
	@cd backend && ( $(LOADTEST_MYSQL_ENV) ./tmp/main > tmp/loadtest-mysql.log 2>&1 & echo $$! > tmp/loadtest-mysql.pid )
	$(call loadtest_wait_backend,loadtest-mysql.pid,loadtest-mysql.log)

loadtest-mysql-stop: ## Stop MySQL load test backend + infrastructure
	@if [ -f backend/tmp/loadtest-mysql.pid ]; then \
		echo "Stopping backend (PID: $$(cat backend/tmp/loadtest-mysql.pid))..."; \
		kill $$(cat backend/tmp/loadtest-mysql.pid) 2>/dev/null || true; \
		rm -f backend/tmp/loadtest-mysql.pid; \
	fi
	docker compose --profile otel --profile mysql-otel stop
	docker compose --profile otel --profile mysql-otel rm -f

loadtest-mysql-backend: loadtest-mysql-start ## Run k6 backend load tests with MySQL + OTel
	@$(MAKE) loadtest-backend-run || ($(MAKE) loadtest-mysql-stop; exit 1)
	@$(MAKE) loadtest-mysql-stop

loadtest-mysql-stress: loadtest-mysql-start ## Run k6 stress tests with MySQL + OTel
	@$(MAKE) loadtest-stress-run || ($(MAKE) loadtest-mysql-stop; exit 1)
	@$(MAKE) loadtest-mysql-stop

# ── Helm / Kubernetes ────────────────────────────────────────────────
HELM_CHART     := helm/k8s-stack-manager
HELM_RELEASE   := k8s-stack-manager
HELM_NAMESPACE := k8s-stack-manager

helm-lint: ## Lint the Helm chart
	helm lint $(HELM_CHART) \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-lint
	helm lint $(HELM_CHART) \
		--values $(HELM_CHART)/tests/external-secrets-values.yaml

helm-test: ## Verify Deployment, Argo Rollout, and External Secrets Helm renders
	@set -e; \
	template_file=$$(mktemp); \
	notes_dir=$$(mktemp -d); \
	trap 'rm -f "$$template_file"; rm -rf "$$notes_dir"' EXIT; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	grep -q 'GITHUB_ALLOWED_REPOSITORIES: ""' "$$template_file"; \
	grep -q 'GITLAB_BASE_URL: ""' "$$template_file"; \
	test "$$(grep -c 'DB_MAX_IDLE_CONNS:' "$$template_file")" = 1; \
	grep -q 'DB_MAX_IDLE_CONNS: "25"' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set-string backend.env.DB_MAX_IDLE_CONNS=10 > "$$template_file"; \
	test "$$(grep -c 'DB_MAX_IDLE_CONNS:' "$$template_file")" = 1; \
	grep -q 'DB_MAX_IDLE_CONNS: "10"' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set-string 'backend.env.GITHUB_ALLOWED_REPOSITORIES=example-org/api\,example-org/web' \
		--set-string backend.env.GITLAB_BASE_URL=https://gitlab.example.com:8443 > "$$template_file"; \
	grep -q 'GITHUB_ALLOWED_REPOSITORIES: "example-org/api,example-org/web"' "$$template_file"; \
	grep -q 'GITLAB_BASE_URL: "https://gitlab.example.com:8443"' "$$template_file"; \
	for rollout_mode in false true; do \
		if [ "$$rollout_mode" = true ]; then \
			workload_template=templates/backend/rollout.yaml; \
			expected_kind=Rollout; \
		else \
			workload_template=templates/backend/deployment.yaml; \
			expected_kind=Deployment; \
		fi; \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only "$$workload_template" \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
			--set argoRollouts.enabled="$$rollout_mode" > "$$template_file"; \
		grep -q "^kind: $${expected_kind}$$" "$$template_file"; \
		awk '/envFrom:/{env=1} env && /configMapRef:/{config_ref=1; next} config_ref && /name: k8s-stack-manager-backend$$/{config=1; config_ref=0} env && /secretRef:/{secret_ref=1; next} secret_ref && /name: k8s-stack-manager-backend$$/{secret=1; secret_ref=0} END{exit !(config && secret)}' "$$template_file"; \
		base_config_checksum=$$(awk '/checksum\/config:/{print $$2; exit}' "$$template_file"); \
		base_secret_checksum=$$(awk '/checksum\/secret:/{print $$2; exit}' "$$template_file"); \
		test -n "$$base_config_checksum"; \
		test -n "$$base_secret_checksum"; \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only "$$workload_template" \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
			--set argoRollouts.enabled="$$rollout_mode" \
			--set-string backend.env.GITLAB_BASE_URL=https://gitlab.example.com:8443 > "$$template_file"; \
		gitlab_config_checksum=$$(awk '/checksum\/config:/{print $$2; exit}' "$$template_file"); \
		gitlab_secret_checksum=$$(awk '/checksum\/secret:/{print $$2; exit}' "$$template_file"); \
		test "$$base_config_checksum" != "$$gitlab_config_checksum"; \
		test "$$base_secret_checksum" = "$$gitlab_secret_checksum"; \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only "$$workload_template" \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
			--set argoRollouts.enabled="$$rollout_mode" \
			--set-string backend.env.GITHUB_ALLOWED_REPOSITORIES=example-org/api > "$$template_file"; \
		allowlist_config_checksum=$$(awk '/checksum\/config:/{print $$2; exit}' "$$template_file"); \
		allowlist_secret_checksum=$$(awk '/checksum\/secret:/{print $$2; exit}' "$$template_file"); \
		test "$$base_config_checksum" != "$$allowlist_config_checksum"; \
		test "$$base_secret_checksum" = "$$allowlist_secret_checksum"; \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only "$$workload_template" \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
			--set argoRollouts.enabled="$$rollout_mode" \
			--set-string backend.env.GITHUB_ALLOWED_REPOSITORIES=example-org/api \
			--set-string backend.secrets.GITHUB_TOKEN=github-token-for-checksum-test > "$$template_file"; \
		token_config_checksum=$$(awk '/checksum\/config:/{print $$2; exit}' "$$template_file"); \
		token_secret_checksum=$$(awk '/checksum\/secret:/{print $$2; exit}' "$$template_file"); \
		test "$$allowlist_config_checksum" = "$$token_config_checksum"; \
		test "$$base_secret_checksum" != "$$token_secret_checksum"; \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only "$$workload_template" \
			--values $(HELM_CHART)/tests/external-secrets-values.yaml \
			--set argoRollouts.enabled="$$rollout_mode" > "$$template_file"; \
		awk '/envFrom:/{env=1} env && /configMapRef:/{config_ref=1; next} config_ref && /name: k8s-stack-manager-backend$$/{config=1; config_ref=0} env && /secretRef:/{secret_ref=1; next} secret_ref && /name: k8s-stack-manager-backend$$/{secret=1; secret_ref=0} END{exit !(config && secret)}' "$$template_file"; \
		external_secret_checksum=$$(awk '/checksum\/secret:/{print $$2; exit}' "$$template_file"); \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only "$$workload_template" \
			--values $(HELM_CHART)/tests/external-secrets-values.yaml \
			--set argoRollouts.enabled="$$rollout_mode" \
			--set-string backend.secrets.GITHUB_TOKEN=ignored-in-external-secrets-mode > "$$template_file"; \
		ignored_inline_checksum=$$(awk '/checksum\/secret:/{print $$2; exit}' "$$template_file"); \
		test "$$external_secret_checksum" = "$$ignored_inline_checksum"; \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only "$$workload_template" \
			--values $(HELM_CHART)/tests/external-secrets-values.yaml \
			--set argoRollouts.enabled="$$rollout_mode" \
			--set-string externalSecrets.data[4].remoteRef.key=k8s-stack-manager-github-token-new-reference > "$$template_file"; \
		changed_mapping_checksum=$$(awk '/checksum\/secret:/{print $$2; exit}' "$$template_file"); \
		test "$$external_secret_checksum" != "$$changed_mapping_checksum"; \
	done; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	grep -q 'Source: k8s-stack-manager/templates/mysql/secret.yaml' "$$template_file"; \
	grep -q 'DEPLOY_LOG_RETENTION_DAYS: "180"' "$$template_file" && grep -q 'AUDIT_LOG_RETENTION_DAYS: "365"' "$$template_file" \
		&& grep -q 'NOTIFICATION_RETENTION_DAYS: "90"' "$$template_file" && grep -q 'DELIVERY_LOG_RETENTION_DAYS: "30"' "$$template_file" \
		&& grep -q 'ACTION_RUN_RETENTION_DAYS: "180"' "$$template_file" \
		|| { echo "backend ConfigMap must have the default log retention" >&2; exit 1; }; \
	grep -q 'TRUSTED_PROXIES: "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"' "$$template_file" \
		|| { echo "backend ConfigMap must trust the private ranges by default" >&2; exit 1; }; \
	if grep -q 'UNIQUE_INSTANCE_NAMES:' "$$template_file"; then echo "UNIQUE_INSTANCE_NAMES must be off by default" >&2; exit 1; fi; \
	grep -q 'MAX_INSTANCE_TTL_DAYS: "30"' "$$template_file" \
		|| { echo "backend ConfigMap must have MAX_INSTANCE_TTL_DAYS 30 by default" >&2; exit 1; }; \
	if grep -q 'HELM_HISTORY_MAX:' "$$template_file"; then echo "HELM_HISTORY_MAX must not render by default" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.maxInstanceTtlDays=7 > "$$template_file"; \
	grep -q 'MAX_INSTANCE_TTL_DAYS: "7"' "$$template_file" \
		|| { echo "backend.maxInstanceTtlDays=7 must render MAX_INSTANCE_TTL_DAYS 7" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.env.MAX_INSTANCE_TTL_DAYS=14 > "$$template_file"; \
	test "$$(grep -c 'MAX_INSTANCE_TTL_DAYS:' "$$template_file")" = 1 && grep -q 'MAX_INSTANCE_TTL_DAYS: "14"' "$$template_file" \
		|| { echo "backend.env.MAX_INSTANCE_TTL_DAYS must win over backend.maxInstanceTtlDays" >&2; exit 1; }; \
	for bad in 0 366 abc 1.5; do \
		if helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set-string backend.maxInstanceTtlDays=$$bad >/dev/null 2>&1; then \
			echo "backend.maxInstanceTtlDays=$$bad must fail the render" >&2; exit 1; fi; \
	done; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.maxInstanceTtlDays=0 >/dev/null 2>&1; then \
		echo "a numeric backend.maxInstanceTtlDays=0 must fail the render" >&2; exit 1; fi; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.maxInstanceTtlDays=1 \
		--set-string backend.env.DEFAULT_INSTANCE_TTL_MINUTES=2880 >/dev/null 2>&1; then \
		echo "a TTL default above backend.maxInstanceTtlDays must fail the render" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.maxInstanceTtlDays=1 \
		| grep -q 'MAX_INSTANCE_TTL_DAYS: "1"' || { echo "the default TTL (480) must fit backend.maxInstanceTtlDays=1" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.helmHistoryMax=5 \
		| grep -q 'HELM_HISTORY_MAX: "5"' || { echo "backend.helmHistoryMax=5 must render HELM_HISTORY_MAX 5" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.helmHistoryMax=5 \
		--set-string backend.env.HELM_HISTORY_MAX=3 > "$$template_file"; \
	test "$$(grep -c 'HELM_HISTORY_MAX:' "$$template_file")" = 1 && grep -q 'HELM_HISTORY_MAX: "3"' "$$template_file" \
		|| { echo "backend.env.HELM_HISTORY_MAX must win over backend.helmHistoryMax" >&2; exit 1; }; \
	for flag in '--set-string backend.helmHistoryMax=0' '--set-string backend.helmHistoryMax=1' '--set-string backend.helmHistoryMax=-1' \
		'--set-string backend.helmHistoryMax=abc' '--set-string backend.helmHistoryMax=2.5' '--set backend.helmHistoryMax=1' \
		'--set-json backend.helmHistoryMax=1' '--set-json backend.helmHistoryMax=2.5'; do \
		if helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template $$flag > "$$template_file" 2>&1; then \
			echo "$$flag must fail the render" >&2; exit 1; fi; \
		grep -q 'backend.helmHistoryMax must be a whole number of 2 or more' "$$template_file" \
			|| { echo "$$flag must fail with the backend.helmHistoryMax message" >&2; cat "$$template_file" >&2; exit 1; }; \
	done; \
	for json in 5 1000000; do \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set-json backend.helmHistoryMax=$$json \
			| grep -q "HELM_HISTORY_MAX: \"$$json\"" || { echo "a number backend.helmHistoryMax=$$json (float64) must render HELM_HISTORY_MAX $$json" >&2; exit 1; }; \
	done; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.env.TRUSTED_PROXIES=10.1.0.0/16 > "$$template_file"; \
	test "$$(grep -c 'TRUSTED_PROXIES:' "$$template_file")" = 1 && grep -q 'TRUSTED_PROXIES: "10.1.0.0/16"' "$$template_file" \
		|| { echo "backend.env.TRUSTED_PROXIES must win over backend.trustedProxies" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set 'backend.trustedProxies=null' > "$$template_file"; \
	if grep -q 'TRUSTED_PROXIES:' "$$template_file"; then echo "an empty backend.trustedProxies must render no TRUSTED_PROXIES" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set backend.uniqueInstanceNames=true > "$$template_file"; \
	grep -q 'UNIQUE_INSTANCE_NAMES: "true"' "$$template_file" \
		|| { echo "backend.uniqueInstanceNames=true must render UNIQUE_INSTANCE_NAMES" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set backend.priorityClassName=pc-test --set frontend.priorityClassName=pc-test \
		--set mysql.priorityClassName=pc-test --set otel.enabled=true --set otel.priorityClassName=pc-test > "$$template_file"; \
	test "$$(grep -c 'priorityClassName: "pc-test"' "$$template_file")" = 4 \
		|| { echo "priorityClassName must render on backend, frontend, mysql and otel" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/traefik/ingressroute.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template --set ingress.traefik.priority=10000 > "$$template_file"; \
	awk '/PathPrefix\(`\/api`\)/{a=1} a && /priority:/{api=$$2; a=0} /PathPrefix\(`\/`\)/{f=1} f && /priority:/{fe=$$2; f=0} END{exit !(api==10000 && fe==9999)}' "$$template_file" \
		|| { echo "IngressRoute: / must have a lower priority than /api" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/mysql/secret.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set mysql.auth.password=inline-user-password > "$$template_file"; \
	if grep -q 'MYSQL_PASSWORD:' "$$template_file"; then echo "unexpected match: MYSQL_PASSWORD:" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/mysql/secret.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set mysql.auth.user=app-user --set mysql.auth.password=inline-user-password > "$$template_file"; \
	grep -q 'MYSQL_PASSWORD:' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/secret.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set backend.secrets.GITHUB_TOKEN=github-token-for-template \
		--set mysql.auth.rootPassword=inline-root-password > "$$template_file"; \
	grep -q 'DB_PASSWORD: "aW5saW5lLXJvb3QtcGFzc3dvcmQ="' "$$template_file"; \
	grep -q 'GITHUB_TOKEN: "Z2l0aHViLXRva2VuLWZvci10ZW1wbGF0ZQ=="' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/secret.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set mysql.auth.rootPassword=inline-root-password --set mysql.auth.user=app-user \
		--set mysql.auth.password=inline-user-password > "$$template_file"; \
	grep -q 'DB_PASSWORD: "aW5saW5lLXVzZXItcGFzc3dvcmQ="' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/secret.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set backend.secrets.DB_PASSWORD=explicit-backend-password \
		--set mysql.auth.user=app-user --set mysql.auth.password=inline-user-password > "$$template_file"; \
	grep -q 'DB_PASSWORD: "ZXhwbGljaXQtYmFja2VuZC1wYXNzd29yZA=="' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--values $(HELM_CHART)/tests/external-secrets-values.yaml > "$$template_file"; \
	if grep -q 'Source: k8s-stack-manager/templates/mysql/secret.yaml' "$$template_file"; then echo "unexpected match: Source: k8s-stack-manager/templates/mysql/secret.yaml" >&2; exit 1; fi; \
	grep -q 'key: k8s-stack-manager-mysql-root-password' "$$template_file"; \
	awk '/^kind: ExternalSecret$$/{backend=0} /^  name: .*backend$$/{backend=1} backend && /secretKey: GITHUB_TOKEN/{token=1} backend && token && /key: k8s-stack-manager-github-token$$/{remote=1} END{exit !(token && remote)}' "$$template_file"; \
	    awk '/^kind: ExternalSecret$$/{backend=0} /^  name: .*backend$$/{backend=1} backend && /secretKey: ADMIN_PASSWORD/{admin=1} END{exit !admin}' "$$template_file"; \
	awk '/^kind: ExternalSecret$$/{mysql=0} /^  name: .*mysql$$/{mysql=1} mysql && /secretKey: MYSQL_PASSWORD/{password=1} mysql && /key: k8s-stack-manager-mysql-password/{remote=1} END{exit !(password && remote)}' "$$template_file"; \
			awk '/^kind: ExternalSecret$$/{backend=0} /^  name: .*backend$$/{backend=1} backend && /secretKey: DB_PASSWORD/{password=1} backend && password && /key: k8s-stack-manager-mysql-password$$/{key=1} backend && password && /property: application-password$$/{property=1} backend && password && /version: user-v2$$/{version=1} END{exit !(password && key && property && version)}' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/external-secrets/external-secret.yaml \
		--values $(HELM_CHART)/tests/external-secrets-values.yaml --set mysql.auth.user= > "$$template_file"; \
	if grep -q 'secretKey: MYSQL_PASSWORD' "$$template_file"; then echo "unexpected match: secretKey: MYSQL_PASSWORD" >&2; exit 1; fi; \
			awk '/^kind: ExternalSecret$$/{backend=0} /^  name: .*backend$$/{backend=1} backend && /secretKey: DB_PASSWORD/{password=1} backend && password && /key: k8s-stack-manager-mysql-root-password$$/{key=1} backend && password && /property: root-password$$/{property=1} backend && password && /version: root-v1$$/{version=1} END{exit !(password && key && property && version)}' "$$template_file"; \
			helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/external-secrets/external-secret.yaml \
				--values $(HELM_CHART)/tests/external-secrets-values.yaml --set mysql.enabled=false \
				--set externalSecrets.data[2].secretKey=DB_PASSWORD \
				--set externalSecrets.data[2].remoteRef.key=k8s-stack-manager-external-db-password \
				--set externalSecrets.data[2].remoteRef.property=external-password \
				--set externalSecrets.data[2].remoteRef.version=external-v1 > "$$template_file"; \
			awk '/^kind: ExternalSecret$$/{backend=0} /^  name: .*backend$$/{backend=1} backend && /secretKey: DB_PASSWORD/{password=1} backend && password && /key: k8s-stack-manager-external-db-password$$/{key=1} backend && password && /property: external-password$$/{property=1} backend && password && /version: external-v1$$/{version=1} END{exit !(password && key && property && version)}' "$$template_file"; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.data[0].secretKey=UNUSED >/dev/null 2>&1; then exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.data[0].secretKey=UNUSED 2>&1 | grep -q 'must include a JWT_SECRET mapping'; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.data[2].secretKey=UNUSED >/dev/null 2>&1; then exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.data[2].secretKey=UNUSED 2>&1 | grep -q 'must include a MYSQL_ROOT_PASSWORD mapping'; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.data[3].secretKey=UNUSED >/dev/null 2>&1; then exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.data[3].secretKey=UNUSED 2>&1 | grep -q 'must include a MYSQL_PASSWORD mapping'; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.secretStore.create=false --set-string externalSecrets.secretStore.name= >/dev/null 2>&1; then exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set externalSecrets.secretStore.create=false --set-string externalSecrets.secretStore.name= 2>&1 | grep -q 'externalSecrets.secretStore.name must be set'; \
	for kind in deployment rollout; do \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/$$kind.yaml \
			--values $(HELM_CHART)/tests/backend-extra-values.yaml \
			--set argoRollouts.enabled=$$( [ $$kind = rollout ] && echo true || echo false ) > "$$template_file"; \
		grep -q 'name: HELM_REGISTRY_CONFIG' "$$template_file"; \
		grep -q 'mountPath: /etc/helm-registry' "$$template_file"; \
		grep -q 'secretName: helm-registry-config' "$$template_file"; \
	done; \
	cp -R $(HELM_CHART) "$$notes_dir/chart"; \
	cp "$$notes_dir/chart/templates/NOTES.txt" "$$notes_dir/chart/templates/notes-render.txt"; \
	helm template $(HELM_RELEASE) "$$notes_dir/chart" --show-only templates/notes-render.txt --debug \
		> "$$template_file" 2>&1 || true; \
	grep -q 'WARNING: backend.secrets.JWT_SECRET is still the placeholder value' "$$template_file" \
		|| { echo "NOTES must warn about the JWT_SECRET placeholder" >&2; exit 1; }; \
	grep -q 'WARNING: backend.secrets.ADMIN_PASSWORD' "$$template_file" \
		|| { echo "NOTES must warn about the ADMIN_PASSWORD placeholder" >&2; exit 1; }; \
	if grep -q 'WARNING: CORS_ALLOWED_ORIGINS' "$$template_file"; then echo "NOTES must not warn about CORS with the default same-origin" >&2; exit 1; fi; \
	if grep -q 'WARNING: SELF_REGISTRATION' "$$template_file"; then echo "NOTES must not warn about SELF_REGISTRATION with the default false" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) "$$notes_dir/chart" --show-only templates/notes-render.txt --debug \
		--set-string 'backend.env.CORS_ALLOWED_ORIGINS=*' --set-string backend.env.SELF_REGISTRATION=true > "$$template_file" 2>&1 || true; \
	grep -q 'WARNING: CORS_ALLOWED_ORIGINS allows all origins' "$$template_file" \
		|| { echo "NOTES must warn about CORS_ALLOWED_ORIGINS=*" >&2; exit 1; }; \
	grep -q 'WARNING: SELF_REGISTRATION is "true"' "$$template_file" \
		|| { echo "NOTES must warn about SELF_REGISTRATION=true" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) "$$notes_dir/chart" --show-only templates/notes-render.txt --debug \
		--values $(HELM_CHART)/tests/external-secrets-values.yaml \
		--set-string 'backend.env.CORS_ALLOWED_ORIGINS=*' > "$$template_file" 2>&1 || true; \
	grep -q 'WARNING: CORS_ALLOWED_ORIGINS' "$$template_file" \
		|| { echo "NOTES did not render with externalSecrets.enabled" >&2; exit 1; }; \
	if grep -Eq 'WARNING: (backend.secrets|mysql.auth)' "$$template_file"; then echo "NOTES must not warn about placeholder secrets with externalSecrets.enabled" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/deployment.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	if grep -q 'name: HELM_REGISTRY_CONFIG' "$$template_file"; then echo "unexpected match: HELM_REGISTRY_CONFIG" >&2; exit 1; fi; \
	for kind in deployment rollout; do \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/$$kind.yaml \
			--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
			--set argoRollouts.enabled=$$( [ $$kind = rollout ] && echo true || echo false ) > "$$template_file"; \
		awk '/- name: POD_NAME$$/{n=1} n==1 && /fieldPath: metadata.name$$/{name=1; n=0} /- name: POD_NAMESPACE$$/{s=1} s==1 && /fieldPath: metadata.namespace$$/{ns=1; s=0} END{exit !(name && ns)}' "$$template_file" \
			|| { echo "$$kind: POD_NAME/POD_NAMESPACE downward API env missing" >&2; exit 1; }; \
	done; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	grep -q 'Source: k8s-stack-manager/templates/backend/leader-election-rbac.yaml' "$$template_file" \
		|| { echo "leader election Role/RoleBinding missing" >&2; exit 1; }; \
	awk '/^kind: Role$$/{role=1} role && /apiGroups: \["coordination.k8s.io"\]/{api=1} role && api && /verbs: \["create"\]/{create=1} role && api && /verbs: \["get", "update"\]/{getupd=1} /^---/{role=0} END{exit !(create && getupd)}' "$$template_file" \
		|| { echo "leader election Role must allow leases create, get, update" >&2; exit 1; }; \
	awk '/^kind: RoleBinding$$/{rb=1} rb && /kind: Role$$/{ref=1} rb && /kind: ServiceAccount$$/{sa=1} rb && sa && /name: .*-backend$$/{name=1} /^---/{rb=0; sa=0} END{exit !(ref && name)}' "$$template_file" \
		|| { echo "leader election RoleBinding must bind the backend service account" >&2; exit 1; }; \
	grep -q 'LEADER_ELECTION_ENABLED: "true"' "$$template_file" \
		|| { echo "LEADER_ELECTION_ENABLED must default to true in the chart" >&2; exit 1; }; \
	grep -q 'LEADER_ELECTION_LEASE_NAME: "$(HELM_RELEASE)-workers"' "$$template_file" \
		|| { echo "LEADER_ELECTION_LEASE_NAME missing" >&2; exit 1; }; \
	grep -q 'WS_FANOUT_ENABLED: "true"' "$$template_file" \
		|| { echo "WS_FANOUT_ENABLED must default to true in the chart" >&2; exit 1; }; \
	grep -q 'WS_FANOUT_POLL_INTERVAL: "500ms"' "$$template_file" \
		|| { echo "WS_FANOUT_POLL_INTERVAL missing" >&2; exit 1; }; \
	grep -q 'WS_FANOUT_RETENTION: "5m"' "$$template_file" \
		|| { echo "WS_FANOUT_RETENTION missing" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set backend.wsFanout.enabled=false > "$$template_file"; \
	grep -q 'WS_FANOUT_ENABLED: "false"' "$$template_file" \
		|| { echo "backend.wsFanout.enabled=false must render WS_FANOUT_ENABLED false" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set backend.leaderElection.enabled=false > "$$template_file"; \
	if grep -q 'Source: k8s-stack-manager/templates/backend/leader-election-rbac.yaml' "$$template_file"; then echo "unexpected leader election Role with leaderElection.enabled=false" >&2; exit 1; fi; \
	grep -q 'LEADER_ELECTION_ENABLED: "false"' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	if grep -q 'SECURE_COOKIES' "$$template_file"; then echo "unexpected match: SECURE_COOKIES" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.traefik.tls.secretName=stack-manager-tls > "$$template_file"; \
	grep -q 'SECURE_COOKIES: "true"' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.type=ingress --set ingress.tls[0].secretName=stack-manager-tls > "$$template_file"; \
	grep -q 'SECURE_COOKIES: "true"' "$$template_file"; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.traefik.tls.secretName=stack-manager-tls --set backend.env.SECURE_COOKIES=false > "$$template_file"; \
	grep -q 'SECURE_COOKIES: "false"' "$$template_file"; \
	[ "$$(grep -c 'SECURE_COOKIES' "$$template_file")" -eq 1 ]; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.host=stacks.example.com --set ingress.traefik.tls.secretName=stack-manager-tls > "$$template_file"; \
	grep -q 'APP_PUBLIC_URL: "https://stacks.example.com"' "$$template_file" \
		|| { echo "APP_PUBLIC_URL must be https://<ingress.host> with TLS (traefik)" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.type=ingress --set ingress.host=stacks.example.com --set ingress.tls[0].secretName=stack-manager-tls > "$$template_file"; \
	grep -q 'APP_PUBLIC_URL: "https://stacks.example.com"' "$$template_file" \
		|| { echo "APP_PUBLIC_URL must be https://<ingress.host> with TLS (ingress)" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.host=stacks.example.com --set ingress.traefik.tls.secretName=stack-manager-tls \
		--set backend.env.APP_PUBLIC_URL=https://ui.example.org > "$$template_file"; \
	grep -q 'APP_PUBLIC_URL: "https://ui.example.org"' "$$template_file" \
		|| { echo "an explicit backend.env.APP_PUBLIC_URL must win" >&2; exit 1; }; \
	[ "$$(grep -c 'APP_PUBLIC_URL' "$$template_file")" -eq 1 ] \
		|| { echo "APP_PUBLIC_URL must render once" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.host=stacks.example.com > "$$template_file"; \
	if grep -q 'APP_PUBLIC_URL' "$$template_file"; then echo "unexpected APP_PUBLIC_URL without TLS" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set ingress.traefik.tls.secretName=stack-manager-tls > "$$template_file"; \
	if grep -q 'APP_PUBLIC_URL' "$$template_file"; then echo "unexpected APP_PUBLIC_URL without ingress.host" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	if grep -q 'NAMESPACE_LABELS' "$$template_file"; then echo "unexpected NAMESPACE_LABELS without backend.namespaceLabels" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--values $(HELM_CHART)/tests/namespace-labels-values.yaml > "$$template_file"; \
	grep -q 'NAMESPACE_LABELS: "pod-security.kubernetes.io/enforce=baseline,pod-security.kubernetes.io/warn=restricted"' "$$template_file" \
		|| { echo "backend.namespaceLabels must render sorted NAMESPACE_LABELS" >&2; exit 1; }; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/namespace-labels-values.yaml \
		--set backend.namespaceLabels.managed-by=x > "$$template_file" 2>&1; then echo "backend.namespaceLabels.managed-by must fail" >&2; exit 1; fi; \
	grep -q 'the key managed-by is reserved' "$$template_file" || { echo "missing managed-by error" >&2; exit 1; }; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/namespace-labels-values.yaml \
		--set 'backend.namespaceLabels.k8s-stack-manager\.io/instance-name=x' > "$$template_file" 2>&1; then echo "backend.namespaceLabels instance-name must fail" >&2; exit 1; fi; \
	grep -q 'the key k8s-stack-manager.io/instance-name is reserved' "$$template_file" || { echo "missing instance-name error" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	if grep -q 'Source: k8s-stack-manager/templates/frontend/branding-configmap.yaml' "$$template_file"; then echo "unexpected branding ConfigMap without branding.files" >&2; exit 1; fi; \
	if grep -qE 'APP_(TITLE|LOGO_URL|FAVICON_URL):|location /branding/|mountPath: /usr/share/nginx/html/branding' "$$template_file"; then echo "unexpected branding settings in the default render" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--values $(HELM_CHART)/tests/branding-values.yaml > "$$template_file"; \
	grep -q 'APP_TITLE: "Platform Portal"' "$$template_file" || { echo "branding.title must render APP_TITLE" >&2; exit 1; }; \
	grep -q 'APP_LOGO_URL: "/branding/logo.svg"' "$$template_file" || { echo "branding.logoUrl must render APP_LOGO_URL" >&2; exit 1; }; \
	grep -q 'APP_FAVICON_URL: "/branding/favicon.png"' "$$template_file" || { echo "branding.faviconUrl must render APP_FAVICON_URL" >&2; exit 1; }; \
	grep -q 'Source: k8s-stack-manager/templates/frontend/branding-configmap.yaml' "$$template_file" || { echo "branding ConfigMap missing" >&2; exit 1; }; \
	grep -q '"logo.svg": "<svg' "$$template_file" || { echo "branding.files must render into data" >&2; exit 1; }; \
	awk '/^binaryData:$$/{b=1} b && /"favicon.png": "iVBOR/{f=1} END{exit !f}' "$$template_file" || { echo "branding.binaryFiles must render into binaryData" >&2; exit 1; }; \
	grep -q 'location /branding/' "$$template_file" || { echo "nginx /branding/ location missing" >&2; exit 1; }; \
	grep -q 'image/svg+xml svg;' "$$template_file" || { echo "nginx must serve svg as image/svg+xml" >&2; exit 1; }; \
	if grep -q 'svgz' "$$template_file"; then echo "nginx must not map svgz" >&2; exit 1; fi; \
	grep -q 'add_header X-Content-Type-Options "nosniff" always;' "$$template_file" || { echo "nginx /branding/ must send nosniff" >&2; exit 1; }; \
	for kind in deployment rollout; do \
		helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/frontend/$$kind.yaml \
			--values $(HELM_CHART)/tests/branding-values.yaml \
			--set argoRollouts.enabled=$$( [ $$kind = rollout ] && echo true || echo false ) > "$$template_file"; \
		grep -q 'mountPath: /usr/share/nginx/html/branding' "$$template_file" || { echo "$$kind: branding mount missing" >&2; exit 1; }; \
		grep -qE 'name: [a-z0-9-]+-frontend-branding$$' "$$template_file" || { echo "$$kind: branding volume missing" >&2; exit 1; }; \
		grep -q 'checksum/branding:' "$$template_file" || { echo "$$kind: branding checksum missing" >&2; exit 1; }; \
	done; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--values $(HELM_CHART)/tests/branding-values.yaml --set backend.env.APP_TITLE=Override > "$$template_file"; \
	grep -q 'APP_TITLE: "Override"' "$$template_file" || { echo "backend.env.APP_TITLE must win over branding.title" >&2; exit 1; }; \
	[ "$$(grep -c 'APP_TITLE' "$$template_file")" -eq 1 ]; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/branding-values.yaml \
		--set 'branding.files.bad/name\.svg=x' > "$$template_file" 2>&1; then echo "invalid branding file name must fail" >&2; exit 1; fi; \
	grep -q 'branding.files: invalid file name' "$$template_file" || { echo "missing file name error" >&2; exit 1; }; \
	big_file=$$(mktemp); \
	head -c 930000 /dev/zero | tr '\0' a > "$$big_file"; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/branding-values.yaml \
		--set-file "branding.files.big\.svg=$$big_file" > "$$template_file" 2>&1; then rm -f "$$big_file"; echo "branding files over 900 KiB must fail" >&2; exit 1; fi; \
	rm -f "$$big_file"; \
	grep -q 'the limit is 900 KiB' "$$template_file" || { echo "missing branding size error" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --show-only templates/backend/configmap.yaml \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	grep -q 'STACK_PORT_FORWARD_ENABLED: "false"' "$$template_file" && grep -q 'STACK_PORT_FORWARD_MAX_DURATION: "1h"' "$$template_file" \
		&& grep -q 'STACK_PORT_FORWARD_IDLE_TIMEOUT: "15m"' "$$template_file" && grep -q 'STACK_PORT_FORWARD_MAX_PER_USER: "5"' "$$template_file" \
		&& grep -q 'STACK_STREAM_ALLOW_API_KEYS: "false"' "$$template_file" \
		|| { echo "backend ConfigMap must render the port-forward defaults (off)" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/port-forward.yaml \
		--show-only templates/backend/configmap.yaml > "$$template_file"; \
	grep -q 'STACK_PORT_FORWARD_ENABLED: "true"' "$$template_file" && grep -q 'STACK_PORT_FORWARD_MAX_DURATION: "2h"' "$$template_file" \
		&& grep -q 'STACK_PORT_FORWARD_IDLE_TIMEOUT: "30m"' "$$template_file" && grep -q 'STACK_PORT_FORWARD_MAX_PER_USER: "8"' "$$template_file" \
		&& grep -q 'STACK_STREAM_ALLOW_API_KEYS: "true"' "$$template_file" \
		|| { echo "tests/port-forward.yaml must render the five stream variables" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/port-forward.yaml \
		--show-only templates/backend/clusterrole.yaml > "$$template_file"; \
	grep -q 'resources: \["pods/portforward"\]' "$$template_file" \
		|| { echo "cluster scope with backend.portForward.enabled must grant pods/portforward" >&2; exit 1; }; \
	grep -q 'name: .*-backend-stream-scope' "$$template_file" && grep -q 'operations: \["CONNECT"\]' "$$template_file" \
		|| { echo "cluster scope must render the stream admission policy" >&2; exit 1; }; \
	if grep -q 'name: .*-backend-scope$$' "$$template_file"; then echo "cluster scope must not render the namespace-scope policy" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/port-forward.yaml \
		--set backend.portForward.enabled=false --show-only templates/backend/clusterrole.yaml > "$$template_file"; \
	if grep -q 'resources: \["pods/portforward"\]' "$$template_file"; then echo "backend.portForward.enabled=false must not grant pods/portforward" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --values $(HELM_CHART)/tests/port-forward.yaml \
		--set backend.rbac.admissionPolicy.enabled=false --show-only templates/backend/clusterrole.yaml > "$$template_file"; \
	if grep -q 'ValidatingAdmissionPolicy' "$$template_file"; then echo "admissionPolicy.enabled=false must render no policy" >&2; exit 1; fi; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--show-only templates/backend/clusterrole.yaml > "$$template_file"; \
	if grep -q 'resources: \["pods/portforward"\]' "$$template_file"; then echo "namespace scope must not add a pods/portforward rule" >&2; exit 1; fi; \
	grep -q 'name: .*-backend-scope$$' "$$template_file" && ! grep -q 'backend-stream-scope' "$$template_file" \
		|| { echo "namespace scope must keep its own policy only" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set metrics.enabled=true --set metrics.grafanaDashboard.enabled=true \
		--show-only templates/grafana-dashboard.yaml > "$$template_file"; \
	grep -q 'grafana_dashboard: "1"' "$$template_file" || { echo "missing grafana_dashboard label" >&2; exit 1; }; \
	grep -q '"uid": "k8s-stack-manager"' "$$template_file" || { echo "missing dashboard JSON in the ConfigMap" >&2; exit 1; }; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set metrics.enabled=true --set metrics.grafanaDashboard.enabled=true \
		--set metrics.grafanaDashboard.namespace=grafana --set metrics.grafanaDashboard.annotations.grafana_folder=Platform \
		--show-only templates/grafana-dashboard.yaml > "$$template_file"; \
	grep -q '  namespace: grafana' "$$template_file" || { echo "grafanaDashboard.namespace not rendered" >&2; exit 1; }; \
	grep -q 'grafana_folder: Platform' "$$template_file" || { echo "grafanaDashboard.annotations not rendered" >&2; exit 1; }; \
	if helm template $(HELM_RELEASE) $(HELM_CHART) --set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template \
		--set metrics.grafanaDashboard.enabled=true > "$$template_file" 2>&1; then echo "grafanaDashboard without metrics.enabled must fail" >&2; exit 1; fi; \
	grep -q 'metrics.grafanaDashboard.enabled needs metrics.enabled' "$$template_file" || { echo "missing grafanaDashboard error" >&2; exit 1; }

helm-template: ## Render templates locally (dry-run)
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template

helm-install: ## Install the chart into the current cluster
	@if [ -z "$${JWT_SECRET}" ]; then \
		echo "Error: JWT_SECRET environment variable must be set for helm-install."; \
		echo "Usage: JWT_SECRET=your-secret make helm-install"; \
		exit 1; \
	fi
	helm install $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		--set backend.secrets.JWT_SECRET=$${JWT_SECRET}

helm-upgrade: ## Upgrade an existing release
	@if [ -z "$${JWT_SECRET}" ]; then \
		echo "Error: JWT_SECRET environment variable must be set for helm-upgrade."; \
		echo "Usage: JWT_SECRET=your-secret make helm-upgrade"; \
		exit 1; \
	fi
	helm upgrade $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NAMESPACE) \
		--set backend.secrets.JWT_SECRET=$${JWT_SECRET}

helm-uninstall: ## Uninstall the release
	helm uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

helm-release: helm-lint ## Package the Helm chart for release (CI uses chart-releaser-action)
	@echo "Packaging Helm chart $(HELM_CHART)..."
	helm package $(HELM_CHART) --destination .cr-release-packages
	@echo "Chart packaged in .cr-release-packages/. Actual GitHub release is handled by .github/workflows/helm-release.yml"


