import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import AuditHistory, { AuditHistorySection } from '../index';

vi.mock('../../../api/client', () => ({
  auditService: { list: vi.fn() },
}));

const authState = { role: 'devops' };
vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'u1', username: 'alice', role: authState.role } }),
}));

import { auditService } from '../../../api/client';

const entry = (id: string) => ({
  id, user_id: 'u1', username: 'alice', action: 'update', entity_type: 'stack_instance',
  entity_id: 'i1', details: '', timestamp: '2026-11-13T10:00:00Z',
});

describe('AuditHistory', () => {
  afterEach(() => {
    vi.clearAllMocks();
    authState.role = 'devops';
  });

  it('lists the entries of the entity, 10 per page', async () => {
    const list = auditService.list as ReturnType<typeof vi.fn>;
    list.mockResolvedValue({ data: [entry('a1')], total: 25, limit: 10, offset: 0 });
    render(<MemoryRouter><AuditHistory entityId="i1" /></MemoryRouter>);
    await waitFor(() => expect(screen.getByText('alice')).toBeInTheDocument());
    expect(list).toHaveBeenCalledWith({ entity_id: 'i1', limit: 10, offset: 0 });

    await userEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() => expect(list).toHaveBeenLastCalledWith({ entity_id: 'i1', limit: 10, offset: 10 }));
  });

  it('shows the entity type of each entry', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue({
      data: [{ ...entry('a1'), entity_type: 'value_override' }, { ...entry('a2'), entity_type: 'stack_instance' }],
      total: 2, limit: 10, offset: 0,
    });
    render(<MemoryRouter><AuditHistory entityId="i1" /></MemoryRouter>);
    await waitFor(() => expect(screen.getByText('value_override')).toBeInTheDocument());
    expect(screen.getByRole('columnheader', { name: 'Type' })).toBeInTheDocument();
    expect(screen.getByText('stack_instance')).toBeInTheDocument();
  });

  it('shows an empty state', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue({ data: [], total: 0, limit: 10, offset: 0 });
    render(<MemoryRouter><AuditHistory entityId="i1" /></MemoryRouter>);
    await waitFor(() => expect(screen.getByText('No audit entries.')).toBeInTheDocument());
  });

  it('section renders nothing for role user', () => {
    authState.role = 'user';
    const { container } = render(<MemoryRouter><AuditHistorySection entityId="i1" /></MemoryRouter>);
    expect(container).toBeEmptyDOMElement();
    expect(auditService.list).not.toHaveBeenCalled();
  });

  it('section loads only when opened', async () => {
    (auditService.list as ReturnType<typeof vi.fn>).mockResolvedValue({ data: [entry('a1')], total: 1, limit: 10, offset: 0 });
    render(<MemoryRouter><AuditHistorySection entityId="i1" /></MemoryRouter>);
    expect(auditService.list).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: /audit history/i }));
    await waitFor(() => expect(screen.getByText('alice')).toBeInTheDocument());
  });
});
