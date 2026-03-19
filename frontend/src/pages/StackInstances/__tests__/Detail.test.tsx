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

import { instanceService, definitionService } from '../../../api/client';

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
    (instanceService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockInstance);
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinition);
    (instanceService.getOverrides as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <Routes>
          <Route path="/stack-instances/:id" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    );

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
    (instanceService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockInstance);
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinition);
    (instanceService.getOverrides as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <Routes>
          <Route path="/stack-instances/:id" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByRole('tab', { name: 'frontend' })).toBeInTheDocument();
    });
  });

  it('opens delete confirmation dialog', async () => {
    const user = userEvent.setup();
    (instanceService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockInstance);
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinition);
    (instanceService.getOverrides as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <Routes>
          <Route path="/stack-instances/:id" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    );

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
    (instanceService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockInstance);
    (definitionService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockDefinition);
    (instanceService.getOverrides as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    render(
      <MemoryRouter initialEntries={['/stack-instances/123']}>
        <Routes>
          <Route path="/stack-instances/:id" element={<Detail />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Test Instance')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /back to dashboard/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/');
  });
});
