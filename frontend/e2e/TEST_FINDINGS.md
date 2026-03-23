# E2E Test Findings & Fixes

## Summary

Started with 102 e2e tests (commit `b968524`) showing **38 failures**. Through investigation and targeted fixes, grew to **128 tests — all passing** in ~25s with 9 parallel workers.

## Root Cause 1: `useBlocker` Requires Data Router (Critical)

**Impact**: ~16 tests failed — any test navigating to Stack Instance Detail or Stack Definition Form pages crashed with an uncaught error.

**Symptom**: Pages rendered `ErrorBoundary` "Something went wrong" instead of their content. Console showed:

```
useBlocker must be used within a data router
```

**Root Cause**: The `useUnsavedChanges` hook imported `useBlocker` from `react-router-dom`, which requires `createBrowserRouter` (data router). The app uses `<BrowserRouter>`, which does not support `useBlocker`.

**Fix**: Removed `useBlocker` and all associated logic from `useUnsavedChanges.ts`. The hook now only uses the `beforeunload` event listener to warn users when closing/refreshing a tab with unsaved changes. In-app navigation blocking would require migrating to `createBrowserRouter`.

**Files changed**:
- `frontend/src/hooks/useUnsavedChanges.ts` — removed `useBlocker` import and blocker logic
- `frontend/src/hooks/__tests__/useUnsavedChanges.test.ts` — removed 3 blocker-related tests

## Root Cause 2: `loginAsAdmin` Did Not Navigate (Medium)

**Impact**: ~5 tests failed — tests that called `loginAsAdmin()` then immediately interacted with UI elements without navigating first.

**Symptom**: Tests timed out waiting for elements on `about:blank`.

**Root Cause**: The `loginAsAdmin()` helper was refactored from a UI-based login (which navigated to `/` after sign-in) to an API-based login that injects a JWT token via `addInitScript()`. The new version does not navigate to any page.

**Fix**: Added `await page.goto('/')` after `loginAsAdmin(page)` in affected tests.

**Files changed**:
- `frontend/e2e/instances.spec.ts` — added `page.goto('/')` in `beforeEach`
- `frontend/e2e/auth.spec.ts` — added `page.goto('/')` in logout and session-persist tests

## Root Cause 3: Strict-Mode Heading Violations on Dashboard (Fixed)

**Impact**: 3-5 instance tests failed when the dashboard rendered an instance name in both the "Recent Stacks" sidebar (`<h6>` from MUI `Typography variant="subtitle2"`) and the main card grid (`<h2>` from `Typography variant="h6" component="h2"`).

**Symptom**: `page.getByRole('heading', { name: instName })` resolved to 2 elements — Playwright strict mode rejected this.

**Root Cause**: MUI `Typography` with `variant="subtitle2"` renders as `<h6>` by default (a heading element), not `<div>`. When an instance appeared in both "Recent Stacks" and the main grid, there were two heading elements with the same text.

**Fix**: Applied both frontend and test-side fixes:
- `frontend/src/pages/StackInstances/Dashboard.tsx` — added `component="div"` to Typography in "My Favorites" and "Recent Stacks" sidebar sections
- `frontend/e2e/instances.spec.ts` — added `{ level: 2 }` to heading locators for disambiguation

## Root Cause 4: Parallelism + Rate Limiting (Intermittent)

**Impact**: 4-8 tests fail intermittently when workers run against a backend with `RATE_LIMIT=1000` (dev mode).

**Symptom**: `loginAsAdmin()` gets HTTP 429 after retries, or `waitForURL` timeouts on template save redirects (backend is slow under concurrent API load).

**Root Cause**: The `make dev-local` target runs with `RATE_LIMIT=1000` (requests per minute per IP). The `make test-e2e` target uses `RATE_LIMIT=10000`. When running e2e tests against a dev backend, the rate limit is too low for concurrent workers.

**Mitigation**: Run e2e tests via `make test-e2e` which starts the backend with `RATE_LIMIT=10000`, or reduce workers to 1 (`--workers=1`). In practice, we run CI with 1 worker and typically use up to 9 workers for local runs.

## Root Cause 5: Shared Database State Between Parallel Workers (Critical)

**Impact**: Cascading failures across deployment and instance tests when the "set default cluster" test deleted the cluster it had set as default.

