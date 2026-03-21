.PHONY: dev seed dev-backend dev-frontend dev-local dev-local-backend dev-local-frontend prod prod-backend prod-frontend build clean prune test test-backend test-backend-integration test-backend-all test-frontend test-e2e integration-infra-start integration-infra-stop install docs fmt lint azurite-start azurite-stop loadtest loadtest-start loadtest-start-backend loadtest-start-frontend loadtest-stop loadtest-stop-backend loadtest-stop-frontend loadtest-backend loadtest-backend-run loadtest-frontend loadtest-frontend-run

# Development mode for both services
dev:
	NODE_ENV=development GO_ENV=development PORT=3000 BACKEND_PORT=8081 GIN_MODE=debug docker compose up --build --remove-orphans

# Development mode with local K8s cluster access (rancher-desktop, Docker Desktop K8s, etc.)
dev-k8s:
	NODE_ENV=development GO_ENV=development PORT=3000 BACKEND_PORT=8081 GIN_MODE=debug docker compose -f docker-compose.yml -f docker-compose.k8s.yml up --build --remove-orphans

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
	NODE_ENV=production GO_ENV=production PORT=80 BACKEND_PORT=8080 GIN_MODE=release docker compose up --build --remove-orphans

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

# Start infrastructure for integration tests
integration-infra-start:
	@echo "Starting integration test infrastructure..."
	GO_ENV=test docker compose up -d azurite
	@echo "Waiting for Azurite to be ready..."
	@until curl -s -o /dev/null http://localhost:10002/ 2>/dev/null; do \
		echo "Waiting for Azurite..."; \
		sleep 2; \
	done
	@echo "Integration infrastructure is ready!"

# Stop infrastructure for integration tests
integration-infra-stop:
	@echo "Stopping integration test infrastructure..."
	GO_ENV=test docker compose stop azurite

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
test-e2e: integration-infra-start
	@echo "Building and starting backend..."
	@cd backend && go build -o tmp/main ./api/main.go
	@cd backend && USE_AZURE_TABLE=true USE_AZURITE=true \
		AZURE_TABLE_ACCOUNT_NAME=devstoreaccount1 \
		AZURE_TABLE_ACCOUNT_KEY="Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==" \
		AZURE_TABLE_ENDPOINT=127.0.0.1:10002 \
		JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
		ADMIN_USERNAME=admin ADMIN_PASSWORD=admin SELF_REGISTRATION=true \
		HELM_BINARY=$${HELM_BINARY:-helm} \
		KUBECONFIG_PATH=$${KUBECONFIG_PATH:-$$HOME/.kube/config} \
		RATE_LIMIT=10000 PORT=8081 ./tmp/main &
	@echo "Waiting for backend to be healthy..."
	@until curl -sf http://localhost:8081/health/live >/dev/null 2>&1; do \
		sleep 1; \
	done
	@echo "Backend is ready, running Playwright tests..."
	cd frontend && npx playwright test || (kill $$(lsof -ti:8081) 2>/dev/null; $(MAKE) integration-infra-stop; exit 1)
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
	USE_AZURE_TABLE=true USE_AZURITE=true \
	AZURE_TABLE_ACCOUNT_NAME=devstoreaccount1 \
	AZURE_TABLE_ACCOUNT_KEY="Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==" \
	AZURE_TABLE_ENDPOINT=127.0.0.1:10002 \
	JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
	ADMIN_USERNAME=admin ADMIN_PASSWORD=admin \
	SELF_REGISTRATION=true \
	HELM_BINARY=$${HELM_BINARY:-helm} \
	KUBECONFIG_PATH=$${KUBECONFIG_PATH:-$$HOME/.kube/config} \
	PORT=8081 GIN_MODE=debug

# Run backend locally against Azurite with hot reload.
# Uses 'air' (Go live reload) if installed, otherwise falls back to 'go run'.
# Install air: go install github.com/air-verse/air@latest
GO_AIR := $(shell go env GOPATH 2>/dev/null)/bin/air
dev-local-backend:
	@if [ -x "$(GO_AIR)" ]; then \
		echo "Using air for hot reload..."; \
		cd backend && $(DEV_LOCAL_ENV) $(GO_AIR); \
	else \
		echo "air not found — using go run (no hot reload). Install: go install github.com/air-verse/air@latest"; \
		cd backend && $(DEV_LOCAL_ENV) go run ./api/main.go; \
	fi

