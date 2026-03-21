import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import useCountdown from '../../hooks/useCountdown';

describe('useCountdown', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('returns null when expiresAt is undefined', () => {
    const { result } = renderHook(() => useCountdown(undefined));
    expect(result.current).toBeNull();
  });

  it('returns null when expiresAt is null', () => {
    const { result } = renderHook(() => useCountdown(null));
    expect(result.current).toBeNull();
  });

  it('shows hours and minutes for distant expiry', () => {
    const future = new Date(Date.now() + 3 * 60 * 60_000 + 42 * 60_000).toISOString();
    const { result } = renderHook(() => useCountdown(future));

    expect(result.current).not.toBeNull();
    expect(result.current!.remaining).toBe('3h 42m');
    expect(result.current!.isWarning).toBe(false);
    expect(result.current!.isCritical).toBe(false);
    expect(result.current!.isExpired).toBe(false);
  });

  it('shows only minutes for less than 1 hour', () => {
    const future = new Date(Date.now() + 45 * 60_000).toISOString();
    const { result } = renderHook(() => useCountdown(future));

    expect(result.current!.remaining).toBe('45m');
    expect(result.current!.isWarning).toBe(true);
    expect(result.current!.isCritical).toBe(false);
  });

  it('sets isCritical when less than 30 minutes', () => {
    const future = new Date(Date.now() + 15 * 60_000).toISOString();
    const { result } = renderHook(() => useCountdown(future));

    expect(result.current!.remaining).toBe('15m');
    expect(result.current!.isWarning).toBe(true);
    expect(result.current!.isCritical).toBe(true);
  });

  it('sets isExpired when past expiry', () => {
    const past = new Date(Date.now() - 60_000).toISOString();
    const { result } = renderHook(() => useCountdown(past));

    expect(result.current!.remaining).toBe('Expired');
    expect(result.current!.isExpired).toBe(true);
  });

  it('updates every 60 seconds', () => {
    // Start at 2h 1m remaining
    const future = new Date(Date.now() + 2 * 60 * 60_000 + 1 * 60_000).toISOString();
    const { result } = renderHook(() => useCountdown(future));

    expect(result.current!.remaining).toBe('2h 1m');

    // Advance 60 seconds
    act(() => {
      vi.advanceTimersByTime(60_000);
    });

    expect(result.current!.remaining).toBe('2h 0m');
  });
});
