# API Integration Layer

Centralized API client and service objects for communicating with the backend.

## Files

| File | Purpose |
|------|---------|
| `config.ts` | `API_BASE_URL` — `http://localhost:8081` in dev, `/api` in prod |
| `client.ts` | Axios instance, auth interceptor, all service objects and TypeScript types |

## Configuration

```typescript
// config.ts
export const API_BASE_URL = import.meta.env.DEV
  ? 'http://localhost:8081'
  : '/api';
```

In production, nginx strips the `/api` prefix before proxying to the backend.

## Axios Instance

The shared axios instance (`api`) is created in `client.ts` with:

- `baseURL` set from `API_BASE_URL`
- `Content-Type: application/json` default header
- Request interceptor that attaches `Authorization: Bearer <token>` from localStorage
- Response interceptor for error logging

## Service Objects

All API calls go through service objects defined in `client.ts`:

| Service | Endpoints | Description |
|---------|-----------|-------------|
| `authService` | `/api/v1/auth/*` | Login, register, current user |
| `templateService` | `/api/v1/templates/*` | Stack template CRUD, publish, chart management |
| `definitionService` | `/api/v1/stack-definitions/*` | Stack definition CRUD, chart configs |
| `instanceService` | `/api/v1/stack-instances/*` | Instance CRUD, clone, overrides, value export |
| `gitService` | `/api/v1/git/*` | Branch listing, validation, provider status |
| `auditService` | `/api/v1/audit-logs` | Filterable audit log |
| `userService` | `/api/v1/users` | User management (admin) |
| `apiKeyService` | `/api/v1/users/:id/api-keys` | Per-user API key management |

## Usage

```typescript
import { templateService } from '../api/client';

// List all templates
const templates = await templateService.list();

// Create a template
const template = await templateService.create({ name: 'My Stack', ... });
```

## Adding a New Service

1. Add TypeScript interfaces for request/response types in `client.ts`
2. Create a service object with methods using the shared `api` axios instance
3. Export the service object
4. Use `try/catch` with `console.error` for error handling in service methods
