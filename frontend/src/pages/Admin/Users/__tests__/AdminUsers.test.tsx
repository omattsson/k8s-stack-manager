import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import AdminUsers from '../index';

vi.mock('../../../../api/client', () => ({
  userService: {
    list: vi.fn(),
    create: vi.fn(),
    delete: vi.fn(),
  },
  apiKeyService: {
    list: vi.fn(),
    create: vi.fn(),
    delete: vi.fn(),
  },
}));

vi.mock('../../../../context/AuthContext', () => ({
  useAuth: vi.fn(),
}));

import { userService } from '../../../../api/client';
import { useAuth } from '../../../../context/AuthContext';

const adminUser = {
  id: '1',
  username: 'admin',
  role: 'admin',
  display_name: 'Admin User',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const mockUsers = [
  adminUser,
  {
    id: '2',
    username: 'alice',
    role: 'devops',
    display_name: 'Alice',
    created_at: '2026-02-01T00:00:00Z',
    updated_at: '2026-02-01T00:00:00Z',
  },
];

describe('AdminUsers Page', () => {
  beforeEach(() => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: adminUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows a loading spinner initially', () => {
    (userService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays the page title and Add User button', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('User Management');
      expect(screen.getByRole('button', { name: /add user/i })).toBeInTheDocument();
    });
  });

  it('displays the user list on success', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    await waitFor(() => {
      // username column cells
      expect(screen.getAllByText('admin').length).toBeGreaterThan(0);
      expect(screen.getByText('alice')).toBeInTheDocument();
      // role chips
      expect(screen.getByText('devops')).toBeInTheDocument();
    });
  });

  it('shows an error alert when user fetch fails', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/failed to load users/i)).toBeInTheDocument();
    });
  });

  it('shows empty state when no users exist', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no users found/i)).toBeInTheDocument();
    });
  });

  it('shows access denied for non-admin users', async () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: { id: '3', username: 'bob', role: 'user', display_name: 'Bob', created_at: '', updated_at: '' },
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.getByText(/you do not have permission/i)).toBeInTheDocument();
  });

  it('disables delete button for the current user row', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    await waitFor(() => {
      const deleteButtons = screen.getAllByRole('button', { name: /delete user/i });
      // The admin's own row button should be disabled
      const adminDeleteBtn = screen.getByRole('button', { name: /delete user admin/i });
      expect(adminDeleteBtn).toBeDisabled();
      // alice's delete button should be enabled
      expect(deleteButtons.some((b) => !b.hasAttribute('disabled'))).toBe(true);
    });
  });

  it('displays role chips with correct labels', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    await waitFor(() => {
      // role chips are rendered as text within the table
      const chips = screen.getAllByText('admin');
      expect(chips.length).toBeGreaterThan(0);
      expect(screen.getByText('devops')).toBeInTheDocument();
    });
  });
});
