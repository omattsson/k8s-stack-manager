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
    getDeployLog: vi.fn(),
    getStatus: vi.fn(),
  },
  definitionService: {
    get: vi.fn(),
  },
  gitService: {
    branches: vi.fn(),
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

import { instanceService, definitionService } from '../../../api/client';

type MockFn = ReturnType<typeof vi.fn>;

const setupMocks = (instanceOverrides: Partial<typeof mockInstance> = {}, opts: { logs?: unknown[]; status?: unknown; deployLogReject?: boolean; statusReject?: boolean } = {}) => {
  const inst = { ...mockInstance, ...instanceOverrides };
  (instanceService.get as MockFn).mockResolvedValue(inst);
  (definitionService.get as MockFn).mockResolvedValue(mockDefinition);
  (instanceService.getOverrides as MockFn).mockResolvedValue([]);
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
      { id: 'log-1', stack_instance_id: '123', action: 'deploy', status: 'success', started_at: '2025-01-01T00:00:00Z', finished_at: '2025-01-01T00:01:00Z' },
      { id: 'log-2', stack_instance_id: '123', action: 'deploy', status: 'failed', started_at: '2025-01-02T00:00:00Z', finished_at: '2025-01-02T00:01:00Z' },
    ];
    setupMocks({ status: 'draft' }, { logs: mockLogs });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await waitFor(() => {
      expect(screen.getByText('Deployment History')).toBeInTheDocument();
      expect(screen.getByTestId('deployment-log-viewer')).toBeInTheDocument();
    });
  });

  it('does not show Deployment History when no logs exist', async () => {
    setupMocks({ status: 'draft' }, { logs: [] });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.queryByText('Deployment History')).not.toBeInTheDocument();
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

  it('shows warning alert in lifecycle for stopped status', async () => {
    setupMocks({ status: 'stopped' }, { deployLogReject: true });

    renderDetail();

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    expect(screen.getByText('Instance is stopped')).toBeInTheDocument();
  });
});
