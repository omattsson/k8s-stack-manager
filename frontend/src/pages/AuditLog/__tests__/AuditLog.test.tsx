import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import AuditLog from '../index';

vi.mock('../../../api/client', () => ({
  auditService: {
    list: vi.fn(),
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

import { auditService } from '../../../api/client';

describe('Audit Log Page', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner initially', () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays audit log entries', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', user_id: '1', username: 'admin', action: 'create', entity_type: 'stack_instance', entity_id: '123', details: 'Created instance', timestamp: '2024-01-01T00:00:00Z' },
    ]);
    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('admin')).toBeInTheDocument();
      expect(screen.getByText('create')).toBeInTheDocument();
    });
  });

  it('shows error on failure', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('error'));
    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  it('shows empty state when no entries', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no audit log entries found/i)).toBeInTheDocument();
    });
  });
});
