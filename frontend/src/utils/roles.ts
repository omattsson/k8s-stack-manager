export const ROLE_RANK: Record<string, number> = { user: 1, devops: 2, admin: 3 };

export function hasAtLeastRole(userRole: string | undefined, requiredRole: string): boolean {
  return (ROLE_RANK[userRole ?? ''] ?? 0) >= (ROLE_RANK[requiredRole] ?? 999);
}
