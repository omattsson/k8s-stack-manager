import { StrictMode } from 'react';
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, within, act } from '@testing-library/react';
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
    listAll: vi.fn(),
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

const authState = vi.hoisted(() => ({
  user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin' } as
    { id: string; username: string; role: string; display_name: string },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({
    user: authState.user,
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
    authState.user = { id: '1', username: 'admin', role: 'admin', display_name: 'Admin' };
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
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
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
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

  it('loads all instances, not only the first API page, and shows the count', async () => {
    const user = userEvent.setup();
    const many = Array.from({ length: 30 }, (_, i) => mockInstance({
      id: `inst-${i}`,
      name: `stack-${String(i).padStart(2, '0')}`,
      status: i < 4 ? 'running' : 'draft',
    }));
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue(many);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('stack-29')).toBeInTheDocument();
    });
    expect(instanceService.listAll).toHaveBeenCalledTimes(1);
    expect(screen.getByText('30 instances')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'running' }));
    expect(screen.getByText('Showing 4 of 30 instances')).toBeInTheDocument();
  });

  it('shows error alert when fetch fails', async () => {
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
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
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('No stack instances found')).toBeInTheDocument();
    });
  });

  it('removes the row on a WebSocket instance.deleted message', async () => {
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
      mockInstance({ id: 'inst-1', name: 'Gone Instance', status: 'running' }),
      mockInstance({ id: 'inst-2', name: 'Kept Instance', status: 'running' }),
    ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Gone Instance')).toBeInTheDocument();
    });

    act(() => {
      broadcastWs({ type: 'instance.deleted', payload: { instance_id: 'inst-1', log_id: 'log-9' } });
    });

    await waitFor(() => {
      expect(screen.queryByText('Gone Instance')).not.toBeInTheDocument();
    });
    expect(screen.getByText('Kept Instance')).toBeInTheDocument();
  });

  it('updates instance status on WebSocket deployment.status message', async () => {
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

  it('reloads the list for a deployment.status of an instance that is not in it', async () => {
    const listAll = instanceService.listAll as ReturnType<typeof vi.fn>;
    listAll
      .mockResolvedValueOnce([mockInstance({ id: 'inst-1', name: 'Old Stack' })])
      .mockResolvedValue([
        mockInstance({ id: 'inst-1', name: 'Old Stack' }),
        mockInstance({ id: 'inst-2', name: 'New Stack', status: 'deploying' }),
      ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Old Stack')).toBeInTheDocument();
    });

    act(() => {
      broadcastWs({
        type: 'deployment.status',
        payload: { instance_id: 'inst-2', status: 'deploying', log_id: 'log-3' },
      });
    });

    await waitFor(() => {
      expect(screen.getByText('New Stack')).toBeInTheDocument();
    }, { timeout: 3000 });
  });

  it('reloads the list on instance.created', async () => {
    const listAll = instanceService.listAll as ReturnType<typeof vi.fn>;
    listAll
      .mockResolvedValueOnce([mockInstance({ id: 'inst-1', name: 'Old Stack' })])
      .mockResolvedValue([
        mockInstance({ id: 'inst-1', name: 'Old Stack' }),
        mockInstance({ id: 'inst-3', name: 'Draft Stack', status: 'draft' }),
      ]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Old Stack')).toBeInTheDocument();
    });

    act(() => {
      broadcastWs({ type: 'instance.created', payload: { instance_id: 'inst-3' } });
    });

    await waitFor(() => {
      expect(screen.getByText('Draft Stack')).toBeInTheDocument();
    }, { timeout: 3000 });
  });

  it('reloads the list at most once for the same unknown instance', async () => {
    const listAll = instanceService.listAll as ReturnType<typeof vi.fn>;
    listAll.mockResolvedValue([mockInstance({ id: 'inst-1', name: 'Only Stack' })]);
    render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Only Stack')).toBeInTheDocument();
    });
    const callsBefore = listAll.mock.calls.length;

    act(() => {
      broadcastWs({ type: 'deployment.status', payload: { instance_id: 'ghost', status: 'deploying' } });
    });
    await waitFor(() => {
      expect(listAll.mock.calls.length).toBe(callsBefore + 1);
    }, { timeout: 3000 });

    act(() => {
      broadcastWs({ type: 'deployment.status', payload: { instance_id: 'ghost', status: 'running' } });
      broadcastWs({ type: 'instance.created', payload: { instance_id: 'ghost' } });
    });
    await new Promise((resolve) => setTimeout(resolve, 800));
    expect(listAll.mock.calls.length).toBe(callsBefore + 1);
  });

  it('shows countdown chip for running instance with expiry', async () => {
    const mockUseCountdown = useCountdown as unknown as ReturnType<typeof vi.fn>;
    mockUseCountdown.mockReturnValue({
      remaining: '3h 42m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });

    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

    expect(screen.queryByText(/Expires in|Stops at/)).not.toBeInTheDocument();
    expect(screen.queryByText('Expired')).not.toBeInTheDocument();
  });

  it('renders favorites section with hint when no favorites', async () => {
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
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
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
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
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);
    });

    const renderDashboard = () => render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    it('shows checkboxes for a devops user on instances owned by others', async () => {
      authState.user = { id: 'u-devops', username: 'devops1', role: 'devops', display_name: 'DevOps' };
      renderDashboard();
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });
      expect(screen.getByRole('checkbox', { name: 'Select Instance A' })).toBeInTheDocument();
      expect(screen.getByRole('checkbox', { name: 'Select Instance B' })).toBeInTheDocument();
      expect(screen.getByRole('checkbox', { name: 'Select all instances' })).toBeInTheDocument();
    });

    it('offers no bulk selection to a user with role user', async () => {
      // The user owns Instance A; bulk routes still need the devops role.
      authState.user = { id: '1', username: 'alice', role: 'user', display_name: 'Alice' };
      renderDashboard();
      await waitFor(() => {
        expect(screen.getByText('Instance A')).toBeInTheDocument();
      });
      expect(screen.queryByRole('checkbox', { name: 'Select Instance A' })).not.toBeInTheDocument();
      expect(screen.queryByRole('checkbox', { name: 'Select Instance B' })).not.toBeInTheDocument();
      expect(screen.queryByRole('checkbox', { name: 'Select all instances' })).not.toBeInTheDocument();
      // Details stay available for every instance.
      expect(screen.getAllByRole('button', { name: 'Details' })).toHaveLength(2);
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);

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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);

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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue(twoInstances);

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

    describe('selection across filters (#753)', () => {
      const devops = { id: 'u-devops', username: 'devops1', role: 'devops', display_name: 'DevOps' };
      const threeRunningTwoDraft = [
        mockInstance({ id: 'r1', name: 'Run One', status: 'running' }),
        mockInstance({ id: 'r2', name: 'Run Two', status: 'running' }),
        mockInstance({ id: 'r3', name: 'Run Three', status: 'running' }),
        mockInstance({ id: 'd1', name: 'Draft One', status: 'draft' }),
        mockInstance({ id: 'd2', name: 'Draft Two', status: 'draft' }),
      ];
      const header = () => screen.getByRole('checkbox', { name: 'Select all instances' });
      const toolbar = () => within(screen.getByRole('toolbar', { name: 'Bulk actions' }));
      const filterBy = (user: ReturnType<typeof userEvent.setup>, status: string) =>
        user.click(screen.getByRole('button', { name: status }));
      const setup = async () => {
        authState.user = devops;
        (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue(threeRunningTwoDraft);
        renderDashboard();
        await screen.findByText('Run One');
        return userEvent.setup({ delay: null });
      };

      it('counts hidden stacks in the toolbar and keeps them selected', async () => {
        const user = await setup();
        await filterBy(user, 'running');
        await user.click(header());
        expect(toolbar().getByText('3 selected')).toBeInTheDocument();

        await filterBy(user, 'draft');

        expect(toolbar().getByText('3 selected (3 not shown)')).toBeInTheDocument();
        expect(screen.getByRole('checkbox', { name: 'Select Draft One' })).not.toBeChecked();

        await filterBy(user, 'All Statuses');
        expect(toolbar().getByText('3 selected')).toBeInTheDocument();
      });

      it('shows the header as empty when no visible stack is selected', async () => {
        const user = await setup();
        await filterBy(user, 'running');
        await user.click(header());
        await filterBy(user, 'draft');

        expect(header()).not.toBeChecked();
        expect(header()).toHaveAttribute('data-indeterminate', 'false');
        expect(screen.getByText('Select all (2)')).toBeInTheDocument();
      });

      it('does not treat equal counts of other stacks as all selected', async () => {
        const user = await setup();
        await user.click(screen.getByRole('checkbox', { name: 'Select Run One' }));
        await user.click(screen.getByRole('checkbox', { name: 'Select Run Two' }));
        await filterBy(user, 'draft'); // two visible stacks, two other stacks selected

        expect(header()).not.toBeChecked();
        expect(screen.queryByText(/Deselect all/)).not.toBeInTheDocument();
      });

      it('shows the header as partly selected from the visible stacks only', async () => {
        const user = await setup();
        await filterBy(user, 'draft');
        await user.click(screen.getByRole('checkbox', { name: 'Select Draft One' }));

        expect(header()).toHaveAttribute('data-indeterminate', 'true');
      });

      it('select all adds to the hidden selection, and deselect all keeps it', async () => {
        const user = await setup();
        await filterBy(user, 'running');
        await user.click(header());
        await filterBy(user, 'draft');

        await user.click(header());
        expect(toolbar().getByText('5 selected (3 not shown)')).toBeInTheDocument();
        expect(header()).toBeChecked();

        await user.click(header());
        expect(toolbar().getByText('3 selected (3 not shown)')).toBeInTheDocument();
        expect(header()).not.toBeChecked();
      });

      it('drops a stack that a reload no longer returns from the selection', async () => {
        const user = await setup();
        await user.click(screen.getByRole('checkbox', { name: 'Select Run One' }));
        await user.click(screen.getByRole('checkbox', { name: 'Select Run Two' }));
        await filterBy(user, 'draft');
        expect(toolbar().getByText('2 selected (2 not shown)')).toBeInTheDocument();

        // Run One was deleted without an instance.deleted message. A new stack triggers the reload.
        (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue(threeRunningTwoDraft.filter((i) => i.id !== 'r1'));
        act(() => {
          broadcastWs({ type: 'instance.created', payload: { instance_id: 'new-stack' } });
        });

        await waitFor(() => {
          expect(toolbar().getByText('1 selected (1 not shown)')).toBeInTheDocument();
        }, { timeout: 3000 });
      });

      it('sends hidden stacks to the bulk action and lists them in the dialog', async () => {
        const user = await setup();
        (instanceService.bulkStop as ReturnType<typeof vi.fn>).mockResolvedValue({ total: 1, succeeded: 1, failed: 0, results: [] });
        await user.click(screen.getByRole('checkbox', { name: 'Select Run One' }));
        await filterBy(user, 'draft');
        await user.click(toolbar().getByRole('button', { name: 'Stop' }));

        const dialog = within(await screen.findByRole('dialog'));
        expect(dialog.getByText('Run One')).toBeInTheDocument();
        await user.click(dialog.getByRole('button', { name: 'Stop' }));
        await waitFor(() => expect(instanceService.bulkStop).toHaveBeenCalledWith(['r1']));
      });
    });

    it.each([
      [409, 'Too many instances selected', 'Bulk deploy failed (HTTP 409: Too many instances selected)'],
      [500, 'dial tcp 10.0.0.1:3306: refused', 'Bulk deploy failed (HTTP 500: Internal server error)'],
    ])('shows the API message of a failed bulk request: %i (#753)', async (status, message, shown) => {
      const user = userEvent.setup({ delay: null });
      (instanceService.bulkDeploy as ReturnType<typeof vi.fn>).mockRejectedValue({ response: { status, data: { error: message } } });
      renderDashboard();
      await screen.findByText('Instance A');

      await user.click(screen.getByRole('checkbox', { name: 'Select Instance A' }));
      await user.click(screen.getByRole('button', { name: 'Deploy' }));
      const dialog = within(await screen.findByRole('dialog'));
      await user.click(dialog.getByRole('button', { name: 'Deploy' }));

      expect(await screen.findByText(shown)).toBeInTheDocument();
      expect(screen.queryByText(/dial tcp/)).not.toBeInTheDocument();
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

    it('formats cluster capacity in cores and binary units', async () => {
      const { dashboardService } = await import('../../../api/client');
      (dashboardService.getOverview as ReturnType<typeof vi.fn>).mockResolvedValue({
        clusters: [{
          id: 'c1', name: 'prod', health_status: 'healthy', node_count: 3, ready_node_count: 3,
          allocatable_cpu: '191000m', total_cpu: '192000m', allocatable_memory: '740.8Gi', total_memory: '741.1Gi',
        }],
        recent_deployments: [],
        expiring_soon: [],
        failing_instances: [],
      });
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance()]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      expect(await screen.findByText('CPU: 191 allocatable / 192 cores')).toBeInTheDocument();
      expect(screen.getByText('Memory: 741 GiB allocatable / 741 GiB')).toBeInTheDocument();
    });

    it('hides the cluster health widget for role user', async () => {
      authState.user = { id: '2', username: 'alice', role: 'user', display_name: 'Alice' };
      const { dashboardService } = await import('../../../api/client');
      (dashboardService.getOverview as ReturnType<typeof vi.fn>).mockResolvedValue({
        clusters: [{ id: 'c1', name: 'prod', health_status: '' }],
        recent_deployments: [],
        expiring_soon: [],
        failing_instances: [],
      });
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance()]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      expect(await screen.findByText('Recent Deployments')).toBeInTheDocument();
      expect(screen.queryByText('Cluster Health')).not.toBeInTheDocument();
      expect(screen.queryByText('unknown')).not.toBeInTheDocument();
    });
  });

  describe('Filtering and Search', () => {
    it('filters instances by status', async () => {
      const user = userEvent.setup();
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

    it('shows the no-match state with Clear filters when a filter matches nothing', async () => {
      const user = userEvent.setup();
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

      expect(screen.getByText('No stacks match these filters')).toBeInTheDocument();
    });
  });

  describe('Empty results (#753)', () => {
    const renderPage = () => render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    it('offers Clear filters, not create, when a filter matches nothing', async () => {
      const user = userEvent.setup();
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', name: 'Test Instance', status: 'running' }),
      ]);
      renderPage();
      await screen.findByText('Test Instance');

      await user.click(screen.getByRole('button', { name: 'stopped' }));
      await user.type(screen.getByPlaceholderText('Search instances...'), 'zzz');

      expect(screen.getByText('No stacks match these filters')).toBeInTheDocument();
      expect(screen.queryByText('No stack instances found')).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: 'Create your first instance' })).not.toBeInTheDocument();

      await user.click(screen.getByRole('button', { name: 'Clear filters' }));

      expect(screen.getByText('Test Instance')).toBeInTheDocument();
      expect(screen.getByPlaceholderText('Search instances...')).toHaveValue('');
      expect(screen.queryByText('No stacks match these filters')).not.toBeInTheDocument();
    });

    it('keeps the create advice for a list that is really empty', async () => {
      (isSetupWizardDismissed as ReturnType<typeof vi.fn>).mockReturnValue(true);
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      renderPage();

      expect(await screen.findByText('No stack instances found')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Create your first instance' })).toBeInTheDocument();
      expect(screen.queryByText('No stacks match these filters')).not.toBeInTheDocument();
      (isSetupWizardDismissed as ReturnType<typeof vi.fn>).mockReturnValue(false);
    });
  });

  describe('Definition name and expiry (#753)', () => {
    const renderPage = () => render(
      <MemoryRouter>
        <NotificationProvider>
          <Dashboard />
        </NotificationProvider>
      </MemoryRouter>
    );

    it('shows definition_name on cards', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', definition_name: 'Hello Stack' }),
      ]);
      renderPage();

      expect(await screen.findByText('Definition: Hello Stack')).toBeInTheDocument();
    });

    it('shows definition_name in Recent Stacks', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance({ id: '1' })]);
      (instanceService.recent as ReturnType<typeof vi.fn>).mockResolvedValueOnce([
        mockInstance({ id: '2', name: 'Recent One', definition_name: 'Recent Definition', updated_at: '2026-01-01T00:00:00Z' }),
      ]);
      renderPage();

      expect(await screen.findByText('Recent Definition')).toBeInTheDocument();
    });

    it('omits the line when the definition no longer exists', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance({ id: '1' })]);
      renderPage();

      await screen.findByText('Test Instance');
      expect(screen.queryByText(/^Definition:/)).not.toBeInTheDocument();
    });
  });

  describe('Instance Card Details', () => {
    it('shows cluster name on instance card when cluster_id is set', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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

    it('shows the primary ingress URL when one ingress is marked primary', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: '1', name: 'Tools App', status: 'running', namespace: 'stack-tools' }),
      ]);
      (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue({
        namespace: 'stack-tools',
        status: 'healthy',
        ingresses: [
          { url: 'https://tools-db.example.com' },
          { url: 'https://tools.example.com', primary: true },
        ],
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
        expect(screen.getByText('https://tools.example.com')).toBeInTheDocument();
      });
      expect(screen.queryByText('https://tools-db.example.com')).not.toBeInTheDocument();
    });

    it('fetches the URL again and reloads the list when a deploy finishes', async () => {
      const listAll = instanceService.listAll as ReturnType<typeof vi.fn>;
      const getStatus = instanceService.getStatus as ReturnType<typeof vi.fn>;
      listAll.mockResolvedValue([
        mockInstance({ id: 'r1', name: 'Redeploy App', status: 'running', namespace: 'stack-r1' }),
      ]);
      getStatus.mockResolvedValue({
        namespace: 'stack-r1', status: 'healthy',
        ingresses: [{ url: 'https://r1-db.example.com' }], charts: [], pods: [],
      });
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('https://r1-db.example.com')).toBeInTheDocument();
      });
      const listCallsBefore = listAll.mock.calls.length;

      getStatus.mockResolvedValue({
        namespace: 'stack-r1', status: 'healthy',
        ingresses: [{ url: 'https://r1-db.example.com' }, { url: 'https://r1.example.com', primary: true }],
        charts: [], pods: [],
      });
      act(() => {
        broadcastWs({ type: 'deployment.status', payload: { instance_id: 'r1', status: 'deploying' } });
        broadcastWs({ type: 'deployment.status', payload: { instance_id: 'r1', status: 'running' } });
      });

      await waitFor(() => {
        expect(screen.getByText('https://r1.example.com')).toBeInTheDocument();
      }, { timeout: 3000 });
      await waitFor(() => {
        expect(listAll.mock.calls.length).toBe(listCallsBefore + 1);
      }, { timeout: 3000 });
    });

    it.each([
      ['healthy', 'CheckCircleIcon'],
      ['progressing', 'AutorenewIcon'],
      ['degraded', 'WarningAmberIcon'],
      ['error', 'CancelIcon'],
      ['not_found', 'HelpOutlineOutlinedIcon'],
    ])('shows pod health %s with its own shape and a name', async (status, iconTestId) => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
        mockInstance({ id: 'inst-h', name: 'Health App', status: 'running', namespace: 'stack-h' }),
      ]);
      (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue({
        namespace: 'stack-h',
        status,
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
      const icon = await screen.findByRole('img', { name: `Pod health: ${status}` });
      // Colour alone must not tell the states apart: each has its own icon.
      expect(icon).toHaveAttribute('data-testid', iconTestId);
    });

    // The list loads twice under StrictMode, and a WebSocket event can reload it at
    // any time. A new list while a status request runs must not lose its answer.
    it('keeps the pod health when the list changes while the status is loading', async () => {
      const list = () => [mockInstance({ id: 'inst-h', name: 'Health App', status: 'running', namespace: 'stack-h' })];
      const answers: Array<(value: unknown) => void> = [];
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockImplementation(
        () => new Promise((resolve) => { answers.push(resolve); }),
      );
      let answerStatus: (value: unknown) => void = () => undefined;
      (instanceService.getStatus as ReturnType<typeof vi.fn>).mockReturnValue(new Promise((resolve) => { answerStatus = resolve; }));
      render(
        <StrictMode>
          <MemoryRouter>
            <NotificationProvider>
              <Dashboard />
            </NotificationProvider>
          </MemoryRouter>
        </StrictMode>
      );
      await waitFor(() => expect(answers).toHaveLength(2));
      // The first list starts the status request.
      await act(async () => { answers[0](list()); });
      await waitFor(() => expect(instanceService.getStatus).toHaveBeenCalledTimes(1));
      // The second list arrives while that request runs.
      await act(async () => { answers[1](list()); });
      await screen.findAllByText('Health App');
      await act(async () => {
        answerStatus({ namespace: 'stack-h', status: 'degraded', charts: [], last_checked: '2026-01-01T00:00:00Z' });
      });
      expect(await screen.findByRole('img', { name: 'Pod health: degraded' })).toBeInTheDocument();
    });

    it('shows pod health when status is fetched for running instance', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
        expect(screen.getByRole('img', { name: /pod health: healthy/i })).toBeInTheDocument();
      });
    });

    it('navigates to detail page when Details button is clicked', async () => {
      // This test verifies the Details button renders on cards
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
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
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Welcome to K8s Stack Manager')).toBeInTheDocument();
      });
    });

    it('does not show wizard when all resources exist', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance()]);
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
      expect(screen.queryByText('Welcome to K8s Stack Manager')).not.toBeInTheDocument();
    });

    it('shows wizard at step 2 when clusters exist but no templates', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (templateService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Welcome to K8s Stack Manager')).toBeInTheDocument();
      });
      expect(screen.getByRole('button', { name: 'Create a Template' })).toBeInTheDocument();
    });

    it('shows wizard at step 3 when a cluster and a template exist but no instance', async () => {
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Welcome to K8s Stack Manager')).toBeInTheDocument();
      });
      expect(screen.getByText('Step 3 of 3')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Deploy an Instance' })).toBeInTheDocument();
      expect(screen.queryByRole('heading', { level: 1, name: 'Stack Instances' })).not.toBeInTheDocument();
    });

    it('does not show wizard at step 3 when dismissed', async () => {
      (isSetupWizardDismissed as ReturnType<typeof vi.fn>).mockReturnValue(true);
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('No stack instances found')).toBeInTheDocument();
      });
      expect(screen.queryByText('Welcome to K8s Stack Manager')).not.toBeInTheDocument();
    });

    it('does not show wizard when a search matches no instance', async () => {
      const user = userEvent.setup();
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance()]);
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
      await user.type(screen.getByPlaceholderText('Search instances...'), 'no-match-xyz');
      await waitFor(() => {
        expect(screen.getByText('No stacks match these filters')).toBeInTheDocument();
      });
      expect(screen.queryByText('Welcome to K8s Stack Manager')).not.toBeInTheDocument();
    });

    it('does not show wizard when a status filter matches no instance', async () => {
      const user = userEvent.setup();
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([mockInstance({ status: 'running' })]);
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
      await user.click(screen.getByRole('button', { name: 'stopped' }));
      await waitFor(() => {
        expect(screen.getByText('No stacks match these filters')).toBeInTheDocument();
      });
      expect(screen.queryByText('Welcome to K8s Stack Manager')).not.toBeInTheDocument();
    });

    it('does not show wizard when dismissed', async () => {
      (isSetupWizardDismissed as ReturnType<typeof vi.fn>).mockReturnValue(true);
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('No stack instances found')).toBeInTheDocument();
      });
      expect(screen.queryByText('Welcome to K8s Stack Manager')).not.toBeInTheDocument();
    });

    it('hides wizard and shows dashboard when skip is clicked', async () => {
      const user = userEvent.setup();
      (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
      render(
        <MemoryRouter>
          <NotificationProvider>
            <Dashboard />
          </NotificationProvider>
        </MemoryRouter>
      );
      await waitFor(() => {
        expect(screen.getByText('Welcome to K8s Stack Manager')).toBeInTheDocument();
      });
      await user.click(screen.getByText('Skip setup'));
      await waitFor(() => {
        expect(screen.queryByText('Welcome to K8s Stack Manager')).not.toBeInTheDocument();
      });
    });
  });
});
