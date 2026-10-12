---
applyTo: "**/*.{ts,tsx}"
---

# TypeScript and React Frontend Instructions

## Project setup

The frontend is in `frontend/`. It uses Vite 8, React 19, and TypeScript 6 (strict mode), with SWC (`@vitejs/plugin-react-swc`). Tests: Vitest 5 and Testing Library (unit), Playwright (e2e, 26 specs in `e2e/`). Node 22 or later.

## Architecture

- Entry: `src/main.tsx`, then `App.tsx`, then `routes.tsx`.
- Routing: `react-router-dom` v7 with `<Routes>` and `<Route>` in `src/routes.tsx`. `ProtectedRoute` guards pages (optional `requiredRole`).
- UI library: Material UI (MUI) v9. Use MUI components (`Box`, `Paper`, `Typography`, `Alert`, and others) instead of raw HTML. Native controls that MUI does not provide (for example `<input type="file">`) are accepted.
- API client: the Axios instance in `src/api/client.ts`. A request interceptor adds the JSON Web Token (JWT). A response interceptor refreshes the token after a 401 and retries the request once (`src/api/refresh.ts`).
- API config: `src/api/config.ts`. `API_BASE_URL` is `http://localhost:8081` in dev and `''` (same origin) in production. Client methods include the `/api/v1/...` prefix. Vite proxies `/api` and `/ws` in Docker dev. The ingress routes them in the Helm chart.
- WebSocket: `reconnecting-websocket` with the hook in `src/hooks/useWebSocket.ts`.

## Component patterns

### Pages

Each page is a directory in `src/pages/`. Simple pages export from `index.tsx`. Domain pages use named files:

```text
src/pages/StackInstances/     # Dashboard.tsx, Detail.tsx, Form.tsx, Compare.tsx, widgets/
src/pages/StackDefinitions/   # List.tsx, Form.tsx, ImportDefinitionDialog.tsx, UpgradeDialog.tsx
src/pages/Templates/          # Gallery.tsx, Builder.tsx, Instantiate.tsx, Preview.tsx, VersionHistory.tsx
src/pages/Admin/              # Clusters/, NotificationChannels/, OrphanedNamespaces/, Users/
src/pages/Analytics/          # index.tsx (simple page)
```

Shared dialogs and wizards are in `src/components/` (`DeployPreviewDialog`, `SetupWizard`, `QuickDeployDialog`, `QuotaConfigDialog`). Shared states: `LoadingState`, `EmptyState`, `ErrorBoundary`.

Register new pages in `src/routes.tsx`:

```tsx
<Route path="/new-page" element={<ProtectedRoute><NewPage /></ProtectedRoute>} />
```

Add the navigation item in `src/components/Layout/index.tsx`. Set `minRole` for a role-gated item.

### Layout

`src/components/Layout/index.tsx` is the shared shell (AppBar, navigation, footer). All pages render inside it.

### API services

Define service objects in `src/api/client.ts`. Each service object and method has TSDoc with a description, `@param`, `@returns`, and `@see` (HTTP method and route). Each method uses `try/catch` with `console.error`:

```typescript
/** Health service for liveness and readiness checks. Maps to `/health`. */
export const healthService = {
  /**
   * Check API server liveness.
   * @returns Health status object
   * @see GET /health/live
   */
  checkLiveness: async () => {
    try {
      const response = await api.get('/health/live');
      return response.data;
    } catch (error) {
      console.error('Failed to check liveness:', error);
      throw error;
    }
  },
};
```

Paginated list endpoints return `{ data, total }` for `page` and `pageSize`. `MAX_PAGE_SIZE` (100) is exported from `client.ts`.

## TypeScript config

- Target ES2020. Strict mode is on.
- `noUnusedLocals` and `noUnusedParameters` are on. Remove dead code.
- Module resolution: `bundler`. JSX: `react-jsx` (no React import for JSX).
- ESLint (`eslint.config.js`): `@typescript-eslint/no-explicit-any` and `react-hooks/exhaustive-deps` are warnings. The jsx-a11y rules are on. `npm run lint` fails above 5 warnings.

## State management

The app uses React hooks (`useState`, `useEffect`) and four contexts (`AuthContext`, `BrandingContext`, `NotificationContext`, `ThemeContext`). There is no global state library.

## Development

- Dev server: `npm run dev` (Vite on port 3000 with HMR).
- Build: `npm run build` (`tsc`, then `vite build`).
- Lint: `npm run lint` (`tsc --noEmit` and ESLint). Format: `npm run format` (`eslint --fix`).
- Tests: `npm test` (`vitest run`).
- In Docker: `make dev-frontend` (Vite dev server in a container).

## Conventions

- Use functional components only.
- Define interfaces for component props and API response types (in the component file, or in `src/types/` for shared types).
- Style with the MUI `sx` prop. Do not add CSS files.
- Run async work in `useEffect` and clean up intervals and subscriptions.
- Add TSDoc to each service object and method in `api/client.ts` (description, params, return type, route).
- Test with `vi.mock` for API services. After each test, call `vi.clearAllMocks()`.