**Symptom**: Tests depending on a default cluster (deploy, status, clean operations) failed with 404 or missing cluster errors.

**Root Cause**: The `clusters.spec.ts` "set default cluster" test changed the default cluster to a test cluster, then deleted it in cleanup without restoring the original default.

**Fix**: Added default cluster restoration in the `finally` block of `clusters.spec.ts`:
```typescript
// Restore the original default cluster before cleanup
const original = clusters.find((c) => c.name === 'default');
if (original) {
  await page.request.post(`/api/v1/clusters/${original.id}/default`, ...);
}
```

## Least-Privilege Authentication

Tests now use the minimum required role for each page/API:

| Spec File | Login Role | Reason |
|-----------|-----------|--------|
| `auth.spec.ts` | admin | Tests admin registration flow |
| `navigation.spec.ts` | user (admin for admin-nav tests) | Regular nav is available to all users |
| `profile.spec.ts` | user | Profile page works for any authenticated user |
| `instances.spec.ts` | devops | Stack instances require devops role |
| `definitions.spec.ts` | devops | Stack definitions require devops role |
| `templates.spec.ts` | devops | Templates require devops role |
| `deployment.spec.ts` | devops | Deployment operations require devops role |
| `audit-log.spec.ts` | devops | Audit logs require devops role |
| `analytics.spec.ts` | admin | Analytics page calls `/api/v1/analytics/users` which requires admin |
| `clusters.spec.ts` | admin | Cluster CRUD requires admin role |
| `cluster-health.spec.ts` | admin | Cluster health requires admin/devops, mocks API responses |
| `orphaned-namespaces.spec.ts` | admin | Admin-only page |

Helper functions added to `helpers.ts`: `loginAsUser(page)`, `loginAsDevops(page)`.

## New Tests Added (27 tests)

| Spec File | Tests | Description |
|-----------|-------|-------------|
| `cluster-health.spec.ts` | 10 | Full page with mocked API: selector, toggle, summary cards, tables, error handling, access control |
| `orphaned-namespaces.spec.ts` | 6 | Page load, empty state, column headers, refresh, breadcrumbs, access control |
| `instances.spec.ts` | 3 | Favorites toggle, export values, TTL selector |
| `templates.spec.ts` | 5 | Quick deploy dialog, quick deploy navigation, favorites, category filter, My Templates tab |
| `clusters.spec.ts` | 2 | Test connection, set default cluster |
| `profile.spec.ts` | 1 | API Keys heading disambiguation |

## Other Fixes Applied

| File | Change | Reason |
|------|--------|--------|
| `helpers.ts` | Refactored `loginAsAdmin` to API-based auth with 429 retry | Reduce API calls and avoid rate-limit pressure |
| `helpers.ts` | Added `loginAsUser`, `loginAsDevops` with auto-registration | Least-privilege testing |
| `helpers.ts` | Added `createAndPublishTemplate`, `instantiateTemplate` helpers | Shared setup across instance/definition tests |
| `instances.spec.ts` | Fixed `getByText('8 hours')` → `{ exact: true }` | Avoided matching "48 hours" |
| `instances.spec.ts` | Fixed TTL heading locator to `getByRole('heading')` | Strict-mode: matched 3 elements |
| `templates.spec.ts` | Fixed card locators to `.MuiCard-root` | `div.filter({ hasText })` matched outermost div |
| `cluster-health.spec.ts` | Added `mockClusterAPIs()` with `page.route()` | Deterministic tests without real cluster |
| `cluster-health.spec.ts` | `getByText('CPU', { exact: true })` | Avoided matching "CPU Capacity" |
| `orphaned-namespaces.spec.ts` | Accept 200 or 503 on refresh | No K8s cluster in test environment |
| `orphaned-namespaces.spec.ts` | `.or().first()` on alternative locators | Multiple matches when both visible |
| `profile.spec.ts` | `getByRole('heading', { name: 'API Keys' })` | Strict-mode: text matched heading and paragraph |
| `navigation.spec.ts` | Split admin/user nav tests with separate logins | Admin nav links only visible to admin users |
| `playwright.config.ts` | Derive `workers` from `PW_WORKERS` (otherwise Playwright default) | Configurable parallelism for local/CI runs |
| Various spec files | Adjusted selectors, labels, and test flows | Align tests with actual UI implementation |
