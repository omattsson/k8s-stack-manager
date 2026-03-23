import { type ReactElement } from 'react';
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import ProtectedRoute from '../index';

const mockUseAuth = vi.fn();

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => mockUseAuth(),
}));

// Mock LoadingState to avoid pulling in all MUI dependencies of the real one
vi.mock('../../LoadingState', () => ({
  default: () => <div role="status">Loading...</div>,
}));

const renderWithRouter = (
  ui: ReactElement,
  { initialEntries = ['/protected'] } = {}
) => {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <Routes>
        <Route path="/login" element={<div>Login Page</div>} />
        <Route path="/protected" element={ui} />
      </Routes>
    </MemoryRouter>
  );
};

describe('ProtectedRoute', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  describe('loading state', () => {
    it('shows loading indicator while auth is loading', () => {
      mockUseAuth.mockReturnValue({
        user: null,
        isAuthenticated: false,
        isLoading: true,
      });

      renderWithRouter(
        <ProtectedRoute>
          <div>Protected Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByRole('status')).toBeInTheDocument();
      expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
    });
  });

  describe('unauthenticated', () => {
    it('redirects to /login when not authenticated', async () => {
      mockUseAuth.mockReturnValue({
        user: null,
        isAuthenticated: false,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute>
          <div>Protected Content</div>
        </ProtectedRoute>
      );

      await waitFor(() => {
        expect(screen.getByText('Login Page')).toBeInTheDocument();
      });
      expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
    });
  });

  describe('authenticated without role requirement', () => {
    it('renders children when authenticated', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'alice', role: 'user', display_name: 'Alice', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute>
          <div>Protected Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByText('Protected Content')).toBeInTheDocument();
    });
  });

  describe('role-based access', () => {
    it('renders children when user has exact required role', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute requiredRole="admin">
          <div>Admin Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByText('Admin Content')).toBeInTheDocument();
    });

    it('renders children when user role exceeds required role', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute requiredRole="devops">
          <div>DevOps Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByText('DevOps Content')).toBeInTheDocument();
    });

    it('shows permission error when user role is below required role', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'dev', role: 'user', display_name: 'Dev', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute requiredRole="admin">
          <div>Admin Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/do not have permission/i)).toBeInTheDocument();
      expect(screen.queryByText('Admin Content')).not.toBeInTheDocument();
    });

    it('blocks user role from devops pages', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'dev', role: 'user', display_name: 'Dev', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute requiredRole="devops">
          <div>DevOps Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.queryByText('DevOps Content')).not.toBeInTheDocument();
    });

    it('allows devops role to access devops pages', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'ops', role: 'devops', display_name: 'Ops', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute requiredRole="devops">
          <div>DevOps Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByText('DevOps Content')).toBeInTheDocument();
    });

    it('allows admin role to access devops pages', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute requiredRole="devops">
          <div>DevOps Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByText('DevOps Content')).toBeInTheDocument();
    });

    it('blocks devops role from admin pages', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'ops', role: 'devops', display_name: 'Ops', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute requiredRole="admin">
          <div>Admin Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.queryByText('Admin Content')).not.toBeInTheDocument();
    });
  });

  describe('no requiredRole with authenticated user', () => {
    it('renders children for any role when no requiredRole is specified', () => {
      mockUseAuth.mockReturnValue({
        user: { id: '1', username: 'dev', role: 'user', display_name: 'Dev', created_at: '', updated_at: '' },
        isAuthenticated: true,
        isLoading: false,
      });

      renderWithRouter(
        <ProtectedRoute>
          <div>Any Role Content</div>
        </ProtectedRoute>
      );

      expect(screen.getByText('Any Role Content')).toBeInTheDocument();
    });
  });
});
