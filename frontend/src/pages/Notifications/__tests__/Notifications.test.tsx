import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
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
});
