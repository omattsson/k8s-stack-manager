# API Integration Layer

This folder has the centralized API client and the service objects that talk to the backend.

## Files

| File | Purpose |
|------|---------|
| `config.ts` | `API_BASE_URL`, `WS_BASE_URL`, and the shared Axios configuration |
| `client.ts` | The Axios instance, the interceptors, all service objects, and the TypeScript types for the API |
| `refresh.ts` | Silent refresh of the access token (`refreshAccessToken`) |

## Configuration

```typescript
// config.ts
export const API_BASE_URL = process.env.NODE_ENV === 'production' ? '' : 'http://localhost:8081';
```

- In development, the browser calls the backend directly at `http://localhost:8081`.
- In a production build, the base URL is empty. The browser calls the origin of the page. The ingress (or the nginx of the Docker image) sends `/api` to the backend.
- The service paths include the prefix `/api/v1`. Nothing removes the `/api` prefix on the way to the backend.
- `WS_BASE_URL` follows the same rule for the WebSocket (`ws://localhost:8081` in development, the page origin with `ws` or `wss` in production).

## Axios Instance

`client.ts` creates the shared Axios instance (`api`) with these settings:

- `baseURL` from `API_BASE_URL`.
- The default header `Content-Type: application/json`.
- A request interceptor. It adds `Authorization: Bearer <token>` from `localStorage` (key `token`).
- A response interceptor. When a request gets 401 (except the auth routes), it calls `refreshAccessToken()` and retries the request once. A refresh that the server rejects ends the session and redirects to `/login`.

`refresh.ts` calls `POST /api/v1/auth/refresh` with the refresh-token cookie. The refresh runs once at a time in a tab. A Web Lock (`auth-refresh`) shares it between tabs.

## Service Objects

All API calls go through the service objects in `client.ts`. The table shows the main services. See [../../README.md](../../README.md#api-services) for the list of methods.

| Service | Endpoints | Description |
|---------|-----------|-------------|
| `authService` | `/api/v1/auth/*` | Login, register, current user, refresh, logout |
| `oidcService` | `/api/v1/auth/oidc/*` | OIDC configuration and authorize URL |
| `uiConfigService` | `/api/v1/ui-config` | Public branding values |
| `templateService` | `/api/v1/templates/*` | Template CRUD, publish, chart management, quick deploy, versions, bulk operations |
| `definitionService` | `/api/v1/stack-definitions/*` | Definition CRUD, chart configs, import, export, upgrade |
| `instanceService` | `/api/v1/stack-instances/*` | Instance CRUD, clone, deploy, rollback, overrides, value export, status, custom actions, bulk operations |
| `branchOverrideService` | `/api/v1/stack-instances/:id/branches` | Per-chart branch overrides |
| `gitService` | `/api/v1/git/*` | Branch listing, validation, provider status |
| `auditService` | `/api/v1/audit-logs` | Filterable audit log and export |
| `userService` | `/api/v1/users` | User management (`admin`) |
| `apiKeyService` | `/api/v1/users/:id/api-keys` | Per-user API key management |
| `adminService` | `/api/v1/admin/orphaned-namespaces` | Orphaned namespaces |
| `clusterService` | `/api/v1/clusters/*` | Cluster registration, test connection, set default, health, quotas |
| `sharedValuesService` | `/api/v1/clusters/:id/shared-values` | Per-cluster shared values |
| `cleanupPolicyService` | `/api/v1/admin/cleanup-policies` | Cleanup policies |
| `notificationService` | `/api/v1/notifications/*` | Notifications and preferences |
| `notificationChannelService` | `/api/v1/admin/notification-channels/*` | Webhook notification channels |
| `favoriteService` | `/api/v1/favorites` | Bookmarks |
| `analyticsService` | `/api/v1/analytics/*` | Usage statistics |
| `dashboardService` | `/api/v1/dashboard` | Landing page overview |

## Usage

```typescript
import { templateService } from '../api/client';

// List the first page of templates (25 by default)
const templates = await templateService.list();

// List all templates (reads all pages)
const allTemplates = await templateService.listAll();

// Create a template
const template = await templateService.create({ name: 'My Stack', ... });
```

The list endpoints are paged (`page` and `pageSize`, default 25, maximum 100). `list` returns one page. `listAll` reads all pages with the largest page size.

## Adding a New Service

1. Add TypeScript interfaces for the request and response types in `client.ts` (or in `src/types/`).
2. Create a service object with methods that use the shared `api` Axios instance.
3. Add a TSDoc comment to the service object and to each method, with `@param`, `@returns`, and `@see` (HTTP method and route).
4. Export the service object.
5. Use `try/catch` with `console.error` for the error handling in the service methods, and throw the error again.
