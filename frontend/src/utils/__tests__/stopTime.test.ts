import { describe, it, expect } from 'vitest';
import {
  formatStopTime,
  fromLocalInputValue,
  hasFixedStopTime,
  MAX_STOP_TIME_MARGIN_MINUTES,
  MIN_STOP_TIME_MINUTES,
  stopTimeBounds,
  stopTimeError,
  toLocalInputValue,
} from '../stopTime';

describe('stopTime', () => {
  const now = new Date(2026, 9, 16, 12, 0, 0); // local time

  it('converts between local input values and ISO times', () => {
    const local = new Date(2026, 9, 17, 18, 5);
    expect(toLocalInputValue(local)).toBe('2026-10-17T18:05');
    expect(fromLocalInputValue('2026-10-17T18:05')).toBe(local.toISOString());
  });

  it.each(['', '2026-10-17', 'abc', '2026-13-40T99:99'])('rejects the input value %j', (value) => {
    expect(fromLocalInputValue(value)).toBeNull();
  });

  it('bounds: now plus the minimum, to now plus the limit minus the clock margin', () => {
    const { min, max } = stopTimeBounds(7, now);
    expect(min.getTime()).toBe(now.getTime() + MIN_STOP_TIME_MINUTES * 60_000);
    expect(max.getTime()).toBe(now.getTime() + 7 * 86_400_000 - MAX_STOP_TIME_MARGIN_MINUTES * 60_000);
  });

  it('bounds: a later current expiry moves the minimum after it', () => {
    const after = new Date(now.getTime() + 3 * 3600_000);
    expect(stopTimeBounds(30, now, after).min.getTime()).toBe(after.getTime() + 60_000);
  });

  it('bounds: an invalid limit gives 30 days', () => {
    expect(stopTimeBounds(0, now).max.getTime()).toBe(now.getTime() + 30 * 86_400_000 - MAX_STOP_TIME_MARGIN_MINUTES * 60_000);
  });

  it('checks a stop time against the bounds', () => {
    const bounds = stopTimeBounds(7, now);
    expect(stopTimeError(new Date(now.getTime() + 86_400_000).toISOString(), bounds)).toBeNull();
    expect(stopTimeError(bounds.max.toISOString(), bounds)).toBeNull();
    expect(stopTimeError(null, bounds)).toBe('Choose a stop date and time');
    expect(stopTimeError(new Date(now.getTime() + 5 * 60_000).toISOString(), bounds)).toMatch(/^Choose a time after /);
    expect(stopTimeError(new Date(bounds.max.getTime() + 60_000).toISOString(), bounds)).toMatch(/^Choose a time before /);
  });

  it('formats a stop time with the time left', () => {
    const stop = new Date(now.getTime() + (2 * 24 + 6) * 3600_000);
    expect(formatStopTime(stop.toISOString(), now)).toMatch(/\(in 2d 6h\)$/);
    expect(formatStopTime(undefined, now)).toBe('');
    expect(formatStopTime('not a date', now)).toBe('');
  });

  it('knows a fixed stop time', () => {
    expect(hasFixedStopTime({ ttl_minutes: 0, expires_at: '2026-10-17T16:00:00Z' })).toBe(true);
    expect(hasFixedStopTime({ expires_at: '2026-10-17T16:00:00Z' })).toBe(true);
    expect(hasFixedStopTime({ ttl_minutes: 60, expires_at: '2026-10-17T16:00:00Z' })).toBe(false);
    expect(hasFixedStopTime({ ttl_minutes: 0 })).toBe(false);
    expect(hasFixedStopTime(null)).toBe(false);
  });
});
