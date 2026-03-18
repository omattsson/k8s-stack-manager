.PHONY: dev dev-backend dev-frontend dev-local prod prod-backend prod-frontend mysql mysql-start mysql-stop mysql-logs mysql-shell build clean prune test test-backend test-backend-integration test-backend-all test-frontend test-e2e integration-infra-start integration-infra-stop install docs fmt lint azurite-start azurite-stop

# MySQL service management
mysql-start:
	@echo "Starting MySQL container..."
	GO_ENV=development docker compose up -d db
	@echo "Waiting for MySQL to be ready..."
	@until docker compose exec db mysqladmin ping -h localhost -u$${MYSQL_USER:-appuser} -p$${MYSQL_PASSWORD:-apppass} --silent; do \
		echo "Waiting for MySQL..."; \
		sleep 2; \
	done
	@echo "MySQL is ready!"

mysql-stop:
	@echo "Stopping MySQL container..."
	docker compose stop db

mysql-logs:
	docker compose logs -f db

mysql-shell:
	docker compose exec db mysql -u$${MYSQL_USER:-appuser} -p$${MYSQL_PASSWORD:-apppass} $${MYSQL_DATABASE:-app}

# Development mode for both services
dev:
	NODE_ENV=development PORT=3000 BACKEND_PORT=8081 GIN_MODE=debug docker compose up --build

# Development mode for backend only
dev-backend:
	NODE_ENV=development BACKEND_PORT=8081 GIN_MODE=debug docker compose up --build backend

# Development mode for frontend only
dev-frontend:
	NODE_ENV=development PORT=3000 docker compose up --build frontend

# Production mode for both services
prod:
	NODE_ENV=production PORT=80 BACKEND_PORT=8080 GIN_MODE=release docker compose up --build

# Production mode for backend only
prod-backend:
	NODE_ENV=production BACKEND_PORT=8080 GIN_MODE=release docker compose up --build backend

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
	GO_ENV=test docker compose up -d db azurite
	@echo "Waiting for MySQL to be ready..."
	@until docker compose exec db mysqladmin ping -h localhost -u$${MYSQL_USER:-appuser} -p$${MYSQL_PASSWORD:-apppass} --silent 2>/dev/null; do \
		echo "Waiting for MySQL..."; \
		sleep 2; \
	done
	@echo "Waiting for Azurite to be ready..."
	@until curl -s -o /dev/null http://localhost:10002/ 2>/dev/null; do \
		echo "Waiting for Azurite..."; \
		sleep 2; \
	done
	@echo "Integration infrastructure is ready!"

# Stop infrastructure for integration tests
integration-infra-stop:
	@echo "Stopping integration test infrastructure..."
	GO_ENV=test docker compose stop db azurite

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
	@cd backend && DB_HOST=localhost DB_PORT=3306 DB_NAME=$${MYSQL_DATABASE:-app} DB_USER=$${MYSQL_USER:-appuser} DB_PASSWORD=$${MYSQL_PASSWORD:-apppass} \
		USE_AZURE_TABLE=true USE_AZURITE=true \
		AZURE_TABLE_ACCOUNT_NAME=devstoreaccount1 \
		AZURE_TABLE_ACCOUNT_KEY="Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==" \
		AZURE_TABLE_ENDPOINT=127.0.0.1:10002 \
		JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
		ADMIN_USERNAME=admin ADMIN_PASSWORD=admin SELF_REGISTRATION=true \
		PORT=8081 ./tmp/main &
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

# Run backend locally against Azurite (start Azurite first with: docker compose up -d azurite)
dev-local:
	cd backend && \
	USE_AZURE_TABLE=true USE_AZURITE=true \
	AZURE_TABLE_ACCOUNT_NAME=devstoreaccount1 \
	AZURE_TABLE_ACCOUNT_KEY="Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==" \
	AZURE_TABLE_ENDPOINT=127.0.0.1:10002 \
	JWT_SECRET="dev-secret-change-in-production-minimum-16-chars" \
	ADMIN_USERNAME=admin ADMIN_PASSWORD=admin \
	SELF_REGISTRATION=true \
	go run ./api/main.go

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


