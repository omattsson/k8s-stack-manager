import { request } from '@playwright/test';
import { apiLogin, ensureAdminEnabled, ensureDefaultCluster } from './helpers';
import { startStub } from './stub/server';

/**
 * Runs once before all specs. Many specs create stack instances, and an
 * instance create needs a default cluster (a record only; the tests need no
 * cluster access). Set up the shared default cluster here, so that no spec
 * depends on another spec to create it.
 *
 * Every run logs in as `admin` here first. last-admin.spec.ts disables `admin`
 * for a moment; if its worker died before its afterAll (SIGKILL, OOM), `admin`
 * stays disabled, and every spec would fail with 403 "Account disabled" and
 * hide the cause. ensureAdminEnabled enables it again (database fallback, when
 * E2E_DB_* are set) and prints a warning.
 *
 * It also starts the test stub (e2e/stub/server.ts): the subscriber of the
 * custom actions and an HTTPS webhook receiver. The returned function stops it
 * after the run (global teardown).
 */
export default async function globalSetup() {
  const stopStub = await startStub();
  const ctx = await request.newContext();
  try {
    const repaired = await ensureAdminEnabled(ctx);
    if (repaired) console.warn(`\n${repaired}\n`);
    const token = await apiLogin(ctx);
    await ensureDefaultCluster(ctx, token);
  } catch (err) {
    await stopStub();
    throw err;
  } finally {
    await ctx.dispose();
  }
  return stopStub;
}
