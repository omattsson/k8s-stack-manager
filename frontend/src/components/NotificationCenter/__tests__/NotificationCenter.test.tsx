import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import NotificationCenter from '../index';

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
    countUnread: vi.fn(),
    markAsRead: vi.fn(),
    markAllAsRead: vi.fn(),
  },
}));

vi.mock('../../../hooks/useWebSocket', () => ({
  useWebSocket: vi.fn(),
}));

vi.mock('../../../context/NotificationContext', () => ({
  useNotification: vi.fn().mockReturnValue({
    showInfo: vi.fn(),
    showSuccess: vi.fn(),
    showError: vi.fn(),
    showWarning: vi.fn(),
  }),
}));

import { notificationService } from '../../../api/client';

const mockNotifications = [
  {
    id: 'n1',
    user_id: 'u1',
    type: 'deployment.success',
    title: 'Deploy succeeded',
    message: 'Instance my-stack deployed',
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
    message: 'Instance bad-stack failed',
    is_read: true,
    entity_type: 'stack_instance',
    entity_id: 'inst-2',
    created_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
  },
];

describe('NotificationCenter', () => {
  beforeEach(() => {
    (notificationService.countUnread as ReturnType<typeof vi.fn>).mockResolvedValue({ unread_count: 3 });
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

  it('renders the bell icon button', () => {
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );
    expect(screen.getByRole('button', { name: /open notifications/i })).toBeInTheDocument();
  });

  it('fetches and displays unread count badge', async () => {
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(notificationService.countUnread).toHaveBeenCalled();
    });
    expect(screen.getByText('3')).toBeInTheDocument();
  });

  it('opens popover with notifications on click', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole('button', { name: /open notifications/i }));

    await waitFor(() => {
      expect(screen.getByText('Notifications')).toBeInTheDocument();
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
      expect(screen.getByText('Deploy failed')).toBeInTheDocument();
    });
  });

  it('shows empty state when there are no notifications', async () => {
    (notificationService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      notifications: [],
      total: 0,
      unread_count: 0,
    });

    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole('button', { name: /open notifications/i }));

    await waitFor(() => {
      expect(screen.getByText('No notifications yet')).toBeInTheDocument();
    });
  });

  it('calls markAllAsRead when "Mark all read" button is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole('button', { name: /open notifications/i }));

    await waitFor(() => {
      expect(screen.getByText('Mark all read')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /mark all read/i }));

    await waitFor(() => {
      expect(notificationService.markAllAsRead).toHaveBeenCalled();
    });
  });

  it('shows "View all notifications" link', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole('button', { name: /open notifications/i }));

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /view all notifications/i })).toBeInTheDocument();
    });
  });

  it('handles error in countUnread gracefully', async () => {
    (notificationService.countUnread as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network'));

    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );

    // Should not crash, bell button still visible
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /open notifications/i })).toBeInTheDocument();
    });
  });

  it('calls markAsRead when an unread notification is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole('button', { name: /open notifications/i }));

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
        <NotificationCenter />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole('button', { name: /open notifications/i }));

    await waitFor(() => {
      expect(screen.getByText('Deploy succeeded')).toBeInTheDocument();
    });

    // Click notification with entity_type 'stack_instance' and entity_id 'inst-1'
    await user.click(screen.getByText('Deploy succeeded'));

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/stack-instances/inst-1');
    });
  });

  it('does not call markAsRead when an already-read notification is clicked', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <NotificationCenter />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole('button', { name: /open notifications/i }));

    await waitFor(() => {
      expect(screen.getByText('Deploy failed')).toBeInTheDocument();
    });

    // Click the already-read notification (n2, is_read: true)
    await user.click(screen.getByText('Deploy failed'));

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/stack-instances/inst-2');
    });

    // markAsRead should NOT have been called for an already-read notification
    expect(notificationService.markAsRead).not.toHaveBeenCalled();
  });
});
