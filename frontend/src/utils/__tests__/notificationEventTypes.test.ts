import { describe, it, expect } from 'vitest';
import fixture from './notification-event-types.json';
import {
  INSTANCE_EVENT_LABELS,
  SYSTEM_EVENT_LABELS,
  PENDING_EVENT_LABELS,
  eventTypeLabel,
  eventTypesForRole,
  mergePreferences,
} from '../notificationEventTypes';

describe('notification event types', () => {
  // The backend test writes the fixture from notifier.InstanceEventTypes and
  // notifier.SystemEventTypes, so this test keeps both lists equal (issue #498).
  it('matches the backend instance event types', () => {
    expect(Object.keys(INSTANCE_EVENT_LABELS)).toEqual(fixture.instance);
  });

  it('matches the backend system event types', () => {
    expect(Object.keys(SYSTEM_EVENT_LABELS)).toEqual(fixture.system);
  });

  it.each([
    ['user', fixture.instance],
    ['devops', [...fixture.instance, ...fixture.system]],
    ['admin', [...fixture.instance, ...fixture.system]],
  ])('gives role %s its event types', (role, want) => {
    expect(eventTypesForRole(role)).toEqual(want);
  });
});

describe('mergePreferences', () => {
  it('defaults missing types to on and keeps stored values', () => {
    const prefs = mergePreferences([{ event_type: 'deployment.error', enabled: false }], 'user');
    expect(prefs).toHaveLength(fixture.instance.length);
    expect(prefs.find((p) => p.event_type === 'deployment.error')?.enabled).toBe(false);
    expect(prefs.find((p) => p.event_type === 'deployment.success')?.enabled).toBe(true);
  });

  it('keeps a stored known type outside the role and drops an unknown type', () => {
    const prefs = mergePreferences(
      [
        { event_type: 'quota.warning', enabled: false },
        { event_type: 'bogus.event', enabled: true },
      ],
      'user',
    );
    expect(prefs.map((p) => p.event_type)).toContain('quota.warning');
    expect(prefs.map((p) => p.event_type)).not.toContain('bogus.event');
  });
});

describe('eventTypeLabel', () => {
  it('labels every preference type and the pending stack.expired type', () => {
    for (const t of [...fixture.instance, ...fixture.system]) {
      expect(eventTypeLabel(t)).not.toBe(t);
    }
    expect(eventTypeLabel('stack.expired')).toBe('Stack expired (TTL reached)');
    expect(eventTypeLabel('bogus.event')).toBe('bogus.event');
  });

  it('keeps pending types out of the preference list until the backend offers them', () => {
    // The backend rejects unknown types on save, so a pending type must not
    // be in the preference list. Move it when the fixture gets it.
    for (const t of Object.keys(PENDING_EVENT_LABELS)) {
      expect(fixture.instance).not.toContain(t);
      expect(fixture.system).not.toContain(t);
      expect(eventTypesForRole('admin')).not.toContain(t);
    }
  });
});
