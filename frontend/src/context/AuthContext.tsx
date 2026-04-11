import { createContext, useContext, useState, useEffect, useCallback, useMemo } from 'react';
import type { ReactNode } from 'react';
import { authService, oidcService } from '../api/client';
import { reconnectWebSocket } from '../hooks/useWebSocket';
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

function decodeJwtPayload(token: string): JwtPayload | null {
  try {
    const base64Url = token.split('.')[1];
    const base64 = base64Url.replaceAll('-', '+').replaceAll('_', '/');
    const padded = base64.padEnd(base64.length + (4 - (base64.length % 4)) % 4, '=');
    const json = atob(padded);
    return JSON.parse(json);
  } catch {
    return null;
  }
}

function isTokenExpired(payload: JwtPayload): boolean {
  return Date.now() >= payload.exp * 1000;
}

function userFromPayload(payload: JwtPayload): User {
  return {
    id: payload.user_id,
    username: payload.username,
    display_name: payload.username,
    role: payload.role,
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

  useEffect(() => {
    const init = async () => {
      const token = localStorage.getItem('token');
      if (token) {
        const payload = decodeJwtPayload(token);
        if (payload && !isTokenExpired(payload)) {
          setUser(userFromPayload(payload));
          setAuthProvider(payload.auth_provider ?? null);
          setAuthEmail(payload.email ?? null);
        } else {
          // Token expired — attempt a silent refresh via the httpOnly cookie
          try {
            const { token: newToken } = await authService.refresh();
            localStorage.setItem('token', newToken);
            const newPayload = decodeJwtPayload(newToken);
            if (newPayload) {
              setUser(userFromPayload(newPayload));
              setAuthProvider(newPayload.auth_provider ?? null);
              setAuthEmail(newPayload.email ?? null);
            }
          } catch {
            localStorage.removeItem('token');
          }
        }
      }
      setIsLoading(false);
    };
    init();
  }, []);

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
    localStorage.setItem('token', response.token);
    setUser(response.user);
    reconnectWebSocket();
  }, []);

  const logout = useCallback(async () => {
    // Capture token before clearing so the logout request can attach it.
    const token = localStorage.getItem('token');
    localStorage.removeItem('token');
    setUser(null);
    reconnectWebSocket();
    if (token) {
      try {
        await authService.logout(token);
      } catch {
        // Best-effort server-side revocation; local state already cleared
      }
    }
  }, []);

  const loginWithOIDC = useCallback(async (redirect?: string) => {
    const result = await oidcService.getAuthorizeUrl(redirect);
    globalThis.location.href = result.redirect_url;
  }, []);

  const handleOIDCCallback = useCallback((token: string) => {
    localStorage.setItem('token', token);
    const payload = decodeJwtPayload(token);
    if (payload && !isTokenExpired(payload)) {
      setUser(userFromPayload(payload));
      setAuthProvider(payload.auth_provider ?? null);
      setAuthEmail(payload.email ?? null);
    }
    reconnectWebSocket();
  }, []);

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
