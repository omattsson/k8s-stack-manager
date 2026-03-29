import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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

import { userService, apiKeyService } from '../../../../api/client';
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
  {
    id: '1',
    username: 'admin',
    role: 'admin',
    display_name: 'Admin User',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: '2',
    username: 'devuser',
    role: 'user',
    display_name: 'Dev User',
    created_at: '2026-01-10T00:00:00Z',
    updated_at: '2026-01-10T00:00:00Z',
  },
  {
    id: '3',
    username: 'ops',
    role: 'devops',
    display_name: 'Ops User',
    created_at: '2026-02-01T00:00:00Z',
    updated_at: '2026-02-01T00:00:00Z',
  },
];

const renderWithProviders = () => {
  return render(
    <MemoryRouter>
      <AdminUsers />
    </MemoryRouter>
  );
};

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

  it('renders loading spinner initially', () => {
    (userService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));

    renderWithProviders();

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays users in a table when fetch succeeds', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByText('devuser')).toBeInTheDocument();
    });

    expect(screen.getByText('ops')).toBeInTheDocument();
    expect(screen.getByText('Dev User')).toBeInTheDocument();
    expect(screen.getByText('Ops User')).toBeInTheDocument();
  });

  it('shows error alert when fetch fails', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  it('shows role chips', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByText('devuser')).toBeInTheDocument();
    });

    // devops and user role chips are unique
    expect(screen.getByText('devops')).toBeInTheDocument();
    expect(screen.getByText('user')).toBeInTheDocument();
  });

  it('opens create user dialog when Add User button is clicked', async () => {
    const user = userEvent.setup();
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByText('devuser')).toBeInTheDocument();
    });

    const addButton = screen.getByRole('button', { name: /add user/i });
    await user.click(addButton);

    await waitFor(() => {
      const dialog = screen.getByRole('dialog');
      expect(within(dialog).getByText('Add User')).toBeInTheDocument();
      expect(within(dialog).getByRole('textbox', { name: /username/i })).toBeInTheDocument();
    });
  });

  it('creates a user successfully through the dialog', async () => {
    const user = userEvent.setup();
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    (userService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: '4',
      username: 'newuser',
      role: 'user',
    });

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByText('devuser')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /add user/i }));

    await waitFor(() => {
      expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: /username/i })).toBeInTheDocument();
    });

    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByRole('textbox', { name: /username/i }), 'newuser');
    // Password field is type=password, so it doesn't have role=textbox
    const passwordInput = within(dialog).getByLabelText(/password/i);
    await user.type(passwordInput, 'secretpass');

    // Re-mock list to include the new user
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      ...mockUsers,
      { id: '4', username: 'newuser', role: 'user' },
    ]);

    const createButton = within(dialog).getByRole('button', { name: /create/i });
    await user.click(createButton);

    await waitFor(() => {
      expect(userService.create).toHaveBeenCalledWith(
        expect.objectContaining({ username: 'newuser', password: 'secretpass' })
      );
    });
  });

  it('shows create user error on failure', async () => {
    const user = userEvent.setup();
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    (userService.create as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { data: { error: 'Username already exists' } },
    });

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByText('devuser')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /add user/i }));

    await waitFor(() => {
      expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: /username/i })).toBeInTheDocument();
    });

    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByRole('textbox', { name: /username/i }), 'duplicate');
    await user.type(within(dialog).getByLabelText(/password/i), 'pass');

    await user.click(within(dialog).getByRole('button', { name: /create/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to create user')).toBeInTheDocument();
    });
  });

  it('opens delete confirmation when delete button is clicked', async () => {
    const user = userEvent.setup();
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByText('devuser')).toBeInTheDocument();
    });

    // Click delete on devuser (non-current user)
    const deleteButton = screen.getByLabelText('Delete user devuser');
    await user.click(deleteButton);

    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeInTheDocument();
    });
  });

  it('expands a user row to show API keys section', async () => {
    const user = userEvent.setup();
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    (apiKeyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    renderWithProviders();

    await waitFor(() => {
      expect(screen.getByText('devuser')).toBeInTheDocument();
    });

    // Click expand on first user row
    const expandButtons = screen.getAllByLabelText(/expand/i);
    await user.click(expandButtons[0]);

    await waitFor(() => {
      expect(apiKeyService.list).toHaveBeenCalled();
    });
  });

  it('shows disabled delete button for the current user', async () => {
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue([adminUser]);

    renderWithProviders();

    await waitFor(() => {
      // Wait for the table to render by checking for Admin User display name
      expect(screen.getByText('Admin User')).toBeInTheDocument();
    });

    // The delete button for current user should be disabled
    const deleteButton = screen.getByLabelText(/delete user/i);
    expect(deleteButton).toBeDisabled();
  });
});
