import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Notifications from '../index';

const mockNavigate = vi.fn();

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../../api/client', () => ({
  notificationService: {
    list: vi.fn(),
    markAsRead: vi.fn(),
    markAllAsRead: vi.fn(),
  },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'u1', username: 'me', role: 'user' } }),
}));

import { notificationService } from '../../../api/client';

const mockNotifications = [
  {
    id: 'n1',
    user_id: 'u1',
    type: 'deployment.success',
    title: 'Deploy succeeded',
    message: 'Instance my-stack deployed successfully',
    is_read: false,
    entity_type: 'stack_instance',
    entity_id: 'inst-1',
    created_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
  },
  {
    id: 'n2',
    user_id: 'u1',
    type: 'deployment.error',
    title: 'Deploy failed',
    message: 'Instance bad-stack failed to deploy',
    is_read: true,
    entity_type: 'stack_instance',
    entity_id: 'inst-2',
    created_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
  },
];

const triggerNotifications = [
  { id: 't1', trigger: { type: 'user', id: 'u2', name: 'alice' }, title: 'Stack stopped' },
  { id: 't2', trigger: { type: 'user', id: 'u1', name: 'me' }, title: 'Stack created' },
  { id: 't3', trigger: { type: 'ttl' }, title: 'Stack expired' },
  { id: 't4', trigger: { type: 'cleanup-policy', id: 'p1' }, title: 'Stack cleaned' },
  { id: 't5', title: 'Old notification' },
].map((n) => ({
  user_id: 'u1',
  type: 'deployment.stopped',
  message: `Message of ${n.id}`,
  is_read: true,
  created_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
  ...n,
}));

