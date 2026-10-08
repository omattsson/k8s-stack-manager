import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';

// Mock authService before importing AuthContext
vi.mock('../../api/client', () => ({
  authService: {
    login: vi.fn(),
    logout: vi.fn(),
    refresh: vi.fn(),
  },
}));

// Capture listeners of the shared refresh so tests can simulate an in-tab refresh.
const refreshListeners = vi.hoisted(() => new Set<(token: string) => void>());
vi.mock('../../api/refresh', () => ({
  TOKEN_STORAGE_KEY: 'token',
  isAuthRejection: (error: unknown) => {
    const status = (error as { response?: { status?: number } } | null)?.response?.status;
    return status === 401 || status === 403;
  },
  onTokenRefreshed: (listener: (token: string) => void) => {
    refreshListeners.add(listener);
    return () => {
      refreshListeners.delete(listener);
    };
  },
}));

import { AuthProvider, useAuth } from '../AuthContext';
import { authService } from '../../api/client';

// Helper to create a fake JWT token with a given payload
function fakeJwt(payload: Record<string, unknown>): string {
  const header = btoa(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const body = btoa(JSON.stringify(payload));
  const signature = 'fakesig';
  return `${header}.${body}.${signature}`;
}

/**
 * Creates a mock localStorage backed by a plain Map.
 * All methods are vi.fn() so tests can assert calls without
 * corrupting jsdom's native localStorage implementation.
 */
function createMockLocalStorage() {
  const store = new Map<string, string>();
  return {
    getItem: vi.fn((key: string) => store.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value);
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key);
    }),
    clear: vi.fn(() => {
      store.clear();
    }),
    get length() {
      return store.size;
    },
    key: vi.fn((index: number) => {
      const keys = Array.from(store.keys());
      return keys[index] ?? null;
    }),
  };
}

const wrapper = ({ children }: { children: ReactNode }) => (
  <AuthProvider>{children}</AuthProvider>
);

