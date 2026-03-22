import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor, within, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import SharedValuesPage from '../index';
import { NotificationProvider } from '../../../context/NotificationContext';

vi.mock('../../../api/client', () => ({
  clusterService: {
    list: vi.fn(),
  },
  sharedValuesService: {
    list: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
  },
}));

import { clusterService, sharedValuesService } from '../../../api/client';

const mockClusters = [
  {
    id: 'c1',
    name: 'production',
    description: 'Production cluster',
    api_server_url: 'https://prod.example.com:6443',
    region: 'westeurope',
    health_status: 'healthy' as const,
    is_default: true,
    max_namespaces: 50,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
];

const mockSharedValues = [
  {
    id: 'sv1',
    cluster_id: 'c1',
    name: 'Base Config',
    description: 'Base configuration values',
    values: 'global:\n  env: production',
    priority: 0,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-15T00:00:00Z',
  },
  {
    id: 'sv2',
    cluster_id: 'c1',
    name: 'Monitoring Override',
    description: 'Monitoring settings',
    values: 'monitoring:\n  enabled: true',
    priority: 10,
    created_at: '2026-01-02T00:00:00Z',
    updated_at: '2026-01-16T00:00:00Z',
  },
];

describe('SharedValues Page', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders loading spinner initially', () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));

    render(
      <MemoryRouter>
        <NotificationProvider>
          <SharedValuesPage />
        </NotificationProvider>
      </MemoryRouter>,
    );

    expect(screen.getByRole('heading', { name: 'Shared Values' })).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('renders table with shared values on success', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (sharedValuesService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockSharedValues);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <SharedValuesPage />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Base Config')).toBeInTheDocument();
    });

    expect(screen.getByText('Monitoring Override')).toBeInTheDocument();
    expect(screen.getByText('Base configuration values')).toBeInTheDocument();
    expect(screen.getByText('Monitoring settings')).toBeInTheDocument();

    // Verify priority column values
    const rows = screen.getAllByRole('row');
    // Header row + 2 data rows
    expect(rows).toHaveLength(3);

    // Check priority ordering (0 before 10)
    const firstDataRow = rows[1];
    expect(within(firstDataRow).getByText('0')).toBeInTheDocument();
    expect(within(firstDataRow).getByText('Base Config')).toBeInTheDocument();
  });

  it('opens create dialog and submits', async () => {
    const user = userEvent.setup();
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockClusters);
    (sharedValuesService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    (sharedValuesService.create as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'sv-new',
      cluster_id: 'c1',
      name: 'New Config',
      description: 'New desc',
      values: 'key: value',
      priority: 5,
      created_at: '2026-03-21T00:00:00Z',
      updated_at: '2026-03-21T00:00:00Z',
    });

    render(
      <MemoryRouter>
        <NotificationProvider>
          <SharedValuesPage />
        </NotificationProvider>
      </MemoryRouter>,
    );

    // Wait for loading to finish
    await waitFor(() => {
      expect(screen.getByText('No shared values configured for this cluster.')).toBeInTheDocument();
    });

    // Click Add button
    await user.click(screen.getByRole('button', { name: /add shared values/i }));

    // Dialog should be open
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    // Fill in the form using fireEvent.change (faster than userEvent.type for long strings)
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'New Config' } });
    fireEvent.change(screen.getByLabelText(/Description/), { target: { value: 'New desc' } });
    fireEvent.change(screen.getByLabelText(/Priority/), { target: { value: '5' } });
    fireEvent.change(screen.getByLabelText(/Values \(YAML\)/), { target: { value: 'key: value' } });

    // After filling, re-mock list to return the new item
    (sharedValuesService.list as ReturnType<typeof vi.fn>).mockResolvedValue([{
      id: 'sv-new',
      cluster_id: 'c1',
      name: 'New Config',
      description: 'New desc',
      values: 'key: value',
      priority: 5,
      created_at: '2026-03-21T00:00:00Z',
      updated_at: '2026-03-21T00:00:00Z',
    }]);

    // Click Save
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() => {
      expect(sharedValuesService.create).toHaveBeenCalledWith('c1', {
        name: 'New Config',
        description: 'New desc',
        priority: 5,
        values: 'key: value',
      });
    });
  });

  it('shows error alert on API failure', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));

    render(
      <MemoryRouter>
        <NotificationProvider>
          <SharedValuesPage />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('Failed to load clusters')).toBeInTheDocument();
    });
  });

  it('shows info message when no clusters exist', async () => {
    (clusterService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <NotificationProvider>
          <SharedValuesPage />
        </NotificationProvider>
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByText('No clusters registered. Add a cluster first.')).toBeInTheDocument();
    });
  });
});
