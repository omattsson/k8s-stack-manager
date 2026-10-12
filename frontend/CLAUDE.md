# Frontend: TypeScript/React Instructions

## Project Setup
- The frontend uses Vite 8, React 19 and TypeScript 6 (strict mode). It uses SWC through `@vitejs/plugin-react-swc`.
- Unit tests run on Vitest 5 with Testing Library. End-to-end tests run on Playwright (`e2e/`, 26 specs).
- Node 22 or later is required. The Dockerfile uses `node:26-alpine`.

## Architecture
- **Entry:** `src/main.tsx`, then `App.tsx`, then `routes.tsx`. `App.tsx` wraps the routes in the providers (theme, branding, notification, auth).
- **Routing:** `react-router-dom` v7 with `<Routes>` and `<Route>` in `src/routes.tsx`. `ProtectedRoute` guards a route and takes an optional `requiredRole`.
- **UI library:** MUI v9 (`@mui/material`). Use MUI components instead of raw HTML.
- **API client:** Axios instance in `src/api/client.ts`. `src/api/refresh.ts` refreshes the access token (one refresh for all tabs, with Web Locks).
- **API config:** `src/api/config.ts`. `API_BASE_URL` is `http://localhost:8081` in dev and `''` (same origin) in production.
  - Endpoints in `client.ts` include the full `/api/v1/...` path. In production, requests go to `/api/v1/...` on the same origin.
  - nginx (`location /api/` to `backend:8081/api/`) and the Vite dev proxy both keep the `/api` prefix.
- **WebSocket:** `reconnecting-websocket`. The hook `useWebSocket` shares one connection for the whole tab. The connection opens at the first subscription and closes after the last unsubscribe. The hook sends `subscribe` and `unsubscribe` messages for instance logs.
- **Contexts** (`src/context/`):
  - `AuthContext.tsx`: authentication state and JWT.
  - `BrandingContext.tsx`: branding from `GET /api/v1/ui-config`.
  - `NotificationContext.tsx`: toasts and snackbars.
  - `ThemeContext.tsx`: light and dark toggle.
- **Hooks** (`src/hooks/`): `useCountdown.ts` (countdown timer), `useUnsavedChanges.ts` (unsaved changes warning), `useWebSocket.ts` (real-time updates).
- **Theme** (`src/theme/`): `index.ts` (MUI theme export), `palette.ts`, `typography.ts`, `components.ts` (default props and style overrides).
- **Types:** `src/types/index.ts` has the shared TypeScript types.
- **Utils** (`src/utils/`):
  - `roles.ts`: role ranking (`hasAtLeastRole`) and `canModifyInstance`.
  - `timeAgo.ts`: relative timestamps ("2m ago", "3h ago").
  - `expiry.ts`: duration text for the instance expiry.
  - `instanceName.ts`: instance name rule (RFC 1123 label, at most 50 characters).
  - `instanceState.ts`, `deployHistory.ts`, `templateVersion.ts`: instance and template helpers.
  - `quantity.ts`: Kubernetes quantity parsing (cores, binary units).
  - `jwt.ts`: JWT payload decoding. `apiError.ts`: server error messages. `download.ts`: save a Blob as a file.
  - `notificationHelpers.tsx`: toast helpers. `notificationEventTypes.ts`: in-app event types.
  - `recentTemplates.ts`: recently used templates in localStorage.
  - `setupWizard.ts`: dismissed flag of the first-run wizard in localStorage.

