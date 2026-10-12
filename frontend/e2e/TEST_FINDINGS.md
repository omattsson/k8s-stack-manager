# E2E Test Findings and Fixes

This document records the problems that the first stabilization of the end-to-end (e2e) tests found, and the fixes. It is a historical record. The numbers in the summary are from that time. Later changes added more specs (see the folder `frontend/e2e/`). The sections "Least-privilege authentication" and "Running the tests" describe the current state.

## Summary

The work started with 102 e2e tests (commit `b968524`). 38 of them failed. After investigation and fixes, there were 128 tests, and all of them passed in about 25 seconds with 9 parallel workers.

## Root Cause 1: `useBlocker` Needs a Data Router (Critical)

**Impact:** About 16 tests failed. Each test that opened the Stack Instance Detail page or the Stack Definition Form page crashed with an uncaught error.

**Symptom:** The pages showed the `ErrorBoundary` message "Something went wrong" and not their content. The console showed:

```
useBlocker must be used within a data router
```

**Root cause:** The `useUnsavedChanges` hook imported `useBlocker` from `react-router-dom`. `useBlocker` needs `createBrowserRouter` (a data router). The application uses `<BrowserRouter>`, which does not support `useBlocker`.

**Fix:** The fix removed `useBlocker` and all related logic from `useUnsavedChanges.ts`. The hook now uses only the `beforeunload` event. It warns the user when the user closes or reloads a tab with unsaved changes. To block navigation inside the application, the application must first change to `createBrowserRouter`.

**Files changed:**

- `frontend/src/hooks/useUnsavedChanges.ts`: removed the `useBlocker` import and the blocker logic
- `frontend/src/hooks/__tests__/useUnsavedChanges.test.ts`: removed 3 tests for the blocker

## Root Cause 2: `loginAsAdmin` Did Not Navigate (Medium)

**Impact:** About 5 tests failed. They called `loginAsAdmin()` and then used UI elements without a navigation.

**Symptom:** The tests timed out while they waited for elements on `about:blank`.

**Root cause:** The `loginAsAdmin()` helper was changed from a login through the UI (which opened `/` after the login) to a login through the API. The new helper puts a JWT in `localStorage` with `addInitScript()`. It does not open a page.

**Fix:** The fix added `await page.goto('/')` after `loginAsAdmin(page)` in the affected tests.

**Files changed:**

- `frontend/e2e/instances.spec.ts`: added `page.goto('/')` in `beforeEach`
- `frontend/e2e/auth.spec.ts`: added `page.goto('/')` in the logout and session-persistence tests

## Root Cause 3: Strict-Mode Heading Violations on the Dashboard (Fixed)

**Impact:** 3 to 5 instance tests failed when the dashboard showed an instance name twice: in the "Recent Stacks" sidebar (an `<h6>` from MUI `Typography variant="subtitle2"`) and in the main card grid (an `<h2>` from `Typography variant="h6" component="h2"`).

**Symptom:** `page.getByRole('heading', { name: instName })` found 2 elements. Playwright strict mode rejected this.

**Root cause:** MUI `Typography` with `variant="subtitle2"` renders an `<h6>` by default (a heading element), not a `<div>`. When an instance was in both "Recent Stacks" and the main grid, there were two headings with the same text.

**Fix:** The fix changed the frontend and the tests:

- `frontend/src/pages/StackInstances/Dashboard.tsx`: added `component="div"` to the Typography elements in the sidebar sections
- `frontend/e2e/instances.spec.ts`: added `{ level: 2 }` to the heading locators

## Root Cause 4: Parallel Workers and the Rate Limit (Intermittent)

**Impact:** 4 to 8 tests failed from time to time when many workers ran against a backend with `RATE_LIMIT=1000`.

**Symptom:** `loginAsAdmin()` got HTTP 429 after its retries, or `waitForURL` timed out on the redirect after a template save (the backend was slow under concurrent API load).

**Root cause:** `make dev-local` starts the backend with `RATE_LIMIT=1000` (requests each minute for each IP address) by default. `make test-e2e` starts the backend with `RATE_LIMIT=10000` and `LOGIN_RATE_LIMIT=10000`. With the lower limit, concurrent workers reach the limit.

**Mitigation:** Run the e2e tests with `make test-e2e`. It starts the backend with the higher limits. Or reduce the workers with `PW_WORKERS=1` (or `--workers=1`). The Playwright configuration uses 1 worker in CI (`CI` is set). Locally it uses `PW_WORKERS`, or the Playwright default.

## Root Cause 5: Shared Database State Between Parallel Workers (Critical)

**Impact:** Many deployment and instance tests failed one after another when the "set default cluster" test deleted the cluster that it had set as the default.

**Symptom:** Tests that depend on a default cluster (deploy, status, clean) failed with 404 or with a missing-cluster error.

**Root cause:** The "set default cluster" test in `clusters.spec.ts` made a test cluster the default. Its cleanup deleted that cluster and did not restore the original default.

**Fix:** The fix restores the original default cluster in the `finally` block of the test:

```typescript
// Restore the original default cluster before cleanup
const original = clusters.find((c) => c.name === 'default');
if (original) {
  await page.request.post(`/api/v1/clusters/${original.id}/default`, ...);
}
```

