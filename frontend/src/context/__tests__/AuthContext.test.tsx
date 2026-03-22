import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';

// Mock authService before importing AuthContext
vi.mock('../../api/client', () => ({
  authService: {
    login: vi.fn(),
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

const wrapper = ({ children }: { children: ReactNode }) => (
  <AuthProvider>{children}</AuthProvider>
);

describe('AuthContext', () => {
  let getItemSpy: ReturnType<typeof vi.spyOn>;
  let setItemSpy: ReturnType<typeof vi.spyOn>;
  let removeItemSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    getItemSpy = vi.spyOn(Storage.prototype, 'getItem');
    setItemSpy = vi.spyOn(Storage.prototype, 'setItem');
    removeItemSpy = vi.spyOn(Storage.prototype, 'removeItem');
    localStorage.clear();
  });

  afterEach(() => {
    vi.clearAllMocks();
    getItemSpy.mockRestore();
    setItemSpy.mockRestore();
    removeItemSpy.mockRestore();
    localStorage.clear();
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
      localStorage.setItem('token', token);

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
        created_at: '',
        updated_at: '',
      });
    });

    it('clears expired token from localStorage on mount', async () => {
      const pastExp = Math.floor(Date.now() / 1000) - 3600;
      const token = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'user',
        exp: pastExp,
      });
      localStorage.setItem('token', token);

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isLoading).toBe(false);
      });

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
      expect(removeItemSpy).toHaveBeenCalledWith('token');
    });

    it('handles malformed token gracefully', async () => {
      localStorage.setItem('token', 'not-a-jwt');

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
      expect(setItemSpy).toHaveBeenCalledWith('token', 'jwt-token-123');
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
    it('clears token and sets user to null', async () => {
      const futureExp = Math.floor(Date.now() / 1000) + 3600;
      const token = fakeJwt({
        user_id: '42',
        username: 'alice',
        role: 'admin',
        exp: futureExp,
      });
      localStorage.setItem('token', token);

      const { result } = renderHook(() => useAuth(), { wrapper });

      await waitFor(() => {
        expect(result.current.isAuthenticated).toBe(true);
      });

      act(() => {
        result.current.logout();
      });

      expect(result.current.user).toBeNull();
      expect(result.current.isAuthenticated).toBe(false);
      expect(removeItemSpy).toHaveBeenCalledWith('token');
    });
  });
});
