import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import AuthCallback from './index';

const mockNavigate = vi.fn();
const mockHandleOIDCCallback = vi.fn();

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
 * Renders AuthCallback with the given search string (e.g. "?token=abc" or "?error=invalid_state").
 * Uses MemoryRouter so that useSearchParams reads from the initial location.
 */
function renderCallback(search: string) {
  return render(
    <MemoryRouter initialEntries={[`/auth/callback${search}`]}>
      <AuthCallback />
    </MemoryRouter>
  );
}

describe('AuthCallback Page', () => {
  afterEach(() => {
    vi.clearAllMocks();
    vi.restoreAllMocks();
  });

  describe('Token in URL (success path)', () => {
    it('calls handleOIDCCallback with the token', async () => {
      const token = 'header.payload.sig';
      renderCallback(`?token=${token}`);

      await waitFor(() => {
        expect(mockHandleOIDCCallback).toHaveBeenCalledWith(token);
      });
    });

    it('navigates to root after processing token', async () => {
      renderCallback('?token=sometoken');

      await waitFor(() => {
        expect(mockNavigate).toHaveBeenCalledWith('/', { replace: true });
      });
    });

    it('shows "Completing sign-in" text while processing (navigate is mocked)', () => {
      // navigate is mocked so the component stays in the loading view
      renderCallback('?token=sometoken');

      expect(screen.getByText(/completing sign-in/i)).toBeInTheDocument();
    });

    it('shows a loading spinner while processing', () => {
      renderCallback('?token=sometoken');

      expect(screen.getByRole('progressbar')).toBeInTheDocument();
    });
  });

  describe('Error param in URL', () => {
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
    it('shows generic error message when search params are empty', () => {
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
