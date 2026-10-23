// Load test users: each user has its own role and API key.
import http from 'k6/http';

export const ROLES = ['user', 'devops', 'admin'];

// Requests sent at the same time in setup and teardown.
const CHUNK = 10;

/**
 * Short random ID of a test run (lowercase, so it fits in instance names).
 * @returns {string}
 */
export function newRunId() {
  return (Date.now().toString(36).slice(-4) + Math.random().toString(36).slice(2, 4)).toLowerCase();
}

/**
 * Role of each of the n users. mix is "user:devops:admin" (weights, for
 * example "30:8:2"). Each role with a weight above 0 gets at least one user
 * when n allows it. The roles alternate, so the first virtual users already
 * have a mix of roles.
 * @param {number} n - Number of users
 * @param {string} mix - Weights
 * @returns {string[]} Roles
 */
export function rolePlan(n, mix) {
  const weights = String(mix).split(':').map((w) => Number(w.trim()));
  if (weights.length !== ROLES.length || weights.some((w) => !Number.isFinite(w) || w < 0)) {
    throw new Error(`USER_MIX "${mix}" must be three numbers user:devops:admin, for example 30:8:2`);
  }
  const sum = weights.reduce((a, b) => a + b, 0);
  if (sum <= 0) throw new Error(`USER_MIX "${mix}" has no user`);
  if (!Number.isInteger(n) || n < 1) throw new Error(`USERS must be a positive integer (got ${n})`);

  // Counts by the largest remainder, with at least one user for each weight > 0.
  const exact = weights.map((w) => (n * w) / sum);
  const counts = exact.map(Math.floor);
  let left = n - counts.reduce((a, b) => a + b, 0);
  const order = exact.map((e, i) => [e - Math.floor(e), i]).sort((a, b) => b[0] - a[0]);
  for (const [, i] of order) {
    if (left <= 0) break;
    counts[i]++;
    left--;
  }
  for (let i = 0; i < counts.length; i++) {
    if (weights[i] > 0 && counts[i] === 0) {
      const donor = counts.indexOf(Math.max(...counts));
      if (counts[donor] > 1) {
        counts[donor]--;
        counts[i]++;
      }
    }
  }

  // Spread: always pick the role that is furthest behind its share.
  const used = [0, 0, 0];
  const plan = [];
  for (let k = 0; k < n; k++) {
    let best = -1;
    let bestScore = Infinity;
    for (let i = 0; i < ROLES.length; i++) {
      if (used[i] >= counts[i]) continue;
      const score = used[i] / counts[i];
      if (score < bestScore) {
        bestScore = score;
        best = i;
      }
    }
    used[best]++;
    plan.push(ROLES[best]);
  }
  return plan;
}

/**
 * Logs in and returns the JWT, or null.
 * @param {string} baseUrl - Backend URL
 * @param {string} username
 * @param {string} password
 * @returns {string|null}
 */
export function adminLogin(baseUrl, username, password) {
  const res = http.post(`${baseUrl}/api/v1/auth/login`, JSON.stringify({ username, password }), {
    headers: { 'Content-Type': 'application/json' },
    tags: { name: 'POST /auth/login (setup)' },
  });
  if (res.status !== 200) return null;
  return res.json('token') || null;
}

function chunks(list, size) {
  const out = [];
  for (let i = 0; i < list.length; i += size) out.push(list.slice(i, i + size));
  return out;
}

/**
 * Creates one user for each entry of the plan, with the name
 * k6u-<runId>-<group><nnn>, and one API key for each user (expires in 1 day).
 * The caller admin registers the users with their role (an admin may set
 * it on register); a user with another role gets PUT /users/:id/role.
 * Every registered user goes into `created` before an error is thrown, so
 * the caller can delete them after a failure.
 * @param {string} baseUrl - Backend URL
 * @param {object} headers - Headers of an admin request (JWT)
 * @param {{role: string, group: string}[]} plan - Role and group of each user
 * @param {string} runId - Run ID
 * @param {object[]} created - Receives {id, username, role, group, key, keyId}
 * @returns {object[]} created
 */