describe('Notifications Page', () => {
  beforeEach(() => {
    (notificationService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      notifications: mockNotifications,
      total: 2,
      unread_count: 1,
    });
    (notificationService.markAsRead as ReturnType<typeof vi.fn>).mockResolvedValue({});
    (notificationService.markAllAsRead as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading state initially', () => {
    (notificationService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );
    expect(screen.getByRole('status')).toBeInTheDocument();
  });

  it('renders page heading and notifications after loading', async () => {
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Notifications');
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
      expect(screen.getByText('Deploy failed')).toBeInTheDocument();
    });
  });

  it('shows error alert when fetch fails', async () => {
    (notificationService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network'));
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText(/failed to load notifications/i)).toBeInTheDocument();
    });
  });

  it('shows empty state when no notifications exist', async () => {
    (notificationService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      notifications: [],
      total: 0,
      unread_count: 0,
    });
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(screen.getByText('No notifications yet')).toBeInTheDocument();
    });
  });

  it('renders filter toggle buttons', async () => {
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^all$/i })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /^unread/i })).toBeInTheDocument();
    });
  });

  it('calls markAllAsRead when button is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /mark all read/i })).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /mark all read/i }));

    await waitFor(() => {
      expect(notificationService.markAllAsRead).toHaveBeenCalled();
    });
  });

  it('switches to unread filter when toggled', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /^unread/i }));

    await waitFor(() => {
      expect(notificationService.list).toHaveBeenCalledWith(true, 25, 0);
    });
  });

  it('renders pagination controls', async () => {
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
    });
    // MUI TablePagination renders rows per page
    expect(screen.getByText(/1–2 of 2/i)).toBeInTheDocument();
  });

  it('calls markAsRead when an unread notification is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
    });

    // Click the unread notification (n1)
    await user.click(screen.getByText('Deploy succeeded'));

    await waitFor(() => {
      expect(notificationService.markAsRead).toHaveBeenCalledWith('n1');
    });
  });

  it('navigates to entity URL when notification is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
    });

    await user.click(screen.getByText('Deploy succeeded'));

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/stack-instances/inst-1');
    });
  });

  it('navigates to next page when pagination is clicked', async () => {
    // Return a larger total to enable pagination
    (notificationService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      notifications: mockNotifications,
      total: 50,
      unread_count: 10,
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
    });

    // Initial call: list(false, 25, 0)
    expect(notificationService.list).toHaveBeenCalledWith(false, 25, 0);

    // Click the next page button
    const nextPageButton = screen.getByRole('button', { name: /next page/i });
    await user.click(nextPageButton);

    await waitFor(() => {
      // Second page: list(false, 25, 25)
      expect(notificationService.list).toHaveBeenCalledWith(false, 25, 25);
    });
  });

  describe('capped total (#654)', () => {
    const listMock = () => notificationService.list as ReturnType<typeof vi.fn>;

    it('shows "of 1000+" for a capped count and pages forward', async () => {
      listMock().mockImplementation(
        async (_unread: boolean, _limit: number, offset: number) => ({
          notifications: mockNotifications,
          total: offset + 1000,
          total_capped: true,
          unread_count: 7,
        }),
      );
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <Notifications />
        </MemoryRouter>,
      );

      expect(await screen.findByText('1–25 of 1000+')).toBeInTheDocument();
      const next = screen.getByRole('button', { name: /next page/i });
      expect(next).toBeEnabled();

      await user.click(next);

      await waitFor(() => {
        expect(notificationService.list).toHaveBeenLastCalledWith(false, 25, 25);
      });
      expect(await screen.findByText('26–50 of 1025+')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /next page/i })).toBeEnabled();
    });

    it('shows the exact count when the total is not capped', async () => {
      listMock().mockResolvedValue({
        notifications: mockNotifications,
        total: 30,
        total_capped: false,
        unread_count: 1,
      });
      render(
        <MemoryRouter>
          <Notifications />
        </MemoryRouter>,
      );

      expect(await screen.findByText('1–25 of 30')).toBeInTheDocument();
      expect(screen.queryByText(/of 30\+/)).not.toBeInTheDocument();
    });

    it('treats a response without total_capped as not capped', async () => {
      listMock().mockResolvedValue({
        notifications: mockNotifications,
        total: 1000,
        unread_count: 1,
      });
      render(
        <MemoryRouter>
          <Notifications />
        </MemoryRouter>,
      );

      expect(await screen.findByText('1–25 of 1000')).toBeInTheDocument();
      expect(screen.queryByText(/of 1000\+/)).not.toBeInTheDocument();
    });
  });

  describe('capped unread count (#654)', () => {
    const listMock = () => notificationService.list as ReturnType<typeof vi.fn>;

    it('shows "Unread (1000+)" and clears it on mark all read', async () => {
      listMock().mockResolvedValue({
        notifications: mockNotifications,
        total: 1000,
        total_capped: true,
        unread_count: 1000,
        unread_count_capped: true,
      });
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <Notifications />
        </MemoryRouter>,
      );

      expect(await screen.findByRole('button', { name: 'Unread (1000+)' })).toBeInTheDocument();

      await user.click(screen.getByRole('button', { name: /mark all read/i }));

      expect(await screen.findByRole('button', { name: 'Unread' })).toBeInTheDocument();
      expect(screen.queryByText(/\+\)/)).not.toBeInTheDocument();
    });

    it('keeps the "+" when one notification is read', async () => {
      listMock().mockResolvedValue({
        notifications: mockNotifications,
        total: 1000,
        total_capped: true,
        unread_count: 1000,
        unread_count_capped: true,
      });
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <Notifications />
        </MemoryRouter>,
      );

      await screen.findByRole('button', { name: 'Unread (1000+)' });
      await user.click(screen.getByText(mockNotifications[0].title));

      expect(await screen.findByRole('button', { name: 'Unread (999+)' })).toBeInTheDocument();
    });

    it('shows the exact unread count without unread_count_capped', async () => {
      listMock().mockResolvedValue({
        notifications: mockNotifications,
        total: 30,
        unread_count: 1000,
      });
      render(
        <MemoryRouter>
          <Notifications />
        </MemoryRouter>,
      );

      expect(await screen.findByRole('button', { name: 'Unread (1000)' })).toBeInTheDocument();
    });
  });

  it('shows who did the action under each notification (#652)', async () => {
    (notificationService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      notifications: triggerNotifications,
      total: triggerNotifications.length,
      unread_count: 0,
    });
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );

    const item = async (title: string) =>
      within((await screen.findByText(title)).closest('li') as HTMLElement);
    expect((await item('Stack stopped')).getByText('by alice')).toBeInTheDocument();
    expect((await item('Stack created')).getByText('by you')).toBeInTheDocument();
    expect((await item('Stack expired')).getByText('by expiry')).toBeInTheDocument();
    expect((await item('Stack cleaned')).getByText('by a cleanup policy')).toBeInTheDocument();
    const old = await item('Old notification');
    expect(old.getByText('5m ago')).toBeInTheDocument();
    expect(old.queryByText(/^by /)).not.toBeInTheDocument();
  });

  it('shows no "by" line for notifications without a trigger', async () => {
    render(
      <MemoryRouter>
        <Notifications />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
    });
    expect(screen.queryByText(/^by /)).not.toBeInTheDocument();
  });
});
