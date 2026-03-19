# K8s Stack Manager — Frontend

React SPA for the K8s Stack Manager, built with TypeScript, Vite, and Material UI.

## Tech Stack

- React 19 + TypeScript (strict mode)
- Vite with SWC (`@vitejs/plugin-react-swc`)
- Material UI (MUI) v7 for components and styling
- React Router v7 for routing
- Axios for API calls
- Vitest + Testing Library (unit tests)
- Playwright (e2e tests)

## Project Structure

```
frontend/
├── Dockerfile           # Multi-stage: dev (Vite) + prod (nginx)
├── nginx.conf           # Production reverse proxy
├── vite.config.ts       # Vite config + dev proxy
└── src/
    ├── api/
    │   ├── config.ts    # API_BASE_URL: localhost:8081 (dev) | /api (prod)
    │   └── client.ts    # Axios instance + all service objects
    ├── components/
    │   └── Layout/      # AppBar + nav + footer shell
    ├── context/         # Auth + WebSocket context providers
    ├── pages/
    │   ├── Admin/       # User management (admin only)
    │   ├── AuditLog/    # Audit log viewer
    │   ├── Login/       # Login page
    │   ├── Profile/     # User profile
    │   ├── StackDefinitions/  # Definition list + form
    │   ├── StackInstances/    # Instance dashboard + detail + form
    │   └── Templates/         # Template gallery, builder, preview
    ├── routes.tsx       # Route definitions + protected routes
    ├── App.tsx          # Root component
    └── main.tsx         # Entry point
```

## Pages and Routes

| Route | Page | Access |
|-------|------|--------|
| `/login` | Login | Public |
| `/` | Stack Instances dashboard | Authenticated |
| `/templates` | Template gallery | Authenticated |
| `/templates/new` | Template builder | Authenticated |
| `/templates/:id` | Template preview | Authenticated |
| `/templates/:id/edit` | Template editor | Authenticated |
| `/templates/:id/use` | Instantiate template | Authenticated |
| `/stack-definitions` | Definition list | Authenticated |
| `/stack-definitions/new` | Definition form | Authenticated |
| `/stack-definitions/:id/edit` | Definition editor | Authenticated |
| `/stack-instances/new` | Instance form | Authenticated |
| `/stack-instances/:id` | Instance detail | Authenticated |
| `/audit-log` | Audit log viewer | Authenticated |
| `/admin/users` | User management | Admin |
| `/profile` | User profile | Authenticated |

## API Services

All services are defined in `src/api/client.ts`:

| Service | Methods |
|---------|---------|
| `authService` | login, register, me |
| `templateService` | list, get, create, update, delete, publish, unpublish, instantiate, clone, addChart, updateChart, deleteChart |
| `definitionService` | list, get, create, update, delete, addChart, updateChart, deleteChart |
| `instanceService` | list, get, create, update, delete, clone, getOverrides, setOverride, exportValues |
| `gitService` | branches, validateBranch, providers |
| `auditService` | list |
| `userService` | list, create, delete |
| `apiKeyService` | list, create, delete |

## Development

```bash
npm install          # Install dependencies
npm run dev          # Dev server on port 3000 with HMR
npm run build        # Production build (tsc + vite)
npm run lint         # ESLint
npm run format       # Prettier
npm test             # Vitest unit tests
```

### Proxy Configuration

- **Dev (Vite)**: `/api` requests are proxied to `backend:8081` with the `/api` prefix stripped
- **Prod (nginx)**: `/api/` location proxies to `backend:8081/` (trailing slash strips prefix)

## Patterns

- **MUI components only** — no raw HTML; use `sx` prop for styling
- **Functional components** with `useState`/`useEffect` for state
- **Loading/Error/Content** rendering pattern on all data-fetching pages
- **TypeScript interfaces** for all props, API responses, and state
- Tests in `__tests__/` directories alongside pages
