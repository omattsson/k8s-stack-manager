import { test, expect, request as playwrightRequest } from '@playwright/test';
import type { APIRequestContext, APIResponse } from '@playwright/test';
import {
  API_BASE,
  ADMIN_PASSWORD,
  E2E_DB_SKIP_REASON,
  apiLogin,
  e2eDbConfigured,
  enableUserInDb,
  uniqueName,
} from './helpers';

/**
 * Last enabled admin (#585, scenario 5).
 *
 * The spec disables the built-in `admin` account (and any other enabled admin)
 * for a moment, so a fresh admin of the spec is the only enabled admin. It runs
 * only in the Playwright project `isolated-last-admin`: the teardown project of
 * `isolated`, so `--project=isolated` (`make test-e2e-isolated` and the last
 * phase of `make test-e2e`, one worker) runs it after session-lifecycle, also
 * when that fails. It needs E2E_ISOLATED=1 and the E2E_DB_* variables (the
 * database fallback below); else it skips.
 * afterAll always enables the disabled admins again (through the API, else
 * directly in the database) and checks that `admin` can log in. If the worker
 * dies before afterAll, session-lifecycle.spec.ts enables `admin` at its next
 * run (ensureAdminEnabled in helpers.ts).
 *
 * Which answer proves the guard: the repository refuses to remove the last
 * enabled admin with ErrLastAdmin (409 "The last enabled admin cannot be
 * demoted, disabled or deleted"). Over HTTP that path cannot be reached: the
 * caller must be an enabled admin other than the target (handlers/users.go
 * refuses the own account first: role 403, disable and delete 400; the
 * guarded change then refuses a caller that is no enabled admin: 403 "Admin
 * role required"), and an enabled admin other than the target means the
 * target is not the last one. So the spec pins the answers that do protect
 * the last admin: the own-account refusals, the refusals of every other
 * caller, and the serialization of two admins who remove each other at the
 * same time (exactly one wins, an enabled admin remains).
 *
 * The isolated backend issues 10s access tokens: every step logs in again.
 */

const USERS_URL = `${API_BASE}/api/v1/users`;
const ME_URL = `${API_BASE}/api/v1/auth/me`;
const PASSWORD = 'e2e-last-admin-password';

interface Account {
  id: string;
  username: string;
}

interface UserRow {
  id: string;
  username: string;
  role: string;
  disabled: boolean;
}

function bearer(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}

/** A fresh access token of a spec account (tokens live 10s in the isolated phase). */
async function tokenOf(api: APIRequestContext, account: Account): Promise<string> {
  return apiLogin(api, account.username, PASSWORD);
}

async function listUsers(api: APIRequestContext, token: string): Promise<UserRow[]> {
  const res = await api.get(USERS_URL, { headers: bearer(token) });
  expect(res.status(), 'GET /users').toBe(200);
  return res.json();
}

async function enabledAdmins(api: APIRequestContext, token: string): Promise<UserRow[]> {
  return (await listUsers(api, token)).filter((u) => u.role === 'admin' && !u.disabled);
}

type Op = 'demote' | 'disable' | 'delete';

/** Demote (to user), disable or delete `targetId` as the caller of `token`. */
async function runOp(api: APIRequestContext, token: string, op: Op, targetId: string): Promise<APIResponse> {
  switch (op) {
    case 'demote':
      return api.put(`${USERS_URL}/${targetId}/role`, { headers: bearer(token), data: { role: 'user' } });
    case 'disable':
      return api.put(`${USERS_URL}/${targetId}/disable`, { headers: bearer(token) });
    case 'delete':
      return api.delete(`${USERS_URL}/${targetId}`, { headers: bearer(token) });
  }
}

const OP_SUCCESS: Record<Op, number> = { demote: 200, disable: 200, delete: 204 };

/** The answers of the handler for an op on the own account. */
const OWN_ACCOUNT: Record<Op, { status: number; error: string }> = {
  demote: { status: 403, error: 'Cannot change your own role' },
  disable: { status: 400, error: 'Cannot change your own account status' },
  delete: { status: 400, error: 'Cannot delete your own account' },
};

