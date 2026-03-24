import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
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
});
