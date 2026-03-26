import { describe, it, expect, vi, afterEach } from 'vitest';
import { timeAgo } from '../timeAgo';

describe('timeAgo', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('returns empty string for falsy input', () => {
    expect(timeAgo(null)).toBe('');
    expect(timeAgo(undefined)).toBe('');
    expect(timeAgo('')).toBe('');
  });

  it('returns empty string for invalid date string', () => {
    expect(timeAgo('not-a-date')).toBe('');
  });

  it('returns "just now" for future dates', () => {
    const future = new Date(Date.now() + 60_000).toISOString();
    expect(timeAgo(future)).toBe('just now');
  });

  it('returns "just now" for less than 60 seconds ago', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2025-01-15T12:00:30Z'));
    expect(timeAgo('2025-01-15T12:00:00Z')).toBe('just now');
  });

  it('returns minutes ago', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2025-01-15T12:05:00Z'));
    expect(timeAgo('2025-01-15T12:00:00Z')).toBe('5m ago');
  });

  it('returns hours ago', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2025-01-15T15:00:00Z'));
    expect(timeAgo('2025-01-15T12:00:00Z')).toBe('3h ago');
  });

  it('returns days ago', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2025-01-18T12:00:00Z'));
    expect(timeAgo('2025-01-15T12:00:00Z')).toBe('3d ago');
  });

  it('returns weeks ago', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2025-02-05T12:00:00Z'));
    expect(timeAgo('2025-01-15T12:00:00Z')).toBe('3w ago');
  });

  it('returns months ago', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2025-04-15T12:00:00Z'));
    expect(timeAgo('2025-01-15T12:00:00Z')).toBe('3mo ago');
  });
});