test.describe.configure({ mode: 'serial' });

test.describe('Last enabled admin (#585)', () => {
  test.skip(
    !process.env.E2E_ISOLATED,
    'Disables the built-in admin for a moment: runs only in the isolated projects (make test-e2e-isolated sets E2E_ISOLATED=1)',
  );
  // Without database access afterAll has no fallback to enable `admin` again.
  test.skip(!e2eDbConfigured(), `Disables the built-in admin for a moment. ${E2E_DB_SKIP_REASON}`);

  let api: APIRequestContext;
  /** The only enabled admin (a spec account). Changes when a race round has another winner. */
  let lastAdmin: Account;
  /** Every account that the spec created (deleted in afterAll). */
  const created: Account[] = [];
  /** Enabled admins that the spec disabled (enabled again in afterAll). */
  const disabledBySpec: Account[] = [];

  async function createAccount(adminToken: string, prefix: string, role: 'user' | 'devops' = 'user'): Promise<Account> {
    const username = uniqueName(prefix);
    const res = await api.post(`${API_BASE}/api/v1/auth/register`, {
      headers: bearer(adminToken),
      data: { username, password: PASSWORD, role },
    });
    expect(res.ok(), `register ${username}: ${res.status()}`).toBe(true);
    const account = { id: (await res.json()).id as string, username };
    created.push(account);
    return account;
  }

  /** Create an account and give it the admin role (a role change by `adminToken`). */
  async function createAdmin(adminToken: string, prefix: string): Promise<Account> {
    const account = await createAccount(adminToken, prefix);
    const res = await api.put(`${USERS_URL}/${account.id}/role`, { headers: bearer(adminToken), data: { role: 'admin' } });
    expect(res.status(), `promote ${account.username}`).toBe(200);
    expect(await res.json()).toMatchObject({ old_role: 'user', new_role: 'admin', changed: true });
    return account;
  }

  test.beforeAll(async () => {
    api = await playwrightRequest.newContext();
    const builtinToken = await apiLogin(api);
    lastAdmin = await createAdmin(builtinToken, 'e2e-last-admin');

    // The fresh admin disables every other enabled admin (the built-in admin,
    // and any other left in the database). Recorded before each call, so
    // afterAll also enables an account whose disable answered with an error.
    const token = await tokenOf(api, lastAdmin);
    for (const other of await enabledAdmins(api, token)) {
      if (other.id === lastAdmin.id) continue;
      disabledBySpec.push({ id: other.id, username: other.username });
      const res = await api.put(`${USERS_URL}/${other.id}/disable`, { headers: bearer(await tokenOf(api, lastAdmin)) });
      expect(res.status(), `disable ${other.username}`).toBe(200);
    }
    expect(disabledBySpec.map((a) => a.username)).toContain('admin');
    expect((await enabledAdmins(api, await tokenOf(api, lastAdmin))).map((u) => u.id)).toEqual([lastAdmin.id]);
  });

  /** Status of an admin login, with the 429 retry of apiLogin. */
  async function adminLoginStatus(): Promise<number> {
    const login = () => api.post(`${API_BASE}/api/v1/auth/login`, { data: { username: 'admin', password: ADMIN_PASSWORD } });
    let res = await login();
    for (let attempt = 0; res.status() === 429 && attempt < 5; attempt++) {
      await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)));
      res = await login();
    }
    return res.status();
  }

  test.afterAll(async () => {
    if (!api) return; // skipped before beforeAll
    const problems: string[] = [];
    try {
      // 1. Enable the admins that the spec disabled, through the API.
      for (const account of disabledBySpec) {
        try {
          const res = await api.put(`${USERS_URL}/${account.id}/enable`, { headers: bearer(await tokenOf(api, lastAdmin)) });
          if (res.status() !== 200) problems.push(`enable ${account.username} through the API: ${res.status()}`);
        } catch (err) {
          problems.push(`enable ${account.username} through the API: ${String(err)}`);
        }
      }
      // 2. Fallback: whatever the API did, `admin` must be enabled again.
      const status = await adminLoginStatus();
      if (status !== 200) {
        problems.push(`admin login after the API enable: ${status}`);
        for (const account of disabledBySpec) await enableUserInDb(account.username);
      }
    } finally {
      try {
        // 3. `admin` can log in again, and its token works.
        const adminToken = await apiLogin(api);
        const me = await api.get(ME_URL, { headers: bearer(adminToken) });
        expect(me.status(), `admin after the spec (${problems.join('; ') || 'no problems'})`).toBe(200);
        expect((await me.json()).role).toBe('admin');
        for (const account of disabledBySpec) {
          const row = (await listUsers(api, adminToken)).find((u) => u.id === account.id);
          expect(row?.disabled, `${account.username} enabled again`).toBe(false);
        }
      } finally {
        try {
          // 4. Delete the spec accounts (admin is an enabled admin again, so
          //    the last spec admin is not the last one any more). Runs even
          //    when step 3 failed: then as the last spec admin, which can
          //    delete every account but its own.
          const token = await apiLogin(api).catch(() => tokenOf(api, lastAdmin));
          const failed: string[] = [];
          for (const account of created) {
            const res = await api.delete(`${USERS_URL}/${account.id}`, { headers: bearer(token) });
            if (![204, 404].includes(res.status())) failed.push(`${account.username}: ${res.status()}`);
          }
          expect(failed, 'delete the spec accounts').toEqual([]);
        } finally {
          await api.dispose();
        }
      }
      expect(problems, 'the API path of the restore').toEqual([]);
    }
  });

  test('the only enabled admin cannot demote, disable or delete the own account', async () => {
    const token = await tokenOf(api, lastAdmin);
    expect((await enabledAdmins(api, token)).map((u) => u.id)).toEqual([lastAdmin.id]);

    // The ID in another case is the same account (MySQL compares IDs without case).
    for (const id of [lastAdmin.id, lastAdmin.id.toUpperCase()]) {
      for (const op of ['demote', 'disable', 'delete'] as const) {
        const res = await runOp(api, token, op, id);
        expect(res.status(), `${op} own account (${id})`).toBe(OWN_ACCOUNT[op].status);
        expect(await res.json()).toEqual({ error: OWN_ACCOUNT[op].error });
      }
      const devops = await api.put(`${USERS_URL}/${id}/role`, { headers: bearer(token), data: { role: 'devops' } });
      expect(devops.status(), `own role to devops (${id})`).toBe(403);
      expect(await devops.json()).toEqual({ error: OWN_ACCOUNT.demote.error });
    }

    // Nothing changed and nothing was revoked.
    expect((await enabledAdmins(api, token)).map((u) => u.id)).toEqual([lastAdmin.id]);
    const me = await api.get(ME_URL, { headers: bearer(token) });
    expect(me.status()).toBe(200);
    expect((await me.json()).role).toBe('admin');
  });

  test('no other caller can demote, disable or delete the last enabled admin', async () => {
    // The disabled built-in admin cannot log in (and its tokens were revoked by the disable).
    const builtin = await api.post(`${API_BASE}/api/v1/auth/login`, { data: { username: 'admin', password: ADMIN_PASSWORD } });
    expect(builtin.status()).toBe(403);
    expect(await builtin.json()).toEqual({ error: 'Account disabled' });

    // devops and user callers are refused by the admin gate.
    const adminToken = await tokenOf(api, lastAdmin);
    for (const role of ['devops', 'user'] as const) {
      const caller = await createAccount(adminToken, `e2e-last-admin-${role}`, role);
      const callerToken = await tokenOf(api, caller);
      for (const op of ['demote', 'disable', 'delete'] as const) {
        const res = await runOp(api, callerToken, op, lastAdmin.id);
        expect(res.status(), `${op} by ${role}`).toBe(403);
        expect(await res.json()).toEqual({ error: 'Insufficient permissions' });
      }
    }

    expect((await enabledAdmins(api, await tokenOf(api, lastAdmin))).map((u) => u.id)).toEqual([lastAdmin.id]);
  });

  /**
   * One round: a new admin and the current last admin remove each other at
   * the same time. Returns the status of the losing request: 403 when it
   * waited for the row lock and then found its caller removed (the requests
   * overlapped), 401 when its token was already revoked (they ran one after
   * the other).
   */
  async function raceRound(op: Op): Promise<number> {
    // A second admin: now the two are the only enabled admins.
    const partner = await createAdmin(await tokenOf(api, lastAdmin), `e2e-last-admin-${op}`);
    const [tokenA, tokenB] = await Promise.all([tokenOf(api, lastAdmin), tokenOf(api, partner)]);
    expect((await enabledAdmins(api, tokenA)).map((u) => u.id).sort()).toEqual([lastAdmin.id, partner.id].sort());

    const [resA, resB] = await Promise.all([runOp(api, tokenA, op, partner.id), runOp(api, tokenB, op, lastAdmin.id)]);
    const outcomes = [
      { winner: lastAdmin, loser: partner, res: resA, other: resB },
      { winner: partner, loser: lastAdmin, res: resB, other: resA },
    ].filter((o) => o.res.status() === OP_SUCCESS[op]);
    expect(outcomes.length, `statuses ${resA.status()} and ${resB.status()}: exactly one change succeeds`).toBe(1);
    const { winner, loser, other } = outcomes[0];
    // From here on the winner is the admin that afterAll uses.
    lastAdmin = winner;

    const otherBody = await other.json().catch(() => ({}));
    if (other.status() === 403) {
      expect(otherBody).toEqual({ error: 'Admin role required' });
    } else {
      expect(other.status(), `the losing ${op}: ${JSON.stringify(otherBody)}`).toBe(401);
    }

    // The winner is the only enabled admin, and it works.
    const winnerToken = await tokenOf(api, winner);
    expect((await enabledAdmins(api, winnerToken)).map((u) => u.id)).toEqual([winner.id]);
    const loserRow = (await listUsers(api, winnerToken)).find((u) => u.id === loser.id);
    if (op === 'demote') expect(loserRow).toMatchObject({ role: 'user', disabled: false });
    if (op === 'disable') expect(loserRow).toMatchObject({ role: 'admin', disabled: true });
    if (op === 'delete') expect(loserRow).toBeUndefined();

    // The winner now cannot remove itself either.
    const own = await runOp(api, winnerToken, op, winner.id);
    expect(own.status()).toBe(OWN_ACCOUNT[op].status);
    return other.status();
  }

  /** Rounds per op: stop at the first round that took the locked path (403). */
  const MAX_ROUNDS = 5;
  /** Number of rounds of each op that ended in the locked 403 path. */
  const lockedRounds: Record<Op, number> = { demote: 0, disable: 0, delete: 0 };

  for (const op of ['demote', 'disable', 'delete'] as const) {
    test(`two last admins who ${op} each other at the same time: exactly one wins, an enabled admin remains`, async () => {
      const losers: number[] = [];
      for (let round = 0; round < MAX_ROUNDS && lockedRounds[op] === 0; round++) {
        const status = await raceRound(op);
        losers.push(status);
        if (status === 403) lockedRounds[op]++;
      }
      test.info().annotations.push({
        type: lockedRounds[op] > 0 ? 'race' : 'warning',
        description: `${op}: losing statuses ${losers.join(', ')}${lockedRounds[op] > 0 ? '' : ' (no round overlapped)'}`,
      });
    });
  }

  test('the races took the locked path at least once', async () => {
    // A 401 loser means the two requests ran one after the other, which does
    // not exercise the row lock. Up to 15 rounds ran; at least one must have
    // overlapped (the loser waited for the lock and got 403).
    expect(
      lockedRounds.demote + lockedRounds.disable + lockedRounds.delete,
      `rounds that ended in 403 "Admin role required": ${JSON.stringify(lockedRounds)}`,
    ).toBeGreaterThan(0);
  });
});
