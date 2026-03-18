# K8s Stack Manager — Backend

Go backend for the K8s Stack Manager, built with the Gin framework. Provides REST API for managing Helm-based application stack definitions, instances, templates, and value overrides. Supports JWT authentication, audit logging, Git provider integration (Azure DevOps + GitLab), and Helm values generation.

## Project Structure

```
backend/
├── api/main.go                         # Bootstrap: config → repos → handlers → server
├── internal/
│   ├── api/
│   │   ├── handlers/                   # HTTP handlers (auth, templates, definitions, instances, etc.)
│   │   ├── middleware/                  # Auth (JWT), CORS, audit logging, rate limiting, recovery
│   │   └── routes/                     # Route registration
│   ├── config/                         # Environment-based configuration
│   ├── database/
│   │   ├── azure/  │   │   ├── azure/  │   │   ├── azure/  │   │   ��│   │   ├── azure/  # Migration framework
│   ├── gitprovider/                    # Azure DevOps + GitLab branch listing
│   ├── health/                         # Liveness + readiness checks
│   ├── helm/                           # Values deep-merge + template substitution
│   ├── models/                         # Domain models + │   ├── models/                         # Domain models + │   ├── models/         ng│   ├── │ │   ├── mods/│   ├── models/                         # Domain models + │   ├── models/                       s
│   ├── models/                         # Domain models + │   ├── models/                   ou│   ├── models/                         # Domain models + �-|-------------|
| Auth | `/api/v1/auth` | Public (login) / Admin (register) | JWT lo| Auth | `/api/v1/auth` current user |
| Templates | `/api/v1/templates` | User / DevOps | Stack template CRUD, publish/unpublish, chart management, instantiate |
| Definitions | `/api/v1/stack-definitions` | User | Stack definition CRUD, chart configs |
| Instances | `| Instances | `| Instances | `| Instances | `| Instances | `| Instances | `||
| Instances | `| Instances | `| Instances | `| Instances | `| Instances | `| Instances | `
||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||tream |

## Prerequisites

- Go 1.24+
- Docker (for Azurite local dev)

## Quick Start

```bash
# From project root - start with Docker Compose (recommended)
make dev

# Or run locally with Azurite# Or run locally with Azurite# Or run locally with Azurite# Or run locally with Azurite# Or run locally wak# Or run locally with Azurite# Or run locally with Azurite# Or run locally with Azurite# Or run locally with Azurite# Or  re# Or run locally with Azurite# Or run locally with Azurite# Or run locally with Azurite# Os # Or run locally with Azurite# Or run locally with Azurite# Or run locally with Azurite# Or (see `docker-compose.yml` for full list):

| Variable | Default | Description |
|---|---|---|
| `JWT_SECRET` | (required) | JWT signing secret (min 16 chars) |
| `JWT_EXPIRATION` | `24h` | Token expiration |
| `ADMIN_USERNAME` | `admin` | Initial admin username |
| `ADMIN_PASSWORD` | (required) | Initial admin password |
| `USE_AZURE_TABLE` | `false` | Enable Azure Table Storage |
| `USE_AZURITE` | `false` | Use Azurite emulator |
| `A|URE_TABLE_| `A|URE_TABLE_| `A|URE_TABLE_ls || `A|URE_TABLOPS| `A|U| | `A|URE_TABLE_| `A|URE_TABLE_| `A|URE_TABLE_ls || `A|UR| | GitLab access token |
| `DEFAULT_BRANCH` | `master` | Default Git branch |

## Data Storage

- **Azure Table Storage** (via Azurite locally): Users, Templates, Definitions, Instances, Overrides, AuditLogs
- **MySQL** (GORM): Legacy Items table only

## Testing

```bash
make test              # Unit tests
make test-coverage     # With coverage report
```

Tests use testify + httptest with mock repositories.

## Swagger

Available at http://localhost:8081/swagger/index.html when running.
Regenerate: `cd backend && make docs`
