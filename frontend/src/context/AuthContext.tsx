import { createContext, useContext, useState, useEffect, useCallback, useMemo } from 'react';
import type { ReactNode } from 'react';
import { authService, oidcService } from '../api/client';
import { isAuthRejection, onTokenRefreshed, TOKEN_STORAGE_KEY } from '../api/refresh';
import { reconnectWebSocket } from '../hooks/useWebSocket';
import { decodeJwtPayload, isTokenExpired } from '../utils/jwt';
import type { User, JwtPayload } from '../types';

interface OidcConfig {
  enabled: boolean;
  provider_name: string;
  local_auth_enabled: boolean;
}

interface AuthContextType {
  user: User | null;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  isAuthenticated: boolean;
  isLoading: boolean;
  oidcConfig: OidcConfig | null;
  oidcLoading: boolean;
  loginWithOIDC: (redirect?: string) => Promise<void>;
  handleOIDCCallback: (token: string) => void;
  authProvider: string | null;
  authEmail: string | null;
}

const AuthContext = createContext<AuthContextType | undefined>(undefined);

function userFromPayload(payload: JwtPayload): User {
  return {
    id: payload.user_id,
    username: payload.username,
    display_name: payload.display_name || payload.username,
    role: payload.role,
    auth_provider: payload.auth_provider ?? 'local',
    disabled: false,
    service_account: false,
    created_at: '',
    updated_at: '',
  };
}

export const AuthProvider = ({ children }: { children: ReactNode }) => {
  const [user, setUser] = useState<User | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [oidcConfig, setOidcConfig] = useState<OidcConfig | null>(null);
  const [oidcLoading, setOidcLoading] = useState(true);
  const [authProvider, setAuthProvider] = useState<string | null>(null);
  const [authEmail, setAuthEmail] = useState<string | null>(null);

  /** Set user, authProvider and authEmail from a token. Returns false when the token is unusable. */
  const applyToken = useCallback((token: string): boolean => {
    const payload = decodeJwtPayload(token);
    if (!payload || isTokenExpired(payload)) return false;
    setUser(userFromPayload(payload));
    setAuthProvider(payload.auth_provider ?? null);
    setAuthEmail(payload.email ?? null);
    return true;
  }, []);

  const clearSession = useCallback(() => {
    setUser(null);
    setAuthProvider(null);
    setAuthEmail(null);
  }, []);

  useEffect(() => {
    const init = async () => {
      const token = localStorage.getItem(TOKEN_STORAGE_KEY);
      if (token && !applyToken(token)) {
        // Token expired — attempt a silent refresh via the httpOnly cookie.
        // The shared refresh stores the new token and is single flight across tabs.
        try {
          const { token: newToken } = await authService.refresh(token);
          if (!applyToken(newToken)) {
            localStorage.removeItem(TOKEN_STORAGE_KEY);
          }
        } catch (error) {
          if (isAuthRejection(error)) {
            localStorage.removeItem(TOKEN_STORAGE_KEY);
          } else {
            // Network error, timeout or 5xx (for example a backend restart): keep
            // the token and the user from its payload. Pages show their normal
            // API errors, and the next 401 retries the refresh via the interceptor.
            const payload = decodeJwtPayload(token);
            if (payload) {
              setUser(userFromPayload(payload));
              setAuthProvider(payload.auth_provider ?? null);
              setAuthEmail(payload.email ?? null);
            }
          }
        }
      }
      setIsLoading(false);
    };
    init();
  }, [applyToken]);

  // Refreshes in this tab (for example the 401 interceptor) update the session state.
  useEffect(() => onTokenRefreshed((token) => {
    applyToken(token);
  }), [applyToken]);

  // Other tabs: a new token (refresh or login) updates this tab; a removed token
  // (logout or failed refresh) logs this tab out. ProtectedRoute then routes to
  // /login, and the login page itself is not redirected again.
  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== TOKEN_STORAGE_KEY && event.key !== null) return;
      const token = event.key === null ? null : event.newValue;
      if (token) {
        applyToken(token);
        return;
      }
      clearSession();
      reconnectWebSocket();
    };
    globalThis.addEventListener('storage', onStorage);
    return () => globalThis.removeEventListener('storage', onStorage);
  }, [applyToken, clearSession]);

  useEffect(() => {
    const fetchOidcConfig = async () => {
      try {
        const config = await oidcService.getConfig();
        setOidcConfig(config);
      } catch {
        setOidcConfig({ enabled: false, provider_name: '', local_auth_enabled: true });
      } finally {
        setOidcLoading(false);
      }
    };
    fetchOidcConfig();
  }, []);

  const login = useCallback(async (username: string, password: string) => {
    const response = await authService.login({ username, password });
    localStorage.setItem(TOKEN_STORAGE_KEY, response.token);
    setUser(response.user);
    const payload = decodeJwtPayload(response.token);
    setAuthProvider(payload?.auth_provider ?? null);
    setAuthEmail(payload?.email ?? null);
    reconnectWebSocket();
  }, []);

  const logout = useCallback(async () => {
    // Capture token before clearing so the logout request can attach it.
    const token = localStorage.getItem(TOKEN_STORAGE_KEY);
    localStorage.removeItem(TOKEN_STORAGE_KEY);
    clearSession();
    reconnectWebSocket();
    try {
      await authService.logout(token ?? undefined);
    } catch {
      // Best-effort server-side revocation; local state already cleared
    }
  }, [clearSession]);

  const loginWithOIDC = useCallback(async (redirect?: string) => {
    const result = await oidcService.getAuthorizeUrl(redirect);
    globalThis.location.href = result.redirect_url;
  }, []);

  const handleOIDCCallback = useCallback((token: string) => {
    localStorage.setItem(TOKEN_STORAGE_KEY, token);
    applyToken(token);
    reconnectWebSocket();
  }, [applyToken]);

  const isAuthenticated = user !== null;

  const value = useMemo(
    () => ({ user, login, logout, isAuthenticated, isLoading, oidcConfig, oidcLoading, loginWithOIDC, handleOIDCCallback, authProvider, authEmail }),
    [user, login, logout, isAuthenticated, isLoading, oidcConfig, oidcLoading, loginWithOIDC, handleOIDCCallback, authProvider, authEmail]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};

export const useAuth = (): AuthContextType => {
  const context = useContext(AuthContext);
  if (context === undefined) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
};
