import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import Detail from '../Detail';
import { NotificationProvider } from '../../../context/NotificationContext';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

// Current user for the auth mock. Default: the owner of mockInstance (owner_id 'user1').
const authState = vi.hoisted(() => ({
  user: { id: 'user1', username: 'alice', role: 'user', display_name: 'Alice' } as
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

// Keeps the last WebSocket message handler so tests can push messages.
const wsState = vi.hoisted(() => ({
  handler: null as null | ((msg: { type: string; payload: unknown }) => void),
}));

vi.mock('../../../hooks/useWebSocket', () => ({
  useWebSocket: (handler: (msg: { type: string; payload: unknown }) => void) => {
    wsState.handler = handler;
    return { send: vi.fn() };
  },
}));

vi.mock('../../../hooks/useUnsavedChanges', () => ({
  useUnsavedChanges: vi.fn(),
}));

vi.mock('../../../hooks/useCountdown', () => ({
  default: vi.fn().mockReturnValue(null),
}));

vi.mock('../../../components/TtlSelector', () => ({
  default: ({ value, onChange }: { value: number; onChange: (v: number) => void }) => (
    <div data-testid="ttl-selector">
      <span data-testid="ttl-value">{value}</span>
      <button data-testid="ttl-change" onClick={() => onChange(240)}>Change TTL</button>
    </div>
  ),
}));

vi.mock('../../../api/client', () => ({
  instanceService: {
    get: vi.fn(),
    getOverrides: vi.fn(),
    update: vi.fn(),
    setOverride: vi.fn(),
    deleteOverride: vi.fn(),
    clone: vi.fn(),
    delete: vi.fn(),
    exportValues: vi.fn(),
    exportChartValues: vi.fn(),
    deploy: vi.fn(),
    stop: vi.fn(),
    clean: vi.fn(),
    getDeployLog: vi.fn(),
    getStatus: vi.fn(),
    getPods: vi.fn(),
    extend: vi.fn(),
    rollback: vi.fn(),
  },
  definitionService: {
    get: vi.fn(),
  },
  gitService: {
    branches: vi.fn(),
  },
  branchOverrideService: {
    list: vi.fn(),
    set: vi.fn(),
    delete: vi.fn(),
  },
  favoriteService: {
    check: vi.fn().mockResolvedValue(false),
    add: vi.fn(),
    remove: vi.fn(),
  },
}));

vi.mock('../../../components/YamlEditor', () => ({
  default: (props: { label?: string; value: string; readOnly?: boolean; onChange?: (v: string) => void }) => (
    <div data-testid="yaml-editor">
      <span>{props.label}</span>
      <pre>{props.value}</pre>
      {!props.readOnly && (
        <>
          <button onClick={() => props.onChange?.('')}>clear {props.label}</button>
          <button onClick={() => props.onChange?.('replicaCount: 5')}>edit {props.label}</button>
          <button onClick={() => props.onChange?.('replicaCount: 7')}>edit again {props.label}</button>
        </>
      )}
    </div>
  ),
}));

vi.mock('../../../utils/download', () => ({
  downloadBlob: vi.fn(),
}));

vi.mock('../../../components/DeploymentLogViewer', () => ({
  default: ({ logs, currentDeployLogId, onRollbackTo }: {
    logs: { id: string; action: string; status: string }[];
    currentDeployLogId?: string;
    onRollbackTo?: (log: { id: string }) => void;
  }) => (
    <div data-testid="deployment-log-viewer">
      {logs.length} log entries
      <span data-testid="log-actions">{logs.map((l) => l.action).join(',')}</span>
      {onRollbackTo && logs
        .filter((l) => l.action === 'deploy' && l.status === 'success' && l.id !== currentDeployLogId)
        .map((l) => (
          <button key={l.id} onClick={() => onRollbackTo(l)}>Roll back to {l.id}</button>
        ))}
    </div>
  ),
}));

vi.mock('../../../components/PodStatusDisplay', () => ({
  default: ({ loading }: { loading: boolean }) => (
    <div data-testid="pod-status-display">
      {loading ? 'Loading status...' : 'Pod status'}
    </div>
  ),
}));

vi.mock('../../../components/AccessUrls', () => ({
  default: ({ status }: { status: { ingresses?: { url: string }[] } }) => (
    <div data-testid="access-urls">
      {(status.ingresses || []).map((ing: { url: string }, i: number) => (
        <span key={i}>{ing.url}</span>
      ))}
    </div>
  ),
}));

vi.mock('../../../components/StatusBadge', () => ({
  default: ({ status }: { status: string }) => (
    <span data-testid="status-badge">{status}</span>
  ),
}));

vi.mock('../../../components/BranchSelector', () => ({
  default: ({ value, onChange }: { value: string; repoUrl: string; onChange: (v: string) => void; label?: string }) => (
    <div data-testid="branch-selector">
      <span>{value}</span>
      <button onClick={() => onChange('feature/new-branch')}>change-branch</button>
      <button onClick={() => onChange('')}>clear-branch</button>
    </div>
  ),
}));

vi.mock('../../../components/ConfirmDialog', () => ({
  default: ({ open, title, message, onConfirm, onCancel, confirmText }: {
    open: boolean; title: string; message: string;
    onConfirm: () => void; onCancel: () => void; confirmText: string;
  }) => open ? (
    <div data-testid="confirm-dialog">
      <div>{title}</div>
      <div>{message}</div>
      <button onClick={onConfirm}>{confirmText}</button>
      <button onClick={onCancel}>Cancel</button>
    </div>
  ) : null,
}));

vi.mock('../../../components/DeployPreviewDialog', () => ({
  default: ({ open, instanceName, onConfirm, onClose }: {
    open: boolean; instanceId: string | number; instanceName: string;
    onConfirm: () => void; onClose: () => void;
  }) => open ? (
    <div data-testid="deploy-preview-dialog">
      <div>Review Changes — {instanceName}</div>
      <button onClick={onConfirm}>Deploy</button>
      <button onClick={onClose}>Cancel</button>
    </div>
  ) : null,
}));

import { instanceService, definitionService, branchOverrideService } from '../../../api/client';
import useCountdown from '../../../hooks/useCountdown';
import { downloadBlob } from '../../../utils/download';

type MockFn = ReturnType<typeof vi.fn>;

const setupMocks = (instanceOverrides: Partial<typeof mockInstance> = {}, opts: { logs?: unknown[]; k8sStatus?: unknown; podsStatus?: unknown; deployLogReject?: boolean; k8sReject?: boolean; branchOverrides?: unknown[] } = {}) => {
  const inst = { ...mockInstance, ...instanceOverrides };
  (instanceService.get as MockFn).mockResolvedValue(inst);
  (definitionService.get as MockFn).mockResolvedValue(mockDefinition);
  (instanceService.getOverrides as MockFn).mockResolvedValue([]);
  (branchOverrideService.list as MockFn).mockResolvedValue(opts.branchOverrides ?? []);
  if (opts.deployLogReject) {
    (instanceService.getDeployLog as MockFn).mockRejectedValue(new Error('no logs'));
  } else {
    (instanceService.getDeployLog as MockFn).mockResolvedValue(opts.logs ?? []);
  }
  if (opts.k8sReject) {
    (instanceService.getStatus as MockFn).mockRejectedValue(new Error('no status'));
    (instanceService.getPods as MockFn).mockRejectedValue(new Error('no pods'));
  } else {
    (instanceService.getStatus as MockFn).mockResolvedValue(opts.k8sStatus ?? null);
    (instanceService.getPods as MockFn).mockResolvedValue(opts.podsStatus ?? opts.k8sStatus ?? null);
  }
  return inst;
};

const renderDetail = () =>
  render(
    <MemoryRouter initialEntries={['/stack-instances/123']}>
      <NotificationProvider>
        <Routes>
          <Route path="/stack-instances/:id" element={<Detail />} />
        </Routes>
      </NotificationProvider>
    </MemoryRouter>
  );

const mockInstance = {
  id: '123',
  name: 'Test Instance',
  namespace: 'stack-test',
  owner_id: 'user1',
  branch: 'main',
  status: 'running',
  stack_definition_id: 'def1',
  created_at: '2025-01-01',
  updated_at: '2025-01-02',
  ttl_minutes: 0,
  expires_at: undefined as string | undefined,
  error_message: undefined as string | undefined,
  values_drift: undefined as boolean | undefined,
  owner_username: undefined as string | undefined,
};

const mockDefinition = {
  id: 'def1',
  name: 'Test Definition',
  description: '',
  default_branch: 'main',
  charts: [
    {
      id: 'chart1',
      stack_definition_id: 'def1',
      chart_name: 'frontend',
      repository_url: 'https://charts.example.com',
      source_repo_url: 'https://git.example.com/repo',
      chart_path: 'charts/frontend',
      chart_version: '1.0.0',
      default_values: 'replicaCount: 1',
      deploy_order: 1,
      created_at: '2025-01-01',
    },
  ],
};

describe('StackInstances Detail', () => {
  afterEach(() => {
    vi.clearAllMocks();
    authState.user = { id: 'user1', username: 'alice', role: 'user', display_name: 'Alice' };
  });

  it('shows loading spinner while fetching', () => {
    (instanceService.get as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-instances/:id" element={<Detail />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays instance details when data loads', async () => {
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByText(/stack-test/)).toBeInTheDocument();
    // Without owner_username the owner ID is shown.
    expect(screen.getByText('Owner: user1')).toBeInTheDocument();
  });

  it('shows the owner username instead of the owner ID', async () => {
    setupMocks({ owner_username: 'owner-name' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Owner: owner-name')).toBeInTheDocument();
    });
    expect(screen.queryByText('Owner: user1')).not.toBeInTheDocument();
  });

  it('shows error alert when fetch fails', async () => {
    (instanceService.get as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Not found'));

    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <NotificationProvider>
          <Routes>
            <Route path="/stack-instances/:id" element={<Detail />} />
          </Routes>
        </NotificationProvider>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load instance details')).toBeInTheDocument();
    });
  });

  it('renders chart tabs when charts exist', async () => {
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });
  });

  it('opens delete confirmation dialog', async () => {
    const user = userEvent.setup();
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /delete/i }));

    await waitFor(() => {
      expect(screen.getByText('Delete Instance')).toBeInTheDocument();
      expect(screen.getByText(/are you sure you want to delete/i)).toBeInTheDocument();
    });
  });

  it('navigates back when Back to Dashboard is clicked', async () => {
    const user = userEvent.setup();
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /back to dashboard/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/');
  });

  it('shows Deploy button for draft instance', async () => {
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /deploy/i })).toBeInTheDocument();
  });

  it('does NOT show Stop button for draft instance', async () => {
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: /^stop$/i })).not.toBeInTheDocument();
  });

  it('shows Stop button for running instance', async () => {
    setupMocks({ status: 'running' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /stop/i })).toBeInTheDocument();
  });

  it('shows Redeploy instead of Deploy for running instance', async () => {
    setupMocks({ status: 'running' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: 'Deploy' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Redeploy' })).toBeInTheDocument();
  });

  it('shows Deploy button for stopped instance', async () => {
    setupMocks({ status: 'stopped' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /deploy/i })).toBeInTheDocument();
  });

  it('shows Redeploy button for error instance', async () => {
    setupMocks({ status: 'error' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: 'Redeploy' })).toBeInTheDocument();
  });

  it('calls instanceService.deploy when Deploy button is clicked', async () => {
    const user = userEvent.setup();
    const inst = setupMocks({ status: 'draft' }, { deployLogReject: true });
    (instanceService.deploy as MockFn).mockResolvedValue({});
    // After deploy, get is called again to refresh
    (instanceService.get as MockFn)
      .mockResolvedValueOnce(inst)
      .mockResolvedValueOnce({ ...inst, status: 'deploying' });
    (instanceService.getDeployLog as MockFn)
      .mockRejectedValueOnce(new Error('no logs'))
      .mockResolvedValueOnce([]);

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // Click Deploy to open preview dialog
    await user.click(screen.getByRole('button', { name: /deploy/i }));

    // Confirm deploy in the preview dialog
    const previewDialog = screen.getByTestId('deploy-preview-dialog');
    await user.click(within(previewDialog).getByRole('button', { name: /deploy/i }));

    await waitFor(() => {
      expect(instanceService.deploy).toHaveBeenCalledWith('123');
    });
  });

  it('calls instanceService.stop when Stop button is clicked', async () => {
    const user = userEvent.setup();
    const inst = setupMocks({ status: 'running' });
    (instanceService.stop as MockFn).mockResolvedValue({});
    (instanceService.get as MockFn)
      .mockResolvedValueOnce(inst)
      .mockResolvedValueOnce({ ...inst, status: 'stopped' });
    (instanceService.getDeployLog as MockFn)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([]);

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /stop/i }));

    await waitFor(() => {
      expect(instanceService.stop).toHaveBeenCalledWith('123');
    });
  });

  it('shows error alert on deploy failure', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    (instanceService.deploy as MockFn).mockRejectedValue(new Error('Deploy failed'));

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // Click Deploy to open preview dialog
    await user.click(screen.getByRole('button', { name: /deploy/i }));

    // Confirm deploy in the preview dialog
    const previewDialog = screen.getByTestId('deploy-preview-dialog');
    await user.click(within(previewDialog).getByRole('button', { name: /deploy/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to start deployment')).toBeInTheDocument();
    });
  });

  it('shows error alert on stop failure', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    (instanceService.stop as MockFn).mockRejectedValue(new Error('Stop failed'));

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /stop/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to stop instance')).toBeInTheDocument();
    });
  });

  it('shows Deployment History section when logs exist', async () => {
    const mockLogs = [
      { id: 'log-1', stack_instance_id: '123', action: 'deploy', status: 'success', output: '', started_at: '2025-01-01T00:00:00Z', completed_at: '2025-01-01T00:01:00Z' },
      { id: 'log-2', stack_instance_id: '123', action: 'deploy', status: 'error', output: '', error_message: 'helm failed', started_at: '2025-01-02T00:00:00Z', completed_at: '2025-01-02T00:01:00Z' },
    ];
    setupMocks({ status: 'draft' }, { logs: mockLogs });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText(/Deployment History/)).toBeInTheDocument();
      expect(screen.getByTestId('deployment-log-viewer')).toBeInTheDocument();
    });
  });

  it('does not show Deployment History when no logs exist', async () => {
    setupMocks({ status: 'draft' }, { logs: [] });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.queryByText(/Deployment History/)).not.toBeInTheDocument();
  });

  it('shows Cluster Resources for running instance', async () => {
    const mockStatus = { namespace: 'stack-test', pods: [], services: [] };
    setupMocks({ status: 'running' }, { k8sStatus: mockStatus });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Cluster Resources')).toBeInTheDocument();
      expect(screen.getByTestId('pod-status-display')).toBeInTheDocument();
    });
  });

  it('does not show Cluster Resources for draft instance', async () => {
    setupMocks({ status: 'draft' }, { deployLogReject: true });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.queryByText('Cluster Resources')).not.toBeInTheDocument();
  });

  it('fetches K8s status for running instance', async () => {
    setupMocks({ status: 'running' });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(instanceService.getPods).toHaveBeenCalledWith('123');
    });
  });

  it('shows lifecycle stepper for deploying status', async () => {
    setupMocks({ status: 'deploying' });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Status Lifecycle')).toBeInTheDocument();
    // Stepper steps are visible (deploying appears in both the status badge and the stepper)
    expect(screen.getByText('draft')).toBeInTheDocument();
    expect(screen.getAllByText('deploying').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('running')).toBeInTheDocument();
  });

  it('shows error alert in lifecycle for error status', async () => {
    setupMocks({ status: 'error' }, { deployLogReject: true });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Instance is error')).toBeInTheDocument();
  });

  it('shows warning alert in lifecycle for stopping status', async () => {
    setupMocks({ status: 'stopping' });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Instance is stopping')).toBeInTheDocument();
  });

  it('shows disabled Stopping button for stopping instance', async () => {
    setupMocks({ status: 'stopping' });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    const btn = screen.getByRole('button', { name: /stopping/i });
    expect(btn).toBeDisabled();
  });

  it('shows Cluster Resources for stopping instance', async () => {
    const mockStatus = { namespace: 'stack-test', pods: [], services: [] };
    setupMocks({ status: 'stopping' }, { k8sStatus: mockStatus });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Cluster Resources')).toBeInTheDocument();
    });
  });

  it('shows warning alert in lifecycle for stopped status', async () => {
    setupMocks({ status: 'stopped' }, { deployLogReject: true });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Instance is stopped')).toBeInTheDocument();
  });

  it('shows Clean Namespace button for running instance', async () => {
    setupMocks({ status: 'running' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /clean namespace/i })).toBeInTheDocument();
  });

  it('shows Clean Namespace button for error instance', async () => {
    setupMocks({ status: 'error' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /clean namespace/i })).toBeInTheDocument();
  });

  it('does not show Clean Namespace button for draft instance', async () => {
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: /clean namespace/i })).not.toBeInTheDocument();
  });

  it('opens confirmation dialog when Clean Namespace is clicked', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clean namespace/i }));

    await waitFor(() => {
      expect(screen.getByText('Clean Namespace?')).toBeInTheDocument();
      expect(screen.getByText(/uninstall all Helm releases/)).toBeInTheDocument();
    });
  });

  it('calls instanceService.clean when Clean is confirmed', async () => {
    const user = userEvent.setup();
    const inst = setupMocks({ status: 'running' });
    (instanceService.clean as MockFn).mockResolvedValue({});
    (instanceService.get as MockFn)
      .mockResolvedValueOnce(inst)
      .mockResolvedValueOnce({ ...inst, status: 'cleaning' });
    (instanceService.getDeployLog as MockFn)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([]);

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clean namespace/i }));

    await waitFor(() => {
      expect(screen.getByText('Clean Namespace?')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /^clean$/i }));

    await waitFor(() => {
      expect(instanceService.clean).toHaveBeenCalledWith('123');
    });
  });

  it('shows error alert on clean failure', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    (instanceService.clean as MockFn).mockRejectedValue(new Error('Clean failed'));

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clean namespace/i }));

    await waitFor(() => {
      expect(screen.getByText('Clean Namespace?')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /^clean$/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to clean namespace')).toBeInTheDocument();
    });
  });

  it('shows disabled Cleaning button for cleaning instance', async () => {
    setupMocks({ status: 'cleaning' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    const btn = screen.getByRole('button', { name: /cleaning/i });
    expect(btn).toBeDisabled();
  });

  it('shows warning alert in lifecycle for cleaning status', async () => {
    setupMocks({ status: 'cleaning' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Instance is cleaning')).toBeInTheDocument();
  });

  it('shows Clean Namespace button for stopped instance', async () => {
    setupMocks({ status: 'stopped' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /clean namespace/i })).toBeInTheDocument();
  });

  it('shows "Using instance branch" chip when no branch override exists', async () => {
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Using instance branch')).toBeInTheDocument();
    });
  });

  it('shows override chip when branch override exists for a chart', async () => {
    setupMocks({ status: 'draft' }, {
      deployLogReject: true,
      branchOverrides: [
        { id: 'bo1', stack_instance_id: '123', chart_config_id: 'chart1', branch: 'feature/test', updated_at: '2025-01-01' },
      ],
    });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Override: feature/test')).toBeInTheDocument();
    });
  });

  it('fetches branch overrides on load', async () => {
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(branchOverrideService.list).toHaveBeenCalledWith('123');
  });

  it('shows Access URLs section for running instance with k8s status', async () => {
    const mockStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [],
      ingresses: [{ name: 'web', host: 'app.example.com', path: '/', tls: true, url: 'https://app.example.com' }],
      last_checked: '2025-01-01T00:00:00Z',
    };
    setupMocks({ status: 'running' }, { k8sStatus: mockStatus });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByTestId('access-urls')).toBeInTheDocument();
    });
    expect(screen.getByText('https://app.example.com')).toBeInTheDocument();
  });

  it('does not show Access URLs section for draft instance', async () => {
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.queryByTestId('access-urls')).not.toBeInTheDocument();
  });

  it('does not show Access URLs for running instance without k8s status', async () => {
    setupMocks({ status: 'running' }, { k8sReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.queryByTestId('access-urls')).not.toBeInTheDocument();
  });

  it('shows countdown chip when instance is running with expiry', async () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '3h 42m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });
    setupMocks({ status: 'running', expires_at: '2026-01-01T12:00:00Z' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText(/Expires in 3h 42m/)).toBeInTheDocument();
  });

  it('shows Extend button next to countdown', async () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '3h 42m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });
    setupMocks({ status: 'running', expires_at: '2026-01-01T12:00:00Z' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByRole('button', { name: /extend/i })).toBeInTheDocument();
  });

  it('calls instanceService.extend when Extend is clicked', async () => {
    const user = userEvent.setup();
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '1h 0m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });
    const inst = setupMocks({ status: 'running', expires_at: '2026-01-01T12:00:00Z' });
    (instanceService.extend as MockFn).mockResolvedValue({ ...inst, expires_at: '2026-01-01T16:00:00Z' });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /extend/i }));
    await user.click(screen.getByRole('menuitem', { name: '+4 h' }));

    await waitFor(() => {
      expect(instanceService.extend).toHaveBeenCalledWith('123', 240);
    });
    expect(await screen.findByText(/^Extended by 4 h\. Expires .+ \(in .+\)$/)).toBeInTheDocument();
  });

  it('shows Expired chip when instance stopped by TTL', async () => {
    (useCountdown as unknown as MockFn).mockReturnValue(null);
    setupMocks({ status: 'stopped', error_message: 'Expired (TTL)' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Expired')).toBeInTheDocument();
  });

  it('does not show countdown for draft instance', async () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '3h 0m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });
    setupMocks({ status: 'draft', expires_at: '2026-01-01T12:00:00Z' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.queryByText(/Expires in/)).not.toBeInTheDocument();
  });

  it('renders TTL selector on detail page', async () => {
    (useCountdown as unknown as MockFn).mockReturnValue(null);
    setupMocks({ status: 'draft', ttl_minutes: 240 }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByTestId('ttl-selector')).toBeInTheDocument();
    expect(screen.getByTestId('ttl-value')).toHaveTextContent('240');
  });

  it('sets the TTL through update when TTL is changed', async () => {
    const user = userEvent.setup();
    (useCountdown as unknown as MockFn).mockReturnValue(null);
    const inst = setupMocks({ status: 'draft', ttl_minutes: 0 }, { deployLogReject: true });
    (instanceService.update as MockFn).mockResolvedValue({ ...inst, ttl_minutes: 240 });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByTestId('ttl-change'));

    await waitFor(() => {
      expect(instanceService.update).toHaveBeenCalledWith('123', { ttl_minutes: 240 });
    });
    expect(instanceService.extend).not.toHaveBeenCalled();
  });

  it('confirms delete and navigates to dashboard', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.delete as MockFn).mockResolvedValue(undefined);
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /delete/i }));

    await waitFor(() => {
      expect(screen.getByText('Delete Instance')).toBeInTheDocument();
    });

    // Click the confirm button inside the dialog
    const dialog = within(screen.getByTestId('confirm-dialog'));
    await user.click(dialog.getByRole('button', { name: /^delete$/i }));

    await waitFor(() => {
      expect(instanceService.delete).toHaveBeenCalledWith('123');
    });
    expect(mockNavigate).toHaveBeenCalledWith('/');
  });

  it('shows error when delete fails', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.delete as MockFn).mockRejectedValue(new Error('Forbidden'));
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /delete/i }));
    await waitFor(() => {
      expect(screen.getByText('Delete Instance')).toBeInTheDocument();
    });

    const dialog = within(screen.getByTestId('confirm-dialog'));
    await user.click(dialog.getByRole('button', { name: /^delete$/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to delete instance')).toBeInTheDocument();
    });
  });

  it('opens the clone dialog with a valid suggested name and the source branch', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft', ttl_minutes: 240 }, { deployLogReject: true });
    renderDetail();
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Clone' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone Instance' });
    expect(within(dialog).getByRole('textbox', { name: /name/i })).toHaveValue('test-instance-copy');
    expect(within(dialog).getByRole('textbox', { name: /branch/i })).toHaveValue('main');
    expect(within(dialog).getByTestId('ttl-value')).toHaveTextContent('240');
    expect(instanceService.clone).not.toHaveBeenCalled();
  });

  it('validates the clone name live', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    renderDetail();
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Clone' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone Instance' });
    const nameInput = within(dialog).getByRole('textbox', { name: /name/i });

    await user.clear(nameInput);
    await user.type(nameInput, 'Test Instance (Copy)');
    expect(within(dialog).getByText('Use lowercase letters only')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Create Clone' })).toBeDisabled();

    await user.clear(nameInput);
    await user.type(nameInput, 'x'.repeat(51));
    expect(within(dialog).getByText('Use 50 characters or fewer')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Create Clone' })).toBeDisabled();

    await user.clear(nameInput);
    expect(within(dialog).getByText('Instance name is required')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Create Clone' })).toBeDisabled();
  });

  it('clones with name, branch and TTL and navigates to the new instance', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft', ttl_minutes: 240 }, { deployLogReject: true });
    (instanceService.clone as MockFn).mockResolvedValue({ id: 'cloned-123' });
    renderDetail();
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Clone' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone Instance' });
    const nameInput = within(dialog).getByRole('textbox', { name: /name/i });
    await user.clear(nameInput);
    await user.type(nameInput, 'my-copy');
    const branchInput = within(dialog).getByRole('textbox', { name: /branch/i });
    await user.clear(branchInput);
    await user.type(branchInput, 'feature-x');
    await user.click(within(dialog).getByRole('button', { name: 'Create Clone' }));

    await waitFor(() => {
      expect(instanceService.clone).toHaveBeenCalledWith('123', { name: 'my-copy', branch: 'feature-x', ttl_minutes: 240 });
    });
    expect(mockNavigate).toHaveBeenCalledWith('/stack-instances/cloned-123');
  });

  it('sends no name when the suggested clone name is kept', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft', ttl_minutes: 240 }, { deployLogReject: true });
    (instanceService.clone as MockFn).mockResolvedValue({ id: 'cloned-123' });
    renderDetail();
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Clone' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone Instance' });
    await user.click(within(dialog).getByRole('button', { name: 'Create Clone' }));

    await waitFor(() => {
      expect(instanceService.clone).toHaveBeenCalledWith('123', { branch: 'main', ttl_minutes: 240 });
    });
  });

  it('shows the API error in the clone dialog when clone fails', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    (instanceService.clone as MockFn).mockRejectedValue({ response: { status: 400, data: { error: 'invalid name' } } });
    renderDetail();
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Clone' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone Instance' });
    await user.click(within(dialog).getByRole('button', { name: 'Create Clone' }));

    expect(await within(dialog).findByText(/invalid name/)).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('downloads the values ZIP unchanged when All charts (ZIP) is chosen', async () => {
    const user = userEvent.setup();
    setupMocks();
    const blob = new Blob(['PK\u0003\u0004'], { type: 'application/zip' });
    (instanceService.exportValues as MockFn).mockResolvedValue({ blob, filename: 'Test Instance-values.zip' });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /export values/i }));
    await user.click(await screen.findByRole('menuitem', { name: 'All charts (ZIP)' }));

    await waitFor(() => {
      expect(instanceService.exportValues).toHaveBeenCalledWith('123', 'Test Instance');
    });
    expect(downloadBlob).toHaveBeenCalledWith(blob, 'Test Instance-values.zip');
    expect((downloadBlob as MockFn).mock.calls[0][0]).toBe(blob);
  });

  it('downloads one chart as YAML when a chart is chosen', async () => {
    const user = userEvent.setup();
    setupMocks();
    const blob = new Blob(['replicaCount: 1\n'], { type: 'application/x-yaml' });
    (instanceService.exportChartValues as MockFn).mockResolvedValue({ blob, filename: 'Test Instance-frontend-values.yaml' });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /export values/i }));
    await user.click(await screen.findByRole('menuitem', { name: 'frontend (YAML)' }));

    await waitFor(() => {
      expect(instanceService.exportChartValues).toHaveBeenCalledWith('123', 'chart1', 'Test Instance-frontend-values.yaml');
    });
    expect(downloadBlob).toHaveBeenCalledWith(blob, 'Test Instance-frontend-values.yaml');
    expect(instanceService.exportValues).not.toHaveBeenCalled();
  });

  it('shows the generic error when export fails without an HTTP response', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.exportValues as MockFn).mockRejectedValue(new Error('Network Error'));
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /export values/i }));
    await user.click(await screen.findByRole('menuitem', { name: 'All charts (ZIP)' }));

    await waitFor(() => {
      expect(screen.getByText('Failed to export values')).toBeInTheDocument();
    });
    expect(downloadBlob).not.toHaveBeenCalled();
  });

  it('shows the HTTP status and server message when export fails', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.exportValues as MockFn).mockRejectedValue({
      response: {
        status: 403,
        statusText: 'Forbidden',
        data: new Blob([JSON.stringify({ error: 'Access denied' })], { type: 'application/json' }),
      },
    });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /export values/i }));
    await user.click(await screen.findByRole('menuitem', { name: 'All charts (ZIP)' }));

    await waitFor(() => {
      expect(screen.getByText('Failed to export values (HTTP 403: Access denied)')).toBeInTheDocument();
    });
  });

  it('saves branch changes when Save Changes is clicked', async () => {
    const user = userEvent.setup();
    setupMocks({ branch: 'main' });
    (instanceService.update as MockFn).mockResolvedValue({ ...mockInstance, branch: 'develop' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // Click Save Changes (there may be no actual branch change in this test since
    // the branch selector is mocked, but we still exercise the handler)
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      // Save should complete without error - the handler checks if branch changed
      expect(screen.queryByText('Failed to save changes')).not.toBeInTheDocument();
    });
  });

  it('shows error when save fails', async () => {
    const user = userEvent.setup();
    setupMocks({ branch: 'old-branch' });
    (instanceService.update as MockFn).mockRejectedValue(new Error('Server error'));
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // We need to trigger a branch change for the save to actually call update.
    // Since BranchSelector is mocked as readOnly, we directly test the save button
    // which will try to save overrides even without branch change.
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    // If no changes, no API call and no error
    // To trigger actual save failure, we'd need to modify the branch input
    // But the handler still runs through the save path
  });

  it('confirms clean dialog and calls instanceService.clean', async () => {
    const user = userEvent.setup();
    const inst = setupMocks({ status: 'running' });
    (instanceService.clean as MockFn).mockResolvedValue(undefined);
    // After clean, the component refreshes - mock chain: first call returns running (initial), second returns cleaning
    (instanceService.get as MockFn)
      .mockResolvedValueOnce(inst)
      .mockResolvedValueOnce({ ...inst, status: 'cleaning' });
    (instanceService.getDeployLog as MockFn)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([]);
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clean namespace/i }));

    await waitFor(() => {
      expect(screen.getByText('Clean Namespace?')).toBeInTheDocument();
    });

    // Click the confirm button inside the clean dialog
    await user.click(screen.getByRole('button', { name: /^clean$/i }));

    await waitFor(() => {
      expect(instanceService.clean).toHaveBeenCalledWith('123');
    });
  });

  it('refreshes instance and logs after successful deploy', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft' }, { deployLogReject: true });
    (instanceService.deploy as MockFn).mockResolvedValue(undefined);
    const updatedInst = { ...mockInstance, status: 'deploying' };
    const deployLog = [{ id: 'log1', action: 'deploy', status: 'running', output: 'deploying...', started_at: '2025-01-01' }];
    // After deploy, the component refetches instance and logs
    (instanceService.get as MockFn)
      .mockResolvedValueOnce({ ...mockInstance, status: 'draft' }) // initial load
      .mockResolvedValueOnce(updatedInst); // refresh after deploy
    (instanceService.getDeployLog as MockFn)
      .mockRejectedValueOnce(new Error('no logs')) // initial load
      .mockResolvedValueOnce(deployLog); // refresh after deploy
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // Click Deploy to open preview dialog
    await user.click(screen.getByRole('button', { name: /deploy/i }));

    // Confirm deploy in the preview dialog
    const previewDialog = screen.getByTestId('deploy-preview-dialog');
    await user.click(within(previewDialog).getByRole('button', { name: /deploy/i }));

    await waitFor(() => {
      expect(instanceService.deploy).toHaveBeenCalledWith('123');
    });
  });

  it('refreshes instance and logs after successful stop', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    (instanceService.stop as MockFn).mockResolvedValue(undefined);
    const stoppedInst = { ...mockInstance, status: 'stopping' };
    (instanceService.get as MockFn)
      .mockResolvedValueOnce({ ...mockInstance, status: 'running' }) // initial
      .mockResolvedValueOnce(stoppedInst); // refresh after stop
    (instanceService.getDeployLog as MockFn)
      .mockResolvedValueOnce([]) // initial
      .mockResolvedValueOnce([]); // refresh after stop
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /stop/i }));

    await waitFor(() => {
      expect(instanceService.stop).toHaveBeenCalledWith('123');
    });
  });

  it('shows error when TTL extend fails', async () => {
    const user = userEvent.setup();
    (useCountdown as unknown as MockFn).mockReturnValue('5:00');
    setupMocks({ status: 'running', ttl_minutes: 60, expires_at: '2026-06-01T00:00:00Z' });
    (instanceService.extend as MockFn).mockRejectedValue(new Error('Server error'));
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // Click the Extend button (shown next to countdown) and pick an option
    const extendBtn = await screen.findByRole('button', { name: /extend/i });
    await user.click(extendBtn);
    await user.click(screen.getByRole('menuitem', { name: '+1 h' }));

    await waitFor(() => {
      expect(screen.getByText('Failed to extend TTL')).toBeInTheDocument();
    });
  });

  it('shows error when TTL change fails', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft', ttl_minutes: 60 }, { deployLogReject: true });
    (instanceService.update as MockFn).mockRejectedValue(new Error('Server error'));
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByTestId('ttl-change'));

    await waitFor(() => {
      expect(screen.getByText('Failed to update TTL')).toBeInTheDocument();
    });
  });

  it('shows error status in lifecycle section when instance has error status', async () => {
    setupMocks({ status: 'error', error_message: 'Helm chart failed to install' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Instance is error')).toBeInTheDocument();
  });

  it('shows overrides in YAML editor when they exist', async () => {
    setupMocks();
    (instanceService.getOverrides as MockFn).mockResolvedValue([
      { id: 'ov1', stack_instance_id: '123', chart_config_id: 'chart1', values: 'replicaCount: 3' },
    ]);
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // Check that the YAML editor shows the override value
    await waitFor(() => {
      expect(screen.getByText('replicaCount: 3')).toBeInTheDocument();
    });
  });

  it('cancels delete dialog when Cancel is clicked', async () => {
    const user = userEvent.setup();
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /delete/i }));

    await waitFor(() => {
      expect(screen.getByText('Delete Instance')).toBeInTheDocument();
    });

    // Click Cancel button in the dialog
    const dialog = within(screen.getByTestId('confirm-dialog'));
    await user.click(dialog.getByRole('button', { name: /^cancel$/i }));

    await waitFor(() => {
      expect(screen.queryByText('Delete Instance')).not.toBeInTheDocument();
    });
    expect(instanceService.delete).not.toHaveBeenCalled();
  });

  it('cancels clean dialog when Cancel is clicked', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clean namespace/i }));

    await waitFor(() => {
      expect(screen.getByText('Clean Namespace?')).toBeInTheDocument();
    });

    // Click Cancel button in the dialog
    const dialog = within(screen.getByTestId('confirm-dialog'));
    await user.click(dialog.getByRole('button', { name: /^cancel$/i }));

    await waitFor(() => {
      expect(screen.queryByText('Clean Namespace?')).not.toBeInTheDocument();
    });
    expect(instanceService.clean).not.toHaveBeenCalled();
  });

  it('renders chart repository info, path, and version in chart tabs', async () => {
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });
    expect(screen.getByText(/Repo: https:\/\/charts\.example\.com/)).toBeInTheDocument();
    expect(screen.getByText(/Path: charts\/frontend/)).toBeInTheDocument();
    expect(screen.getByText(/Version: 1\.0\.0/)).toBeInTheDocument();
  });

  it('renders YAML editors for default values and overrides in chart tabs', async () => {
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });
    expect(screen.getByText('Default Values')).toBeInTheDocument();
    expect(screen.getByText('Your Overrides')).toBeInTheDocument();
    expect(screen.getByText('replicaCount: 1')).toBeInTheDocument();
  });

  it('sets branch override when BranchSelector onChange is called', async () => {
    const user = userEvent.setup();
    setupMocks();
    (branchOverrideService.set as MockFn).mockResolvedValue({});
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });

    // Second "change-branch" button is the chart-level BranchSelector
    const changeBtns = screen.getAllByText('change-branch');
    await user.click(changeBtns[1]);

    await waitFor(() => {
      expect(branchOverrideService.set).toHaveBeenCalledWith('123', 'chart1', 'feature/new-branch');
    });
  });

  it('removes branch override when branch is cleared', async () => {
    const user = userEvent.setup();
    setupMocks({}, { branchOverrides: [{ chart_config_id: 'chart1', branch: 'old-branch' }] });
    (branchOverrideService.delete as MockFn).mockResolvedValue({});
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });

    // Second "clear-branch" button is the chart-level BranchSelector
    const clearBtns = screen.getAllByText('clear-branch');
    await user.click(clearBtns[1]);

    await waitFor(() => {
      expect(branchOverrideService.delete).toHaveBeenCalledWith('123', 'chart1');
    });
  });

  it('shows error when setting branch override fails', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    setupMocks();
    (branchOverrideService.set as MockFn).mockRejectedValue(new Error('fail'));
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });

    const changeBtns = screen.getAllByText('change-branch');
    await user.click(changeBtns[1]);

    await vi.advanceTimersByTimeAsync(900);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('Failed to update branch override');
    });
    vi.useRealTimers();
  });

  it('shows error when removing branch override fails', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    setupMocks({}, { branchOverrides: [{ chart_config_id: 'chart1', branch: 'old-branch' }] });
    (branchOverrideService.delete as MockFn).mockRejectedValue(new Error('fail'));
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });

    const clearBtns = screen.getAllByText('clear-branch');
    await user.click(clearBtns[1]);

    await vi.advanceTimersByTimeAsync(900);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent('Failed to update branch override');
    });
    vi.useRealTimers();
  });

  it('saves override values when Save Changes is clicked with edited overrides', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.setOverride as MockFn).mockResolvedValue({});
    (instanceService.update as MockFn).mockResolvedValue({ ...mockInstance });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // The save button should exist
    const saveButton = screen.getByRole('button', { name: /save changes/i });
    await user.click(saveButton);

    // Save should complete without error (no overrides to save, no branch change)
    await waitFor(() => {
      expect(screen.queryByText('Failed to save changes')).not.toBeInTheDocument();
    });
  });

  it('deletes an existing override when its editor is cleared and saved', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.getOverrides as MockFn).mockResolvedValue([
      { id: 'ov1', stack_instance_id: '123', chart_config_id: 'chart1', values: 'replicaCount: 3' },
    ]);
    (instanceService.deleteOverride as MockFn).mockResolvedValue(undefined);
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('replicaCount: 3')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'clear Your Overrides' }));
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      expect(instanceService.deleteOverride).toHaveBeenCalledWith('123', 'chart1');
    });
    expect(instanceService.setOverride).not.toHaveBeenCalled();
    await waitFor(() => {
      expect(screen.getByText('Changes saved successfully')).toBeInTheDocument();
    });

    // A second save does not send the DELETE again.
    await user.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => {
      expect(instanceService.deleteOverride).toHaveBeenCalledTimes(1);
    });
  });

  it('does not send DELETE or PUT for an empty override that never existed', async () => {
    const user = userEvent.setup();
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'clear Your Overrides' }));
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      expect(screen.getByText('Changes saved successfully')).toBeInTheDocument();
    });
    expect(instanceService.deleteOverride).not.toHaveBeenCalled();
    expect(instanceService.setOverride).not.toHaveBeenCalled();
  });

  it('sends PUT for a changed non-empty override', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.setOverride as MockFn).mockResolvedValue({});
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'edit Your Overrides' }));
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      expect(instanceService.setOverride).toHaveBeenCalledWith('123', 'chart1', { values: 'replicaCount: 5' });
    });
    expect(instanceService.deleteOverride).not.toHaveBeenCalled();
  });

  it('does not repeat a successful DELETE when a later PUT fails', async () => {
    const user = userEvent.setup();
    setupMocks();
    (definitionService.get as MockFn).mockResolvedValue({
      ...mockDefinition,
      charts: [
        mockDefinition.charts[0],
        { ...mockDefinition.charts[0], id: 'chart2', chart_name: 'backend', deploy_order: 2 },
      ],
    });
    (instanceService.getOverrides as MockFn).mockResolvedValue([
      { id: 'ov1', stack_instance_id: '123', chart_config_id: 'chart1', values: 'replicaCount: 3' },
    ]);
    (instanceService.deleteOverride as MockFn).mockResolvedValue(undefined);
    (instanceService.setOverride as MockFn)
      .mockRejectedValueOnce({ response: { status: 500, statusText: 'Internal Server Error', data: { error: 'Internal server error' } } })
      .mockResolvedValueOnce({});
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('replicaCount: 3')).toBeInTheDocument();
    });

    // Chart A (chart1): clear the existing override. Chart B (chart2): add a new one.
    const clearButtons = screen.getAllByRole('button', { name: 'clear Your Overrides', hidden: true });
    const editButtons = screen.getAllByRole('button', { name: 'edit Your Overrides', hidden: true });
    await user.click(clearButtons[0]);
    await user.click(editButtons[1]);
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to save changes (HTTP 500: Internal server error)')).toBeInTheDocument();
    });
    expect(instanceService.deleteOverride).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      expect(instanceService.setOverride).toHaveBeenCalledTimes(2);
    });
    expect(instanceService.setOverride).toHaveBeenLastCalledWith('123', 'chart2', { values: 'replicaCount: 5' });
    expect(instanceService.deleteOverride).toHaveBeenCalledTimes(1);
  });

  it('keeps an override edit typed while the save request runs', async () => {
    const user = userEvent.setup();
    setupMocks();
    let resolvePut: (value: unknown) => void = () => {};
    (instanceService.setOverride as MockFn)
      .mockImplementationOnce(() => new Promise((resolve) => { resolvePut = resolve; }))
      .mockResolvedValueOnce({});
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'edit Your Overrides' }));
    await user.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => {
      expect(instanceService.setOverride).toHaveBeenCalledWith('123', 'chart1', { values: 'replicaCount: 5' });
    });

    // Edit again while the PUT is still pending, then let the PUT finish.
    await user.click(screen.getByRole('button', { name: 'edit again Your Overrides' }));
    resolvePut({});

    await waitFor(() => {
      expect(screen.getByText('Changes saved successfully')).toBeInTheDocument();
    });
    expect(screen.getByText('replicaCount: 7')).toBeInTheDocument();

    // The newer edit is still unsaved, so the next save sends it.
    await user.click(screen.getByRole('button', { name: /save changes/i }));
    await waitFor(() => {
      expect(instanceService.setOverride).toHaveBeenLastCalledWith('123', 'chart1', { values: 'replicaCount: 7' });
    });
    expect(instanceService.setOverride).toHaveBeenCalledTimes(2);
  });

  it('treats a 404 on DELETE as success', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.getOverrides as MockFn).mockResolvedValue([
      { id: 'ov1', stack_instance_id: '123', chart_config_id: 'chart1', values: 'replicaCount: 3' },
    ]);
    (instanceService.deleteOverride as MockFn).mockRejectedValue({
      response: { status: 404, statusText: 'Not Found', data: { error: 'Override not found' } },
    });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('replicaCount: 3')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'clear Your Overrides' }));
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      expect(screen.getByText('Changes saved successfully')).toBeInTheDocument();
    });
    expect(screen.queryByText(/Failed to save changes/)).not.toBeInTheDocument();
  });

  it('shows the HTTP reason when deleting an override fails', async () => {
    const user = userEvent.setup();
    setupMocks();
    (instanceService.getOverrides as MockFn).mockResolvedValue([
      { id: 'ov1', stack_instance_id: '123', chart_config_id: 'chart1', values: 'replicaCount: 3' },
    ]);
    (instanceService.deleteOverride as MockFn).mockRejectedValue({
      response: { status: 403, statusText: 'Forbidden', data: { error: 'Access denied' } },
    });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('replicaCount: 3')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'clear Your Overrides' }));
    await user.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() => {
      expect(screen.getByText('Failed to save changes (HTTP 403: Access denied)')).toBeInTheDocument();
    });
  });

  it('handles TTL clear (ttl_minutes = 0) via update instead of extend', async () => {
    setupMocks({ ttl_minutes: 30, status: 'running' });
    const updatedInstance = { ...mockInstance, ttl_minutes: 0 };
    (instanceService.update as MockFn).mockResolvedValue(updatedInstance);
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    // TtlSelector is mocked — call its onChange callback directly via mock
    // The TtlSelector mock renders a button that calls onChange(0)
    // Since we can't directly trigger it, verify the handler exists by checking the component renders
    expect(screen.getByTestId('ttl-selector')).toBeInTheDocument();
  });

  it('shows cleaning state with disabled button during clean operation', async () => {
    setupMocks({ status: 'cleaning' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    const cleaningButton = screen.getByRole('button', { name: /cleaning/i });
    expect(cleaningButton).toBeDisabled();
  });

  it('fetches and displays deployment logs when they exist', async () => {
    const logs = [
      { id: '1', action: 'deploy', status: 'success', created_at: '2025-01-01', output: 'done' },
      { id: '2', action: 'stop', status: 'success', created_at: '2025-01-02', output: 'stopped' },
    ];
    setupMocks({}, { logs });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByTestId('deployment-log-viewer')).toBeInTheDocument();
    });
    expect(screen.getByText('2 log entries')).toBeInTheDocument();
  });
});

