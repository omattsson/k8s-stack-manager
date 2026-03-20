import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import OrphanedNamespaces from '../index';

vi.mock('../../../../api/client', () => ({
  adminService: {
    listOrphanedNamespaces: vi.fn(),
    deleteOrphanedNamespace: vi.fn(),
  },
}));

vi.mock('../../../../context/AuthContext', () => ({
  useAuth: vi.fn(),
}));

import { adminService } from '../../../../api/client';
import { useAuth } from '../../../../context/AuthContext';

const adminUser = {
  id: '1',
  username: 'admin',
  role: 'admin',
  display_name: 'Admin User',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const mockOrphaned = [
  {
    name: 'stack-orphan-bob',
    created_at: '2026-01-15T10:00:00Z',
    phase: 'Active',
    resource_counts: { pods: 2, deployments: 1, services: 1 },
    helm_releases: ['nginx', 'redis'],
  },
  {
    name: 'stack-old-alice',
    created_at: '2025-12-01T08:00:00Z',
    phase: 'Active',
    resource_counts: { pods: 0, deployments: 0, services: 0 },
    helm_releases: [],
  },
];

describe('OrphanedNamespaces Page', () => {
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
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockReturnValue(
      new Promise(() => {})
    );

    render(
      <MemoryRouter>
        <OrphanedNamespaces />
      </MemoryRouter>
    );

    expect(screen.getByRole('progressbar')).toBeTruthy();
  });

  it('renders orphaned namespaces in a table', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue(mockOrphaned);

    render(
      <MemoryRouter>
        <OrphanedNamespaces />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('stack-orphan-bob')).toBeTruthy();
    });

    expect(screen.getByText('stack-old-alice')).toBeTruthy();
    expect(screen.getByText('nginx')).toBeTruthy();
    expect(screen.getByText('redis')).toBeTruthy();
  });

  it('shows empty state when no orphaned namespaces exist', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter>
        <OrphanedNamespaces />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/No orphaned namespaces found/)).toBeTruthy();
    });
  });

  it('shows error state when fetch fails', async () => {
    (adminService.listOrphanedNamespaces as ReturnType<typeof vi.fn>).mockRejectedValue(
      new Error('Network error')
    );

    render(
      <MemoryRouter>
        <OrphanedNamespaces />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Failed to load orphaned namespaces')).toBeTruthy();
    });
  });

  it('shows access denied for non-admin users', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: { ...adminUser, role: 'user' },
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <OrphanedNamespaces />
      </MemoryRouter>
    );

    expect(screen.getByText(/Admin role required/)).toBeTruthy();
  });
});
