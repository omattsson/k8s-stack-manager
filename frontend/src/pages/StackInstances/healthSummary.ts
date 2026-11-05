export interface HealthChange {
  id: string;
  name: string;
  status: string;
  /** Pod health, or the lifecycle status of the stack (deploying, stopped, ...). */
  kind: 'health' | 'status';
}

const MAX_NAMED = 5;

/** One sentence for a screen reader, or an empty string when nothing changed. */
export function summarizeHealthChanges(changes: HealthChange[]): string {
  if (changes.length === 0) return '';
  const named = changes.slice(0, MAX_NAMED).map((c) => `${c.name} ${c.kind === 'health' ? 'health ' : ''}${c.status.replace(/_/g, ' ')}`);
  if (changes.length > MAX_NAMED) named.push(`and ${changes.length - MAX_NAMED} more`);
  const list = named.join(', ');
  return `${changes.length} stack ${changes.length === 1 ? 'update' : 'updates'}: ${list}`;
}

/**
 * The stacks whose health differs from the last known value. A stack without a
 * last known value is a first load and gives no change.
 */
export function diffHealth(
  previous: Record<string, string>,
  next: Record<string, string>,
  nameOf: (id: string) => string | undefined,
  kind: HealthChange['kind'] = 'health',
): HealthChange[] {
  const changes: HealthChange[] = [];
  for (const [id, status] of Object.entries(next)) {
    const before = previous[id];
    const name = nameOf(id);
    if (before !== undefined && before !== status && name) changes.push({ id, name, status, kind });
  }
  return changes;
}
