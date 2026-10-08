import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import ExpiringSoonWidget from '../ExpiringSoonWidget';
import { NotificationProvider } from '../../../../context/NotificationContext';
import type { DashboardExpiring } from '../../../../types';

const authState = vi.hoisted(() => ({
  user: { id: 'u-alice', username: 'alice', role: 'user', display_name: 'Alice' } as
    { id: string; username: string; role: string; display_name: string },
}));

vi.mock('../../../../context/AuthContext', () => ({
  useAuth: () => ({ user: authState.user, isAuthenticated: true, isLoading: false }),
}));

vi.mock('../../../../api/client', () => ({
  instanceService: { extend: vi.fn() },
}));

vi.mock('../../../../hooks/useCountdown', () => ({
  default: vi.fn().mockReturnValue({ remaining: '30m', isWarning: true, isCritical: false, isExpired: false }),
}));

const item = (overrides: Partial<DashboardExpiring> = {}): DashboardExpiring => ({
  id: 'i1',
  name: 'my-stack',
  namespace: 'stack-my-stack-alice',
  status: 'running',
  expires_at: '2030-01-01T00:00:00Z',
  ttl_minutes: 60,
  ...overrides,
});

const renderWidget = (instances: DashboardExpiring[]) =>
  render(
    <MemoryRouter>
      <NotificationProvider>
        <ExpiringSoonWidget instances={instances} onExtended={vi.fn()} />
      </NotificationProvider>
    </MemoryRouter>,
  );

describe('ExpiringSoonWidget', () => {
  afterEach(() => {
    authState.user = { id: 'u-alice', username: 'alice', role: 'user', display_name: 'Alice' };
  });

  it('shows the empty message without instances', () => {
    renderWidget([]);
    expect(screen.getByText('No instances expiring soon.')).toBeInTheDocument();
  });

  it('shows Extend TTL for the owner', () => {
    renderWidget([item({ owner_id: 'u-alice' })]);
    expect(screen.getByRole('button', { name: 'Extend TTL' })).toBeInTheDocument();
  });

  it('shows Extend TTL for a devops user on another owner instance', () => {
    authState.user = { id: 'u-bob', username: 'bob', role: 'devops', display_name: 'Bob' };
    renderWidget([item({ owner_id: 'u-alice' })]);
    expect(screen.getByRole('button', { name: 'Extend TTL' })).toBeInTheDocument();
  });

  it('hides Extend TTL for another user with role user', () => {
    authState.user = { id: 'u-bob', username: 'bob', role: 'user', display_name: 'Bob' };
    renderWidget([item({ owner_id: 'u-alice' })]);
    expect(screen.getByText('my-stack')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Extend TTL' })).not.toBeInTheDocument();
  });

  it('keeps Extend TTL when the backend does not send owner_id', () => {
    authState.user = { id: 'u-bob', username: 'bob', role: 'user', display_name: 'Bob' };
    renderWidget([item()]);
    expect(screen.getByRole('button', { name: 'Extend TTL' })).toBeInTheDocument();
  });
});
