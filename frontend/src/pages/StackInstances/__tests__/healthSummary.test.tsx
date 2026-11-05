import { describe, it, expect, vi, afterEach } from 'vitest';
import { diffHealth, summarizeHealthChanges } from '../healthSummary';
import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Dashboard from '../Dashboard';
import { NotificationProvider } from '../../../context/NotificationContext';
import type { WsMessage } from '../../../hooks/useWebSocket';

type MessageHandler = (msg: WsMessage) => void;
const capturedWsHandlers: MessageHandler[] = [];

function broadcastWs(msg: WsMessage) {
  capturedWsHandlers.forEach((h) => h(msg));
}

vi.mock('../../../hooks/useWebSocket', () => ({
  useWebSocket: (handler: MessageHandler) => {
    capturedWsHandlers.push(handler);
    return { send: vi.fn() };
  },
}));

vi.mock('../../../hooks/useCountdown', () => ({
  default: vi.fn().mockReturnValue(null),
}));

vi.mock('../../../utils/setupWizard', () => ({
  isSetupWizardDismissed: vi.fn().mockReturnValue(false),
  dismissSetupWizard: vi.fn(),
}));

vi.mock('../../../api/client', () => ({
  instanceService: {
    listAll: vi.fn(),
    recent: vi.fn().mockResolvedValue([]),
    bulkDeploy: vi.fn(),
    bulkStop: vi.fn(),
    bulkClean: vi.fn(),
    bulkDelete: vi.fn(),
    getStatus: vi.fn().mockRejectedValue(new Error('no status')),
    getPods: vi.fn().mockRejectedValue(new Error('no pods')),
  },
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
  },
  templateService: {
    list: vi.fn().mockResolvedValue([]),
  },
  favoriteService: {
    list: vi.fn().mockResolvedValue([]),
    check: vi.fn().mockResolvedValue(false),
    add: vi.fn(),
    remove: vi.fn(),
  },
  dashboardService: {
    getOverview: vi.fn().mockResolvedValue({
      clusters: [],
      recent_deployments: [],
      expiring_soon: [],
      failing_instances: [],
    }),
  },
}));

const authState = vi.hoisted(() => ({
  user: { id: '1', username: 'admin', role: 'admin', display_name: 'Admin' } as
    { id: string; username: string; role: string; display_name: string },
}));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({
    user: authState.user,
    isAuthenticated: true,
    isLoading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}));

import { instanceService } from '../../../api/client';


const row = (id: string, name: string) => ({
  id, name, status: 'running', branch: 'main', namespace: `ns-${id}`, owner_id: '1',
  stack_definition_id: '1', created_at: '', updated_at: '',
});

const status = (health: string) => ({ namespace: 'x', status: health, charts: [], last_checked: '' });

describe('summarizeHealthChanges and diffHealth', () => {
  it('says nothing without a change', () => {
    expect(summarizeHealthChanges([])).toBe('');
  });

  it('names one stack and several stacks', () => {
    expect(summarizeHealthChanges([{ id: '1', name: 'alpha', status: 'degraded', kind: 'health' as const }])).toBe('1 stack update: alpha health degraded');
    expect(summarizeHealthChanges([
      { id: '1', name: 'alpha', status: 'degraded', kind: 'health' as const },
      { id: '2', name: 'beta', status: 'not_found', kind: 'health' as const },
    ])).toBe('2 stack updates: alpha health degraded, beta health not found');
  });

  it('names at most five stacks', () => {
    const changes = Array.from({ length: 7 }, (_, i) => ({ id: `${i}`, name: `s${i}`, status: 'healthy', kind: 'health' as const }));
    expect(summarizeHealthChanges(changes)).toBe(
      '7 stack updates: s0 health healthy, s1 health healthy, s2 health healthy, s3 health healthy, s4 health healthy, and 2 more',
    );
  });

  it('ignores a first value and an unchanged value', () => {
    const names = (id: string) => ({ a: 'alpha', b: 'beta', c: 'gamma' } as Record<string, string>)[id];
    expect(diffHealth({ a: 'healthy', b: 'healthy' }, { a: 'degraded', b: 'healthy', c: 'healthy' }, names))
      .toEqual([{ id: 'a', name: 'alpha', status: 'degraded', kind: 'health' }]);
  });
});

