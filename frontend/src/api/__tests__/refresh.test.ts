import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const mockPost = vi.hoisted(() => vi.fn());

vi.mock('axios', () => ({
  default: {
    create: vi.fn(() => ({ post: mockPost })),
  },
}));

import axios from 'axios';
import {
  refreshAccessToken,
  onTokenRefreshed,
  isAuthRejection,
  REFRESH_LOCK_NAME,
  REFRESH_TIMEOUT_MS,
} from '../refresh';

// Capture before any test clears mock state.
const refreshClientConfig = vi.mocked(axios.create).mock.calls[0]?.[0];

function fakeJwt(payload: Record<string, unknown>): string {
  const header = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const body = btoa(JSON.stringify(payload));
  return `${header}.${body}.fakesig`;
}

function tokenWithExp(offsetSeconds: number, username = 'alice'): string {
  return fakeJwt({
    user_id: '42',
    username,
    role: 'user',
    exp: Math.floor(Date.now() / 1000) + offsetSeconds,
  });
}

type LockCallback = () => Promise<string>;

function installLocks(request: (name: string, cb: LockCallback) => Promise<string>) {
  Object.defineProperty(navigator, 'locks', {
    value: { request: vi.fn(request) },
    configurable: true,
    writable: true,
  });
}

function removeLocks() {
  // Delete the own property so `'locks' in navigator` is false again (jsdom has no Web Locks).
  Reflect.deleteProperty(navigator, 'locks');
}

