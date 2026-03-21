import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import Detail from '../Detail';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../../hooks/useWebSocket', () => ({
  useWebSocket: () => ({ send: vi.fn() }),
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
    clone: vi.fn(),
    delete: vi.fn(),
    exportValues: vi.fn(),
    deploy: vi.fn(),
    stop: vi.fn(),
    clean: vi.fn(),
    getDeployLog: vi.fn(),
    getStatus: vi.fn(),
    extend: vi.fn(),
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
}));

vi.mock('../../../components/YamlEditor', () => ({
  default: (props: { label?: string; value: string }) => (
    <div data-testid="yaml-editor">
      <span>{props.label}</span>
      <pre>{props.value}</pre>
    </div>
  ),
}));

vi.mock('../../../components/DeploymentLogViewer', () => ({
  default: ({ logs }: { logs: unknown[] }) => (
    <div data-testid="deployment-log-viewer">
      {logs.length} log entries
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
  default: ({ value }: { value: string; repoUrl: string; onChange: (v: string) => void }) => (
    <input data-testid="branch-selector" value={value} readOnly />
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

import { instanceService, definitionService, branchOverrideService } from '../../../api/client';
import useCountdown from '../../../hooks/useCountdown';

type MockFn = ReturnType<typeof vi.fn>;

const setupMocks = (instanceOverrides: Partial<typeof mockInstance> = {}, opts: { logs?: unknown[]; status?: unknown; deployLogReject?: boolean; statusReject?: boolean; branchOverrides?: unknown[] } = {}) => {
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
  if (opts.statusReject) {
    (instanceService.getStatus as MockFn).mockRejectedValue(new Error('no status'));
  } else {
    (instanceService.getStatus as MockFn).mockResolvedValue(opts.status ?? null);
  }
  return inst;
};

const renderDetail = () =>
  render(
    <MemoryRouter initialEntries={['/stack-instances/123']}>
      <Routes>
        <Route path="/stack-instances/:id" element={<Detail />} />
      </Routes>
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
  });

  it('shows loading spinner while fetching', () => {
    (instanceService.get as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <Routes>
          <Route path="/stack-instances/:id" element={<Detail />} />
        </Routes>
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
    expect(screen.getByText(/user1/)).toBeInTheDocument();
  });

  it('shows error alert when fetch fails', async () => {
    (instanceService.get as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Not found'));

    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <Routes>
          <Route path="/stack-instances/:id" element={<Detail />} />
        </Routes>
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

  it('does NOT show Deploy button for running instance', async () => {
    setupMocks({ status: 'running' });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.queryByRole('button', { name: /deploy/i })).not.toBeInTheDocument();
  });

  it('shows Deploy button for stopped instance', async () => {
    setupMocks({ status: 'stopped' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /deploy/i })).toBeInTheDocument();
  });

  it('shows Deploy button for error instance', async () => {
    setupMocks({ status: 'error' }, { deployLogReject: true });
    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /deploy/i })).toBeInTheDocument();
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

    await user.click(screen.getByRole('button', { name: /deploy/i }));

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

    await user.click(screen.getByRole('button', { name: /deploy/i }));

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
    setupMocks({ status: 'running' }, { status: mockStatus });

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
      expect(instanceService.getStatus).toHaveBeenCalledWith('123');
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
    setupMocks({ status: 'stopping' }, { status: mockStatus });

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
    setupMocks({ status: 'running' }, { status: mockStatus });
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
    setupMocks({ status: 'running' }, { statusReject: true });
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

    await waitFor(() => {
      expect(instanceService.extend).toHaveBeenCalledWith('123');
    });
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

  it('calls instanceService.update when TTL is changed', async () => {
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
  });
});
