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
- **Utils**: `src/utils/roles.ts` — role ranking and permission helpers; `src/utils/timeAgo.ts` — relative timestamp formatting ("2m ago", "3h ago"); `src/utils/notificationHelpers.tsx` — toast notification utilities; `src/utils/recentTemplates.ts` — recently used template tracking via localStorage

## Component Patterns
- Functional components only (no class components)
- `useState`/`useEffect` for state, no global state library
- MUI `sx` prop for styling, no separate CSS files
- TypeScript interfaces for all component props and API response types
- Pages: one directory per page under `src/pages/` with `index.tsx` — current pages: Login, AuthCallback, StackInstances (Dashboard), StackDefinitions, Templates, AuditLog, Admin, Profile, Analytics, CleanupPolicies, ClusterHealth, Notifications, SharedValues, NotFound
- Shared components in `src/components/`: Layout, AccessUrls, BranchSelector, ConfirmDialog, DeploymentLogViewer, EmptyState, EntityLink, ErrorBoundary, FavoriteButton, LoadingState, NotificationCenter, PodStatusDisplay, ProtectedRoute, QuickDeployDialog, QuotaConfigDialog, StatusBadge, TtlSelector, YamlEditor

## UX Patterns

### Keyboard Shortcuts
Pages can register keyboard shortcuts via `useEffect` with `keydown` event listeners. Shortcuts must check `document.activeElement` to avoid firing when input/textarea is focused:
```typescript
useEffect(() => {
  const handler = (e: KeyboardEvent) => {
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes((document.activeElement?.tagName || ''))) return;
    if (e.key === 'r') handleRefresh();
  };
  window.addEventListener('keydown', handler);
  return () => window.removeEventListener('keydown', handler);
}, []);
```
Currently used in Dashboard: `/` (focus search), `Escape` (clear selection/search).

### Relative Timestamps
Use `timeAgo(date)` from `src/utils/timeAgo.ts` for human-friendly relative time display. Returns strings like "just now", "2m ago", "3h ago", "5d ago". Combine with MUI `Tooltip` showing the full absolute date for accessibility.

### Recently Used Tracking
Track recently used items in `localStorage` for quick access. Use a validated array of IDs with maximum size limit. Parse safely with JSON validation and shape checking. Example: `localStorage.getItem('recentTemplates')` in the Templates gallery. Use `trackRecentTemplate()` from `src/utils/recentTemplates.ts` to add entries.

### Bulk Operations UI
Multi-select with checkbox + toolbar pattern for bulk actions. Show a `Toolbar` with action buttons when items are selected. Use `ConfirmDialog` before destructive actions. Display results in a dialog with per-item success/error status.

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
