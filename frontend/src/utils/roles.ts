export const ROLE_RANK: Record<string, number> = { user: 1, devops: 2, admin: 3 };

export function hasAtLeastRole(userRole: string | undefined, requiredRole: string): boolean {
  return (ROLE_RANK[userRole ?? ''] ?? 0) >= (ROLE_RANK[requiredRole] ?? 999);
}

/** Roles that may modify any stack instance, regardless of owner. */
const INSTANCE_MANAGER_ROLES = new Set(['admin', 'devops']);

/**
 * Returns true when the user may modify the stack instance and run its
 * lifecycle operations (deploy, stop, clean, delete, overrides, TTL).
 * The owner, an admin and a devops user may modify; other users may only view.
 *
 * @param user - The current user (id and role), or null when not logged in.
 * @param instance - The stack instance (owner_id).
 * @returns True if the user may modify the instance.
 */
export function canModifyInstance(
  user: { id: string; role: string } | null | undefined,
  instance: { owner_id: string } | null | undefined,
): boolean {
  if (!user || !instance) return false;
  if (INSTANCE_MANAGER_ROLES.has(user.role)) return true;
  return user.id !== '' && instance.owner_id === user.id;
}
