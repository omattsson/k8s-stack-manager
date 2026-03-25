import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import AuthCallback from './index';

const mockNavigate = vi.fn();
const mockHandleOIDCCallback = vi.fn();
const mockReplaceState = vi.fn();

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../context/AuthContext', () => ({
  useAuth: () => ({
    handleOIDCCallback: mockHandleOIDCCallback,
  }),
}));

/**
 * Renders AuthCallback.
 * - `search`: query string for error params (e.g. "?error=invalid_state")
 * - `hash`: URL fragment for token/redirect (e.g. "#token=abc&redirect=/foo")
 *
 * Sets window.location.hash and mocks history.replaceState to capture calls.
 */
function renderCallback(search: string, hash = '') {
  // Set the hash so the component can read it
  window.location.hash = hash;

  return render(
    <MemoryRouter initialEntries={[`/auth/callback${search}`]}>
      <AuthCallback />
    </MemoryRouter>,
  );
}

describe('AuthCallback Page', () => {
  beforeEach(() => {
    // Mock history.replaceState to prevent jsdom warnings and to assert calls
    mockReplaceState.mockClear();
    vi.spyOn(window.history, 'replaceState').mockImplementation(mockReplaceState);
  });

  afterEach(() => {
    vi.clearAllMocks();
    vi.restoreAllMocks();
    window.location.hash = '';
  });

  describe('Token in URL fragment (success path)', () => {
    it('calls handleOIDCCallback with the token from the fragment', async () => {
      const token = 'header.payload.sig';
      renderCallback('', `#token=${token}`);

      await waitFor(() => {
        expect(mockHandleOIDCCallback).toHaveBeenCalledWith(token);
      });
    });

    it('navigates to root after processing token', async () => {
      renderCallback('', '#token=sometoken');

      await waitFor(() => {
        expect(mockNavigate).toHaveBeenCalledWith('/', { replace: true });
      });
    });

    it('navigates to the redirect path from the fragment', async () => {
      renderCallback('', '#token=sometoken&redirect=/dashboard');

      await waitFor(() => {
        expect(mockNavigate).toHaveBeenCalledWith('/dashboard', { replace: true });
      });
    });

    it('ignores unsafe redirect paths (protocol-relative)', async () => {
      renderCallback('', '#token=sometoken&redirect=//evil.com');

      await waitFor(() => {
        expect(mockNavigate).toHaveBeenCalledWith('/', { replace: true });
      });
    });

    it('clears the URL fragment immediately', async () => {
      renderCallback('', '#token=sometoken');

      await waitFor(() => {
        expect(mockReplaceState).toHaveBeenCalled();
      });
    });

    it('shows "Completing sign-in" text while processing (navigate is mocked)', () => {
      renderCallback('', '#token=sometoken');

      expect(screen.getByText(/completing sign-in/i)).toBeInTheDocument();
    });

    it('shows a loading spinner while processing', () => {
      renderCallback('', '#token=sometoken');

      expect(screen.getByRole('progressbar')).toBeInTheDocument();
    });
  });

  describe('Error param in URL query string', () => {
    it('shows session-expired message for invalid_state error', () => {
      renderCallback('?error=invalid_state');

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/session expired/i)).toBeInTheDocument();
      expect(screen.getByRole('link', { name: /back to login/i })).toBeInTheDocument();
    });

    it('shows authentication-failed message for auth_failed error', () => {
      renderCallback('?error=auth_failed');

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/authentication failed/i)).toBeInTheDocument();
      expect(screen.getByRole('link', { name: /back to login/i })).toBeInTheDocument();
    });

    it('shows no-account message for no_account error', () => {
      renderCallback('?error=no_account');

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/no account found/i)).toBeInTheDocument();
      expect(screen.getByRole('link', { name: /back to login/i })).toBeInTheDocument();
    });

    it('shows generic error message for unknown error codes', () => {
      renderCallback('?error=some_unknown_code');

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/something went wrong/i)).toBeInTheDocument();
    });

    it('does not call handleOIDCCallback when error param is present', () => {
      renderCallback('?error=invalid_state');

      expect(mockHandleOIDCCallback).not.toHaveBeenCalled();
    });

    it('does not call navigate when error param is present', () => {
      renderCallback('?error=auth_failed');

      expect(mockNavigate).not.toHaveBeenCalled();
    });
  });

  describe('No token and no error', () => {
    it('shows generic error message when no fragment and no query params', () => {
      renderCallback('');

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/something went wrong/i)).toBeInTheDocument();
    });

    it('shows Back to Login link when there is no token', () => {
      renderCallback('');

      expect(screen.getByRole('link', { name: /back to login/i })).toBeInTheDocument();
    });

    it('does not call handleOIDCCallback when there is no token', () => {
      renderCallback('');

      expect(mockHandleOIDCCallback).not.toHaveBeenCalled();
    });
  });
});
