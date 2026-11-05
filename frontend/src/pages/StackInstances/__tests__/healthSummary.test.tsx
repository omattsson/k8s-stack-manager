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
    expect(summarizeHealthChanges([{ id: '1', name: 'alpha', status: 'degraded' }])).toBe('1 stack changed health: alpha degraded');
    expect(summarizeHealthChanges([
      { id: '1', name: 'alpha', status: 'degraded' },
      { id: '2', name: 'beta', status: 'not_found' },
    ])).toBe('2 stacks changed health: alpha degraded, beta not found');
  });

  it('ignores a first value and an unchanged value', () => {
    const names = (id: string) => ({ a: 'alpha', b: 'beta', c: 'gamma' } as Record<string, string>)[id];
    expect(diffHealth({ a: 'healthy', b: 'healthy' }, { a: 'degraded', b: 'healthy', c: 'healthy' }, names))
      .toEqual([{ id: 'a', name: 'alpha', status: 'degraded' }]);
  });
});

describe('Dashboard pod health announcement', () => {
  afterEach(() => {
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

  it('is silent on the first load, then summarises a change in one message', async () => {
    (instanceService.listAll as ReturnType<typeof vi.fn>).mockResolvedValue([row('a', 'alpha'), row('b', 'beta')]);
    (instanceService.getStatus as ReturnType<typeof vi.fn>).mockResolvedValue(status('healthy'));
    renderDashboard();

    await waitFor(() => expect(screen.getAllByRole('img', { name: 'Pod health: healthy' })).toHaveLength(2));
    await new Promise((resolve) => setTimeout(resolve, 1300));
    expect(screen.getAllByRole('status').map((el) => el.textContent).join('')).not.toMatch(/changed health/);

    act(() => {
      broadcastWs({ type: 'instance.status', payload: { instance_id: 'a', namespace_status: { status: 'degraded' } } });
    });
    act(() => {
      broadcastWs({ type: 'instance.status', payload: { instance_id: 'b', namespace_status: { status: 'error' } } });
    });
    await waitFor(() => {
      expect(screen.getByText('2 stacks changed health: alpha degraded, beta error')).toBeInTheDocument();
    }, { timeout: 3000 });
    // The cards are not a live region.
    expect(document.querySelector('.MuiGrid-container[aria-live]')).toBeNull();
  });
});