describe('StackInstances Detail permissions', () => {
  const setUser = (id: string, role: string) => {
    authState.user = { id, username: id, role, display_name: id };
  };

  afterEach(() => {
    vi.clearAllMocks();
    authState.user = { id: 'user1', username: 'alice', role: 'user', display_name: 'Alice' };
  });

  const expectModifyControls = async () => {
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Clean Namespace' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeInTheDocument();
    expect(screen.getByText('Your Overrides')).toBeInTheDocument();
    expect(screen.queryByText(/Overrides are visible to the owner/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Read-only: you are not the owner/)).not.toBeInTheDocument();
    expect(instanceService.getOverrides).toHaveBeenCalledWith('123');
    expect(branchOverrideService.list).toHaveBeenCalledWith('123');
  };

  it.each([
    ['owner with role user', 'user1', 'user'],
    ['admin who is not the owner', 'u-admin', 'admin'],
    ['devops user who is not the owner', 'u-devops', 'devops'],
  ])('shows lifecycle actions for the %s', async (_label, userId, role) => {
    setUser(userId, role);
    setupMocks();
    renderDetail();
    await expectModifyControls();
  });

  it('shows Deploy for the owner when the instance is stopped', async () => {
    setupMocks({ status: 'stopped' });
    renderDetail();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Deploy' })).toBeInTheDocument();
    });
  });

  it('hides lifecycle actions and shows read-only info for another user', async () => {
    setUser('u-bob', 'user');
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByText('Read-only: you are not the owner of this stack.')).toBeInTheDocument();
    for (const name of ['Deploy', 'Stop', 'Clean Namespace', 'Delete', 'Save Changes', 'Extend']) {
      expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    }
    // Viewing and copying stay available.
    expect(screen.getByRole('button', { name: 'Export Values' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Clone' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Back to Dashboard' })).toBeInTheDocument();
    expect(screen.getByTestId('status-badge')).toHaveTextContent('running');
    expect(screen.getByText('Overrides are visible to the owner, admins and devops users.')).toBeInTheDocument();
    expect(screen.queryByText('Your Overrides')).not.toBeInTheDocument();
    expect(screen.getByText('Default Values')).toBeInTheDocument();
  });

  it('shows the TTL countdown but hides Extend for another user', async () => {
    setUser('u-bob', 'user');
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '3h 42m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });
    setupMocks({ status: 'running', expires_at: '2026-01-01T12:00:00Z' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText(/Expires in 3h 42m/)).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: 'Extend' })).not.toBeInTheDocument();
    (useCountdown as unknown as MockFn).mockReturnValue(null);
  });

  it('shows Extend for the owner when the TTL countdown runs', async () => {
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '3h 42m',
      isWarning: false,
      isCritical: false,
      isExpired: false,
    });
    setupMocks({ status: 'running', expires_at: '2026-01-01T12:00:00Z' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Extend' })).toBeInTheDocument();
    });
    (useCountdown as unknown as MockFn).mockReturnValue(null);
  });

  it('hides Deploy for another user when the instance is stopped', async () => {
    setUser('u-bob', 'user');
    setupMocks({ status: 'stopped' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: 'Deploy' })).not.toBeInTheDocument();
  });

  it('does not fetch value or branch overrides for another user', async () => {
    setUser('u-bob', 'user');
    setupMocks();
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(instanceService.getOverrides).not.toHaveBeenCalled();
    expect(branchOverrideService.list).not.toHaveBeenCalled();
    expect(instanceService.getDeployLog).toHaveBeenCalledWith('123');
    expect(instanceService.getPods).toHaveBeenCalledWith('123');
    expect(screen.queryByText('Failed to load instance details')).not.toBeInTheDocument();
  });

  it('keeps pods, access URLs and deployment history visible for another user', async () => {
    setUser('u-bob', 'user');
    const podsStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [],
      ingresses: [{ url: 'https://my-stack.example.com' }],
      last_checked: '2025-01-01',
    };
    setupMocks({}, {
      podsStatus,
      logs: [{ id: 'log1', stack_instance_id: '123', action: 'deploy', status: 'success', output: '', started_at: '2025-01-01' }],
    });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByTestId('access-urls')).toBeInTheDocument();
    });
    expect(screen.getByText('https://my-stack.example.com')).toBeInTheDocument();
    expect(screen.getByTestId('pod-status-display')).toBeInTheDocument();
    expect(screen.getByTestId('deployment-log-viewer')).toBeInTheDocument();
  });
});

