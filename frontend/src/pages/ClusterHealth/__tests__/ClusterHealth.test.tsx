import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import ClusterHealth from '../index';

vi.mock('../../../api/client', () => ({
  clusterService: {
    list: vi.fn(),
    getHealthSummary: vi.fn(),
    getNodes: vi.fn(),
    getNamespaces: vi.fn(),
    getUtilization: vi.fn(),
  },
}));

import { clusterService } from '../../../api/client';

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
    max_instances_per_user: 0,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
];

const mockSummary = {
  node_count: 3,
  ready_node_count: 3,
  total_cpu: '8000m',
  total_memory: '31.4Gi',
  allocatable_cpu: '7200m',
  allocatable_memory: '28.5Gi',
  namespace_count: 12,
};

const mockNodes = [
  {
    name: 'node-1',
    status: 'Ready',
    conditions: [{ type: 'Ready', status: 'True' }],
    capacity: { cpu: '4000m', memory: '16Gi', pods: '110' },
    allocatable: { cpu: '3600m', memory: '14.5Gi', pods: '110' },
    pod_count: 25,
  },
];

const mockNamespaces = [
  { name: 'default', phase: 'Active', created_at: '2026-01-01T00:00:00Z' },
  { name: 'kube-system', phase: 'Active', created_at: '2026-01-01T00:00:00Z' },
];

const mockUtilization = {
  namespaces: [
    {
      namespace: 'stack-myapp-dev',
      cpu_used: '200m',
      cpu_limit: '2000m',
      memory_used: '128Mi',
      memory_limit: '1Gi',
      pod_count: 5,
      pod_limit: 50,
    },
    {
      namespace: 'stack-api-staging',
      cpu_used: '1800m',
      cpu_limit: '2000m',
      memory_used: '900Mi',
      memory_limit: '1Gi',
      pod_count: 48,
      pod_limit: 50,
    },
  ],
};

describe('ClusterHealth Page', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders loading spinner initially', () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));

    render(
      <MemoryRouter>
        <ClusterHealth />
      </MemoryRouter>,
    );

    expect(screen.getByText('Cluster Health')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('renders health data after loading', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.getHealthSummary as ReturnType<typeof vi.fn>).mockResolvedValue(mockSummary);
    (clusterService.getNodes as ReturnType<typeof vi.fn>).mockResolvedValue(mockNodes);
    (clusterService.getNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockNamespaces);
    (clusterService.getUtilization as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [] });

    render(
      <MemoryRouter>
        <ClusterHealth />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('3 / 3')).toBeInTheDocument();
    });

    expect(screen.getByText('node-1')).toBeInTheDocument();
    expect(screen.getByText('default')).toBeInTheDocument();
    expect(screen.getByText('kube-system')).toBeInTheDocument();
    expect(screen.getByText('12')).toBeInTheDocument();
  });

  it('renders namespace resource utilization table', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.getHealthSummary as ReturnType<typeof vi.fn>).mockResolvedValue(mockSummary);
    (clusterService.getNodes as ReturnType<typeof vi.fn>).mockResolvedValue(mockNodes);
    (clusterService.getNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockNamespaces);
    (clusterService.getUtilization as ReturnType<typeof vi.fn>).mockResolvedValue(mockUtilization);

    render(
      <MemoryRouter>
        <ClusterHealth />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Namespace Resource Usage')).toBeInTheDocument();
    });

    expect(screen.getByText('stack-myapp-dev')).toBeInTheDocument();
    expect(screen.getByText('stack-api-staging')).toBeInTheDocument();
    // Check CPU usage text is rendered
    expect(screen.getByText('0.2 / 2 cores (10%)')).toBeInTheDocument();
    expect(screen.getByText('1.8 / 2 cores (90%)')).toBeInTheDocument();
    expect(screen.getByText('128 MiB / 1 GiB (13%)')).toBeInTheDocument();
    // Check pod counts
    expect(screen.getByText('5 / 50')).toBeInTheDocument();
    expect(screen.getByText('48 / 50')).toBeInTheDocument();
  });

  it('renders error state', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));

    render(
      <MemoryRouter>
        <ClusterHealth />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Failed to load clusters')).toBeInTheDocument();
    });
  });

  it('renders info message when no clusters exist', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <ClusterHealth />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('No clusters registered. Add a cluster first.')).toBeInTheDocument();
    });
  });

  it('calculates percentages across units and shows readable memory', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.getHealthSummary as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockSummary,
      total_cpu: '192000m',
      allocatable_cpu: '192000m',
      total_memory: '741.1Gi',
      allocatable_memory: '740.8Gi',
      requested_cpu: '19200m',
      requested_memory: '74.1Gi',
    });
    (clusterService.getNodes as ReturnType<typeof vi.fn>).mockResolvedValue([
      { ...mockNodes[0], capacity: { cpu: '64', memory: '259033492Ki', pods: '110' } },
    ]);
    (clusterService.getNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockNamespaces);
    (clusterService.getUtilization as ReturnType<typeof vi.fn>).mockResolvedValue({
      namespaces: [
        {
          namespace: 'stack-big-quota',
          cpu_used: '1620m',
          cpu_limit: '16',
          memory_used: '2560Mi',
          memory_limit: '24Gi',
          pod_count: 3,
          pod_limit: 0,
        },
      ],
    });

    render(
      <MemoryRouter>
        <ClusterHealth />
      </MemoryRouter>,
    );

    expect(await screen.findByText('1.62 / 16 cores (10%)')).toBeInTheDocument();
    expect(screen.getByText('2.5 GiB / 24 GiB (10%)')).toBeInTheDocument();
    // Node capacity in readable units, not raw Ki.
    expect(screen.getByText('64 cores')).toBeInTheDocument();
    expect(screen.getByText('247 GiB')).toBeInTheDocument();
    expect(screen.queryByText('259033492Ki')).not.toBeInTheDocument();
    // Summary shows requests against allocatable, not allocatable against capacity.
    expect(screen.getByText('Requested 10% of allocatable. Capacity 192 cores.')).toBeInTheDocument();
    expect(screen.getByText('19.2 cores / 192 cores')).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: 'CPU requested 10% of allocatable' })).toHaveAttribute('aria-valuenow', '10');
    expect(screen.getByRole('progressbar', { name: 'Memory requested 10% of allocatable' })).toHaveAttribute('aria-valuenow', '10');
  });

  it('shows allocatable without a bar when the API sends no requests', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (clusterService.getHealthSummary as ReturnType<typeof vi.fn>).mockResolvedValue(mockSummary);
    (clusterService.getNodes as ReturnType<typeof vi.fn>).mockResolvedValue(mockNodes);
    (clusterService.getNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockNamespaces);
    (clusterService.getUtilization as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [] });

    render(
      <MemoryRouter>
        <ClusterHealth />
      </MemoryRouter>,
    );

    expect(await screen.findByText('7.2 cores')).toBeInTheDocument();
    expect(screen.getByText('Allocatable. Capacity 8 cores.')).toBeInTheDocument();
    expect(screen.getByText('CPU', { exact: true })).toBeInTheDocument();
    expect(screen.queryByRole('progressbar', { name: /requested/ })).not.toBeInTheDocument();
  });
});