# Run frontend dev server locally with HMR (port 3000)
dev-local-frontend:
	cd frontend && npm run dev

# Run both backend and frontend locally with hot reload (no Docker except Azurite)
# Ctrl+C stops both processes. Backend: port 8081, Frontend: port 3000
dev-local: azurite-start
	@echo "Starting backend (port 8081) and frontend (port 3000)..."
	@trap 'kill 0' EXIT; \
	$(MAKE) dev-local-backend & \
	$(MAKE) dev-local-frontend & \
	wait

azurite-start:
	@echo "Starting Azurite..."
	docker compose up -d azurite
	@echo "Waiting for Azurite to be ready..."
	@until curl -s -o /dev/null http://localhost:10002/ 2>/dev/null; do \
		echo "Waiting for Azurite..."; \
		sleep 1; \
	done
	@echo "Azurite is ready!"

azurite-stop:
	docker compose stop azurite

# ── Load Testing ─────────────────────────────────────────────────────
# Starts backend in release mode with high rate limit, runs tests, then cleans up.
# Requires: k6 (brew install k6) for backend tests, Azurite running.

# Env vars for load test backend (release mode, high rate limit, no debug logging)
LOADTEST_ENV = \
	USE_AZURE_TABLE=true USE_AZURITE=true \
	AZURE_TABLE_ACCOUNT_NAME=$${AZURE_TABLE_ACCOUNT_NAME:-devstoreaccount1} \
	AZURE_TABLE_ACCOUNT_KEY=$${AZURE_TABLE_ACCOUNT_KEY:-Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==} \
	AZURE_TABLE_ENDPOINT=$${AZURE_TABLE_ENDPOINT:-127.0.0.1:10002} \
	JWT_SECRET=$${JWT_SECRET:-dev-secret-change-in-production-minimum-16-chars} \
	ADMIN_USERNAME=$${ADMIN_USERNAME:-admin} ADMIN_PASSWORD=$${ADMIN_PASSWORD:-admin} \
	SELF_REGISTRATION=true \
	HELM_BINARY=$${HELM_BINARY:-helm} \
	KUBECONFIG_PATH=$${KUBECONFIG_PATH:-$$HOME/.kube/config} \
	RATE_LIMIT=10000 PORT=8081 GIN_MODE=release

loadtest-start-backend: azurite-start ## Build and start backend in release mode for load testing
	@echo "Building backend (release mode)..."
	@cd backend && mkdir -p tmp
	@cd backend && go build -o tmp/main ./api/main.go
	@echo "Starting backend (GIN_MODE=release, RATE_LIMIT=10000)..."
	@cd backend && ( $(LOADTEST_ENV) ./tmp/main > tmp/loadtest.log 2>&1 & echo $$! > tmp/loadtest-backend.pid )
	@until curl -sf http://localhost:8081/health/live >/dev/null 2>&1; do sleep 1; done
	@echo "Backend is ready on :8081 (logs: backend/tmp/loadtest.log)"

loadtest-start-frontend: ## Start frontend dev server for load testing
	@echo "Starting frontend dev server..."
	@cd frontend && ( npm run dev > /tmp/loadtest-frontend.log 2>&1 & echo $$! > /tmp/loadtest-frontend.pid )
	@until curl -sf http://localhost:3000 >/dev/null 2>&1; do sleep 1; done
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

loadtest-frontend: loadtest-start ## Run Playwright frontend load tests (starts/stops backend)
	@$(MAKE) loadtest-frontend-run || ($(MAKE) loadtest-stop; exit 1)
	@$(MAKE) loadtest-stop

loadtest-frontend-run: ## Run Playwright load tests (assumes backend already running)
	@echo "Running frontend load tests (workers=$${LOAD_WORKERS:-5})..."
	cd frontend && NODE_PATH=node_modules npx playwright test --config=../loadtest/frontend/playwright.config.ts


