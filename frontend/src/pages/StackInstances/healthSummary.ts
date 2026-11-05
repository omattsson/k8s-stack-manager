export interface HealthChange {
  id: string;
  name: string;
  status: string;
}

/** One sentence for a screen reader, or an empty string when nothing changed. */
export function summarizeHealthChanges(changes: HealthChange[]): string {
  if (changes.length === 0) return '';
  const list = changes.map((c) => `${c.name} ${c.status.replace(/_/g, ' ')}`).join(', ');
  return `${changes.length} ${changes.length === 1 ? 'stack' : 'stacks'} changed health: ${list}`;
}

/**
 * The stacks whose health differs from the last known value. A stack without a
 * last known value is a first load and gives no change.
 */
export function diffHealth(
  previous: Record<string, string>,
  next: Record<string, string>,
  nameOf: (id: string) => string | undefined,
): HealthChange[] {
  const changes: HealthChange[] = [];
  for (const [id, status] of Object.entries(next)) {
    const before = previous[id];
    const name = nameOf(id);
    if (before !== undefined && before !== status && name) changes.push({ id, name, status });
  }
  return changes;
}
