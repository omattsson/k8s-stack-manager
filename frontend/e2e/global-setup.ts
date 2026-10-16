import { request } from '@playwright/test';
import { apiLogin, ensureDefaultCluster } from './helpers';

/**
 * Runs once before all specs. Many specs create stack instances, and an
 * instance create needs a default cluster (a record only; the tests need no
 * cluster access). Set up the shared default cluster here, so that no spec
 * depends on another spec to create it.
 */
export default async function globalSetup() {
  const ctx = await request.newContext();
  try {
    const token = await apiLogin(ctx);
    await ensureDefaultCluster(ctx, token);
  } finally {
    await ctx.dispose();
  }
}
