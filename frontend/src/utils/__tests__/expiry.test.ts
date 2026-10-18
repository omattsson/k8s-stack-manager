import { describe, it, expect } from 'vitest';
import { formatDurationShort, formatExpiry } from '../expiry';

describe('formatDurationShort', () => {
  it.each([
    [0, '0h 0m'],
    [59, '0h 59m'],
    [312, '5h 12m'],
    [1440, '1d 0h'],
    [1440 * 2 + 180, '2d 3h'],
    [-5, '0h 0m'],
  ])('formats %i minutes as %s', (minutes, expected) => {
    expect(formatDurationShort(minutes)).toBe(expected);
  });
});

describe('formatExpiry', () => {
  it('returns an empty string without an expiry', () => {
    expect(formatExpiry(undefined)).toBe('');
    expect(formatExpiry('not-a-date')).toBe('');
  });

  it('shows the time and the remaining duration for an expiry today', () => {
    const now = new Date(2030, 0, 1, 9, 18);
    const expiry = new Date(2030, 0, 1, 14, 30);
    const time = expiry.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    expect(formatExpiry(expiry.toISOString(), now)).toBe(`Expires ${time} (in 5h 12m)`);
  });

  it('adds the date for an expiry on another day', () => {
    const now = new Date(2030, 0, 1, 9, 0);
    const expiry = new Date(2030, 0, 2, 10, 0);
    const when = expiry.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    expect(formatExpiry(expiry.toISOString(), now)).toBe(`Expires ${when} (in 1d 1h)`);
  });
});
