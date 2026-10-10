import type { StackInstance } from '../types';

/**
 * Fields that an update response may not have although the server knows them:
 * the follow state. Other computed fields (for example `values_drift` or
 * `cluster_name`) come from the response: the JSON omits a false or empty
 * value, so an absent field means false or empty.
 */
const KEEP_IF_ABSENT = ['following', 'follower_count'] as const satisfies readonly (keyof StackInstance)[];

/**
 * Merges an instance from an update response (for example update or extend)
 * into the current page state. All fields come from the response, so a
 * cleared field (for example `expires_at` or `values_drift`) stays cleared.
 * Only the follow state keeps its current value when the response does not
 * have it.
 *
 * @param prev - The current instance state, or null before the first load.
 * @param updated - The instance from the update response.
 * @returns The new instance state.
 */
export function mergeInstanceUpdate(prev: StackInstance | null, updated: StackInstance): StackInstance {
  if (!prev || prev.id !== updated.id) return updated;
  const merged: StackInstance = { ...updated };
  for (const field of KEEP_IF_ABSENT) {
    if (merged[field] === undefined && prev[field] !== undefined) {
      (merged as unknown as Record<string, unknown>)[field] = prev[field];
    }
  }
  return merged;
}
