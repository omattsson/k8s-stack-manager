import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import AuditLog from '../index';

vi.mock('../../../api/client', () => ({
  auditService: {
    list: vi.fn(),
    export: vi.fn(),
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

const mockAuditData = {
  data: [
    { id: '1', user_id: '1', username: 'admin', action: 'create', entity_type: 'stack_instance', entity_id: '123', details: 'Created instance', timestamp: '2024-01-01T00:00:00Z' },
    { id: '2', user_id: '1', username: 'admin', action: 'delete', entity_type: 'stack_template', entity_id: '456', details: 'Deleted template', timestamp: '2024-01-02T00:00:00Z' },
  ],
  total: 50,
  limit: 25,
  offset: 0,
};

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
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      data: [
        { id: '1', user_id: '1', username: 'admin', action: 'create', entity_type: 'stack_instance', entity_id: '123', details: 'Created instance', timestamp: '2024-01-01T00:00:00Z' },
      ],
      total: 1,
      limit: 25,
      offset: 0,
    });
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
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      data: [],
      total: 0,
      limit: 25,
      offset: 0,
    });
    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no audit log entries found/i)).toBeInTheDocument();
    });
  });

  it('renders filter controls', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockAuditData);

    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    expect(screen.getByLabelText('User ID')).toBeInTheDocument();
    expect(screen.getByLabelText('Entity Type')).toBeInTheDocument();
    expect(screen.getByLabelText('Action')).toBeInTheDocument();
    expect(screen.getByLabelText('From')).toBeInTheDocument();
    expect(screen.getByLabelText('To')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /filter/i })).toBeInTheDocument();
  });

  it('handles pagination display', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockAuditData);

    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    // Pagination should be rendered
    expect(screen.getByRole('combobox', { name: /rows per page/i })).toBeInTheDocument();
  });

  it('applies quick date chip filter', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockAuditData);
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    await user.click(screen.getByText('Last 7 days'));

    await waitFor(() => {
      expect(auditService.list).toHaveBeenCalledWith(
        expect.objectContaining({
          start_date: expect.any(String),
          end_date: expect.any(String),
        }),
      );
    });
  });

  it('shows export button for admin users', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockAuditData);

    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    expect(screen.getByRole('button', { name: /export/i })).toBeInTheDocument();
  });

  it('exports as JSON when clicking export menu item', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockAuditData);
    (auditService.export as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /export/i }));
    await user.click(screen.getByText('Export as JSON'));

    await waitFor(() => {
      expect(auditService.export).toHaveBeenCalledWith(expect.any(Object), 'json');
    });
  });

  it('calls list with user ID filter', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue(mockAuditData);
    const user = userEvent.setup();

    render(
      <MemoryRouter>
        <AuditLog />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
    });

    await user.type(screen.getByLabelText('User ID'), 'u123');
    await user.click(screen.getByRole('button', { name: /filter/i }));

    await waitFor(() => {
      expect(auditService.list).toHaveBeenCalledWith(
        expect.objectContaining({ user_id: 'u123' }),
      );
    });
  });
});