describe('Dashboard pod health announcement', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    capturedWsHandlers.length = 0;
  });

  const renderDashboard = () => render(
    <MemoryRouter>
      <NotificationProvider>
        <Dashboard />
      </NotificationProvider>
    </MemoryRouter>,
  );

  const send = (id: string, health: string) => act(() => {
    broadcastWs({ type: 'instance.status', payload: { instance_id: id, namespace_status: { status: health } } });
  });
  const live = () => screen.getAllByRole('status').map((el) => el.textContent).join('');

  it('is silent on the first load, then summarises a change in one message', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([row('a', 'alpha'), row('b', 'beta')]);
    (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue(status('healthy'));
    renderDashboard();

    await waitFor(() => expect(screen.getAllByRole('img', { name: 'Pod health: healthy' })).toHaveLength(2));
    await act(() => vi.advanceTimersByTimeAsync(1500));
    expect(live()).not.toMatch(/stack update/);

    send('a', 'degraded');
    send('b', 'error');
    await act(() => vi.advanceTimersByTimeAsync(1100));
    expect(screen.getByText('2 stack updates: alpha health degraded, beta health error')).toBeInTheDocument();
    // The cards are not a live region.
    expect(document.querySelector('.MuiGrid-container[aria-live]')).toBeNull();
  });

  it('announces within a second of the first change, even with a steady stream', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([row('a', 'alpha'), row('b', 'beta')]);
    (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue(status('healthy'));
    renderDashboard();
    await waitFor(() => expect(screen.getAllByRole('img', { name: 'Pod health: healthy' })).toHaveLength(2));
    await act(() => vi.advanceTimersByTimeAsync(1500));

    send('a', 'degraded');
    await act(() => vi.advanceTimersByTimeAsync(600));
    send('b', 'error');
    await act(() => vi.advanceTimersByTimeAsync(600));
    expect(live()).toContain('2 stack updates: alpha health degraded, beta health error');
  });

  it('drops a stack that ends the batch where it began', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([row('a', 'alpha'), row('b', 'beta')]);
    (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue(status('healthy'));
    renderDashboard();
    await waitFor(() => expect(screen.getAllByRole('img', { name: 'Pod health: healthy' })).toHaveLength(2));
    await act(() => vi.advanceTimersByTimeAsync(1500));

    send('a', 'degraded');
    send('b', 'error');
    send('a', 'healthy');
    await act(() => vi.advanceTimersByTimeAsync(1100));
    expect(live()).toContain('1 stack update: beta health error');
    expect(live()).not.toContain('alpha');
  });

  it('announces a lifecycle change from a deployment status message', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([row('a', 'alpha'), row('b', 'beta')]);
    (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue(status('healthy'));
    renderDashboard();
    await waitFor(() => expect(screen.getAllByRole('img', { name: 'Pod health: healthy' })).toHaveLength(2));
    await act(() => vi.advanceTimersByTimeAsync(1500));

    act(() => {
      broadcastWs({ type: 'deployment.status', payload: { instance_id: 'a', status: 'stopped' } });
    });
    await act(() => vi.advanceTimersByTimeAsync(1100));
    expect(live()).toBe('1 stack update: alpha stopped');
  });

  it('clears the text before it repeats the same text', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    // Two stacks with one name give the same sentence twice.
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([row('a', 'alpha'), row('b', 'alpha')]);
    (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue(status('healthy'));
    renderDashboard();
    await waitFor(() => expect(screen.getAllByRole('img', { name: 'Pod health: healthy' })).toHaveLength(2));
    await act(() => vi.advanceTimersByTimeAsync(1500));

    send('a', 'degraded');
    await act(() => vi.advanceTimersByTimeAsync(1100));
    expect(live()).toBe('1 stack update: alpha health degraded');
    send('b', 'degraded');
    // The batch timer fires: the text is empty for a moment, then the same text returns.
    await act(() => vi.advanceTimersByTimeAsync(1050));
    expect(live()).toBe('');
    await act(() => vi.advanceTimersByTimeAsync(150));
    expect(live()).toBe('1 stack update: alpha health degraded');
  });
});
