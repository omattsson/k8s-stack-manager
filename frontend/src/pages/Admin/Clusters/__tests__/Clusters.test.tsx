import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import Clusters from '../index';
import { NotificationProvider } from '../../../../context/NotificationContext';

vi.mock('../../../../api/client', () => ({
  clusterService: {
    list: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    testConnection: vi.fn(),
    setDefault: vi.fn(),
    getQuotas: vi.fn(),
    updateQuotas: vi.fn(),
    deleteQuotas: vi.fn(),
  },
}));

vi.mock('../../../../context/AuthContext', () => ({
  useAuth: vi.fn(),
}));

import { clusterService } from '../../../../api/client';
import { useAuth } from '../../../../context/AuthContext';

const adminUser = {
  id: '1',
  username: 'admin',
  role: 'admin',
  display_name: 'Admin User',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const mockClusters = [
  {
    id: 'c1',
    name: 'production',
    description: 'Production cluster',
    api_server_url: 'https://prod.example.com:6443',
    region: 'westeurope',
    health_status: 'healthy',
    is_default: true,
    max_namespaces: 50,
    max_instances_per_user: 10,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'c2',
    name: 'staging',
    description: '',
    api_server_url: 'https://staging.example.com:6443',
    region: '',
    health_status: 'unreachable',
    is_default: false,
    max_namespaces: 0,
    max_instances_per_user: 0,
    created_at: '2026-01-02T00:00:00Z',
    updated_at: '2026-01-02T00:00:00Z',
  },
];

describe('Clusters Page', () => {
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
    (clusterService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    expect(screen.getByRole('progressbar')).toBeTruthy();
  });

  it('renders error alert on fetch failure', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('fetch failed'));

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeTruthy();
    });
  });

  it('renders empty state when no clusters exist', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/no clusters configured/i)).toBeTruthy();
    });
  });

  it('renders clusters in a table', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('production')).toBeTruthy();
      expect(screen.getByText('staging')).toBeTruthy();
    });

    expect(screen.getByText('https://prod.example.com:6443')).toBeTruthy();
    expect(screen.getByText('westeurope')).toBeTruthy();
  });

  it('shows default chip for the default cluster', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      // The table header also contains "Default", so find the Chip specifically.
      const chips = screen.getAllByText('Default');
      expect(chips.length).toBeGreaterThanOrEqual(2); // header + chip
    });
  });

  it('has accessible action buttons including resource quotas', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByLabelText('Resource quotas for production')).toBeTruthy();
      expect(screen.getByLabelText('Resource quotas for staging')).toBeTruthy();
      expect(screen.getByLabelText('Test connection for production')).toBeTruthy();
      expect(screen.getByLabelText('Edit production')).toBeTruthy();
      expect(screen.getByLabelText('Delete production')).toBeTruthy();
      expect(screen.getByLabelText('Set staging as default')).toBeTruthy();
    });
  });

  it('opens create dialog when Add Cluster button is clicked', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('production')).toBeTruthy();
    });

    await user.click(screen.getByRole('button', { name: /add cluster/i }));

    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeTruthy();
      expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: /^name/i })).toBeTruthy();
    });

    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByRole('textbox', { name: /description/i })).toBeTruthy();
    expect(within(dialog).getByRole('textbox', { name: /api server url/i })).toBeTruthy();
  });

  it('opens edit dialog pre-filled with cluster data', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('production')).toBeTruthy();
    });

    await user.click(screen.getByLabelText('Edit production'));

    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeTruthy();
    });

    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByRole('textbox', { name: /^name/i })).toHaveValue('production');
    expect(within(dialog).getByRole('textbox', { name: /description/i })).toHaveValue('Production cluster');
  });

  it('creates a cluster and refreshes list', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'c3',
      name: 'dev-cluster',
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('production')).toBeTruthy();
    });

    await user.click(screen.getByRole('button', { name: /add cluster/i }));

    await waitFor(() => {
      expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: /^name/i })).toBeTruthy();
    });

    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByRole('textbox', { name: /^name/i }), 'dev-cluster');
    await user.type(within(dialog).getByRole('textbox', { name: /api server url/i }), 'https://dev.example.com');
    await user.type(within(dialog).getByRole('textbox', { name: /kubeconfig data/i }), 'apiVersion: v1');

    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      ...mockClusters,
      { id: 'c3', name: 'dev-cluster' },
    ]);

    await user.click(within(dialog).getByRole('button', { name: /create/i }));

    await waitFor(() => {
      expect(clusterService.create).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'dev-cluster', api_server_url: 'https://dev.example.com' })
      );
    });
  });

  it('shows dialog error when create fails', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('production')).toBeTruthy();
    });

    await user.click(screen.getByRole('button', { name: /add cluster/i }));

    await waitFor(() => {
      expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: /^name/i })).toBeTruthy();
    });

    const dialog = screen.getByRole('dialog');
    await user.type(within(dialog).getByRole('textbox', { name: /^name/i }), 'production');
    await user.type(within(dialog).getByRole('textbox', { name: /api server url/i }), 'https://x.example.com');

    // No kubeconfig data or path provided — triggers client-side validation
    await user.click(within(dialog).getByRole('button', { name: /create/i }));

    await waitFor(() => {
      expect(screen.getByText(/kubeconfig data or kubeconfig path is required/i)).toBeTruthy();
    });
  });

  it('opens delete confirmation and deletes cluster', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.delete as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('staging')).toBeTruthy();
    });

    await user.click(screen.getByLabelText('Delete staging'));

    await waitFor(() => {
      expect(screen.getByText(/are you sure/i)).toBeTruthy();
    });

    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([mockClusters[0]]);

    const dialog = screen.getByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: /delete/i }));

    await waitFor(() => {
      expect(clusterService.delete).toHaveBeenCalledWith('c2');
    });
  });

  it('tests cluster connection and shows success notification', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.testConnection as ReturnType<typeof vi.fn>).mockResolvedValue({
      success: true,
      message: 'Connection successful',
      server_version: 'v1.28.0',
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('production')).toBeTruthy();
    });

    await user.click(screen.getByLabelText('Test connection for production'));

    await waitFor(() => {
      expect(clusterService.testConnection).toHaveBeenCalledWith('c1');
    });
  });

  it('sets a cluster as default', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.setDefault as ReturnType<typeof vi.fn>).mockResolvedValue({ message: 'ok' });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('staging')).toBeTruthy();
    });

    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    await user.click(screen.getByLabelText('Set staging as default'));

    await waitFor(() => {
      expect(clusterService.setDefault).toHaveBeenCalledWith('c2');
    });
  });

  it('updates an existing cluster via edit dialog', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.update as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockClusters[0],
      description: 'Updated desc',
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <Clusters />
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('production')).toBeTruthy();
    });

    await user.click(screen.getByLabelText('Edit production'));

    await waitFor(() => {
      expect(screen.getByRole('dialog')).toBeTruthy();
    });

    const dialog = screen.getByRole('dialog');
    const descField = within(dialog).getByRole('textbox', { name: /description/i });
    await user.clear(descField);
    await user.type(descField, 'Updated desc');

    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);

    await user.click(within(dialog).getByRole('button', { name: /update/i }));

    await waitFor(() => {
      expect(clusterService.update).toHaveBeenCalledWith(
        'c1',
        expect.objectContaining({ description: 'Updated desc' })
      );
    });
  });
});
