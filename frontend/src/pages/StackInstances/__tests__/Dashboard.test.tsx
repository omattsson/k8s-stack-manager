import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Dashboard from '../Dashboard';
import { NotificationProvider } from '../../../context/NotificationContext';
import type { WsMessage } from '../../../hooks/useWebSocket';

type MessageHandler = (msg: WsMessage) => void;
let capturedWsHandler: MessageHandler | null = null;

vi.mock('../../../hooks/useWebSocket', () => ({
  useWebSocket: (handler: MessageHandler) => {
    capturedWsHandler = handler;
    return { send: vi.fn() };
  },
}));

vi.mock('../../../hooks/useCountdown', () => ({
  default: vi.fn().mockReturnValue(null),
}));

vi.mock('../../../api/client', () => ({
  instanceService: {
    list: vi.fn(),
    recent: vi.fn().mockResolvedValue([]),
    bulkDeploy: vi.fn(),
    bulkStop: vi.fn(),
    bulkClean: vi.fn(),
    bulkDelete: vi.fn(),
  },
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
  },
  favoriteService: {
    list: vi.fn().mockResolvedValue([]),
    check: vi.fn().mockResolvedValue(false),
    add: vi.fn(),
    remove: vi.fn(),
  },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({
    user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin' },
    isAuthenticated: true,
    isLoading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

import { instanceService, favoriteService } from '../../../api/client';
import useCountdown from '../../../hooks/useCountdown';

const mockInstance = (overrides: Record<string, unknown> = {}) => ({
  id: '1',
  name: 'Test Instance',
  status: 'running',
  branch: 'main',
  namespace: 'stack-test',
  owner_id: '1',
  stack_definition_id: '1',
  created_at: '',
  updated_at: '',
  ...overrides,
});

describe('Dashboard', () => {
  afterEach(() => {
    vi.clearAllMocks();
    capturedWsHandler = null;
  });

  // Reset default mocks that survive clearAllMocks
  beforeEach(() => {
    (instanceService.recent as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (favoriteService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
  });

  it('shows loading spinner initially', () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    (instanceService.recent as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays instances when fetch succeeds', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance(),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
  });

  it('shows error alert when fetch fails', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  it('shows empty state when no instances', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no stack instances found/i)).toBeInTheDocument();
    });
  });

  it('updates instance status on WebSocket deployment.status message', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ id: 'inst-1', name: 'WS Instance', status: 'draft' }),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    // Wait for initial render with draft status.
    await waitFor(() => {
      expect(screen.getByText('WS Instance')).toBeInTheDocument();
    });
    // 'draft' appears in both status filter chip and instance badge
    expect(screen.getAllByText('draft').length).toBeGreaterThanOrEqual(2);

    // Simulate a WebSocket deployment.status message.
    act(() => {
      capturedWsHandler?.({
        type: 'deployment.status',
        payload: { instance_id: 'inst-1', status: 'deploying', log_id: 'log-1' },
      });
    });

    await waitFor(() => {
      // 'deploying' should now appear in both filter chip and badge
      expect(screen.getAllByText('deploying').length).toBeGreaterThanOrEqual(2);
    });
    // 'draft' should now only appear in the status filter chip
    expect(screen.getAllByText('draft')).toHaveLength(1);
  });

  it('ignores WebSocket messages for unknown instance IDs', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ id: 'inst-1', name: 'My Instance' }),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('My Instance')).toBeInTheDocument();
    });
    // 'running' appears in both status filter chip and instance badge
    expect(screen.getAllByText('running').length).toBeGreaterThanOrEqual(2);

    // Send a message for a different instance.
    act(() => {
      capturedWsHandler?.({
        type: 'deployment.status',
        payload: { instance_id: 'unknown-id', status: 'error', log_id: 'log-2' },
      });
    });

    // Status should remain unchanged.
    expect(screen.getAllByText('running').length).toBeGreaterThanOrEqual(2);
  });

  it('shows countdown chip for running instance with expiry', async () => {
    const mockUseCountdown = useCountdown as unknown as ReturnType<typeof vi.fn>;
    mockUseCountdown.mockReturnValue({
      remaining: '3h 42m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });

    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ name: 'TTL Instance', namespace: 'stack-ttl', expires_at: '2026-01-01T12:00:00Z', ttl_minutes: 240 }),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('TTL Instance')).toBeInTheDocument();
    });

    expect(screen.getByText(/3h 42m/)).toBeInTheDocument();
  });

  it('shows Expired chip for TTL-expired stopped instance', async () => {
    const mockUseCountdown = useCountdown as unknown as ReturnType<typeof vi.fn>;
    mockUseCountdown.mockReturnValue(null);

    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ name: 'Expired Instance', status: 'stopped', namespace: 'stack-exp', error_message: 'Expired (TTL)' }),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Expired Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Expired')).toBeInTheDocument();
  });

  it('does not show countdown chip for instance without expiry', async () => {
    const mockUseCountdown = useCountdown as unknown as ReturnType<typeof vi.fn>;
    mockUseCountdown.mockReturnValue(null);

    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ name: 'No TTL Instance', namespace: 'stack-nottl' }),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('No TTL Instance')).toBeInTheDocument();
    });

    expect(screen.queryByText(/⏱/)).not.toBeInTheDocument();
    expect(screen.queryByText('Expired')).not.toBeInTheDocument();
  });

  it('renders favorites section with hint when no favorites', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Favorites')).toBeInTheDocument();
      expect(screen.getByText('Star instances to add them here')).toBeInTheDocument();
    });
  });

  it('renders favorited instances in favorites section', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ id: 'inst-1', name: 'Fav Instance', namespace: 'stack-fav' }),
    ]);
    (favoriteService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: 'fav-1', user_id: '1', entity_type: 'instance', entity_id: 'inst-1', created_at: '' },
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('My Favorites')).toBeInTheDocument();
      // Instance should appear in the favorites section (and also in the main list)
      expect(screen.getAllByText('Fav Instance').length).toBeGreaterThanOrEqual(1);
    });
  });

  it('renders recent stacks section when recent instances exist', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (instanceService.recent as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ id: 'r-1', name: 'Recent Stack', status: 'draft', namespace: 'stack-rec', updated_at: '2026-01-15T10:00:00Z' }),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Recent Stacks')).toBeInTheDocument();
      expect(screen.getByText('Recent Stack')).toBeInTheDocument();
    });
  });

  it('hides recent stacks section when empty', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ name: 'Some Instance', namespace: 'stack-some' }),
    ]);
    (instanceService.recent as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Some Instance')).toBeInTheDocument();
    });
    expect(screen.queryByText('Recent Stacks')).not.toBeInTheDocument();
  });

  describe('Bulk Operations', () => {
    const twoInstances = [
      mockInstance({ id: 'inst-1', name: 'Instance A', namespace: 'stack-a' }),
      mockInstance({ id: 'inst-2', name: 'Instance B', namespace: 'stack-b', status: 'stopped' }),
    ];

    beforeEach(() => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);
    });

    it('shows checkboxes on each instance card', async () => {
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });
      expect(screen.getByRole('checkbox', { name: 'Select Instance A' })).toBeInTheDocument();
      expect(screen.getByRole('checkbox', { name: 'Select Instance B' })).toBeInTheDocument();
    });

    it('shows bulk action toolbar when instances are selected', async () => {
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      // No toolbar initially
      expect(screen.queryByRole('toolbar', { name: 'Bulk actions' })).not.toBeInTheDocument();

      // Select one instance
      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));

      expect(screen.getByRole('toolbar', { name: 'Bulk actions' })).toBeInTheDocument();
      expect(screen.getByText('1 selected')).toBeInTheDocument();
    });

    it('select all checkbox selects all filtered instances', async () => {
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select all instances' }));

      expect(screen.getByText('2 selected')).toBeInTheDocument();
    });

    it('shows confirm dialog before bulk deploy', async () => {
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      const deployBtn = screen.getByRole('button', { name: 'Deploy' });
      await user.click(deployBtn);

      // Confirm dialog should be visible
      expect(screen.getByText('Confirm Bulk Deploy')).toBeInTheDocument();
    });

    it('executes bulk deploy and shows results dialog', async () => {
      const user = userEvent.setup();
      const bulkResult = {
        total: 1,
        succeeded: 1,
        failed: 0,
        results: [
          { instance_id: 'inst-1', instance_name: 'Instance A', status: 'success' as const },
        ],
      };
      (instanceService.bulkDeploy as ReturnType<typeof vi.fn>).mockResolvedValue(bulkResult);
      // Mock the refresh call after bulk operation
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);

      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      await user.click(screen.getByRole('button', { name: 'Deploy' }));

      // Confirm the action in the dialog
      const confirmButtons = screen.getAllByRole('button', { name: 'Deploy' });
      const dialogConfirmButton = confirmButtons[confirmButtons.length - 1];
      await user.click(dialogConfirmButton);

      // Results dialog should appear
      await waitFor(() => {
        expect(screen.getByText('Bulk Operation Results')).toBeInTheDocument();
      });
      expect(screen.getByText('1 succeeded')).toBeInTheDocument();
      expect(instanceService.bulkDeploy).toHaveBeenCalledWith(['inst-1']);
    });

    it('shows failures in results dialog', async () => {
      const user = userEvent.setup();
      const bulkResult = {
        total: 2,
        succeeded: 1,
        failed: 1,
        results: [
          { instance_id: 'inst-1', instance_name: 'Instance A', status: 'success' as const },
          { instance_id: 'inst-2', instance_name: 'Instance B', status: 'error' as const, error: 'Instance not found' },
        ],
      };
      (instanceService.bulkStop as ReturnType<typeof vi.fn>).mockResolvedValue(bulkResult);
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);

      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      // Select all, then stop
      await user.click(screen.getByRole('checkbox', { name: 'Select all instances' }));
      await user.click(screen.getByRole('button', { name: 'Stop' }));

      // Confirm
      const confirmButtons = screen.getAllByRole('button', { name: 'Stop' });
      const dialogConfirmButton = confirmButtons[confirmButtons.length - 1];
      await user.click(dialogConfirmButton);

      await waitFor(() => {
        expect(screen.getByText('Bulk Operation Results')).toBeInTheDocument();
      });
      expect(screen.getByText('1 succeeded')).toBeInTheDocument();
      expect(screen.getByText('1 failed')).toBeInTheDocument();
      expect(screen.getByText('Instance not found')).toBeInTheDocument();
    });

    it('shows warning alert for bulk delete confirmation', async () => {
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      await user.click(screen.getByRole('button', { name: 'Delete' }));

      expect(screen.getByText('Confirm Bulk Delete')).toBeInTheDocument();
      expect(screen.getByText(/cannot be undone/i)).toBeInTheDocument();
    });

    it('clears selection when Clear Selection is clicked', async () => {
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      expect(screen.getByText('1 selected')).toBeInTheDocument();

      await user.click(screen.getByRole('button', { name: /Clear Selection/i }));
      expect(screen.queryByText('1 selected')).not.toBeInTheDocument();
    });

    it('clears selection after closing results dialog', async () => {
      const user = userEvent.setup();
      const bulkResult = {
        total: 1,
        succeeded: 1,
        failed: 0,
        results: [
          { instance_id: 'inst-1', instance_name: 'Instance A', status: 'success' as const },
        ],
      };
      (instanceService.bulkClean as ReturnType<typeof vi.fn>).mockResolvedValue(bulkResult);
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);

      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      await user.click(screen.getByRole('button', { name: 'Clean' }));

      // Confirm
      const confirmButtons = screen.getAllByRole('button', { name: 'Clean' });
      const dialogConfirmButton = confirmButtons[confirmButtons.length - 1];
      await user.click(dialogConfirmButton);

      await waitFor(() => {
        expect(screen.getByText('Bulk Operation Results')).toBeInTheDocument();
      });

      // Close results dialog
      await user.click(screen.getByRole('button', { name: /Close/i }));

      // Selection should be cleared - toolbar gone
      await waitFor(() => {
        expect(screen.queryByRole('toolbar', { name: 'Bulk actions' })).not.toBeInTheDocument();
      });
    });

    it('handles bulk operation API failure gracefully', async () => {
      const user = userEvent.setup();
      (instanceService.bulkDeploy as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Server error'));

      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      await user.click(screen.getByRole('button', { name: 'Deploy' }));

      // Confirm
      const confirmButtons = screen.getAllByRole('button', { name: 'Deploy' });
      const dialogConfirmButton = confirmButtons[confirmButtons.length - 1];
      await user.click(dialogConfirmButton);

      // Should not show results dialog on error
      await waitFor(() => {
        expect(screen.queryByText('Bulk Operation Results')).not.toBeInTheDocument();
      });
    });

    it('cancels confirm dialog without executing action', async () => {
      const user = userEvent.setup();
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      await user.click(screen.getByRole('button', { name: 'Deploy' }));

      expect(screen.getByText('Confirm Bulk Deploy')).toBeInTheDocument();

      // Cancel
      await user.click(screen.getByRole('button', { name: 'Cancel' }));

      // Dialog should be gone but selection preserved
      expect(screen.queryByText('Confirm Bulk Deploy')).not.toBeInTheDocument();
      expect(screen.getByText('1 selected')).toBeInTheDocument();
      expect(instanceService.bulkDeploy).not.toHaveBeenCalled();
    });
  });
});