export function createUsers(baseUrl, headers, plan, runId, created) {
  const password = `k6-${runId}-${Math.random().toString(36).slice(2, 12)}`;
  const counters = {};
  const wanted = plan.map(({ role, group }) => {
    counters[group] = (counters[group] || 0) + 1;
    return { role, group, username: `k6u-${runId}-${group}${String(counters[group]).padStart(3, '0')}` };
  });
  const errors = [];

  for (const part of chunks(wanted, CHUNK)) {
    const responses = http.batch(part.map((u) => [
      'POST',
      `${baseUrl}/api/v1/auth/register`,
      JSON.stringify({ username: u.username, password, display_name: `Load test ${u.username}`, role: u.role }),
      { headers, tags: { name: 'POST /auth/register (setup)' } },
    ]));
    const fresh = [];
    responses.forEach((res, i) => {
      if (res.status !== 201) {
        errors.push(`register ${part[i].username} failed (status ${res.status}): ${res.body}`);
        return;
      }
      const user = {
        id: res.json('id'), username: part[i].username, role: res.json('role'), group: part[i].group, key: '', keyId: '',
      };
      created.push(user);
      fresh.push([user, part[i].role]);
    });
    for (const [user, role] of fresh) {
      if (user.role === role) continue;
      const roleRes = http.put(`${baseUrl}/api/v1/users/${user.id}/role`, JSON.stringify({ role }), {
        headers,
        tags: { name: 'PUT /users/:id/role (setup)' },
      });
      if (roleRes.status === 200) user.role = role;
      else errors.push(`role ${role} for ${user.username} failed (status ${roleRes.status})`);
    }
    if (errors.length > 0) throw new Error(`setup: ${errors.join('; ')}`);
  }

  for (const part of chunks(created.filter((u) => !u.key), CHUNK)) {
    const responses = http.batch(part.map((u) => [
      'POST',
      `${baseUrl}/api/v1/users/${u.id}/api-keys`,
      JSON.stringify({ name: 'k6-users', expires_in_days: 1 }),
      { headers, tags: { name: 'POST /users/:id/api-keys (setup)' } },
    ]));
    responses.forEach((res, i) => {
      if (res.status !== 201) {
        errors.push(`API key for ${part[i].username} failed (status ${res.status})`);
        return;
      }
      part[i].key = res.json('raw_key');
      part[i].keyId = res.json('id');
    });
    if (errors.length > 0) throw new Error(`setup: ${errors.join('; ')}`);
  }
  return created;
}

/**
 * Deletes the users. A user delete also deletes the API keys of the user.
 * @param {string} baseUrl - Backend URL
 * @param {object} headers - Headers of an admin request
 * @param {object[]} users - Users with id
 * @returns {number} Users that could not be deleted
 */
export function deleteUsers(baseUrl, headers, users) {
  let failed = 0;
  for (const part of chunks((users || []).filter((u) => u && u.id), CHUNK)) {
    const responses = http.batch(part.map((u) => [
      'DELETE',
      `${baseUrl}/api/v1/users/${u.id}`,
      null,
      { headers, tags: { name: 'DELETE /users/:id (teardown)' } },
    ]));
    responses.forEach((res) => {
      if (res.status !== 204 && res.status !== 404) failed++;
    });
  }
  return failed;
}

/**
 * The user of a virtual user: users[(vu - 1) % n]. Pass the users of one
 * scenario only, so scenarios never share a user.
 * @param {object[]} users
 * @param {number} vu - __VU
 * @returns {object|null}
 */
export function pickUser(users, vu) {
  if (!users || users.length === 0) return null;
  return users[(vu - 1) % users.length];
}

/**
 * The users of one group (scenario).
 * @param {object[]} users
 * @param {string} group
 * @returns {object[]}
 */
export function usersOfGroup(users, group) {
  return (users || []).filter((u) => u.group === group);
}
