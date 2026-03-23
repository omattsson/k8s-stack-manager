# E2E Test Findings & Fixes

## Summary

After adding 102 e2e tests (commit `b968524`), the initial full-suite run showed **38 failures**.
Through investigation and targeted fixes, failures were reduced. The remaining issues are
documented here alongside the fixes applied.

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

## Root Cause 3: Strict-Mode Heading Violations on Dashboard (Open)

**Impact**: 3-5 instance tests fail intermittently when the dashboard renders an instance name in both the "Recent Stacks" sidebar (`<h6>` from MUI `Typography variant="subtitle2"`) and the main card grid (`<h2>` from `Typography variant="h6" component="h2"`).

**Symptom**: `page.getByRole('heading', { name: instName })` resolves to 2 elements — Playwright strict mode rejects this.

**Root Cause**: MUI `Typography` with `variant="subtitle2"` renders as `<h6>` by default (a heading element), not `<div>`. When an instance appears in both "Recent Stacks" and the main grid, there are two heading elements with the same text.

**Mitigation**: Tests should use `{ level: 2 }` to match only the main card heading, or scope the locator to a specific container. Alternatively, the Dashboard component could add `component="div"` to the subtitle2 Typography in Recent Stacks to prevent it from being a heading.

## Root Cause 4: Parallelism + Rate Limiting (Intermittent)

**Impact**: 4-8 tests fail intermittently when 3 Playwright workers run simultaneously against a backend with `RATE_LIMIT=1000` (dev mode).

**Symptom**: `loginAsAdmin()` gets HTTP 429 after retries, or `waitForURL` timeouts on template save redirects (backend is slow under concurrent API load).

**Root Cause**: The `make dev-local` target runs with `RATE_LIMIT=1000` (requests per minute per IP). The `make test-e2e` target uses `RATE_LIMIT=10000`. When running e2e tests against a dev backend, the rate limit is too low for 3 concurrent workers, each creating templates, definitions, and instances.

**Mitigation**: Run e2e tests via `make test-e2e` which starts the backend with `RATE_LIMIT=10000`, or reduce workers to 1 (`--workers=1`). The Playwright config already uses `workers: 1` for CI.

## Other E2E Fixes Applied

| File | Change | Reason |
|------|--------|--------|
| `helpers.ts` | Refactored `loginAsAdmin` to API-based auth with 429 retry | Reduce API calls and avoid rate-limit pressure |
| `helpers.ts` | Added `createAndPublishTemplate`, `instantiateTemplate` helpers | Shared setup across instance/definition tests |
| `instances.spec.ts` | Added `waitForLoadState('domcontentloaded')` after navigations | Prevent interaction with loading/stale pages |
| `instances.spec.ts` | Changed cluster test to use `kubeconfig_path` instead of `kubeconfig_data` | Avoid sending sensitive data in tests |
| `playwright.config.ts` | Set `workers: 3` for local, `timeout: 60_000` global | Explicit parallelism and generous timeouts |
| `Makefile` | Added `RATE_LIMIT=1000` to `DEV_LOCAL_ENV` | Make rate limit explicit in dev mode |
| Various spec files | Adjusted selectors, labels, and test flows | Align tests with actual UI implementation |