describe('AuthContext', () => {
  let mockStorage: ReturnType<typeof createMockLocalStorage>;
  let originalLocalStorage: Storage;

  beforeEach(() => {
    originalLocalStorage = globalThis.localStorage;
    mockStorage = createMockLocalStorage();
    Object.defineProperty(globalThis, 'localStorage', {
      value: mockStorage,
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    Object.defineProperty(globalThis, 'localStorage', {
      value: originalLocalStorage,
      writable: true,
      configurable: true,
    });
    vi.clearAllMocks();
  });

  describe('useAuth outside provider', () => {
    it('throws when used outside AuthProvider', () => {
      // Suppress console.error for the expected React error
      const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
      expect(() => renderHook(() => useAuth())).toThrow(
        'useAuth must be used within an AuthProvider'
      );
      spy.mockRestore();
    });
  });

  describe('initial state', () => {
    it('starts unauthenticated when no token in localStorage', async () => {
      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
    });

    it('restores user from valid non-expired token in localStorage', async () => {
      const futureExp = Math.floor(Date.now() / 1000) + 3600;
      const token = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'devops',
        exp: futureExp,
      });
      mockStorage.setItem('token', token);

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(result.current.isAuthenticated).toBe(true);
      expect(result.current.user).toEqual({
        id: '42',
        username: 'alice',
        display_name: 'alice',
        role: 'devops',
        auth_provider: 'local',
        disabled: false,
        service_account: false,
        created_at: '',
        updated_at: '',
      });
    });

    it('clears expired token from localStorage on mount when refresh fails', async () => {
      const pastExp = Math.floor(Date.now() / 1000) - 3600;
      const token = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'user',
        exp: pastExp,
      });
      mockStorage.setItem('token', token);
      (authService.refresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        Object.assign(new Error('refresh failed'), { response: { status: 401 } }),
      );

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
      expect(mockStorage.removeItem).toHaveBeenCalledWith('token');
    });

    it('clears an expired token on mount when refresh answers 403', async () => {
      const token = fakeJwt({ user_id: '42', username: 'alice', role: 'user', exp: Math.floor(Date.now() / 1000) - 3600 });
      mockStorage.setItem('token', token);
      (authService.refresh as ReturnType<typeof vi.fn>).mockRejectedValue({ response: { status: 403 } });

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(result.current.isAuthenticated).toBe(false);
      expect(mockStorage.removeItem).toHaveBeenCalledWith('token');
    });

    it.each([
      ['a 500 answer', { response: { status: 500 } }],
      ['a network error', new Error('Network Error')],
    ])('keeps the expired token and the user on mount when refresh fails with %s', async (_label, refreshError) => {
      const token = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'user',
        auth_provider: 'oidc',
        email: 'alice@example.com',
        exp: Math.floor(Date.now() / 1000) - 3600,
      });
      mockStorage.setItem('token', token);
      (authService.refresh as ReturnType<typeof vi.fn>).mockRejectedValue(refreshError);

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(mockStorage.removeItem).not.toHaveBeenCalled();
      expect(mockStorage.getItem('token')).toBe(token);
      expect(result.current.isAuthenticated).toBe(true);
      expect(result.current.user?.username).toBe('alice');
      expect(result.current.authProvider).toBe('oidc');
    });

    it('refreshes expired token on mount when refresh succeeds', async () => {
      const pastExp = Math.floor(Date.now() / 1000) - 3600;
      const expiredToken = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'user',
        exp: pastExp,
      });
      mockStorage.setItem('token', expiredToken);

      const futureExp = Math.floor(Date.now() / 1000) + 3600;
      const freshToken = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'user',
        exp: futureExp,
      });
      (authService.refresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        token: freshToken,
      });

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(result.current.isAuthenticated).toBe(true);
      expect(result.current.user?.username).toBe('alice');
      // The shared refresh stores the token; AuthContext passes the expired one in.
      expect(authService.refresh).toHaveBeenCalledWith(expiredToken);
    });

    it('handles malformed token gracefully', async () => {
      mockStorage.setItem('token', 'not-a-jwt');
      (authService.refresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error('refresh failed'),
      );

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
    });
  });

  describe('login', () => {
    it('stores token and sets user on successful login', async () => {
      const mockUser = {
        id: '10',
        username: 'bob',
        display_name: 'Bob',
        role: 'admin',
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      };
      (authService.login as ReturnType<typeof vi.fn>).mockResolvedValue({
        token: 'jwt-token-123',
        user: mockUser,
      });

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      await act(async () => {
        await result.current.login('bob', 'password');
      });

      expect(authService.login).toHaveBeenCalledWith({
        username: 'bob',
        password: 'password',
      });
      expect(mockStorage.setItem).toHaveBeenCalledWith('token', 'jwt-token-123');
      expect(result.current.user).toEqual(mockUser);
      expect(result.current.isAuthenticated).toBe(true);
    });

    it('propagates error on failed login', async () => {
      (authService.login as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error('Invalid credentials')
      );

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      await expect(
        act(async () => {
          await result.current.login('bob', 'wrong');
        })
      ).rejects.toThrow('Invalid credentials');

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
    });
  });

  describe('logout', () => {
    it('calls backend logout, clears token, and sets user to null', async () => {
      (authService.logout as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
      const futureExp = Math.floor(Date.now() / 1000) + 3600;
      const token = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'admin',
        exp: futureExp,
      });
      mockStorage.setItem('token', token);

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isAuthenticated).toBe(true);
      });

      await act(async () => {
        await result.current.logout();
      });

      expect(authService.logout).toHaveBeenCalledWith(token);
      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
      expect(mockStorage.removeItem).toHaveBeenCalledWith('token');
    });

    it('clears local state even when backend logout fails', async () => {
      (authService.logout as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error('network error'),
      );
      const futureExp = Math.floor(Date.now() / 1000) + 3600;
      const token = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'admin',
        exp: futureExp,
      });
      mockStorage.setItem('token', token);

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isAuthenticated).toBe(true);
      });

      await act(async () => {
        await result.current.logout();
      });

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
      expect(mockStorage.removeItem).toHaveBeenCalledWith('token');
    });
  });
  describe('token sync', () => {
    const futureExp = () => Math.floor(Date.now() / 1000) + 3600;

    async function renderLoggedIn() {
      const token = fakeJwt({ user_id: '42', username: 'alice', role: 'user', exp: futureExp() });
      mockStorage.setItem('token', token);
      const hook = renderHook(() => useAuth(), { wrapper });
      await waitFor(() => {
        expect(hook.result.current.isAuthenticated).toBe(true);
      });
      return hook;
    }

    it('updates user, authProvider and authEmail after an in-tab refresh', async () => {
      const { result } = await renderLoggedIn();
      const refreshed = fakeJwt({
        user_id: '42',
        username: 'alice',
        display_name: 'Alice A',
        role: 'devops',
        auth_provider: 'oidc',
        email: 'alice@example.com',
        exp: futureExp(),
      });

      act(() => {
        for (const listener of refreshListeners) listener(refreshed);
      });

      expect(result.current.user?.role).toBe('devops');
      expect(result.current.user?.display_name).toBe('Alice A');
      expect(result.current.authProvider).toBe('oidc');
      expect(result.current.authEmail).toBe('alice@example.com');
    });

    it('updates the user when another tab stores a new token', async () => {
      const { result } = await renderLoggedIn();
      const otherTabToken = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'admin',
        auth_provider: 'oidc',
        email: 'alice@example.com',
        exp: futureExp(),
      });

      act(() => {
        globalThis.dispatchEvent(new StorageEvent('storage', { key: 'token', newValue: otherTabToken }));
      });

      expect(result.current.user?.role).toBe('admin');
      expect(result.current.authProvider).toBe('oidc');
      expect(result.current.authEmail).toBe('alice@example.com');
    });

    it('ignores storage events for other keys', async () => {
      const { result } = await renderLoggedIn();

      act(() => {
        globalThis.dispatchEvent(new StorageEvent('storage', { key: 'theme', newValue: 'dark' }));
      });

      expect(result.current.isAuthenticated).toBe(true);
    });

    it('logs out when another tab removes the token', async () => {
      const { result } = await renderLoggedIn();

      act(() => {
        globalThis.dispatchEvent(new StorageEvent('storage', { key: 'token', newValue: null }));
      });

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
      expect(result.current.authProvider).toBeNull();
      expect(authService.logout).not.toHaveBeenCalled();
    });

    it('logs out when another tab clears localStorage', async () => {
      const { result } = await renderLoggedIn();

      act(() => {
        globalThis.dispatchEvent(new StorageEvent('storage', { key: null }));
      });

      expect(result.current.isAuthenticated).toBe(false);
    });

    it('removes listeners on unmount', async () => {
      const { unmount } = await renderLoggedIn();
      expect(refreshListeners.size).toBe(1);
      unmount();
      expect(refreshListeners.size).toBe(0);
    });
  });
});
