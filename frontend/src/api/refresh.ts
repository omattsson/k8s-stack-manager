import axios from 'axios';
import { axiosConfig } from './config';
import { isTokenUsable } from '../utils/jwt';

/** localStorage key of the short-lived access token. */
export const TOKEN_STORAGE_KEY = 'token';

/** Web Locks name shared by all tabs of this origin. */
export const REFRESH_LOCK_NAME = 'auth-refresh';

/**
 * Timeout of POST /auth/refresh. A hung request would otherwise hold the
 * `auth-refresh` Web Lock and block the refresh in every tab.
 */
export const REFRESH_TIMEOUT_MS = 15000;

// Separate instance for token refresh — sends the httpOnly cookie and has no
// response interceptors, preventing refresh-retry loops.
const refreshApi = axios.create({ ...axiosConfig, withCredentials: true, timeout: REFRESH_TIMEOUT_MS });

type TokenListener = (token: string) => void;

const listeners = new Set<TokenListener>();

// In-tab single flight: concurrent callers in one tab share one promise.
let inFlight: Promise<string> | null = null;

/**
 * Subscribe to access-token changes made by {@link refreshAccessToken} in this tab.
 * The `storage` event covers changes made by other tabs.
 * @param listener - Called with the new access token
 * @returns A function that removes the listener
 */
export function onTokenRefreshed(listener: TokenListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function notify(token: string): void {
  for (const listener of listeners) {
    listener(token);
  }
}

/**
 * Return a token from localStorage that another tab (or an earlier refresh)
 * stored after `failedToken` was issued, or null when there is none.
 */
function newerStoredToken(failedToken: string | null): string | null {
  const stored = localStorage.getItem(TOKEN_STORAGE_KEY);
  if (stored && stored !== failedToken && isTokenUsable(stored)) {
    return stored;
  }
  return null;
}

async function refreshUnderLock(failedToken: string | null): Promise<string> {
  const adopted = newerStoredToken(failedToken);
  if (adopted) {
    notify(adopted);
    return adopted;
  }
  try {
    const { data } = await refreshApi.post<{ token: string }>('/api/v1/auth/refresh');
    localStorage.setItem(TOKEN_STORAGE_KEY, data.token);
    notify(data.token);
    return data.token;
  } catch (error) {
    // A tab without lock support may have refreshed in parallel.
    const late = newerStoredToken(failedToken);
    if (late) {
      notify(late);
      return late;
    }
    // Only an explicit rejection ends the session. A network error, timeout or
    // 5xx keeps the token so that a later request can retry the refresh.
    if (isAuthRejection(error)) {
      localStorage.removeItem(TOKEN_STORAGE_KEY);
    }
    throw error;
  }
}

/**
 * Check whether a refresh error means the session is invalid.
 * @param error - Error thrown by {@link refreshAccessToken}
 * @returns True for HTTP 401 or 403 (for example "Session revoked"); false for
 *   network errors, timeouts and 5xx responses
 */
export function isAuthRejection(error: unknown): boolean {
  if (typeof error !== 'object' || error === null || !('response' in error)) return false;
  const { response } = error as { response?: { status?: unknown } };
  return response?.status === 401 || response?.status === 403;
}

function hasWebLocks(): boolean {
  return typeof navigator !== 'undefined'
    && 'locks' in navigator
    && typeof navigator.locks?.request === 'function';
}

/**
 * Get a new access token. All refresh paths must use this function.
 *
 * - In one tab, concurrent callers share one refresh.
 * - Across tabs, the Web Locks API (`auth-refresh`) lets only one tab call the
 *   API at a time. A tab that waited for the lock uses the token that the other
 *   tab stored, so the rotated refresh cookie is never sent twice.
 * - Without Web Locks, only the in-tab single flight applies.
 *
 * On failure the error is rethrown. The stored token is removed only when the
 * refresh endpoint answers 401 or 403 (see {@link isAuthRejection}).
 * @param failedToken - The access token that was rejected or expired (defaults to the stored token)
 * @returns The new access token (also stored in localStorage)
 * @see POST /api/v1/auth/refresh
 */
export function refreshAccessToken(failedToken?: string | null): Promise<string> {
  if (inFlight) return inFlight;
  const failed = failedToken === undefined ? localStorage.getItem(TOKEN_STORAGE_KEY) : failedToken;
  const run = (): Promise<string> => refreshUnderLock(failed);
  const pending = hasWebLocks()
    ? navigator.locks.request(REFRESH_LOCK_NAME, run)
    : run();
  inFlight = pending.finally(() => {
    inFlight = null;
  });
  return inFlight;
}
