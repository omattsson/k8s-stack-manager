import type { NotificationChannelFilters } from '../../../types';

/** One choice of a filter picker: the stored ID and the shown label. */
export interface FilterOption {
  id: string;
  label: string;
}

/** Choices of the owner, definition and cluster pickers. */
export interface FilterOptions {
  users: FilterOption[];
  definitions: FilterOption[];
  clusters: FilterOption[];
}

/** Returns true when no filter has a value. */
export function filtersEmpty(filters?: NotificationChannelFilters): boolean {
  return !filters
    || ((filters.instance_name_patterns?.length ?? 0) === 0
      && (filters.owner_ids?.length ?? 0) === 0
      && (filters.definition_ids?.length ?? 0) === 0
      && (filters.cluster_ids?.length ?? 0) === 0);
}

/**
 * Returns a short text for the channel list: "All instances" without
 * filters, else one part per set filter (for example "Names: rdbtest-*;
 * Owners: 2").
 */
export function filtersSummary(filters?: NotificationChannelFilters): string {
  if (!filters || filtersEmpty(filters)) return 'All instances';
  const parts: string[] = [];
  const patterns = filters.instance_name_patterns ?? [];
  if (patterns.length > 0) {
    parts.push(`Names: ${patterns.length > 2 ? `${patterns.slice(0, 2).join(', ')} +${patterns.length - 2}` : patterns.join(', ')}`);
  }
  if (filters.owner_ids?.length) parts.push(`Owners: ${filters.owner_ids.length}`);
  if (filters.definition_ids?.length) parts.push(`Definitions: ${filters.definition_ids.length}`);
  if (filters.cluster_ids?.length) parts.push(`Clusters: ${filters.cluster_ids.length}`);
  return parts.join('; ');
}