describe('refreshAccessToken', () => {
  beforeEach(() => {
    localStorage.clear();
    mockPost.mockReset();
    removeLocks();
  });

  afterEach(() => {
    removeLocks();
  });

  describe('without Web Locks (in-tab fallback)', () => {
    it('shares one API call between concurrent callers', async () => {
      const expired = tokenWithExp(-60);
      const fresh = tokenWithExp(900);
      localStorage.setItem('token', expired);
      mockPost.mockResolvedValueOnce({ data: { token: fresh } });

      const [a, b] = await Promise.all([refreshAccessToken(expired), refreshAccessToken(expired)]);

      expect(mockPost).toHaveBeenCalledTimes(1);
      expect(mockPost).toHaveBeenCalledWith('/api/v1/auth/refresh');
      expect(a).toBe(fresh);
      expect(b).toBe(fresh);
      expect(localStorage.getItem('token')).toBe(fresh);
    });

    it('uses a newer stored token instead of calling the API', async () => {
      const failed = tokenWithExp(-60);
      const newer = tokenWithExp(900);
      localStorage.setItem('token', newer);

      const result = await refreshAccessToken(failed);

      expect(mockPost).not.toHaveBeenCalled();
      expect(result).toBe(newer);
    });

    it('calls the API when the stored token is the one that failed', async () => {
      const failed = tokenWithExp(900);
      const fresh = tokenWithExp(1800, 'alice2');
      localStorage.setItem('token', failed);
      mockPost.mockResolvedValueOnce({ data: { token: fresh } });

      const result = await refreshAccessToken(failed);

      expect(mockPost).toHaveBeenCalledTimes(1);
      expect(result).toBe(fresh);
    });

    it('removes the stored token and rethrows when the refresh fails', async () => {
      const expired = tokenWithExp(-60);
      localStorage.setItem('token', expired);
      const error = Object.assign(new Error('Session revoked'), { response: { status: 401 } });
      mockPost.mockRejectedValueOnce(error);

      await expect(refreshAccessToken(expired)).rejects.toBe(error);
      expect(localStorage.getItem('token')).toBeNull();
    });

    it('removes the stored token when the refresh answers 403', async () => {
      const expired = tokenWithExp(-60);
      localStorage.setItem('token', expired);
      mockPost.mockRejectedValueOnce({ response: { status: 403 } });

      await expect(refreshAccessToken(expired)).rejects.toEqual({ response: { status: 403 } });
      expect(localStorage.getItem('token')).toBeNull();
    });

    it.each([
      ['a 500 answer', { response: { status: 500 } }],
      ['a 503 answer', { response: { status: 503 } }],
      ['a network error', new Error('Network Error')],
      ['a timeout', Object.assign(new Error('timeout of 10000ms exceeded'), { code: 'ECONNABORTED' })],
    ])('keeps the stored token and rethrows on %s', async (_label, refreshError) => {
      const expired = tokenWithExp(-60);
      localStorage.setItem('token', expired);
      mockPost.mockRejectedValueOnce(refreshError);

      await expect(refreshAccessToken(expired)).rejects.toBe(refreshError);
      expect(localStorage.getItem('token')).toBe(expired);
    });

    it('uses a token that another tab stored while the refresh failed', async () => {
      const expired = tokenWithExp(-60);
      const otherTab = tokenWithExp(900);
      localStorage.setItem('token', expired);
      mockPost.mockImplementationOnce(async () => {
        localStorage.setItem('token', otherTab);
        throw new Error('rotated');
      });

      await expect(refreshAccessToken(expired)).resolves.toBe(otherTab);
      expect(localStorage.getItem('token')).toBe(otherTab);
    });

    it('notifies listeners with the new token', async () => {
      const fresh = tokenWithExp(900);
      mockPost.mockResolvedValueOnce({ data: { token: fresh } });
      const listener = vi.fn();
      const unsubscribe = onTokenRefreshed(listener);

      await refreshAccessToken(null);
      unsubscribe();

      expect(listener).toHaveBeenCalledWith(fresh);
    });
  });

  it('creates the refresh client with a 15 s timeout and the refresh cookie', () => {
    expect(REFRESH_TIMEOUT_MS).toBe(15000);
    expect(refreshClientConfig).toMatchObject({ timeout: 15000, withCredentials: true });
  });

  describe('with Web Locks', () => {
    it('refreshes inside the auth-refresh lock', async () => {
      const expired = tokenWithExp(-60);
      const fresh = tokenWithExp(900);
      localStorage.setItem('token', expired);
      mockPost.mockResolvedValueOnce({ data: { token: fresh } });
      installLocks((_name, cb) => cb());

      const result = await refreshAccessToken(expired);

      expect(navigator.locks.request).toHaveBeenCalledWith(REFRESH_LOCK_NAME, expect.any(Function));
      expect(mockPost).toHaveBeenCalledTimes(1);
      expect(result).toBe(fresh);
    });

    it('uses the token another tab stored while this tab waited for the lock', async () => {
      const expired = tokenWithExp(-60);
      const otherTab = tokenWithExp(900);
      localStorage.setItem('token', expired);
      // Simulate another tab that holds the lock, refreshes, and stores its token.
      installLocks(async (_name, cb) => {
        localStorage.setItem('token', otherTab);
        return cb();
      });

      const result = await refreshAccessToken(expired);

      expect(mockPost).not.toHaveBeenCalled();
      expect(result).toBe(otherTab);
      expect(localStorage.getItem('token')).toBe(otherTab);
    });

    it('releases the lock after a timeout so a later caller can refresh', async () => {
      vi.useFakeTimers();
      try {
        const expired = tokenWithExp(-60);
        const fresh = tokenWithExp(900);
        localStorage.setItem('token', expired);
        // Exclusive lock: each callback starts after the previous one settles.
        let held: Promise<unknown> = Promise.resolve();
        installLocks((_name, cb) => {
          const run = held.then(cb);
          held = run.catch(() => undefined);
          return run;
        });
        const timeoutError = Object.assign(new Error(`timeout of ${REFRESH_TIMEOUT_MS}ms exceeded`), {
          code: 'ECONNABORTED',
        });
        // First POST hangs until the client timeout fires.
        mockPost
          .mockImplementationOnce(() => new Promise((_resolve, reject) => {
            setTimeout(() => reject(timeoutError), REFRESH_TIMEOUT_MS);
          }))
          .mockResolvedValueOnce({ data: { token: fresh } });

        const first = refreshAccessToken(expired);
        const firstSettled = expect(first).rejects.toBe(timeoutError);
        await vi.advanceTimersByTimeAsync(REFRESH_TIMEOUT_MS);
        await firstSettled;

        // The timeout is not 401/403: the token stays.
        expect(localStorage.getItem('token')).toBe(expired);

        await expect(refreshAccessToken(expired)).resolves.toBe(fresh);
        expect(mockPost).toHaveBeenCalledTimes(2);
        expect(navigator.locks.request).toHaveBeenCalledTimes(2);
      } finally {
        vi.useRealTimers();
      }
    });

    it('requests the lock once for concurrent callers in one tab', async () => {
      const expired = tokenWithExp(-60);
      const fresh = tokenWithExp(900);
      localStorage.setItem('token', expired);
      mockPost.mockResolvedValueOnce({ data: { token: fresh } });
      installLocks((_name, cb) => cb());

      await Promise.all([refreshAccessToken(expired), refreshAccessToken(expired)]);

      expect(navigator.locks.request).toHaveBeenCalledTimes(1);
      expect(mockPost).toHaveBeenCalledTimes(1);
    });
  });
});

describe('isAuthRejection', () => {
  it.each([
    [{ response: { status: 401 } }, true],
    [{ response: { status: 403 } }, true],
    [{ response: { status: 500 } }, false],
    [{ response: undefined }, false],
    [new Error('Network Error'), false],
    [null, false],
  ])('%j -> %s', (error, expected) => {
    expect(isAuthRejection(error)).toBe(expected);
  });
});
