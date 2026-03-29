import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import CleanupPolicies from '../index';
import { NotificationProvider } from '../../../context/NotificationContext';

vi.mock('../../../api/client', () => ({
  cleanupPolicyService: {
    list: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    run: vi.fn(),
  },
  clusterService: {
    list: vi.fn(),
  },
}));

import { cleanupPolicyService, clusterService } from '../../../api/client';

const mockClusters = [
  {
    id: 'c1',
    name: 'production',
    description: 'Prod cluster',
    api_server_url: 'https://prod:6443',
    region: 'westeurope',
    health_status: 'healthy' as const,
    is_default: true,
    max_namespaces: 50,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
];

const mockPolicies = [
  {
    id: 'p1',
    name: 'Idle Cleanup',
    cluster_id: 'all',
    action: 'stop',
    condition: 'idle_days:7',
    schedule: '0 2 * * *',
    enabled: true,
    dry_run: false,
    last_run_at: '2026-03-20T02:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-03-20T02:00:00Z',
  },
  {
    id: 'p2',
    name: 'TTL Enforcer',
    cluster_id: 'c1',
    action: 'clean',
    condition: 'ttl_expired',
    schedule: '*/30 * * * *',
    enabled: false,
    dry_run: true,
    last_run_at: null,
    created_at: '2026-02-01T00:00:00Z',
    updated_at: '2026-02-01T00:00:00Z',
  },
];

describe('CleanupPolicies Page', () => {
  beforeEach(() => {
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockPolicies);
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading state', () => {
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    (clusterService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    expect(screen.getByRole('heading', { name: 'Cleanup Policies' })).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('renders policy table with data', async () => {
    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    expect(screen.getByText('TTL Enforcer')).toBeInTheDocument();
    expect(screen.getByText('All Clusters')).toBeInTheDocument();
    expect(screen.getByText('production')).toBeInTheDocument();
    expect(screen.getByText('Idle > 7 days')).toBeInTheDocument();
    expect(screen.getByText('TTL expired')).toBeInTheDocument();
    expect(screen.getByText('stop')).toBeInTheDocument();
    expect(screen.getByText('clean')).toBeInTheDocument();
    expect(screen.getByText('Never')).toBeInTheDocument();
  });

  it('opens create dialog', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /add policy/i }));

    expect(screen.getByText('Create Cleanup Policy')).toBeInTheDocument();
    expect(screen.getByLabelText(/^Name/)).toBeInTheDocument();
  });

  it('creates a new policy via the dialog', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (cleanupPolicyService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'p3', name: 'New Policy', cluster_id: 'all', action: 'stop',
      condition: 'idle_days:3', schedule: '0 0 * * *', enabled: true, dry_run: false,
      last_run_at: null, created_at: '', updated_at: '',
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /add policy/i }));
    expect(screen.getByText('Create Cleanup Policy')).toBeInTheDocument();

    // Fill in name
    const nameInput = screen.getByLabelText(/^Name/);
    await user.type(nameInput, 'New Policy');

    // Fill in schedule
    const scheduleInput = screen.getByLabelText(/Schedule \(Cron\)/i);
    await user.clear(scheduleInput);
    await user.type(scheduleInput, '0 0 * * *');

    // Click create
    await user.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => {
      expect(cleanupPolicyService.create).toHaveBeenCalled();
    });
  });

  it('opens edit dialog with pre-populated data', async () => {
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /edit idle cleanup/i }));

    expect(screen.getByText('Edit Policy')).toBeInTheDocument();
    expect(screen.getByDisplayValue('Idle Cleanup')).toBeInTheDocument();
    expect(screen.getByDisplayValue('0 2 * * *')).toBeInTheDocument();
  });

  it('updates edited policy', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.update as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockPolicies[0], name: 'Updated Cleanup',
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /edit idle cleanup/i }));

    const nameInput = screen.getByDisplayValue('Idle Cleanup');
    await user.clear(nameInput);
    await user.type(nameInput, 'Updated Cleanup');

    await user.click(screen.getByRole('button', { name: /^update$/i }));

    await waitFor(() => {
      expect(cleanupPolicyService.update).toHaveBeenCalledWith('p1', expect.objectContaining({
        name: 'Updated Cleanup',
      }));
    });
  });

  it('toggles policy enabled state', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.update as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockPolicies[0], enabled: false,
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    // Find the enabled toggle for Idle Cleanup (first switch)
    const switches = screen.getAllByRole('checkbox');
    const idleSwitch = switches.find((_s, i) => i === 0);
    expect(idleSwitch).toBeDefined();
    await user.click(idleSwitch!);

    await waitFor(() => {
      expect(cleanupPolicyService.update).toHaveBeenCalledWith('p1', expect.objectContaining({
        enabled: false,
      }));
    });
  });

  it('shows delete confirmation and deletes policy', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.delete as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /delete idle cleanup/i }));

    // Confirmation dialog
    await waitFor(() => {
      expect(screen.getByText(/delete policy/i)).toBeInTheDocument();
    });
    expect(screen.getByText(/idle cleanup/i, { selector: 'p strong, p' })).toBeTruthy();

    await user.click(screen.getByRole('button', { name: /^delete$/i }));

    await waitFor(() => {
      expect(cleanupPolicyService.delete).toHaveBeenCalledWith('p1');
    });
  });

  it('shows run results dialog on Run Now', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.run as ReturnType<typeof vi.fn>).mockResolvedValue([
      {
        instance_id: 'i1',
        instance_name: 'my-stack',
        namespace: 'stack-my-stack-user1',
        action: 'stop',
        status: 'dry_run',
      },
    ]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /run idle cleanup/i }));

    expect(screen.getByText(/Run Policy: Idle Cleanup/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /dry run/i }));

    await waitFor(() => {
      expect(screen.getByText('my-stack')).toBeInTheDocument();
    });

    expect(screen.getByText('stack-my-stack-user1')).toBeInTheDocument();
    expect(screen.getByText('dry_run')).toBeInTheDocument();
    expect(cleanupPolicyService.run).toHaveBeenCalledWith('p1', true);
  });

  it('runs policy with Execute button (non-dry-run)', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.run as ReturnType<typeof vi.fn>).mockResolvedValue([
      {
        instance_id: 'i1',
        instance_name: 'my-stack',
        namespace: 'stack-my-stack-user1',
        action: 'stop',
        status: 'success',
      },
    ]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /run idle cleanup/i }));
    await user.click(screen.getByRole('button', { name: /live run/i }));

    await waitFor(() => {
      expect(cleanupPolicyService.run).toHaveBeenCalledWith('p1', false);
    });
  });

  it('shows run error when run fails', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.run as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Run failed'));

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /run idle cleanup/i }));
    await user.click(screen.getByRole('button', { name: /dry run/i }));

    await waitFor(() => {
      expect(screen.getByText(/failed to run/i)).toBeInTheDocument();
    });
  });

  it('shows error state', async () => {
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
    (clusterService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Failed to load cleanup policies')).toBeInTheDocument();
    });
  });

  it('shows empty state when no policies exist', async () => {
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    expect(screen.getByText(/no cleanup policies/i)).toBeInTheDocument();
  });

  it('displays cron schedule description', async () => {
    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Idle Cleanup')).toBeInTheDocument();
    });

    // describeCron('0 2 * * *') → 'Daily at 2:00' rendered in Typography with cron in tooltip
    expect(screen.getByText('Daily at 2:00')).toBeInTheDocument();
    // describeCron('*/30 * * * *') → 'Daily at *:*/30' (dom/mon/dow all *)
    expect(screen.getByText('Daily at *:*/30')).toBeInTheDocument();
  });

  it('shows stopped condition display', async () => {
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      {
        ...mockPolicies[0],
        condition: 'status:stopped,age_days:14',
      },
    ]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Stopped, Age > 14 days')).toBeInTheDocument();
    });
  });

  it('closes create dialog on cancel', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /add policy/i }));
    expect(screen.getByText('Create Cleanup Policy')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /cancel/i }));

    await waitFor(() => {
      expect(screen.queryByText('Create Cleanup Policy')).not.toBeInTheDocument();
    });
  });

  it('validates name is required on save', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <CleanupPolicies />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /add policy/i }));

    // Try to create without name
    await user.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => {
      expect(screen.getByText(/name is required/i)).toBeInTheDocument();
    });
  });
});
