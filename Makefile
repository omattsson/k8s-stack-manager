.PHONY: dev dev-otel dev-api-only compose-api-only seed dev-backend dev-frontend dev-local dev-local-backend dev-local-frontend prod prod-backend prod-frontend build clean prune test test-backend test-backend-integration test-backend-all test-frontend test-e2e integration-infra-start integration-infra-stop install docs fmt lint loadtest loadtest-start loadtest-start-backend loadtest-start-frontend loadtest-stop loadtest-stop-backend loadtest-stop-frontend loadtest-backend loadtest-backend-run loadtest-stress loadtest-stress-run loadtest-frontend loadtest-frontend-run

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
		if [ $$n -ge 30 ]; then echo "ERROR: MySQL failed to start after 30s"; exit 1; fi; \
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

test-frontend:
	cd frontend && npm test

# Run frontend e2e tests (requires MySQL running, starts backend natively)
# Logs are written to frontend/test-logs/ for post-mortem troubleshooting.
test-e2e: integration-infra-start
	@mkdir -p frontend/test-logs
	@echo "Building and starting backend..."
	@cd backend && mkdir -p tmp && go build -o tmp/main ./api
	@cd backend && \
		DB_HOST=$${DB_HOST:-127.0.0.1} DB_PORT=$${DB_PORT:-3306} \
		DB_USER=$${DB_USER:-root} DB_PASSWORD=$${DB_PASSWORD:-rootpassword} DB_NAME=$${DB_NAME:-app} \
		JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
		ADMIN_USERNAME=admin ADMIN_PASSWORD=admin SELF_REGISTRATION=true \
		HELM_BINARY=$${HELM_BINARY:-helm} \
		KUBECONFIG_PATH=$${KUBECONFIG_PATH:-$$HOME/.kube/config} \
		CORS_ALLOWED_ORIGINS=http://localhost:3000 \
		RATE_LIMIT=10000 LOGIN_RATE_LIMIT=10000 PORT=8081 \
		./tmp/main > ../frontend/test-logs/backend.log 2>&1 &
	@echo "Waiting for backend to be healthy..."
	@until curl -sf http://localhost:8081/health/live >/dev/null 2>&1; do \
		sleep 1; \
	done
	@echo "Backend is ready, running Playwright tests..."
	cd frontend && bash -o pipefail -c 'npx playwright test 2>&1 | tee test-logs/playwright.log' || \
		(kill $$(lsof -ti:8081) 2>/dev/null; $(MAKE) integration-infra-stop; exit 1)
	@echo "Stopping backend..."
	@kill $$(lsof -ti:8081) 2>/dev/null || true
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

# Shared env vars for local backend development
DEV_LOCAL_ENV = \
	DB_HOST=$${DB_HOST:-127.0.0.1} DB_PORT=$${DB_PORT:-3306} \
	DB_USER=$${DB_USER:-root} DB_PASSWORD=$${DB_PASSWORD:-rootpassword} DB_NAME=$${DB_NAME:-app} \
	JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
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
# Starts backend in release mode with high rate limit, runs tests, then cleans up.
# Requires: k6 (brew install k6) for backend tests.

# Env vars for load test backend (release mode, high rate limit, no debug logging)
LOADTEST_ENV = \
	JWT_SECRET=$${JWT_SECRET:-dev-secret-change-in-production-minimum-16-chars} \
	ADMIN_USERNAME=$${ADMIN_USERNAME:-admin} ADMIN_PASSWORD=$${ADMIN_PASSWORD:-admin} \
	SELF_REGISTRATION=true \
	HELM_BINARY=$${HELM_BINARY:-helm} \
	KUBECONFIG_PATH=$${KUBECONFIG_PATH:-$$HOME/.kube/config} \
	PPROF_ENABLED=$${PPROF_ENABLED:-true} PPROF_ADDR=$${PPROF_ADDR:-:6060} \
	RATE_LIMIT=1000000 PORT=8081 GIN_MODE=release

# Env vars for MySQL + OTel load testing
LOADTEST_MYSQL_ENV = \
	APP_DEBUG=false \
	DB_HOST=$${DB_HOST:-127.0.0.1} DB_PORT=$${DB_PORT:-3306} \
	DB_USER=$${DB_USER:-root} DB_PASSWORD=$${DB_PASSWORD:-rootpassword} DB_NAME=$${DB_NAME:-app} \
	DB_MAX_OPEN_CONNS=$${DB_MAX_OPEN_CONNS:-200} DB_MAX_IDLE_CONNS=$${DB_MAX_IDLE_CONNS:-20} \
	JWT_SECRET=$${JWT_SECRET:-dev-secret-change-in-production-minimum-16-chars} \
	ADMIN_USERNAME=$${ADMIN_USERNAME:-admin} ADMIN_PASSWORD=$${ADMIN_PASSWORD:-admin} \
	SELF_REGISTRATION=true \
	HELM_BINARY=$${HELM_BINARY:-helm} \
	KUBECONFIG_PATH=$${KUBECONFIG_PATH:-$$HOME/.kube/config} \
	PPROF_ENABLED=$${PPROF_ENABLED:-true} PPROF_ADDR=$${PPROF_ADDR:-:6060} \
	OTEL_ENABLED=true OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
	OTEL_SERVICE_NAME=k8s-stack-manager OTEL_TRACE_SAMPLE_RATE=1.0 \
	RATE_LIMIT=1000000 PORT=8081 GIN_MODE=release

