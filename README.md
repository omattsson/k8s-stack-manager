# K8s Stack Manager

A web application for configuring, storing, and managing multi-service Helm-based application stacks for deployment to a shared Kubernetes cluster.

Developers create **stack definitions** (collections of Helm charts with configuration), launch **stack instances** (per-developer copies with branch and value overrides), and manage everything through an audit-logged UI with Git provider integration.

## Architecture

```
Frontend (React + MUI + TypeScript)
        │
        ▼
Backend (Go + Gin)
  ├── REST API with JWT auth
  ├── Azure Table Storage (Azurite for local dev)
  ├── Git Provider (Azure DevOps + GitLab)
  ├── Helm Values (deep merge + template substitution)
  └── Audit Logging
```

## Quick Start

### Prerequisites
- Docker and Docker Compose
- Go 1.24+ (for local backend development)
- Node.js 20+ (for local frontend development)

### Start with Docker Compose

```bash
make dev
```

This starts all services:
- **Frontend**: http://localhost:3000
- **Backend API**: http://localhost:8081
- **Swagger docs**: http://localhost:8081/swagger/index.html
- **Azurite** (Azure Table Storage emulator): localhost:10002

Default admin credentials: `admin` / `admin` (configured in docker-compose.yml).

### Start Locally (without Docker)

```bash
# Start Azurite
make azurite-start

# Run backend
make dev-local

# In another terminal — run frontend
cd frontend && npm install && npm run dev
```

## Commands

| Command | Description |
|---|---|
| `make dev` | Start full stack via Docker Compose |
| `make dev-local` | Run backend locally against Azurite |
| `make azurite-start` | Start Azurite container |
| `make test` | Run all tests (backend + frontend) |
| `make test-backend` | Backend unit tests |
| `make test-frontend` | Frontend unit tests |
| `make test-backend-all` | Backend unit + integration tests |
| `make test-e2e` | End-to-end Playwright tests |
| `make docs` | Regenerate Swagger documentation |
| `make lint` | Lint backend + frontend |
| `make clean` | Stop containers and remove volumes |
| `make install` | Install all dependencies |

## Project Structure

```
├── backend/                    # Go API server
│   ├── api/main.go            # Application entry point
│   ├── internal/
│   │   ├── api/               # Handlers, middleware, routes
│   │   ├── config/            # Environment-based configuration
│   │   ├── database/azure/    # Azure Table Storage repositories
│   │   ├── gitprovider/       # Azure DevOps + GitLab integration
│   │   ├── helm/              # Values merge + template substitution
│   │   ├── models/            # Domain models + interfaces
│   │   └── websocket/         # Real-time event broadcasting
│   └── docs/                  # Swagger/OpenAPI
├── frontend/                   # React SPA
│   └── src/
│       ├── api/               # API client + types
│       ├── components/        # Shared UI components
│       ├── context/           # Auth + WebSocket contexts
│       ├── pages/             # Page components
│       └── routes.tsx         # Route definitions
└── docker-compose.yml
```

## API Overview

| Group | Prefix | Description |
|-------|--------|-------------|
| Auth | `/api/v1/auth` | Login, register, current user |
| Templates | `/api/v1/templates` | Stack template CRUD, publish, instantiate |
| Definitions | `/api/v1/stack-definitions` | Stack definition CRUD, chart configs |
| Instances | `/api/v1/stack-instances` | Stack instance CRUD, clone, export |
| Overrides | `/api/v1/stack-instances/:id/overrides` | Per-chart value overrides |
| Git | `/api/v1/git` | Branch listing, validation |
| Audit Logs | `/api/v1/audit-logs` | Filterable audit trail |
| Health | `/health/*` | Liveness + readiness |

## Configuration

Key environment variables (see `docker-compose.yml` for full list):

| Variable | Required | Description |
|---|---|---|
| `JWT_SECRET` | Yes | JWT signing secret (min 16 chars) |
| `ADMIN_PASSWORD` | Yes | Initial admin password |
| `USE_AZURE_TABLE` | No | `true` to use Azure Table Storage |
| `USE_AZURITE` | No | `true` to use Azurite emulator |
| `AZURE_DEVOPS_PAT` | No | Azure DevOps personal access token |
| `GITLAB_TOKEN` | No | GitLab access token |
| `DEFAULT_BRANCH` | No | Default Git branch (default: `master`) |

## Testing

```bash
make test                    # All unit tests
make test-backend-all        # Backend unit + integration
make test-e2e                # Playwright end-to-end
cd backend && make test-coverage  # Coverage report (80% threshold)
```

## License

See [LICENSE](LICENSE).