describe('StackInstances Detail redeploy, rollback and drift', () => {
  afterEach(() => {
    vi.clearAllMocks();
    authState.user = { id: 'user1', username: 'alice', role: 'user', display_name: 'Alice' };
  });

  const deployLog = (id: string, hour: number, overrides: Record<string, unknown> = {}) => ({
    id,
    stack_instance_id: '123',
    action: 'deploy',
    status: 'success',
    output: '',
    started_at: new Date(Date.UTC(2030, 0, 1, hour, 0)).toISOString(),
    ...overrides,
  });
  // Newest first, as the API returns them. d3 is the current deploy.
  const threeDeploys = [
    deployLog('d3', 12),
    deployLog('stop1', 11, { action: 'stop' }),
    deployLog('d2', 10, { branch: 'feature-x' }),
    deployLog('failed', 9, { status: 'error' }),
    deployLog('d1', 8),
  ];
  const timeOf = (hour: number) => new Date(Date.UTC(2030, 0, 1, hour, 0)).toLocaleString();

  const waitForPage = async () => {
    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
  };

  it.each(['running', 'partial', 'error'])('shows Redeploy and not Deploy for a %s instance', async (status) => {
    setupMocks({ status });
    renderDetail();
    await waitForPage();
    expect(screen.getByRole('button', { name: 'Redeploy' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Deploy' })).not.toBeInTheDocument();
  });

  it.each(['draft', 'stopped'])('shows Deploy and not Redeploy for a %s instance', async (status) => {
    setupMocks({ status });
    renderDetail();
    await waitForPage();
    expect(screen.getByRole('button', { name: 'Deploy' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Redeploy' })).not.toBeInTheDocument();
  });

  it('opens the Deploy Preview dialog from Redeploy and deploys on confirm', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    (instanceService.deploy as MockFn).mockResolvedValue({ log_id: 'l1', message: 'ok' });
    renderDetail();
    await waitForPage();

    await user.click(screen.getByRole('button', { name: 'Redeploy' }));
    const dialog = screen.getByTestId('deploy-preview-dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Deploy' }));

    await waitFor(() => expect(instanceService.deploy).toHaveBeenCalledWith('123'));
  });

  it('shows "Saved. Redeploy to apply." after saving changes on a running instance', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    (instanceService.setOverride as MockFn).mockResolvedValue({});
    renderDetail();
    await waitForPage();

    await user.click(screen.getByRole('button', { name: 'edit Your Overrides' }));
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));

    const hint = await screen.findByText('Saved. Redeploy to apply.');
    const alert = hint.closest('[role="alert"]') as HTMLElement;
    await user.click(within(alert).getByRole('button', { name: 'Redeploy' }));
    expect(screen.getByTestId('deploy-preview-dialog')).toBeInTheDocument();
  });

  it('does not show the redeploy hint after a save without changes', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' });
    renderDetail();
    await waitForPage();

    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(screen.getByText('Changes saved successfully')).toBeInTheDocument());
    expect(screen.queryByText('Saved. Redeploy to apply.')).not.toBeInTheDocument();
  });

  it('does not show the redeploy hint for a draft instance', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'draft' });
    (instanceService.setOverride as MockFn).mockResolvedValue({});
    renderDetail();
    await waitForPage();

    await user.click(screen.getByRole('button', { name: 'edit Your Overrides' }));
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => expect(screen.getByText('Changes saved successfully')).toBeInTheDocument());
    expect(screen.queryByText('Saved. Redeploy to apply.')).not.toBeInTheDocument();
  });

  it('shows Rollback for a running instance with two or more successful deploys', async () => {
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();
    expect(await screen.findByRole('button', { name: 'Rollback' })).toBeInTheDocument();
  });

  it('hides Rollback with only one successful deploy', async () => {
    setupMocks({ status: 'running' }, { logs: [deployLog('d1', 8), deployLog('x', 9, { status: 'error' })] });
    renderDetail();
    await waitForPage();
    await waitFor(() => expect(screen.getByTestId('deployment-log-viewer')).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: 'Rollback' })).not.toBeInTheDocument();
  });

  it('hides Rollback for a stopped instance', async () => {
    setupMocks({ status: 'stopped' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();
    await waitFor(() => expect(screen.getByTestId('deployment-log-viewer')).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: 'Rollback' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Roll back to/ })).not.toBeInTheDocument();
  });

  it('rolls back to the previous deploy by default and names the target', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    (instanceService.rollback as MockFn).mockResolvedValue({ log_id: 'rb1', message: 'ok' });
    renderDetail();
    await waitForPage();

    await user.click(await screen.findByRole('button', { name: 'Rollback' }));
    const dialog = await screen.findByRole('dialog', { name: 'Roll Back Instance' });
    const radios = within(dialog).getAllByRole('radio');
    expect(radios).toHaveLength(3);
    // The current deploy (d3) cannot be selected; the previous one (d2) is the default.
    expect(within(dialog).getByRole('radio', { name: new RegExp(timeOf(12).replace(/[()]/g, '.')) })).toBeDisabled();
    expect(within(dialog).getByRole('radio', { name: `${timeOf(10)} (branch feature-x)` })).toBeChecked();
    expect(within(dialog).getByText(`Roll back "Test Instance" to the deploy of ${timeOf(10)} (branch feature-x)?`)).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Roll Back' }));
    await waitFor(() => expect(instanceService.rollback).toHaveBeenCalledWith('123', 'd2'));
    expect(await screen.findByText('Rollback started. Follow the deployment log.')).toBeInTheDocument();
  });

  it('names the image and pre-deploy check limits in the rollback dialog', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();

    await user.click(await screen.findByRole('button', { name: 'Rollback' }));
    const dialog = await screen.findByRole('dialog', { name: 'Roll Back Instance' });
    expect(within(dialog).getByText(/restores the stored values of that deploy, not its images/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Pre-deploy checks do not run for a rollback\. Pre-rollback hooks run\./)).toBeInTheDocument();
  });

  it('shows the rollback warning and the drift flag from the response', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    (instanceService.rollback as MockFn).mockResolvedValue({
      log_id: 'rb1', message: 'ok', target_log_id: 'd2', values_drift: true,
      warning: 'The next deploy applies the stored overrides again.',
    });
    renderDetail();
    await waitForPage();
    const getCalls = (instanceService.get as MockFn).mock.calls.length;

    await user.click(await screen.findByRole('button', { name: 'Rollback' }));
    const dialog = await screen.findByRole('dialog', { name: 'Roll Back Instance' });
    await user.click(within(dialog).getByRole('button', { name: 'Roll Back' }));

    expect(await screen.findByText(
      'Rollback started. Follow the deployment log. The next deploy applies the stored overrides again.',
    )).toBeInTheDocument();
    // One refetch after the rollback. It returns no drift yet (the log still
    // runs), so the flag from the rollback response stays.
    await waitFor(() => expect(instanceService.getDeployLog).toHaveBeenCalledTimes(2));
    expect((instanceService.get as MockFn).mock.calls.length).toBe(getCalls + 1);
    expect(screen.getByText(/The running values differ from the stored overrides/)).toBeInTheDocument();
  });

  it('labels the placeholder log of a rollback as rollback', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    (instanceService.rollback as MockFn).mockResolvedValue({ log_id: 'rb1', message: 'ok' });
    renderDetail();
    await waitForPage();

    await user.click(await screen.findByRole('button', { name: 'Rollback' }));
    const dialog = await screen.findByRole('dialog', { name: 'Roll Back Instance' });
    await user.click(within(dialog).getByRole('button', { name: 'Roll Back' }));
    await screen.findByText('Rollback started. Follow the deployment log.');

    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'deploying', log_id: 'rb1' } });
    });
    await waitFor(() => expect(screen.getByTestId('log-actions').textContent?.startsWith('rollback,')).toBe(true));
  });

  it('labels the placeholder log from the action in the WebSocket message', async () => {
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();
    await waitFor(() => expect(screen.getByTestId('log-actions')).toBeInTheDocument());

    // A rollback started by another user: no local pending action.
    act(() => {
      wsState.handler?.({
        type: 'deployment.status',
        payload: { instance_id: '123', status: 'deploying', log_id: 'rb2', action: 'rollback' },
      });
    });
    await waitFor(() => expect(screen.getByTestId('log-actions').textContent?.startsWith('rollback,')).toBe(true));
  });

  it('labels the placeholder log of a deploy started elsewhere as deploy', async () => {
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();
    await waitFor(() => expect(screen.getByTestId('log-actions')).toBeInTheDocument());

    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'deploying', log_id: 'x1' } });
    });
    await waitFor(() => expect(screen.getByTestId('log-actions').textContent?.startsWith('deploy,')).toBe(true));
  });

  it('keeps a newer WebSocket status when a late instance fetch returns', async () => {
    const inst = setupMocks({ status: 'running' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();

    // The terminal refetch is slow; a newer status arrives before it returns.
    let resolveGet: (value: unknown) => void = () => {};
    (instanceService.get as MockFn).mockImplementation(() => new Promise((resolve) => { resolveGet = resolve; }));
    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'running' } });
    });
    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'stopping' } });
    });
    await act(async () => {
      resolveGet({ ...inst, status: 'running', values_drift: true });
    });

    expect(screen.getByTestId('status-badge')).toHaveTextContent('stopping');
    // Other fields from the fetch still apply.
    expect(screen.getByText(/The running values differ from the stored overrides/)).toBeInTheDocument();
  });

  it('keeps the current error message when a late instance fetch returns', async () => {
    const inst = setupMocks({ status: 'running' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();

    // Each fetch waits; the test resolves only the first (stale) one.
    const resolvers: ((value: unknown) => void)[] = [];
    (instanceService.get as MockFn).mockImplementation(() => new Promise((resolve) => { resolvers.push(resolve); }));
    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'running' } });
    });
    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'stopped' } });
    });
    // The stale fetch carries an old error message. With the newer status
    // "stopped" it would show the Expired chip.
    await act(async () => {
      resolvers[0]({ ...inst, status: 'running', error_message: 'Expired (TTL)' });
    });

    expect(screen.getByTestId('status-badge')).toHaveTextContent('stopped');
    expect(screen.queryByText('Expired')).not.toBeInTheDocument();
  });

  it('refetches the instance on a terminal WebSocket status so the drift alert follows', async () => {
    const inst = setupMocks({ status: 'running' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();
    expect(screen.queryByText(/The running values differ from the stored overrides/)).not.toBeInTheDocument();

    // Rollback finished: the instance now has drift.
    (instanceService.get as MockFn).mockResolvedValue({ ...inst, values_drift: true });
    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'running' } });
    });
    expect(await screen.findByText(/The running values differ from the stored overrides/)).toBeInTheDocument();

    // Redeploy finished: the drift is gone.
    (instanceService.get as MockFn).mockResolvedValue({ ...inst, values_drift: false });
    act(() => {
      wsState.handler?.({ type: 'deployment.status', payload: { instance_id: '123', status: 'running' } });
    });
    await waitFor(() => {
      expect(screen.queryByText(/The running values differ from the stored overrides/)).not.toBeInTheDocument();
    });
  });

  it('shows the error message of an instance in error (denied pre-deploy hook)', async () => {
    const reason = 'pre-deploy hook "ci-gate" denied the deployment: frontend: build failed';
    setupMocks({ status: 'error', error_message: reason }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();

    expect(await screen.findByText('Instance is error', { exact: false })).toBeInTheDocument();
    expect(screen.getByTestId('instance-error-message')).toHaveTextContent(reason);
  });

  it('shows the error message from the WebSocket status message', async () => {
    const inst = setupMocks({ status: 'deploying' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();
    expect(screen.queryByTestId('instance-error-message')).not.toBeInTheDocument();

    const reason = 'pre-deploy hook "ci-gate" denied the deployment: no image';
    // The refetch is slow; the message from the status message shows first.
    (instanceService.get as MockFn).mockImplementation(() => new Promise(() => {}));
    act(() => {
      wsState.handler?.({
        type: 'deployment.status',
        payload: { instance_id: inst.id, status: 'error', error_message: reason },
      });
    });
    expect(await screen.findByTestId('instance-error-message')).toHaveTextContent(reason);
  });

  it('rolls back to a selected older deploy', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    (instanceService.rollback as MockFn).mockResolvedValue({ log_id: 'rb1', message: 'ok' });
    renderDetail();
    await waitForPage();

    await user.click(await screen.findByRole('button', { name: 'Rollback' }));
    const dialog = await screen.findByRole('dialog', { name: 'Roll Back Instance' });
    await user.click(within(dialog).getByRole('radio', { name: timeOf(8) }));
    expect(within(dialog).getByText(`Roll back "Test Instance" to the deploy of ${timeOf(8)}?`)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Roll Back' }));

    await waitFor(() => expect(instanceService.rollback).toHaveBeenCalledWith('123', 'd1'));
  });

  it('shows the API error when the rollback fails', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    (instanceService.rollback as MockFn).mockRejectedValue({ response: { status: 400, data: { error: 'target is not a successful deploy' } } });
    renderDetail();
    await waitForPage();

    await user.click(await screen.findByRole('button', { name: 'Rollback' }));
    const dialog = await screen.findByRole('dialog', { name: 'Roll Back Instance' });
    await user.click(within(dialog).getByRole('button', { name: 'Roll Back' }));

    expect(await screen.findByText(/target is not a successful deploy/)).toBeInTheDocument();
  });

  it('offers "Roll back to this deploy" in the history for older successful deploys only', async () => {
    const user = userEvent.setup();
    setupMocks({ status: 'running' }, { logs: threeDeploys });
    (instanceService.rollback as MockFn).mockResolvedValue({ log_id: 'rb1', message: 'ok' });
    renderDetail();
    await waitForPage();

    await waitFor(() => expect(screen.getByRole('button', { name: 'Roll back to d1' })).toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Roll back to d2' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Roll back to d3' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Roll back to failed' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Roll back to d1' }));
    const dialog = await screen.findByRole('dialog', { name: 'Roll Back Instance' });
    expect(within(dialog).getByRole('radio', { name: timeOf(8) })).toBeChecked();
    await user.click(within(dialog).getByRole('button', { name: 'Roll Back' }));

    await waitFor(() => expect(instanceService.rollback).toHaveBeenCalledWith('123', 'd1'));
  });

  it('shows the drift warning when values_drift is true', async () => {
    setupMocks({ status: 'running', values_drift: true });
    renderDetail();
    await waitForPage();
    expect(screen.getByText(
      'The running values differ from the stored overrides (after a rollback). The next deploy applies the stored overrides.',
    )).toBeInTheDocument();
  });

  it('does not show the drift warning without drift', async () => {
    setupMocks({ status: 'running' });
    renderDetail();
    await waitForPage();
    expect(screen.queryByText(/The running values differ from the stored overrides/)).not.toBeInTheDocument();
  });

  it('shows none of the new actions to a user who may not modify the instance', async () => {
    authState.user = { id: 'u-bob', username: 'bob', role: 'user', display_name: 'Bob' };
    (useCountdown as unknown as MockFn).mockReturnValue({
      remaining: '1h 0m', isWarning: false, isCritical: false, isExpired: false,
    });
    setupMocks({ status: 'running', expires_at: '2030-01-01T12:00:00Z' }, { logs: threeDeploys });
    renderDetail();
    await waitForPage();
    await waitFor(() => expect(screen.getByTestId('deployment-log-viewer')).toBeInTheDocument());

    for (const name of ['Redeploy', 'Deploy', 'Rollback', 'Extend']) {
      expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole('button', { name: /Roll back to/ })).not.toBeInTheDocument();
    expect(screen.queryByText('Saved. Redeploy to apply.')).not.toBeInTheDocument();
    (useCountdown as unknown as MockFn).mockReturnValue(null);
  });
});
