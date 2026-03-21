import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Dashboard from '../Dashboard';
import type { WsMessage } from '../../../hooks/useWebSocket';

type MessageHandler = (msg: WsMessage) => void;
let capturedWsHandler: MessageHandler | null = null;

vi.mock('../../../hooks/useWebSocket', () => ({
  useWebSocket: (handler: MessageHandler) => {
    capturedWsHandler = handler;
    return { send: vi.fn() };
  },
}));

vi.mock('../../../hooks/useCountdown', () => ({
  default: vi.fn().mockReturnValue(null),
}));

vi.mock('../../../api/client', () => ({
  instanceService: {
    list: vi.fn(),
  },
  clusterService: {
    list: vi.fn().mockResolvedValue([]),
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

import { instanceService } from '../../../api/client';
import useCountdown from '../../../hooks/useCountdown';

describe('Dashboard', () => {
  afterEach(() => {
    vi.clearAllMocks();
    capturedWsHandler = null;
  });

  it('shows loading spinner initially', () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays instances when fetch succeeds', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'Test Instance', status: 'running', branch: 'main', namespace: 'stack-test', owner_id: '1', stack_definition_id: '1', created_at: '', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
  });

  it('shows error alert when fetch fails', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Network error'));
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
  });

  it('shows empty state when no instances', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/no stack instances found/i)).toBeInTheDocument();
    });
  });

  it('updates instance status on WebSocket deployment.status message', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: 'inst-1', name: 'WS Instance', status: 'draft', branch: 'main', namespace: 'stack-ws', owner_id: '1', stack_definition_id: '1', created_at: '', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );

    // Wait for initial render with draft status.
    await waitFor(() => {
      expect(screen.getByText('WS Instance')).toBeInTheDocument();
    });
    expect(screen.getByText('draft')).toBeInTheDocument();

    // Simulate a WebSocket deployment.status message.
    act(() => {
      capturedWsHandler?.({
        type: 'deployment.status',
        payload: { instance_id: 'inst-1', status: 'deploying', log_id: 'log-1' },
      });
    });

    await waitFor(() => {
      expect(screen.getByText('deploying')).toBeInTheDocument();
    });
    expect(screen.queryByText('draft')).not.toBeInTheDocument();
  });

  it('ignores WebSocket messages for unknown instance IDs', async () => {
    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: 'inst-1', name: 'My Instance', status: 'running', branch: 'main', namespace: 'stack-test', owner_id: '1', stack_definition_id: '1', created_at: '', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('My Instance')).toBeInTheDocument();
    });
    expect(screen.getByText('running')).toBeInTheDocument();

    // Send a message for a different instance.
    act(() => {
      capturedWsHandler?.({
        type: 'deployment.status',
        payload: { instance_id: 'unknown-id', status: 'error', log_id: 'log-2' },
      });
    });

    // Status should remain unchanged.
    expect(screen.getByText('running')).toBeInTheDocument();
  });

  it('shows countdown chip for running instance with expiry', async () => {
    const mockUseCountdown = useCountdown as unknown as ReturnType<typeof vi.fn>;
    mockUseCountdown.mockReturnValue({
      remaining: '3h 42m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });

    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'TTL Instance', status: 'running', branch: 'main', namespace: 'stack-ttl', owner_id: '1', stack_definition_id: '1', created_at: '', updated_at: '', expires_at: '2026-01-01T12:00:00Z', ttl_minutes: 240 },
    ]);
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('TTL Instance')).toBeInTheDocument();
    });

    expect(screen.getByText(/3h 42m/)).toBeInTheDocument();
  });

  it('shows Expired chip for TTL-expired stopped instance', async () => {
    const mockUseCountdown = useCountdown as unknown as ReturnType<typeof vi.fn>;
    mockUseCountdown.mockReturnValue(null);

    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'Expired Instance', status: 'stopped', branch: 'main', namespace: 'stack-exp', owner_id: '1', stack_definition_id: '1', created_at: '', updated_at: '', error_message: 'Expired (TTL)' },
    ]);
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Expired Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Expired')).toBeInTheDocument();
  });

  it('does not show countdown chip for instance without expiry', async () => {
    const mockUseCountdown = useCountdown as unknown as ReturnType<typeof vi.fn>;
    mockUseCountdown.mockReturnValue(null);

    (instanceService.list as ReturnType<typeof vi.fn>).mockResolvedValue([
      { id: '1', name: 'No TTL Instance', status: 'running', branch: 'main', namespace: 'stack-nottl', owner_id: '1', stack_definition_id: '1', created_at: '', updated_at: '' },
    ]);
    render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('No TTL Instance')).toBeInTheDocument();
    });

    expect(screen.queryByText(/⏱/)).not.toBeInTheDocument();
    expect(screen.queryByText('Expired')).not.toBeInTheDocument();
  });
});
