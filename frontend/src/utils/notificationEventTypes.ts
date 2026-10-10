import type { NotificationPreference } from '../types';

/**
 * In-app events of a stack instance. The owner and the followers of the
 * instance get them.
 *
 * The backend has the same list (`notifier.InstanceEventTypes`) and rejects
 * other types in the preferences with 400. The test
 * `__tests__/notificationEventTypes.test.ts` compares both lists through the
 * fixture `__tests__/notification-event-types.json`, which the backend test
 * writes (`go test ./internal/notifier -run TestPreferenceEventTypesFixture -update`).
 */
export const INSTANCE_EVENT_LABELS: Record<string, string> = {
  'deployment.success': 'Deployment succeeded',
  'deployment.error': 'Deployment failed',
  'deployment.partial': 'Deployment partly failed',
  'deployment.warning': 'Deployment warning (post-deploy step failed)',
  'deploy.timeout': 'Deployment timed out',
  'deployment.stopped': 'Stack stopped',
  'stop.error': 'Stop failed',
  'instance.created': 'Stack created',
  'instance.deleted': 'Stack deleted',
  'clean.completed': 'Cleanup completed',
  'clean.error': 'Cleanup failed',
  'rollback.completed': 'Rollback completed',
  'rollback.error': 'Rollback failed',
  'stack.expiring': 'Stack expiring soon',
  'stack.expired': 'Stack expired (TTL)',
  'cleanup.policy.stop': 'Stack stopped by a cleanup policy',
  'cleanup.policy.clean': 'Stack cleaned by a cleanup policy',
};

/**
 * System events. Only admin and devops users get them. The backend has the
 * same list (`notifier.SystemEventTypes`).
 */
export const SYSTEM_EVENT_LABELS: Record<string, string> = {
  'cleanup.policy.executed': 'Cleanup policy ran (admin and devops)',
  'quota.warning': 'Cluster quota warning (admin and devops)',
  'secret.expiring': 'Registry secret expiring (admin and devops)',
};

/** Labels of all event types that a user can set in the preferences. */
export const EVENT_TYPE_LABELS: Record<string, string> = { ...INSTANCE_EVENT_LABELS, ...SYSTEM_EVENT_LABELS };

/**
 * Returns the label of an event type.
 *
 * @param eventType - The event type, for example "deployment.success".
 * @returns The label, or the event type itself when no label is known.
 */
export function eventTypeLabel(eventType: string): string {
  return EVENT_TYPE_LABELS[eventType] ?? eventType;
}

/**
 * Returns the event types that a user with the role can get.
 *
 * @param role - The role of the user.
 * @returns The instance event types, plus the system event types for admin and devops.
 */
export function eventTypesForRole(role?: string): string[] {
  const types = Object.keys(INSTANCE_EVENT_LABELS);
  return role === 'admin' || role === 'devops' ? [...types, ...Object.keys(SYSTEM_EVENT_LABELS)] : types;
}

/**
 * Returns one preference per event type of the role: the stored value, or
 * on (the backend default) when no value is stored. Stored preferences of
 * other known event types (for example system types after a role change) are
 * kept at the end. Stored preferences of unknown event types are dropped,
 * because the backend rejects them on save.
 *
 * @param stored - The preferences from the backend.
 * @param role - The role of the user.
 * @returns The preferences to show and to save.
 */
export function mergePreferences(stored: NotificationPreference[], role?: string): NotificationPreference[] {
  const byType = new Map(stored.map((p) => [p.event_type, p]));
  const types = eventTypesForRole(role);
  const merged = types.map((et) => ({ event_type: et, enabled: byType.get(et)?.enabled ?? true }));
  const extra = stored.filter((p) => !types.includes(p.event_type) && p.event_type in EVENT_TYPE_LABELS);
  return [...merged, ...extra];
}
