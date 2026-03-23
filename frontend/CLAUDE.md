# Frontend — TypeScript/React Instructions

## Project Setup
Built with Vite + React 19 + TypeScript (strict mode). Uses SWC via `@vitejs/plugin-react-swc`.

## Architecture
- **Entry**: `src/main.tsx` → `App.tsx` → `routes.tsx`
- **Routing**: `react-router-dom` v7 with `<Routes>` / `<Route>` in `src/routes.tsx`
- **UI Library**: MUI v7 — use MUI components instead of raw HTML
- **API Client**: Axios instance in `src/api/client.ts`
- **API Config**: `src/api/config.ts` — dev: `http://localhost:8081` (direct), prod: `/api` (nginx strips `/api` prefix via trailing `/` in `proxy_pass`). Endpoints in `client.ts` use full `/api/v1/...` paths.
- **WebSocket**: `reconnecting-websocket` with context provider and hook
- **Contexts**: `src/context/AuthContext.tsx` (authentication state + JWT), `NotificationContext.tsx` (toast/snackbar), `ThemeContext.tsx` (light/dark toggle)
- **Hooks**: `src/hooks/useCountdown.ts` (countdown timer), `useUnsavedChanges.ts` (unsaved changes warning), `useWebSocket.ts` (WebSocket real-time updates)
- **Theme**: `src/theme/` — `index.ts` (MUI theme export), `palette.ts`, `typography.ts`, `components.ts` (default prop/style overrides)
- **Types**: `src/types/index.ts` — shared TypeScript type definitions
- **Utils**: `src/utils/roles.ts` — role ranking and permission helpers

## Component Patterns
- Functional components only (no class components)
- `useState`/`useEffect` for state, no global state library
- MUI `sx` prop for styling, no separate CSS files
- TypeScript interfaces for all component props and API response types
- Pages: one directory per page under `src/pages/` with `index.tsx` — current pages: Login, StackInstances (Dashboard), StackDefinitions, Templates, AuditLog, Admin, Profile, Analytics, CleanupPolicies, ClusterHealth, SharedValues, NotFound
- Shared components in `src/components/`: Layout, AccessUrls, BranchSelector, ConfirmDialog, DeploymentLogViewer, EmptyState, EntityLink, ErrorBoundary, FavoriteButton, LoadingState, PodStatusDisplay, ProtectedRoute, QuickDeployDialog, StatusBadge, TtlSelector, YamlEditor
- Register new pages in `src/routes.tsx`, add nav in `src/components/Layout/index.tsx`

## Page Pattern
- Include loading states using `CircularProgress` centered in a `Box`
- Include error states using `Alert` with `severity="error"`
- Loading → Error → Content rendering hierarchy
- Page titles: `Typography variant="h4" component="h1"`
- Wrap content in MUI `Box` and `Paper` components

## API Services
Define service objects in `src/api/client.ts` alongside the axios instance. Use `try/catch` with `console.error` for error handling.

## TypeScript Config
- Target: ES2020, strict mode
- `noUnusedLocals` and `noUnusedParameters` enforced
- JSX: `react-jsx` (automatic runtime)

## Development
- Dev server: `npm run dev` (port 3000 with HMR)
- Build: `npm run build` (tsc then vite build)
- Lint: `npm run lint`
- Tests: Vitest + Testing Library (unit), Playwright (e2e)
- Create `__tests__/` directory alongside pages with at least one render test
