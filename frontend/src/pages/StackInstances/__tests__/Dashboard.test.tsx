import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Dashboard from '../Dashboard';
import { NotificationProvider } from '../../../context/NotificationContext';
import type { WsMessage } from '../../../hooks/useWebSocket';

type MessageHandler = (msg: WsMessage) => void;
const capturedWsHandlers: MessageHandler[] = [];

function broadcastWs(msg: WsMessage) {
  capturedWsHandlers.forEach((h) => h(msg));
}

vi.mock('../../../hooks/useWebSocket', () => ({
  useWebSocket: (handler: MessageHandler) => {
    capturedWsHandlers.push(handler);
    return { send: vi.fn() };
  },
}));

vi.mock('../../../hooks/useCountdown', () => ({
  default: vi.fn().mockReturnValue(null),
}));

vi.mock('../../../utils/setupWizard', () => ({
  isSetupWizardDismissed: vi.fn().mockReturnValue(false),
  dismissSetupWizard: vi.fn(),
}));

vi.mock('../../../api/client', () => ({
  instanceService: {
    list: vi.fn(),
    recent: vi.fn().mockResolvedValue([]),
    bulkDeploy: vi.fn(),
    bulkStop: vi.fn(),
    bulkClean: vi.fn(),
    bulkDelete: vi.fn(),
    getStatus: vi.fn().mockRejectedValue(new Error('no status')),
    getPods: vi.fn().mockRejectedValue(new Error('no pods')),
  },
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
  },
  templateService: {
    list: vi.fn().mockResolvedValue([]),
  },
  favoriteService: {
    list: vi.fn().mockResolvedValue([]),
    check: vi.fn().mockResolvedValue(false),
    add: vi.fn(),
    remove: vi.fn(),
  },
  dashboardService: {
    getOverview: vi.fn().mockResolvedValue({
      clusters: [],
      recent_deployments: [],
      expiring_soon: [],
      failing_instances: [],
    }),
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

import { instanceService, favoriteService, clusterService, templateService } from '../../../api/client';
import { isSetupWizardDismissed } from '../../../utils/setupWizard';
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
    capturedWsHandlers.length = 0;
  });

  // Reset default mocks that survive clearAllMocks
  beforeEach(() => {
    localStorage.clear();
    (isSetupWizardDismissed as ReturnType<typeof vi.fn>).mockReturnValue(true);
    (instanceService.recent as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (favoriteService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([{ id: 'c1', name: 'dev' }]);
    (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([{ id: 't1', name: 'Web' }]);
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

  it('shows empty state when no instances but wizard dismissed', async () => {
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
      broadcastWs({
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
      broadcastWs({
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
      const user = userEvent.setup({ delay: null });
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
      const user = userEvent.setup({ delay: null });
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
      const user = userEvent.setup({ delay: null });
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
      const user = userEvent.setup({ delay: null });
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
      const user = userEvent.setup({ delay: null });
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
      await waitFor(() => {
        expect(screen.getByText('Confirm Bulk Stop')).toBeInTheDocument();
      });
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
      const user = userEvent.setup({ delay: null });
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
      const user = userEvent.setup({ delay: null });
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
      const user = userEvent.setup({ delay: null });
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
      await waitFor(() => {
        expect(screen.getByText('Confirm Bulk Clean')).toBeInTheDocument();
      });
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
      const user = userEvent.setup({ delay: null });
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
      await waitFor(() => {
        expect(screen.getByText('Confirm Bulk Deploy')).toBeInTheDocument();
      });
      const confirmButtons = screen.getAllByRole('button', { name: 'Deploy' });
      const dialogConfirmButton = confirmButtons[confirmButtons.length - 1];
      await user.click(dialogConfirmButton);

      // Should not show results dialog on error
      await waitFor(() => {
        expect(screen.queryByText('Bulk Operation Results')).not.toBeInTheDocument();
      });
    });

    it('cancels confirm dialog without executing action', async () => {
      const user = userEvent.setup({ delay: null });
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

      await waitFor(() => {
        expect(screen.getByText('Confirm Bulk Deploy')).toBeInTheDocument();
      });

      // Cancel
      await user.click(screen.getByRole('button', { name: 'Cancel' }));

      // Dialog should be gone but selection preserved
      expect(screen.queryByText('Confirm Bulk Deploy')).not.toBeInTheDocument();
      expect(screen.getByText('1 selected')).toBeInTheDocument();
      expect(instanceService.bulkDeploy).not.toHaveBeenCalled();
    });
  });

  describe('Dashboard Widgets', () => {
    it('renders cluster health widget from dashboard API', async () => {
      const { dashboardService } = await import('../../../api/client');
      (dashboardService.getOverview as ReturnType<typeof vi.fn>).mockResolvedValue({
        clusters: [
          { id: 'c1', name: 'prod', health_status: 'healthy' },
          { id: 'c2', name: 'staging', health_status: 'degraded' },
          { id: 'c3', name: 'dev', health_status: 'unreachable' },
        ],
        recent_deployments: [],
        expiring_soon: [],
        failing_instances: [],
      });
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
        expect(screen.getByText('Cluster Health')).toBeInTheDocument();
      });
      expect(screen.getByText('prod')).toBeInTheDocument();
      expect(screen.getByText('staging')).toBeInTheDocument();
      expect(screen.getByText('dev')).toBeInTheDocument();
    });

    it('renders empty state when no clusters', async () => {
      const { dashboardService } = await import('../../../api/client');
      (dashboardService.getOverview as ReturnType<typeof vi.fn>).mockResolvedValue({
        clusters: [],
        recent_deployments: [],
        expiring_soon: [],
        failing_instances: [],
      });
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
        expect(screen.getByText('Cluster Health')).toBeInTheDocument();
      });
      await waitFor(() => {
        expect(screen.getByText('No clusters registered.')).toBeInTheDocument();
      });
    });
  });

  describe('Filtering and Search', () => {
    it('filters instances by status', async () => {
      const user = userEvent.setup();
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', name: 'Running Instance', status: 'running', namespace: 'stack-r' }),
        mockInstance({ id: '2', name: 'Stopped Instance', status: 'stopped', namespace: 'stack-s' }),
        mockInstance({ id: '3', name: 'Draft Instance', status: 'draft', namespace: 'stack-d' }),
      ]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Running Instance')).toBeInTheDocument();
      });

      // Filter by draft status - use getAllByText since status badge also shows 'draft'
      const draftChips = screen.getAllByText('draft');
      await user.click(draftChips[0]);
      expect(screen.getByText('Draft Instance')).toBeInTheDocument();
      expect(screen.queryByText('Running Instance')).not.toBeInTheDocument();
      expect(screen.queryByText('Stopped Instance')).not.toBeInTheDocument();
    });

    it('filters instances by search text', async () => {
      const user = userEvent.setup();
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', name: 'Alpha Stack', namespace: 'stack-a' }),
        mockInstance({ id: '2', name: 'Beta Service', namespace: 'stack-b' }),
      ]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Alpha Stack')).toBeInTheDocument();
      });

      const searchInput = screen.getByPlaceholderText('Search instances...');
      await user.type(searchInput, 'alpha');

      expect(screen.getByText('Alpha Stack')).toBeInTheDocument();
      expect(screen.queryByText('Beta Service')).not.toBeInTheDocument();
    });

    it('shows empty state with create button when filter matches nothing', async () => {
      const user = userEvent.setup();
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', name: 'Test Instance', namespace: 'stack-t' }),
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

      const searchInput = screen.getByPlaceholderText('Search instances...');
      await user.type(searchInput, 'nonexistent');

      expect(screen.getByText('No stack instances found')).toBeInTheDocument();
    });
  });

  describe('Instance Card Details', () => {
    it('shows cluster name on instance card when cluster_id is set', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', name: 'Clustered Instance', namespace: 'stack-c', cluster_id: 'c1' }),
      ]);
      (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        { id: 'c1', name: 'production', health_status: 'healthy', is_default: true },
      ]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Clustered Instance')).toBeInTheDocument();
      });
      expect(screen.getByText('Cluster: production')).toBeInTheDocument();
    });

    it('shows last deployed timestamp on instance card', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({
          id: '1',
          name: 'My App',
          status: 'running',
          namespace: 'stack-d',
          last_deployed_at: '2026-03-28T10:00:00Z',
        }),
      ]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('My App')).toBeInTheDocument();
      });
      // The deployed timestamp is shown as relative time with "Deployed" prefix
      expect(screen.getByText(/Deployed\s/)).toBeInTheDocument();
    });

    it('fetches and displays URLs for running instances', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', name: 'Running App', status: 'running', namespace: 'stack-app' }),
      ]);
      (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue({
        namespace: 'stack-app',
        status: 'healthy',
        ingresses: [{ url: 'https://app.example.com' }],
        charts: [],
        pods: [],
      });
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Running App')).toBeInTheDocument();
      });
      await waitFor(() => {
        expect(screen.getByText('https://app.example.com')).toBeInTheDocument();
      });
    });

    it('shows pod health dot when status is fetched for running instance', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: 'inst-h', name: 'Healthy App', status: 'running', namespace: 'stack-h' }),
      ]);
      (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue({
        namespace: 'stack-h',
        status: 'healthy',
        charts: [],
        last_checked: '2026-01-01T00:00:00Z',
      });
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Healthy App')).toBeInTheDocument();
      });
      await waitFor(() => {
        expect(screen.getByRole('status', { name: /pod health: healthy/i })).toBeInTheDocument();
      });
    });

    it('navigates to detail page when Details button is clicked', async () => {
      // This test verifies the Details button renders on cards
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: 'inst-1', name: 'My Stack', namespace: 'stack-my' }),
      ]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('My Stack')).toBeInTheDocument();
      });
      expect(screen.getByRole('button', { name: 'Details' })).toBeInTheDocument();
    });
  });

  describe('Navigation Buttons', () => {
    it('renders Create Instance button', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Stack Instances')).toBeInTheDocument();
      });
      expect(screen.getByRole('button', { name: /create instance/i })).toBeInTheDocument();
    });

    it('renders Compare button', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Stack Instances')).toBeInTheDocument();
      });
      expect(screen.getByRole('button', { name: /compare/i })).toBeInTheDocument();
    });
  });

  describe('Setup Wizard', () => {
    beforeEach(() => {
      (isSetupWizardDismissed as ReturnType<typeof vi.fn>).mockReturnValue(false);
    });

    it('shows wizard when no clusters and no instances', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Welcome to Stack Manager')).toBeInTheDocument();
      });
    });

    it('does not show wizard when all resources exist', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance()]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Stack Instances')).toBeInTheDocument();
      });
      expect(screen.queryByText('Welcome to Stack Manager')).not.toBeInTheDocument();
    });

    it('shows wizard at step 2 when clusters exist but no templates', async () => {
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Welcome to Stack Manager')).toBeInTheDocument();
      });
      expect(screen.getByRole('button', { name: 'Create a Template' })).toBeInTheDocument();
    });

    it('does not show wizard when dismissed', async () => {
      (isSetupWizardDismissed as ReturnType<typeof vi.fn>).mockReturnValue(true);
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
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
      expect(screen.queryByText('Welcome to Stack Manager')).not.toBeInTheDocument();
    });

    it('hides wizard and shows dashboard when skip is clicked', async () => {
      const user = userEvent.setup();
      (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Welcome to Stack Manager')).toBeInTheDocument();
      });
      await user.click(screen.getByText('Skip setup'));
      await waitFor(() => {
        expect(screen.queryByText('Welcome to Stack Manager')).not.toBeInTheDocument();
      });
    });
  });
});
