# K8s Stack Manager: Frontend

This is the React single-page application (SPA) of K8s Stack Manager. It uses TypeScript, Vite, and Material UI.

## Tech Stack

- React 19 and TypeScript (strict mode)
- Vite 8 with SWC (`@vitejs/plugin-react-swc`)
- Material UI (MUI) v9 for components and styling
- React Router v7 for routing
- Axios for API calls
- Monaco Editor for YAML editing
- Vitest 5 and Testing Library (unit tests)
- Playwright (end-to-end tests)

## Project Structure

```
frontend/
|-- Dockerfile           # Multi-stage: dev (Vite) and prod (nginx)
|-- nginx.conf           # nginx configuration of the Docker image (SPA, /api and /ws proxy)
|-- vite.config.ts       # Vite and Vitest configuration, dev proxy
|-- e2e/                 # Playwright specs (one file for each feature area)
`-- src/
    |-- api/
    |   |-- config.ts    # API_BASE_URL and WS_BASE_URL
    |   |-- client.ts    # Axios instance and all service objects
    |   `-- refresh.ts   # Access token refresh
    |-- components/      # Shared components (Layout, dialogs, status badges, and others)
    |-- context/         # Auth, branding, notification, and theme providers
    |-- hooks/           # useWebSocket, useCountdown, useUnsavedChanges
    |-- pages/           # One folder for each feature (see the table below)
    |-- theme/           # MUI theme (palette, typography, component defaults)
    |-- types/           # Shared TypeScript types
    |-- utils/           # Helpers (roles, quantities, expiry, notification labels, and others)
    |-- routes.tsx       # Route definitions and protected routes
    |-- App.tsx          # Root component
    `-- main.tsx         # Entry point
