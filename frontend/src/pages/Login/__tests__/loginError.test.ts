import { describe, it, expect } from 'vitest';
import { describeLoginError, retryAfterSeconds } from '../loginError';

const withHeaders = (status: number, headers: unknown) => ({ response: { status, data: {}, headers } });

describe('retryAfterSeconds', () => {
  it('reads a delay in seconds', () => {
    expect(retryAfterSeconds(withHeaders(429, { 'retry-after': '45' }))).toBe(45);
  });

  it('reads an HTTP date', () => {
    const now = Date.parse('2026-10-19T12:00:00Z');
    expect(retryAfterSeconds(withHeaders(429, { 'retry-after': 'Mon, 19 Oct 2026 12:02:00 GMT' }), now)).toBe(120);
  });

  it('reads headers through a get() accessor (AxiosHeaders)', () => {
    const headers = { get: (name: string) => (name === 'retry-after' ? '10' : undefined) };
    expect(retryAfterSeconds(withHeaders(429, headers))).toBe(10);
  });

  it('ignores a missing, invalid, zero or too long value', () => {
    expect(retryAfterSeconds(withHeaders(429, {}))).toBeUndefined();
    expect(retryAfterSeconds(withHeaders(429, { 'retry-after': 'soon' }))).toBeUndefined();
    expect(retryAfterSeconds(withHeaders(429, { 'retry-after': '0' }))).toBeUndefined();
    expect(retryAfterSeconds(withHeaders(429, { 'retry-after': '86400' }))).toBeUndefined();
    expect(retryAfterSeconds(new Error('x'))).toBeUndefined();
  });
});

describe('describeLoginError', () => {
  it('rounds a long Retry-After up to minutes', () => {
    expect(describeLoginError(withHeaders(429, { 'retry-after': '90' })))
      .toBe('Too many sign-in attempts. Wait 2 minutes and try again.');
  });

  it('falls back to the credentials message for a 403 without a server message', () => {
    expect(describeLoginError({ response: { status: 403, data: {} } })).toBe('Invalid username or password');
  });
});