loadtest-start-backend: ## Build and start backend in release mode for load testing
	@echo "Stopping Docker backend (load test uses a local binary instead)..."
	@docker compose stop backend 2>/dev/null || true
	@echo "Building backend (release mode)..."
	@cd backend && mkdir -p tmp
	@cd backend && mkdir -p tmp && go build -o tmp/main ./api
	@echo "Starting backend (GIN_MODE=release, RATE_LIMIT=1000000)..."
	@cd backend && ( $(LOADTEST_ENV) ./tmp/main > tmp/loadtest.log 2>&1 & echo $$! > tmp/loadtest-backend.pid )
	@n=0; while ! curl -sf http://localhost:8081/health/live >/dev/null 2>&1; do \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then echo "ERROR: Backend failed to start after 30s. See backend/tmp/loadtest.log"; exit 1; fi; \
		sleep 1; \
	done
	@echo "Backend is ready on :8081 (logs: backend/tmp/loadtest.log)"

loadtest-start-frontend: ## Start frontend dev server for load testing
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
	@echo "Running backend API load test..."
	k6 run loadtest/backend/k6-api.js
	@echo ""
	@echo "Running backend WebSocket load test..."
	k6 run loadtest/backend/k6-websocket.js

loadtest-stress: loadtest-start-backend ## Run stress/optimization load tests (starts/stops backend)
	@$(MAKE) loadtest-stress-run || ($(MAKE) loadtest-stop-backend; exit 1)
	@$(MAKE) loadtest-stop-backend

loadtest-stress-run: ## Run k6 stress tests (assumes backend already running)
	@command -v k6 >/dev/null 2>&1 || { echo "k6 not found. Install: brew install k6"; exit 1; }
	@echo "Running stress/optimization load tests..."
	k6 run loadtest/backend/k6-stress.js

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
		if [ $$n -ge 30 ]; then echo "ERROR: MySQL failed to start after 30s"; exit 1; fi; \
		sleep 1; \
	done
	@echo "MySQL is ready on :3306"

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
		if [ $$n -ge 30 ]; then echo "ERROR: MySQL failed to start after 30s"; exit 1; fi; \
		sleep 1; \
	done
	@echo "Waiting for OTel Collector..."
	@n=0; while ! docker compose --profile otel exec -T otel-collector /otelcol-contrib validate --config /etc/otelcol-contrib/config.yaml >/dev/null 2>&1; do \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then echo "ERROR: OTel Collector failed to start after 30s"; exit 1; fi; \
		sleep 1; \
	done
	@echo "Building backend (release mode)..."
	@cd backend && mkdir -p tmp
	@cd backend && mkdir -p tmp && go build -o tmp/main ./api
	@echo "Starting backend (MySQL + OTel, GIN_MODE=release, RATE_LIMIT=1000000)..."
	@cd backend && ( $(LOADTEST_MYSQL_ENV) ./tmp/main > tmp/loadtest-mysql.log 2>&1 & echo $$! > tmp/loadtest-mysql.pid )
	@n=0; while ! curl -sf http://localhost:8081/health/live >/dev/null 2>&1; do \
		n=$$((n+1)); \
		if [ $$n -ge 30 ]; then echo "ERROR: Backend failed to start after 30s. See backend/tmp/loadtest-mysql.log"; exit 1; fi; \
		sleep 1; \
	done
	@echo "Backend ready on :8081 (MySQL + OTel, logs: backend/tmp/loadtest-mysql.log)"

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

helm-test: ## Verify default and External Secrets Helm renders
	@set -e; \
	template_file=$$(mktemp); \
	notes_dir=$$(mktemp -d); \
	trap 'rm -f "$$template_file"; rm -rf "$$notes_dir"' EXIT; \
	helm template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) \
		--set backend.secrets.JWT_SECRET=dummy-jwt-secret-for-template > "$$template_file"; \
	grep -q 'Source: k8s-stack-manager/templates/mysql/secret.yaml' "$$template_file"; \
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
		--set mysql.auth.rootPassword=inline-root-password > "$$template_file"; \
	grep -q 'DB_PASSWORD: "aW5saW5lLXJvb3QtcGFzc3dvcmQ="' "$$template_file"; \
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
	helm template $(HELM_RELEASE) "$$notes_dir/chart" --show-only templates/notes-render.txt --debug \
		--values $(HELM_CHART)/tests/external-secrets-values.yaml > "$$template_file" 2>&1 || true; \
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
	grep -q 'the limit is 900 KiB' "$$template_file" || { echo "missing branding size error" >&2; exit 1; }

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