```

## Pages and Routes

`ProtectedRoute` redirects a visitor without a session to `/login`. A route with a role needs that role or a higher role. The role order is `user`, `devops`, `admin`.

| Route | Page | Access |
|-------|------|--------|
| `/login` | Login | Public |
| `/auth/callback` | OIDC sign-in callback | Public |
| `/` | Stack instances dashboard | Signed in |
| `/templates` | Template gallery | Signed in |
| `/templates/new` | Template builder | Signed in (the API needs `devops` to save) |
| `/templates/:id` | Template preview | Signed in |
| `/templates/:id/edit` | Template editor | Signed in (the API needs `devops` and the owner or `admin` to save) |
| `/templates/:id/use` | Instantiate a template | Signed in |
| `/stack-definitions` | Definition list | Signed in (Create and Import need `devops`) |
| `/stack-definitions/new` | Definition form | Signed in (the API needs `devops` to create) |
| `/stack-definitions/:id/edit` | Definition editor | Signed in (the owner, `devops`, or `admin` can save; chart sources need `devops`) |
| `/stack-instances/new` | Instance form | Signed in |
| `/stack-instances/compare` | Instance comparison | Signed in |
| `/stack-instances/:id` | Instance detail | Signed in |
| `/notifications` | Notification list | Signed in |
| `/profile` | User profile, API keys, notification preferences | Signed in |
| `/audit-log` | Audit log viewer | `devops` (export: `admin`) |
| `/admin/analytics` | Usage statistics and metrics | `devops` |
| `/admin/cluster-health` | Cluster health dashboard | `devops` |
| `/admin/notification-channels` | Notification channels | `devops` |
| `/admin/users` | User management | `admin` |
| `/admin/clusters` | Cluster management | `admin` |
| `/admin/shared-values` | Per-cluster shared Helm values | `admin` |
| `/admin/cleanup-policies` | Cleanup policy editor | `admin` |
| `/admin/orphaned-namespaces` | Namespace cleanup | `admin` |

## API Services

All services are in `src/api/client.ts`. Each service and method has a TSDoc comment with `@param`, `@returns`, and `@see` (HTTP method and route).

| Service | Methods |
|---------|---------|
| `authService` | login, register, me, refresh, logout, logoutAll |
| `oidcService` | getConfig, getAuthorizeUrl |
| `uiConfigService` | get |
| `templateService` | list, listAll, get, create, update, delete, publish, unpublish, instantiate, clone, addChart, updateChart, deleteChart, quickDeploy, bulkDelete, bulkPublish, bulkUnpublish, listVersions, getVersion, diffVersions |
| `definitionService` | list, listAll, listPaged, get, create, update, delete, addChart, updateChart, deleteChart, exportDefinition, importDefinition, checkUpgrade, applyUpgrade |
| `instanceService` | list, listAll, listPaged, get, create, update, delete, clone, getOverrides, setOverride, deleteOverride, exportValues, exportChartValues, deploy, deployPreview, stop, clean, rollback, listActions, invokeAction, getActionJobLog, getDeployLog, getStatus, getPods, extend, follow, unfollow, recent, bulkDeploy, bulkStop, bulkClean, bulkDelete, compareInstances, getQuotaOverride, setQuotaOverride, deleteQuotaOverride |
| `branchOverrideService` | list, set, delete |
| `gitService` | branches, validateBranch, providers |
| `auditService` | list, export |
| `userService` | list, create, delete, resetPassword, disable, enable, changeRole |
| `apiKeyService` | list, create, delete |
| `adminService` | listOrphanedNamespaces, deleteOrphanedNamespace |
| `clusterService` | list, get, create, update, delete, testConnection, setDefault, getHealthSummary, getNodes, getNamespaces, getQuotas, updateQuotas, deleteQuotas, getUtilization |
| `sharedValuesService` | list, create, update, delete |
| `cleanupPolicyService` | list, create, update, delete, run |
| `notificationService` | list, countUnread, markAsRead, markAllAsRead, getPreferences, updatePreferences |
| `notificationChannelService` | list, create, get, update, delete, getSubscriptions, updateSubscriptions, test, deliveryLogs, eventTypes |
| `favoriteService` | list, add, remove, check |
| `analyticsService` | getOverview, getTemplateStats, getUserStats |
| `dashboardService` | getOverview |

## Development

```bash
npm install          # Install dependencies
npm run dev          # Dev server on port 3000 with hot reload
npm run build        # Production build (tsc and vite build)
npm run lint         # Type check (tsc --noEmit) and ESLint (at most 5 warnings)
npm run format       # ESLint with --fix
npm test             # Vitest unit tests (single run)
npm run test:watch   # Vitest in watch mode
npm run test:coverage  # Vitest with a coverage report
npm run test:e2e     # Playwright end-to-end tests (see the root Makefile target test-e2e)
```

You need Node.js 22.12 or later (Vitest requires it). CI and the Docker images use Node.js 26.

### API address and proxy

- **Base URL.** `src/api/config.ts` sets `API_BASE_URL`. It is `http://localhost:8081` in development and an empty string (same origin) in a production build. The service paths already include `/api/v1`. `WS_BASE_URL` follows the same rule (`ws://localhost:8081` in development, the page origin in production).
- **Dev proxy (Vite).** The dev server proxies `/api` and `/ws` to `VITE_BACKEND_URL` (default `http://backend:8081`). The proxy keeps the `/api` prefix.
- **Docker image (nginx).** `nginx.conf` serves the SPA on port 8080. It proxies `/api/` to `http://backend:8081/api/` (the prefix stays) and `/ws` to the backend.
- **Helm chart.** The chart replaces `nginx.conf` with a ConfigMap. Its nginx serves only the SPA (and `/branding/`). The ingress sends `/api`, `/ws`, `/health`, and `/swagger` to the backend. So open the UI through the ingress.

## Patterns

- **MUI components only.** Do not use raw HTML. Use the `sx` prop for styling.
- **Functional components** with `useState` and `useEffect` for state.
- **Loading, error, and content** rendering on all pages that fetch data.
- **TypeScript interfaces** for all props, API responses, and state.
- **Service objects.** All API calls use the service objects in `api/client.ts`.
- **Tests.** Unit tests are in `__tests__/` folders next to the code. End-to-end tests are in `e2e/`.
