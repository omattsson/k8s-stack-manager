import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import Preview from '../Preview';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../../api/client', () => ({
  templateService: {
    get: vi.fn(),
    clone: vi.fn(),
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

import { templateService } from '../../../api/client';

const mockTemplate = {
  id: 't1',
  name: 'Web Stack Template',
  description: 'A modern web stack',
  category: 'Web',
  version: '1.0',
  owner_id: '1',
  default_branch: 'main',
  is_published: true,
  created_at: '',
  updated_at: '',
  charts: [
    {
      id: 'tc1',
      stack_template_id: 't1',
      chart_name: 'frontend',
      repository_url: 'https://charts.example.com',
      source_repo_url: '',
      chart_path: 'charts/frontend',
      chart_version: '1.0.0',
      default_values: 'replicaCount: 2',
      locked_values: 'image: nginx',
      deploy_order: 1,
      required: true,
      created_at: '',
    },
  ],
};

describe('Templates Preview', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner while fetching', () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays template details when loaded', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });
    expect(screen.getByText('A modern web stack')).toBeInTheDocument();
    expect(screen.getByText('Published')).toBeInTheDocument();
    expect(screen.getByText('Web')).toBeInTheDocument();
    expect(screen.getByText('v1.0')).toBeInTheDocument();
    expect(screen.getByText('Charts (1)')).toBeInTheDocument();
    expect(screen.getByText('frontend')).toBeInTheDocument();
  });

  it('shows error alert when fetch fails', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Not found'));
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load template')).toBeInTheDocument();
    });
  });

  it('shows Use Template button for published templates', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /use template/i })).toBeInTheDocument();
    });
  });

  it('shows Edit and Clone buttons for admin users who own the template', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /edit/i })).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /clone as template/i })).toBeInTheDocument();
    });
  });

  it('displays chart default values and locked values', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText('Default Values')).toBeInTheDocument();
    });
    expect(screen.getByText('replicaCount: 2')).toBeInTheDocument();
    expect(screen.getByText('Locked Values')).toBeInTheDocument();
    expect(screen.getByText('image: nginx')).toBeInTheDocument();
  });

  it('clones template and navigates to edit', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    (templateService.clone as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 't-clone' });

    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /clone as template/i }));

    await waitFor(() => {
      expect(templateService.clone).toHaveBeenCalledWith('t1');
    });
    expect(mockNavigate).toHaveBeenCalledWith('/templates/t-clone/edit');
  });

  it('navigates back to gallery', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);

    render(
      <MemoryRouter initialEntries={['/templates/t1']}>
        <Routes>
          <Route path="/templates/:id" element={<Preview />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('Web Stack Template')).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /back to gallery/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/templates');
  });
});