## Least-Privilege Authentication

The tests use the lowest role that the page or the API needs. The helpers are in `frontend/e2e/helpers.ts`: `loginAsAdmin`, `loginAsDevops`, and `loginAsUser`. `loginAsDevops` and `loginAsUser` create the user on the first call (with the admin account) and reuse it.

| Spec file | Login role | Reason |
|-----------|-----------|--------|
| `auth.spec.ts` | admin | The tests cover the admin registration flow |
| `admin-users.spec.ts` | admin | User management needs the `admin` role |
| `analytics.spec.ts` | admin | The page calls `/api/v1/analytics/users`, which needs the `admin` role |
| `cleanup-policies.spec.ts` | admin | Cleanup policies need the `admin` role |
| `cluster-health.spec.ts` | admin | The page needs `admin` or `devops`. The test mocks the API responses. |
| `clusters.spec.ts` | admin | Cluster create, update, and delete need the `admin` role |
| `orphaned-namespaces.spec.ts` | admin | The page needs the `admin` role |
| `shared-values.spec.ts` | admin | Shared values need the `admin` role |
| `dashboard-widgets.spec.ts` | admin (and user for the role check) | The cluster health widget shows only to `admin` and `devops` |
| `navigation.spec.ts` | user (admin for the admin navigation) | The regular navigation is available to all users |
| `profile.spec.ts` | user | The profile page works for every signed-in user |
| `audit-log.spec.ts` | devops | Reading the audit log needs `devops` |
| `branch-overrides.spec.ts`, `value-overrides.spec.ts`, `compare.spec.ts` | devops | The tests create definitions and instances. Creating a definition needs `devops`. |
| `bulk-operations.spec.ts` | devops | Bulk operations need `devops` |
| `definitions.spec.ts`, `import-export.spec.ts` | devops | Creating, importing, and exporting a definition needs `devops` |
| `deployment.spec.ts`, `instances.spec.ts`, `websocket.spec.ts` | devops | The tests create and operate instances |
| `template-versions.spec.ts`, `templates.spec.ts` | devops | Creating and publishing a template needs `devops` |
| `notifications.spec.ts` | devops | The tests mock the notification API |
| `ux-improvements.spec.ts` | devops (and user for some tests) | Mixed UI checks |
| `oidc-auth.spec.ts` | none | The test mocks the OIDC configuration and uses fake tokens |

## Specs Added During the Stabilization

| Spec file | Description |
|-----------|-------------|
| `cluster-health.spec.ts` | The full page with a mocked API: selector, toggle, summary cards, tables, error handling, access control |
| `orphaned-namespaces.spec.ts` | Page load, empty state, column headers, refresh, breadcrumbs, access control |
| `instances.spec.ts` (additions) | Favorites toggle, export values, TTL selector |
| `templates.spec.ts` (additions) | Quick deploy dialog, quick deploy navigation, favorites, category filter, My Templates tab |
| `clusters.spec.ts` (additions) | Test connection, set default cluster |
| `profile.spec.ts` (addition) | API Keys heading |

## Other Fixes

| File | Change | Reason |
|------|--------|--------|
| `helpers.ts` | `loginAsAdmin` uses the API, with a retry on 429 | Fewer API calls and less rate-limit pressure |
| `helpers.ts` | Added `loginAsUser` and `loginAsDevops` with automatic registration | Least-privilege tests |
| `helpers.ts` | Added the helpers `createAndPublishTemplate` and `instantiateTemplate` | Shared setup for the instance and definition tests |
| `instances.spec.ts` | `getByRole('option', { name: '8 hours', exact: true })` | Does not match "48 hours" |
| `instances.spec.ts` | The TTL heading locator uses `getByRole('heading')` | Strict mode: the text matched 3 elements |
| `templates.spec.ts` | Card locators use `.MuiCard-root` | `div.filter({ hasText })` matched the outermost div |
| `cluster-health.spec.ts` | Added `mockClusterAPIs()` with `page.route()` | Stable tests without a real cluster |
| `cluster-health.spec.ts` | `getByText('CPU', { exact: true })` | Does not match "CPU Capacity" |
| `orphaned-namespaces.spec.ts` | The refresh test accepts 200, 500, or 503 | The test environment has no Kubernetes cluster |
| `orphaned-namespaces.spec.ts` | `.or().first()` on alternative locators | Two matches when both elements are visible |
| `profile.spec.ts` | `getByRole('heading', { name: 'API Keys' })` | Strict mode: the text matched a heading and a paragraph |
| `navigation.spec.ts` | Separate logins for the admin and user navigation tests | The admin links show only to `admin` users |
| `playwright.config.ts` | `workers` comes from `PW_WORKERS` (else the Playwright default; 1 in CI) | Configurable parallelism for local and CI runs |
| Various spec files | Adjusted selectors, labels, and test flows | Align the tests with the real UI |

## Running the Tests

```bash
make test-e2e                          # starts MySQL and the backend, runs Playwright
cd frontend && PW_WORKERS=1 npx playwright test   # run against a backend that you started
```

`make test-e2e` writes the logs to `frontend/test-logs/` (`backend.log` and `playwright.log`). The Playwright configuration starts the Vite dev server (`npm run dev`) when none runs.
