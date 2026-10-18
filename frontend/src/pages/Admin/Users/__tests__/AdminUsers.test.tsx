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
    resetPassword: vi.fn(),
    disable: vi.fn(),
    enable: vi.fn(),
    changeRole: vi.fn(),
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
  auth_provider: 'local',
  disabled: false,
  service_account: false,
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
    auth_provider: 'local',
    disabled: false,
    service_account: false,
    created_at: '2026-02-01T00:00:00Z',
    updated_at: '2026-02-01T00:00:00Z',
  },
  {
    id: '3',
    username: 'oidc-bob',
    role: 'user',
    display_name: 'Bob (OIDC)',
    auth_provider: 'oidc',
    disabled: false,
    service_account: false,
    created_at: '2026-03-01T00:00:00Z',
    updated_at: '2026-03-01T00:00:00Z',
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

  describe('Password Reset', () => {
    it('shows reset password button for local users only', async () => {
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('alice')).toBeInTheDocument();
      });
      expect(screen.getByRole('button', { name: /reset password for admin/i })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /reset password for alice/i })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /reset password for oidc-bob/i })).not.toBeInTheDocument();
    });

    it('opens password reset dialog when button is clicked', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('alice')).toBeInTheDocument();
      });
      await user.click(screen.getByRole('button', { name: /reset password for alice/i }));
      expect(screen.getByRole('heading', { name: 'Reset Password' })).toBeInTheDocument();
      expect(screen.getByText(/set a new password for/i)).toBeInTheDocument();
    });

    it('disables submit when password is too short', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('alice')).toBeInTheDocument();
      });
      await user.click(screen.getByRole('button', { name: /reset password for alice/i }));
      const submitBtn = screen.getByRole('button', { name: /reset password$/i });
      expect(submitBtn).toBeDisabled();
      await user.type(screen.getByLabelText(/new password/i), 'short');
      expect(submitBtn).toBeDisabled();
    });

    it('enables submit when password meets minimum length', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('alice')).toBeInTheDocument();
      });
      await user.click(screen.getByRole('button', { name: /reset password for alice/i }));
      await user.type(screen.getByLabelText(/new password/i), 'longenough');
      expect(screen.getByRole('button', { name: /reset password$/i })).toBeEnabled();
    });

    it('calls resetPassword API and closes dialog on success', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      (userService.resetPassword as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('alice')).toBeInTheDocument();
      });
      await user.click(screen.getByRole('button', { name: /reset password for alice/i }));
      await user.type(screen.getByLabelText(/new password/i), 'newsecurepass');
      await user.click(screen.getByRole('button', { name: /reset password$/i }));
      await waitFor(() => {
        expect(userService.resetPassword).toHaveBeenCalledWith('2', 'newsecurepass');
      });
      await waitFor(() => {
        expect(screen.queryByText('Reset Password')).not.toBeInTheDocument();
      });
    });

    it('shows error when API call fails', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      (userService.resetPassword as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fail'));
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('alice')).toBeInTheDocument();
      });
      await user.click(screen.getByRole('button', { name: /reset password for alice/i }));
      await user.type(screen.getByLabelText(/new password/i), 'newsecurepass');
      await user.click(screen.getByRole('button', { name: /reset password$/i }));
      await waitFor(() => {
        expect(screen.getByText(/failed to reset password/i)).toBeInTheDocument();
      });
    });

    it('closes dialog when cancel is clicked', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('alice')).toBeInTheDocument();
      });
      await user.click(screen.getByRole('button', { name: /reset password for alice/i }));
      expect(screen.getByRole('heading', { name: 'Reset Password' })).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: /cancel/i }));
      await waitFor(() => {
        expect(screen.queryByRole('heading', { name: 'Reset Password' })).not.toBeInTheDocument();
      });
    });
  });

  describe('account status and sign-in method', () => {
    const renderPage = () =>
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );

    it('shows the sign-in method and the status of each user', async () => {
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockUsers[0],
        mockUsers[1],
        { ...mockUsers[2], disabled: true },
      ]);
      renderPage();

      const aliceRow = (await screen.findByText('alice')).closest('tr') as HTMLElement;
      expect(within(aliceRow).getByText('Local')).toBeInTheDocument();
      expect(within(aliceRow).getByText('Active')).toBeInTheDocument();
      const bobRow = screen.getByText('oidc-bob').closest('tr') as HTMLElement;
      expect(within(bobRow).getByText('SSO')).toBeInTheDocument();
      expect(within(bobRow).getByText('Disabled')).toBeInTheDocument();
    });

    it('disables a user after confirmation', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      (userService.disable as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
      renderPage();

      await user.click(await screen.findByRole('button', { name: 'Disable user alice' }));
      const dialog = await screen.findByRole('dialog');
      expect(within(dialog).getByText(/cannot sign in/)).toBeInTheDocument();
      await user.click(within(dialog).getByRole('button', { name: 'Disable' }));

      await waitFor(() => {
        expect(userService.disable).toHaveBeenCalledWith('2');
      });
      const aliceRow = screen.getByText('alice').closest('tr') as HTMLElement;
      expect(await within(aliceRow).findByText('Disabled')).toBeInTheDocument();
      expect(await within(aliceRow).findByRole('button', { name: 'Enable user alice' })).toBeInTheDocument();
    });

    it('enables a disabled user without a dialog', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue([mockUsers[0], { ...mockUsers[1], disabled: true }]);
      (userService.enable as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
      renderPage();

      await user.click(await screen.findByRole('button', { name: 'Enable user alice' }));
      await waitFor(() => {
        expect(userService.enable).toHaveBeenCalledWith('2');
      });
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      const aliceRow = screen.getByText('alice').closest('tr') as HTMLElement;
      expect(await within(aliceRow).findByText('Active')).toBeInTheDocument();
    });

    it('shows an error when the status change fails', async () => {
      const user = userEvent.setup();
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue([mockUsers[0], { ...mockUsers[1], disabled: true }]);
      (userService.enable as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('boom'));
      renderPage();

      await user.click(await screen.findByRole('button', { name: 'Enable user alice' }));
      expect(await screen.findByText('Failed to enable user alice')).toBeInTheDocument();
    });

    it('does not allow a status change or delete on the own row', async () => {
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      renderPage();

      expect(await screen.findByRole('button', { name: 'Disable user admin' })).toBeDisabled();
      expect(screen.getByRole('button', { name: 'Delete user admin' })).toBeDisabled();
    });
  });

  it('shows the server message when a delete is refused', async () => {
    const user = userEvent.setup();
    (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
    (userService.delete as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 409, data: { error: 'The last enabled admin cannot be demoted, disabled or deleted' } },
    });
    render(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>
    );
    await user.click(await screen.findByRole('button', { name: 'Delete user alice' }));
    await user.click(screen.getByRole('button', { name: /^delete$/i }));
    expect(await screen.findByText('The last enabled admin cannot be demoted, disabled or deleted')).toBeInTheDocument();
  });

  describe('Role change', () => {
    /** Table row of a user (the dialog also shows the username). */
    const rowOf = (username: string): HTMLElement =>
      screen.getAllByText(username).map((el) => el.closest('tr')).find(Boolean) as HTMLElement;

    const renderPage = async () => {
      (userService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockUsers);
      render(
        <MemoryRouter>
          <AdminUsers />
        </MemoryRouter>
      );
      await screen.findByText('alice');
    };

    it('shows Edit role for local users only and disables it on the own row', async () => {
      await renderPage();
      expect(screen.getByRole('button', { name: 'Edit role of alice' })).toBeEnabled();
      expect(screen.getByRole('button', { name: 'Edit role of admin' })).toBeDisabled();
      expect(screen.queryByRole('button', { name: 'Edit role of oidc-bob' })).not.toBeInTheDocument();
    });

    it('shows the role of an SSO user read-only with a hint', async () => {
      await renderPage();
      const row = rowOf('oidc-bob');
      expect(within(row).getByText('user')).toBeInTheDocument();
      expect(within(row).getByText('managed by SSO')).toBeInTheDocument();
      const aliceRow = rowOf('alice');
      expect(within(aliceRow).queryByText('managed by SSO')).not.toBeInTheDocument();
    });

    it('changes the role after confirmation and updates the row', async () => {
      const user = userEvent.setup();
      (userService.changeRole as ReturnType<typeof vi.fn>).mockResolvedValue({
        id: '2', old_role: 'devops', new_role: 'admin', changed: true, message: 'Role changed',
      });
      await renderPage();

      await user.click(screen.getByRole('button', { name: 'Edit role of alice' }));
      expect(screen.getByRole('heading', { name: 'Edit Role' })).toBeInTheDocument();
      const submit = screen.getByRole('button', { name: 'Change Role' });
      expect(submit).toBeDisabled(); // unchanged role

      await user.click(screen.getByRole('combobox', { name: /role/i }));
      await user.click(screen.getByRole('option', { name: 'admin' }));
      expect(screen.getByText(/must sign in again/i)).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: 'Change Role' }));

      await waitFor(() => {
        expect(userService.changeRole).toHaveBeenCalledWith('2', 'admin');
      });
      await waitFor(() => {
        expect(screen.queryByRole('heading', { name: 'Edit Role' })).not.toBeInTheDocument();
      });
      const aliceRow = rowOf('alice');
      expect(within(aliceRow).getByText('admin')).toBeInTheDocument();
    });

    it('shows the server message when the change is refused', async () => {
      const user = userEvent.setup();
      (userService.changeRole as ReturnType<typeof vi.fn>).mockRejectedValue({
        response: { status: 409, data: { error: 'Cannot remove the admin role from the last enabled admin' } },
      });
      await renderPage();

      await user.click(screen.getByRole('button', { name: 'Edit role of alice' }));
      await user.click(screen.getByRole('combobox', { name: /role/i }));
      await user.click(screen.getByRole('option', { name: 'user' }));
      await user.click(screen.getByRole('button', { name: 'Change Role' }));

      expect(await screen.findByText('Cannot remove the admin role from the last enabled admin')).toBeInTheDocument();
      expect(screen.getByRole('heading', { name: 'Edit Role' })).toBeInTheDocument();
      const aliceRow = rowOf('alice');
      expect(within(aliceRow).getByText('devops')).toBeInTheDocument();
    });
  });
});