## Component Patterns
- Use functional components only. Do not use class components.
- Use `useState` and `useEffect` for state. Do not use a global state library.
- Use the MUI `sx` prop for styling. Do not use separate CSS files.
- Use TypeScript interfaces for all component props and API response types.
- One directory for each page under `src/pages/`.
  - Simple pages export from `index.tsx`: Login, AuthCallback, AuditLog, Profile, Analytics, CleanupPolicies, ClusterHealth, Notifications, SharedValues, NotFound.
  - Domain pages use named files:
    - `StackInstances/`: `Dashboard.tsx`, `Detail.tsx`, `Form.tsx`, `Compare.tsx`, `CloneDialog.tsx`, `RollbackDialog.tsx`, `ExpiryChip.tsx`, `widgets/`.
    - `StackDefinitions/`: `List.tsx`, `Form.tsx`, `ImportDefinitionDialog.tsx`, `UpgradeDialog.tsx`.
    - `Templates/`: `Gallery.tsx`, `Builder.tsx`, `Instantiate.tsx`, `Preview.tsx`, `VersionHistory.tsx`, `PublishDialog.tsx`, `CloneTemplateDialog.tsx`.
    - `Admin/`: `Clusters/`, `NotificationChannels/`, `OrphanedNamespaces/`, `Users/`.
- Shared components are in `src/components/`:
  - Layout, AccessUrls, BranchSelector, BrandLogo, ConfirmDialog, EmptyState, EntityLink, ErrorBoundary, LoadingState, ProtectedRoute, StatusBadge, TtlSelector, YamlEditor.
  - ChartDiffList and TemplateFieldDiffList (template version diff).
  - DeployPreviewDialog (merged values before deploy, `GET /stack-instances/:id/deploy-preview`).
  - DeploymentLogViewer, PodStatusDisplay, QuickDeployDialog, QuotaConfigDialog.
  - ExtendTtlMenu, FavoriteButton, FollowButton, NotificationCenter.
  - InstanceActionsMenu (custom actions of the instance "Actions" menu), JsonResultView.
  - SetupWizard (first-run checklist: cluster, template, instance).

## UX Patterns

### Keyboard Shortcuts
A page can register keyboard shortcuts with `useEffect` and a `keydown` listener. The shortcut must check `document.activeElement`, so it does not fire when an input or textarea has focus:
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
The Dashboard uses `/` (focus the search) and `Escape` (clear the selection or the search).

### Relative Timestamps
Use `timeAgo(date)` from `src/utils/timeAgo.ts` for relative time. It returns strings like "just now", "2m ago", "3h ago", "5d ago". Add an MUI `Tooltip` with the full absolute date for accessibility.

### Recently Used Tracking
Keep recently used items in `localStorage` for quick access. Use a validated array of IDs with a maximum size. Parse it safely: validate the JSON and check the shape. Example: the key `recentTemplates` in the Templates gallery. Use `trackRecentTemplate()` from `src/utils/recentTemplates.ts` to add entries.

### Bulk Operations UI
Use checkboxes for multi-select and a toolbar for the bulk actions.
- Show a `Toolbar` with action buttons when items are selected.
- Use `ConfirmDialog` before a destructive action.
- Show the results in a dialog with a success or error status for each item.

### Role-Based UI
Hide or disable controls that the backend refuses, for example Create Definition and Add/Remove Chart for the role `user` (`hasAtLeastRole(user?.role, 'devops')`). The backend stays the authority.

- Register new pages in `src/routes.tsx`. Add navigation in `src/components/Layout/index.tsx`.

## Page Pattern
- Show a loading state: a centered `CircularProgress` in a `Box`.
- Show an error state: an `Alert` with `severity="error"`.
- Render in this order: loading, then error, then content.
- Page titles: `Typography variant="h4" component="h1"`.
- Wrap the content in MUI `Box` and `Paper` components.

## API Services
Define service objects in `src/api/client.ts` next to the Axios instance. Use `try/catch` and `console.error` for error handling. Each service object and method needs a TSDoc comment with `@param`, `@returns` and `@see` (HTTP method and route).

## TypeScript Config
- Target: ES2020, strict mode.
- `noUnusedLocals` and `noUnusedParameters` are enforced.
- JSX: `react-jsx` (automatic runtime).

## Development
- Dev server: `npm run dev` (port 3000, HMR).
- Build: `npm run build` (`tsc`, then `vite build`).
- Lint: `npm run lint`.
- Tests: Vitest and Testing Library (unit), Playwright (e2e).
- Create a `__tests__/` directory next to each page. Add at least one render test.
