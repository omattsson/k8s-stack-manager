import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Login from '../index';

const mockNavigate = vi.fn();
const mockLogin = vi.fn();
const mockLoginWithOIDC = vi.fn();

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

let mockIsAuthenticated = false;
let mockOidcLoading = false;
let mockOidcConfig: {
  enabled: boolean;
  provider_name: string;
  local_auth_enabled: boolean;
} | null = null;

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({
    login: mockLogin,
    loginWithOIDC: mockLoginWithOIDC,
    handleOIDCCallback: vi.fn(),
    user: mockIsAuthenticated ? { id: '1', username: 'admin' } : null,
    isAuthenticated: mockIsAuthenticated,
    isLoading: false,
    oidcConfig: mockOidcConfig,
    oidcLoading: mockOidcLoading,
    logout: vi.fn(),
  }),
}));

function renderLogin() {
  return render(
    <MemoryRouter>
      <Login />
    </MemoryRouter>
  );
}

describe('Login Page', () => {
  afterEach(() => {
    vi.clearAllMocks();
    mockIsAuthenticated = false;
  });

  it('renders the login form', () => {
    renderLogin();
    expect(screen.getByRole('heading', { name: /sign in/i })).toBeInTheDocument();
    expect(screen.getByLabelText(/username/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /sign in/i })).toBeInTheDocument();
  });

  it('calls login and navigates on success', async () => {
    mockLogin.mockImplementation(async () => {
      mockIsAuthenticated = true;
    });
    const user = userEvent.setup();

    renderLogin();

    await user.type(screen.getByLabelText(/username/i), 'admin');
    await user.type(screen.getByLabelText(/password/i), 'password');
    await user.click(screen.getByRole('button', { name: /sign in/i }));

    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith('admin', 'password');
      expect(mockNavigate).toHaveBeenCalledWith('/', { replace: true });
    });
  });
});

describe('Login Page — OIDC modes', () => {
  beforeEach(() => {
    mockOidcLoading = false;
    mockOidcConfig = null;
  });

  afterEach(() => {
    vi.clearAllMocks();
    vi.restoreAllMocks();
  });

  describe('OIDC disabled', () => {
    it('shows only local login form when oidcConfig is null', () => {
      renderLogin();

      expect(screen.getByLabelText(/username/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Sign In' })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /sign in with/i })).not.toBeInTheDocument();
    });

    it('shows only local login form when oidcConfig.enabled is false', () => {
      mockOidcConfig = { enabled: false, provider_name: '', local_auth_enabled: true };
      renderLogin();

      expect(screen.getByLabelText(/username/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Sign In' })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /sign in with/i })).not.toBeInTheDocument();
    });

    it('does not show divider or SSO section when OIDC is disabled', () => {
      mockOidcConfig = { enabled: false, provider_name: '', local_auth_enabled: true };
      renderLogin();

      expect(screen.queryByText(/or sign in with credentials/i)).not.toBeInTheDocument();
    });
  });

  describe('OIDC enabled with local auth', () => {
    beforeEach(() => {
      mockOidcConfig = { enabled: true, provider_name: 'Microsoft', local_auth_enabled: true };
    });

    it('shows SSO button, divider, and local login form', () => {
      renderLogin();

      expect(screen.getByRole('button', { name: /sign in with microsoft/i })).toBeInTheDocument();
      expect(screen.getByText(/or sign in with credentials/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/username/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Sign In' })).toBeInTheDocument();
    });

    it('uses provider name fallback "SSO" when provider_name is empty', () => {
      mockOidcConfig = { enabled: true, provider_name: '', local_auth_enabled: true };
      renderLogin();

      expect(screen.getByRole('button', { name: /sign in with sso/i })).toBeInTheDocument();
    });
  });

  describe('OIDC only (local auth disabled)', () => {
    beforeEach(() => {
      mockOidcConfig = { enabled: true, provider_name: 'Microsoft', local_auth_enabled: false };
    });

    it('shows only the SSO button, no local login form', () => {
      renderLogin();

      expect(screen.getByRole('button', { name: /sign in with microsoft/i })).toBeInTheDocument();
      expect(screen.queryByLabelText(/username/i)).not.toBeInTheDocument();
      expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Sign In' })).not.toBeInTheDocument();
    });

    it('does not show the divider when local auth is disabled', () => {
      renderLogin();

      expect(screen.queryByText(/or sign in with credentials/i)).not.toBeInTheDocument();
    });

    it('shows organization account hint text', () => {
      renderLogin();

      expect(screen.getByText(/sign in with your organization account/i)).toBeInTheDocument();
    });
  });

  describe('Loading state', () => {
    it('shows spinner and hides form while OIDC config is loading', () => {
      mockOidcLoading = true;
      renderLogin();

      expect(screen.getByRole('progressbar')).toBeInTheDocument();
      expect(screen.queryByLabelText(/username/i)).not.toBeInTheDocument();
      expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Sign In' })).not.toBeInTheDocument();
    });
  });

  describe('SSO button interaction', () => {
    beforeEach(() => {
      mockOidcConfig = { enabled: true, provider_name: 'Microsoft', local_auth_enabled: true };
    });

    it('calls loginWithOIDC when SSO button is clicked', async () => {
      mockLoginWithOIDC.mockResolvedValue(undefined);
      const user = userEvent.setup();

      renderLogin();

      await user.click(screen.getByRole('button', { name: /sign in with microsoft/i }));

      expect(mockLoginWithOIDC).toHaveBeenCalledOnce();
    });

    it('shows error message when SSO login throws', async () => {
      mockLoginWithOIDC.mockRejectedValue(new Error('Network failure'));
      const user = userEvent.setup();

      renderLogin();

      await user.click(screen.getByRole('button', { name: /sign in with microsoft/i }));

      await waitFor(() => {
        expect(screen.getByRole('alert')).toBeInTheDocument();
        expect(screen.getByText(/failed to initiate sso login/i)).toBeInTheDocument();
      });
    });
  });

  describe('Local login form', () => {
    it('calls login with credentials when local form is submitted', async () => {
      mockOidcConfig = { enabled: true, provider_name: 'Microsoft', local_auth_enabled: true };
      mockLogin.mockResolvedValue(undefined);
      const user = userEvent.setup();

      renderLogin();

      await user.type(screen.getByLabelText(/username/i), 'testuser');
      await user.type(screen.getByLabelText(/password/i), 'testpass');
      await user.click(screen.getByRole('button', { name: 'Sign In' }));

      await waitFor(() => {
        expect(mockLogin).toHaveBeenCalledWith('testuser', 'testpass');
      });
    });

    it('shows error message when local login fails', async () => {
      mockOidcConfig = null;
      mockLogin.mockRejectedValue(new Error('Invalid credentials'));
      const user = userEvent.setup();

      renderLogin();

      await user.type(screen.getByLabelText(/username/i), 'bad');
      await user.type(screen.getByLabelText(/password/i), 'wrong');
      await user.click(screen.getByRole('button', { name: 'Sign In' }));

      await waitFor(() => {
        expect(screen.getByRole('alert')).toBeInTheDocument();
        expect(screen.getByText(/invalid username or password/i)).toBeInTheDocument();
      });
    });
  });
});
