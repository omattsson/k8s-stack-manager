import { describe, it, expect, vi, afterEach } from 'vitest';
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
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockPolicies);
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

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
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

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

  it('shows run results dialog on Run Now', async () => {
    const user = userEvent.setup();
    (cleanupPolicyService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockPolicies);
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
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
});
