import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import OrphanedNamespaces from '../index';
import { NotificationProvider } from '../../../../context/NotificationContext';

vi.mock('../../../../api/client', () => ({
  adminService: {
    listOrphanedNamespaces: vi.fn(),
    deleteOrphanedNamespace: vi.fn(),
  },
}));

vi.mock('../../../../context/AuthContext', () => ({
  useAuth: vi.fn(),
}));

import { adminService } from '../../../../api/client';
import { useAuth } from '../../../../context/AuthContext';

const adminUser = {
  id: '1',
  username: 'admin',
  role: 'admin',
  display_name: 'Admin User',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const mockOrphaned = [
  {
    name: 'stack-orphan-bob',
    created_at: '2026-01-15T10:00:00Z',
    phase: 'Active',
    resource_counts: { pods: 2, deployments: 1, services: 1 },
    helm_releases: ['nginx', 'redis'],
    managed: true,
  },
  {
    name: 'stack-old-alice',
    created_at: '2025-12-01T08:00:00Z',
    phase: 'Active',
    resource_counts: { pods: 0, deployments: 0, services: 0 },
    helm_releases: [],
    managed: true,
  },
];

const renderPage = () =>
  render(
    <MemoryRouter>
      <NotificationProvider>
        <OrphanedNamespaces />
      </NotificationProvider>
    </MemoryRouter>
  );

describe('OrphanedNamespaces Page', () => {
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
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise(() => {})
    );

    render(
      <MemoryRouter>
        <NotificationProvider>
          <OrphanedNamespaces />
        </NotificationProvider>
      </MemoryRouter>
    );

    expect(screen.getByRole('progressbar')).toBeTruthy();
  });

  it('renders orphaned namespaces in a table', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockOrphaned);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <OrphanedNamespaces />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('stack-orphan-bob')).toBeTruthy();
    });

    expect(screen.getByText('stack-old-alice')).toBeTruthy();
    expect(screen.getByText('nginx')).toBeTruthy();
    expect(screen.getByText('redis')).toBeTruthy();
  });

  it('shows empty state when no orphaned namespaces exist', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <OrphanedNamespaces />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/No orphaned namespaces found/)).toBeTruthy();
    });
  });

  it('shows error state when fetch fails', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error('Network error')
    );

    render(
      <MemoryRouter>
        <NotificationProvider>
          <OrphanedNamespaces />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Failed to load orphaned namespaces')).toBeTruthy();
    });
  });

  it('shows access denied for non-admin users', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: { ...adminUser, role: 'user' },
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <OrphanedNamespaces />
        </NotificationProvider>
      </MemoryRouter>
    );

    expect(screen.getByText(/Admin role required/)).toBeTruthy();
  });

  it('requests details and shows zero counts as 0', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue([mockOrphaned[1]]);
    renderPage();

    const row = (await screen.findByText('stack-old-alice')).closest('tr') as HTMLElement;
    expect(adminService.listOrphanedNamespaces).toHaveBeenCalledWith();
    expect(within(row).getAllByText('0')).toHaveLength(3);
    expect(within(row).queryByText('-')).toBeNull();
  });

  it('removes the row after a successful delete without a refetch', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockOrphaned);
    (adminService.deleteOrphanedNamespace as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete namespace stack-orphan-bob' }));
    await user.click(screen.getByRole('button', { name: 'Delete Namespace' }));

    await waitFor(() => {
      expect(screen.queryByText('stack-orphan-bob')).toBeNull();
    });
    expect(adminService.deleteOrphanedNamespace).toHaveBeenCalledWith('stack-orphan-bob', undefined);
    expect(adminService.listOrphanedNamespaces).toHaveBeenCalledTimes(1);
    expect(screen.getByText('stack-old-alice')).toBeTruthy();
  });

  it('keeps the row and shows an error when the delete fails', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockOrphaned);
    (adminService.deleteOrphanedNamespace as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('boom'));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete namespace stack-orphan-bob' }));
    await user.click(screen.getByRole('button', { name: 'Delete Namespace' }));

    expect(await screen.findByText('Failed to delete namespace "stack-orphan-bob"')).toBeTruthy();
    expect(screen.getByText('stack-orphan-bob')).toBeTruthy();
  });

  it('lists unmanaged namespaces apart and requires the full name to delete one', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockOrphaned[0],
      { ...mockOrphaned[1], name: 'stack-foreign-team', managed: false },
    ]);
    (adminService.deleteOrphanedNamespace as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage();

    const unmanagedTable = await screen.findByRole('table', { name: 'Unmanaged namespaces' });
    expect(within(unmanagedTable).getByText('stack-foreign-team')).toBeTruthy();
    expect(within(screen.getByRole('table', { name: 'Orphaned namespaces' })).queryByText('stack-foreign-team')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Delete namespace stack-foreign-team' }));
    const confirmButton = screen.getByRole('button', { name: 'Delete Namespace' });
    expect(confirmButton).toBeDisabled();

    const input = screen.getByLabelText('Type the namespace name to confirm');
    await user.type(input, 'stack-foreign');
    expect(confirmButton).toBeDisabled();
    await user.type(input, '-team');
    expect(confirmButton).toBeEnabled();

    await user.click(confirmButton);
    await waitFor(() => {
      expect(adminService.deleteOrphanedNamespace).toHaveBeenCalledWith('stack-foreign-team', 'stack-foreign-team');
    });
  });

  it('disables delete for a terminating namespace', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue([
      { ...mockOrphaned[0], phase: 'Terminating' },
    ]);
    renderPage();

    expect(await screen.findByRole('button', { name: 'Delete namespace stack-orphan-bob' })).toBeDisabled();
  });

  it('shows the API error text for a 409 and keeps the row', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockOrphaned);
    (adminService.deleteOrphanedNamespace as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 409, data: { error: 'Namespace is not orphaned — a matching stack instance exists' } },
    });
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete namespace stack-orphan-bob' }));
    await user.click(screen.getByRole('button', { name: 'Delete Namespace' }));

    expect(await screen.findByText(
      'Failed to delete namespace "stack-orphan-bob": Namespace is not orphaned — a matching stack instance exists',
    )).toBeTruthy();
    expect(screen.getByText('stack-orphan-bob')).toBeTruthy();
  });

  it('shows the API error text for a 404 and removes the row', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockOrphaned);
    (adminService.deleteOrphanedNamespace as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 404, data: { error: 'Namespace not found' } },
    });
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete namespace stack-orphan-bob' }));
    await user.click(screen.getByRole('button', { name: 'Delete Namespace' }));

    expect(await screen.findByText('Failed to delete namespace "stack-orphan-bob": Namespace not found')).toBeTruthy();
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Delete namespace stack-orphan-bob' })).toBeNull();
    });
  });
});
